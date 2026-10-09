// Command webui serves the go-netmon management API and web interface.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/sdnotify"
	"github.com/jeremyhahn/go-netmon/internal/webui"
	"github.com/jeremyhahn/go-netmon/internal/webui/alarm"
	"github.com/jeremyhahn/go-netmon/internal/webui/api"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/provider"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

const version = "0.1.0"

var (
	// The three address flags carry no default of their own: the default lives
	// in the config package's defaults layer, and a flag left unset must not
	// mask the environment (NETMON_WEBUI_*) or the config file.
	_              = flag.String("listen", "", "Web UI listen address (config webui.listen_address, env NETMON_WEBUI_LISTEN_ADDRESS)")
	_              = flag.String("prometheus", "", "Prometheus URL for querying metrics (config webui.prometheus_url, env NETMON_WEBUI_PROMETHEUS_URL)")
	_              = flag.String("poller", "", "Poller service URL for device management (config webui.poller_url, env NETMON_WEBUI_POLLER_URL)")
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
	log.Printf("Database path: %s", *dbPath)
	log.Printf("MIB storage: %s", *mibStoragePath)
	log.Printf("MIB catalog: %s", *mibCatalogPath)

	if err := runUntilSignal(); err != nil {
		log.Fatalf("Application error: %v", err)
	}

	log.Println("Shutdown complete")
}

// addressOverrides is the flag layer for the bind and target addresses: only
// the flags the operator actually set, so an unset flag never masks the
// environment or the config file.
func addressOverrides() config.Overrides {
	return config.FlagOverrides(flag.CommandLine, map[string]string{
		"listen":     config.KeyWebUIListen,
		"prometheus": config.KeyWebUIPrometheus,
		"poller":     config.KeyWebUIPoller,
	})
}

// runUntilSignal owns the cancellable context so that main can exit through
// log.Fatal without stranding a deferred cancel.
func runUntilSignal() error {
	// When the process began, so that the line marking the end of startup can
	// say how long the service was unable to serve. That number is the whole
	// of #84: a restart is an outage of the dashboard, and it should be stated
	// by the service rather than discovered during an incident.
	started := time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The service manager, if there is one. A process started any other way
	// gets a notifier that discards what it is told, so nothing below has to
	// ask whether it is running under systemd.
	notifier, err := sdnotify.New()
	if err != nil {
		return fmt.Errorf("failed to open the systemd notification socket: %w", err)
	}
	defer func() {
		if closeErr := notifier.Close(); closeErr != nil {
			log.Printf("Error closing the notification socket: %v", closeErr)
		}
	}()
	if notifier.Supervised() {
		log.Println("Supervised by systemd; readiness will be reported once the listener is bound")
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received shutdown signal: %v", sig)
		log.Println("Initiating graceful shutdown...")
		// Tell the manager the service is on its way down before the shutdown
		// starts, so a slow drain reads as deactivating rather than as a
		// service that has silently stopped answering.
		if notifyErr := notifier.Stopping(); notifyErr != nil {
			log.Printf("Failed to report shutdown to systemd: %v", notifyErr)
		}
		cancel()
	}()

	return run(ctx, notifier, started)
}

