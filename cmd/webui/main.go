// Command webui serves the go-netmon management API and web interface.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/webui/api"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/provider"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

const version = "0.1.0"

var (
	listenAddr     = flag.String("listen", ":8080", "Web UI listen address")
	prometheusURL  = flag.String("prometheus", "http://localhost:9091", "Prometheus URL for querying metrics")
	pollerURL      = flag.String("poller", "http://localhost:9080", "Poller service URL for device management")
	dbPath         = flag.String("db", "data/webui", "Database path for Web UI storage (PebbleDB directory)")
	mibStoragePath = flag.String("mib-storage", "data/mibs", "Path to store uploaded MIB files")

	// mibCatalogPath is where the parsed MIB catalog lives: the OID metadata of
	// every imported module, as opposed to the MIB files themselves, which
	// -mib-storage governs. It is separate from -db because the catalog is
	// rebuildable from the records -db holds, and separate from -mib-storage
	// because that directory is shared with the poller, which keeps a catalog
	// of its own there and would not tolerate a second writer.
	mibCatalogPath = flag.String("mib-catalog", "data/mib.db",
		"Path to the parsed MIB catalog (PebbleDB directory)")
)

func main() {
	flag.Parse()

	log.Printf("go-netmon Web UI v%s", version)
	log.Printf("Listen address: %s", *listenAddr)
	log.Printf("Prometheus URL: %s", *prometheusURL)
	log.Printf("Poller URL: %s", *pollerURL)
	log.Printf("Database path: %s", *dbPath)
	log.Printf("MIB storage: %s", *mibStoragePath)
	log.Printf("MIB catalog: %s", *mibCatalogPath)

	if err := runUntilSignal(); err != nil {
		log.Fatalf("Application error: %v", err)
	}

	log.Println("Shutdown complete")
}

// runUntilSignal owns the cancellable context so that main can exit through
// log.Fatal without stranding a deferred cancel.
func runUntilSignal() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received shutdown signal: %v", sig)
		log.Println("Initiating graceful shutdown...")
		cancel()
	}()

	return run(ctx)
}

func run(ctx context.Context) error {
	// Load MIB configuration (or use defaults)
	cfg, err := config.Load()
	if err != nil {
		log.Printf("Warning: failed to load config, using defaults: %v", err)
		cfg = &config.Config{
			MIB: config.MIBConfig{
				AutoLoad: []string{"SNMPv2-MIB", "IF-MIB", "CISCO-BGP4-MIB"},
			},
		}
		// This literal names only the MIB section, so every other section is
		// still a zero value. Logging in particular cannot be used as-is:
		// output "" is not a valid output and would fail the service at
		// startup, which is not much of a fallback.
		config.ApplyDefaults(cfg)
	}

	configureMIB(&cfg.MIB, *mibCatalogPath, *mibStoragePath)
	log.Printf("MIB search paths: %v", cfg.MIB.Paths)

	// Initialize MIB loader
	log.Println("Initializing MIB loader...")
	mibLoader, err := loader.New(&cfg.MIB)
	if err != nil {
		return fmt.Errorf("failed to initialize MIB loader: %w", err)
	}
	defer func() {
		log.Println("Closing MIB loader...")
		if closeErr := mibLoader.Close(); closeErr != nil {
			log.Printf("Error closing MIB loader: %v", closeErr)
		}
	}()

	// Load MIB modules
	log.Println("Loading MIB modules...")
	if loadErr := mibLoader.Load(); loadErr != nil {
		return fmt.Errorf("failed to load MIBs: %w", loadErr)
	}
	log.Println("MIB modules loaded successfully")

	// Get MIB storage
	mibStorageInterface := mibLoader.GetStorage()
	mibStorage, ok := mibStorageInterface.(*storage.Storage)
	if !ok || mibStorage == nil {
		return fmt.Errorf("MIB loader returned no usable storage")
	}

	// Create MIB provider
	mibProvider := provider.NewMIBProvider(mibLoader, mibStorage)

	// Create Prometheus client
	promClient := api.NewPrometheusClient(*prometheusURL)

	// Build the configured logger. Everything level-gated in the web UI goes
	// through it, so logging.level in the config file is what decides whether
	// the debug tracing on the query path runs at all.
	logger, logCloser, err := logging.New(cfg.Logging)
	if err != nil {
		return fmt.Errorf("failed to initialize logging: %w", err)
	}
	defer func() {
		if closeErr := logCloser.Close(); closeErr != nil {
			log.Printf("Error closing log file: %v", closeErr)
		}
	}()
	log.Printf("Logging initialized (level: %s, format: %s, output: %s)",
		cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.Output)

	// Initialize poller client. Its per-request timeout is one link in the
	// budget documented in internal/webui/api/timeouts.go.
	log.Printf("Initializing poller client (URL: %s)...", *pollerURL)
	pollerClient := client.NewPollerClient(*pollerURL, api.PollerRequestTimeout)
	log.Println("Poller client initialized")

	// Create API server
	log.Println("Initializing Web UI server...")
	server, err := api.NewServer(api.Config{
		QueryFunc: func(ctx context.Context, query string) ([]ws.MetricValue, error) {
			return promClient.Query(ctx, query)
		},
		MIBProvider:     mibProvider,
		MIBLoader:       mibLoader,
		DBPath:          *dbPath,
		MIBStoragePath:  *mibStoragePath,
		PollerClient:    pollerClient,
		PromClient:      promClient,
		GoNetmonBaseURL: *pollerURL, // Pass poller URL for OID learner proxy
		Logger:          logger,

		// The poller's global timing. The web UI does not poll, but it has to
		// report the interval a device that inherits the default is polled at,
		// and it has to know the SNMP budget an interval can undercut.
		Polling: api.PollingConfig{
			DefaultInterval: cfg.Global.DefaultPollInterval,
			SNMPTimeout:     cfg.Global.SNMPTimeout,
			SNMPRetries:     cfg.Global.SNMPRetries,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}
	defer func() {
		log.Println("Closing server...")
		if err := server.Close(); err != nil {
			log.Printf("Error closing server: %v", err)
		}
	}()

	// Load stored MIBs from database into MIB storage
	log.Println("Loading stored MIBs from database...")
	if err := server.LoadStoredMIBs(ctx); err != nil {
		log.Printf("Warning: failed to load stored MIBs: %v", err)
	}

	// Load existing devices and register them with the poller
	log.Println("Loading existing devices from database...")
	if err := server.LoadAndRegisterDevices(ctx); err != nil {
		log.Printf("Warning: failed to load and register devices: %v", err)
	}

	// Start the server
	log.Printf("Starting Web UI server on %s", *listenAddr)
	log.Printf("Open http://localhost%s in your browser", *listenAddr)

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		if err := server.Start(ctx, *listenAddr); err != nil {
			errChan <- err
		}
	}()

	// Wait for context cancellation or error
	select {
	case <-ctx.Done():
		log.Println("Server stopped by context")
		return nil
	case err := <-errChan:
		return fmt.Errorf("server error: %w", err)
	}
}

// configureMIB settles where the web UI keeps its parsed MIB catalog and where
// it looks for MIB files.
//
// The catalog path always comes from the command line and never from the config
// file. That file is the poller's: its mib.storage_path names the *poller's*
// catalog, and Pebble admits one writer, so a web UI that adopted it would
// either fail to open a store the poller holds or, with the poller down, take
// the lock and keep the poller out. The two services therefore keep separate
// catalogs by construction rather than by the accident of the web UI container
// having no config file in it.
//
// mibFilesPath is the directory the web UI writes uploaded MIB files to. It is
// shared with the poller, so its uploaded/ subdirectory is put on the search
// path here for the same reason the poller puts it on its own: either service
// must be able to parse a module the other imported.
func configureMIB(mibCfg *config.MIBConfig, catalogPath, mibFilesPath string) {
	mibCfg.StoragePath = catalogPath

	known := make(map[string]bool, len(mibCfg.Paths))
	for _, path := range mibCfg.Paths {
		known[path] = true
	}

	// The imported-MIB store, plus whatever the base image ships. The
	// "/app/mibs/*" entries that used to be here named a directory bind-mounted
	// from the repository working copy: the deployment could not be moved to a
	// host without that checkout, and a module resolved from there rather than
	// from the store looked imported while living nowhere the backup covered.
	// Imports are copied into the store, so the store is the whole source.
	for _, path := range []string{
		filepath.Join(mibFilesPath, "uploaded"),
		"/app/mibs/standard",
		"/usr/share/snmp/mibs",
	} {
		if known[path] {
			continue
		}
		known[path] = true
		mibCfg.Paths = append(mibCfg.Paths, path)
	}
}