func run(ctx context.Context, notifier *sdnotify.Notifier, started time.Time) error {
	// Load MIB configuration (or use defaults)
	overrides := addressOverrides()
	cfg, err := config.LoadWithOverrides(overrides)
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
		// The addresses still honor the environment and flags without a
		// usable file. A file that cannot be parsed is skipped here.
		if resolveErr := config.ResolveAddresses(cfg, "", overrides); resolveErr != nil {
			return resolveErr
		}
	}

	log.Printf("Listen address: %s", cfg.WebUI.ListenAddress)
	log.Printf("Prometheus URL: %s", cfg.WebUI.PrometheusURL)
	log.Printf("Poller URL: %s", cfg.WebUI.PollerURL)

	configureMIB(&cfg.MIB, *mibCatalogPath, *mibStoragePath)
	log.Printf("MIB search paths: %v", cfg.MIB.Paths)

	// Initialize MIB loader
	log.Println("Initializing MIB loader...")
	status(notifier, "opening the MIB catalog")
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
	mibLoader.SetProgress(mibProgress(notifier, "auto-loading MIB modules"))
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
	promClient := api.NewPrometheusClient(cfg.WebUI.PrometheusURL)

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
	log.Printf("Initializing poller client (URL: %s)...", cfg.WebUI.PollerURL)
	pollerClient := client.NewPollerClient(cfg.WebUI.PollerURL, api.PollerRequestTimeout)
	log.Println("Poller client initialized")

	// The UI's static assets, compiled into this binary. Resolving them here
	// rather than inside the server keeps the server free of any opinion about
	// where its assets live, and makes a broken embed a startup error instead
	// of a UI that answers 404 to everything but /api.
	staticAssets, err := webui.Static()
	if err != nil {
		return fmt.Errorf("failed to open embedded web UI assets: %w", err)
	}

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
		GoNetmonBaseURL: cfg.WebUI.PollerURL, // Pass poller URL for OID learner proxy
		Assets:          staticAssets,
		Logger:          logger,

		// What the unit's status line says through the longest stretch of a
		// cold start.
		StartupProgress: mibProgress(notifier, "loading imported MIB modules"),

		// The poller's global timing. The web UI does not poll, but it has to
		// report the interval a device that inherits the default is polled at,
		// and it has to know the SNMP budget an interval can undercut.
		Polling: api.PollingConfig{
			DefaultInterval: cfg.Global.DefaultPollInterval,
			SNMPTimeout:     cfg.Global.SNMPTimeout,
			SNMPRetries:     cfg.Global.SNMPRetries,
		},

		// How often every connected browser is pushed a round of updates.
		StreamInterval: time.Duration(cfg.WebUI.StreamInterval) * time.Second,

		// The conditions the bell reports. They come from the same file the
		// poller reads, and SIGHUP below re-reads it without a restart --
		// which matters more here than for anything else in the file, because
		// restarting this service costs minutes of MIB parsing and an
		// operator tuning a threshold would do it several times.
		AlarmRules: alarm.RulesFrom(cfg.Alarms),
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
	status(notifier, "loading imported MIB modules")
	if err := server.LoadStoredMIBs(ctx); err != nil {
		log.Printf("Warning: failed to load stored MIBs: %v", err)
	}

	// Load existing devices and register them with the poller
	log.Println("Loading existing devices from database...")
	if err := server.LoadAndRegisterDevices(ctx); err != nil {
		log.Printf("Warning: failed to load and register devices: %v", err)
	}

	// Keep the metric catalog warm. A dashboard load reads what is held
	// rather than four Prometheus queries and 26,000 rows of JSON.
	go server.RunCatalogRefresh(ctx)

	// Evaluate the alarm rules for as long as the server runs.
	if evaluator := server.Alarms(); evaluator != nil {
		log.Printf("Watching %d alarm rules", evaluator.RuleCount())
		go evaluator.Run(ctx)
	}

	go reloadOnSignal(ctx, server)

	// Bind before announcing anything. Everything above this line — the MIB
	// catalog above all — happens with nothing listening, and on a cold
	// catalog that is minutes. Binding is the point at which the service
	// becomes usable, so it is the point the readiness signal belongs at, and
	// the point the log line an operator greps for belongs at.
	status(notifier, "binding "+cfg.WebUI.ListenAddress)
	// listenErr rather than err: the outer err is still live here, and
	// shadowing it would hide a failure to bind behind a name that already
	// means something else in this function.
	listener, listenErr := server.Listen(ctx, cfg.WebUI.ListenAddress)
	if listenErr != nil {
		return listenErr
	}

	// The line to grep for, and the one that was missing. Everything before it
	// happened with nothing listening, so the elapsed time it reports is how
	// long the dashboard was down for this restart.
	log.Printf("Listening on %s — the web UI is ready to serve (startup took %s, during which it served nothing)",
		listener.Addr(), time.Since(started).Truncate(time.Millisecond))
	log.Printf("Open http://localhost%s in your browser", cfg.WebUI.ListenAddress)

	// READY=1 goes out only now. Sending it any earlier is the defect this
	// replaces: systemd would call the unit active while the port refused
	// every connection, and a deploy check could not tell that from a wedge.
	if notifyErr := notifier.Ready(); notifyErr != nil {
		return fmt.Errorf("failed to report readiness to systemd: %w", notifyErr)
	}
	status(notifier, "serving on "+listener.Addr().String())

	// Start server in a goroutine
	errChan := make(chan error, 1)
	go func() {
		// A graceful shutdown closes the server and Serve reports that as
		// ErrServerClosed. That is the expected end of a stop, not a failure:
		// exiting non-zero on it would make every `systemctl restart` look
		// like a failed one, which is the class of lie this change exists to
		// remove.
		if err := server.Serve(ctx, listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

// status sets the unit's status line, the one thing a service that has not
// finished starting can say to somebody looking at it with `systemctl status`.
// A failure to report is worth a line in the journal and nothing more: it costs
// visibility, not correctness.
func status(notifier *sdnotify.Notifier, text string) {
	if err := notifier.Status(text); err != nil {
		log.Printf("Failed to report startup status to systemd: %v", err)
	}
}

// mibProgress builds a reporter that keeps the unit's status line current
// through a module load that takes minutes.
//
// It writes only to the service manager. The journal already carries a line per
// module, and a second copy of it there would bury the load it describes.
//
// The returned reporter is called from the goroutine running the load and is
// not safe for concurrent use, which is what the load loops do.
func mibProgress(notifier *sdnotify.Notifier, phase string) mib.LoadProgress {
	reporting := true
	return func(done, total int, module string) {
		if !reporting {
			return
		}
		if err := notifier.Status(fmt.Sprintf("%s: %d/%d (%s)", phase, done, total, module)); err != nil {
			// Said once and then given up on. A broken notification socket
			// would otherwise put a line in the journal per module.
			log.Printf("Progress reporting to systemd stopped after %d/%d: %v", done, total, err)
			reporting = false
		}
	}
}

// reloadOnSignal re-reads the configuration on SIGHUP.
//
// Only the alarm rules are re-applied, and that is the honest scope rather than
// a partial implementation: the MIB catalog, the record store and the listening
// socket are all bound at startup, and pretending a reload could change them
// would be worse than saying it cannot. What an operator actually edits between
// restarts is a threshold.
//
// A file that will not parse or will not validate leaves the running rules
// exactly as they were. A reload that emptied the bell because of a typo would
// be a monitoring outage caused by the act of configuring monitoring.
func reloadOnSignal(ctx context.Context, server *api.Server) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP)
	defer signal.Stop(signals)

	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			cfg, err := config.Load()
			if err != nil {
				log.Printf("Reload refused, keeping the running rules: %v", err)
				continue
			}
			rules := alarm.RulesFrom(cfg.Alarms)
			server.ReloadAlarms(rules)
			log.Printf("Reloaded configuration: %d alarm rules", len(rules))
		}
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
