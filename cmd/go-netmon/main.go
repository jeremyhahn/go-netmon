// Command go-netmon runs the SNMP poller: it loads MIBs, polls the configured
// devices and writes the results to Prometheus.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sync/errgroup"

	"github.com/jeremyhahn/go-netmon/internal/api"
	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/metrics"
	"github.com/jeremyhahn/go-netmon/internal/mib/loader"
	"github.com/jeremyhahn/go-netmon/internal/mib/storage"
	"github.com/jeremyhahn/go-netmon/internal/poller"
)

const version = "0.1.0"

var (
	// Internal metrics for go-netmon itself
	pollCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "gonetmon_polls_total",
			Help: "Total number of SNMP polls performed",
		},
		[]string{"device", "status"},
	)
	pollDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "gonetmon_poll_duration_seconds",
			Help:    "Duration of SNMP polls",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"device"},
	)
	// gonetmon_exports_total and the other export health metrics
	// (gonetmon_export_last_success_timestamp_seconds,
	// gonetmon_export_buffered_samples, gonetmon_export_consecutive_failures,
	// gonetmon_export_stalled) are registered by internal/metrics, where the
	// exporter that actually knows the outcome of each flush lives.
)

func init() {
	// Register internal metrics
	prometheus.MustRegister(pollCounter)
	prometheus.MustRegister(pollDuration)
}

func main() {
	log.Printf("go-netmon v%s - SNMP Monitoring Service", version)

	// Check for WebUI sync mode
	webuiURL := os.Getenv("WEBUI_URL")
	syncEnabled := webuiURL != ""

	if syncEnabled {
		log.Printf("WebUI configuration sync enabled: %s", webuiURL)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	log.Printf("Loaded configuration: monitoring %d devices", len(cfg.Devices))

	if err := runUntilSignal(cfg, webuiURL); err != nil {
		log.Fatalf("Application error: %v", err)
	}

	log.Println("Shutdown complete")
}

// runUntilSignal owns the cancellable context so that main can exit through
// log.Fatal without stranding a deferred cancel.
func runUntilSignal(cfg *config.Config, webuiURL string) error {
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

	return run(ctx, cfg, webuiURL)
}

func run(ctx context.Context, cfg *config.Config, webuiURL string) error {
	// Ensure uploaded MIBs path is included in MIB search paths
	// This is shared with the WebUI via Docker volume
	uploadedMIBPath := "/app/data/uploaded"
	if envPath := os.Getenv("MIB_UPLOADED_PATH"); envPath != "" {
		uploadedMIBPath = envPath
	}

	// Add uploaded path if not already present
	hasUploadedPath := false
	for _, p := range cfg.MIB.Paths {
		if p == uploadedMIBPath {
			hasUploadedPath = true
			break
		}
	}
	if !hasUploadedPath {
		cfg.MIB.Paths = append(cfg.MIB.Paths, uploadedMIBPath)
		log.Printf("Added uploaded MIBs path to search paths: %s", uploadedMIBPath)
	}

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

	// Load all configured MIBs
	log.Println("Loading MIB modules...")
	if loadErr := mibLoader.Load(); loadErr != nil {
		return fmt.Errorf("failed to load MIBs: %w", loadErr)
	}
	log.Println("MIB modules loaded successfully")

	// Create Prometheus exporter
	log.Printf("Initializing Prometheus exporter (remote_write_url: %s)", cfg.Prometheus.RemoteWriteURL)
	flushInterval := time.Duration(cfg.Prometheus.FlushInterval) * time.Second

	// Get MIB storage for generic table index extraction
	// GetStorage boxes a concrete *storage.Storage, so the interface is never nil;
	// the assertion is what tells us whether a usable store came back.
	mibStorage, _ := mibLoader.GetStorage().(*storage.Storage)

	exporter, err := metrics.NewPrometheusExporter(
		cfg.Prometheus.RemoteWriteURL,
		cfg.Prometheus.BatchSize,
		flushInterval,
		mibStorage, // Pass MIB storage for generic index extraction
	)
	if err != nil {
		return fmt.Errorf("failed to create Prometheus exporter: %w", err)
	}
	defer func() {
		log.Println("Flushing and closing Prometheus exporter...")
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer flushCancel()

		if err := exporter.Flush(flushCtx); err != nil {
			log.Printf("Warning: failed to flush exporter: %v", err)
		}

		if err := exporter.Close(); err != nil {
			log.Printf("Error closing exporter: %v", err)
		}
	}()

	// Create poller
	log.Println("Initializing SNMP poller...")
	// Build the configured logger. The poller's per-OID tracing is gated on
	// it, so logging.level is what decides whether the polling path logs a
	// line per OID per poll or nothing at all.
	logger, logCloser, logErr := logging.New(cfg.Logging)
	if logErr != nil {
		return fmt.Errorf("failed to initialize logging: %w", logErr)
	}
	defer func() {
		if closeErr := logCloser.Close(); closeErr != nil {
			log.Printf("Error closing log file: %v", closeErr)
		}
	}()
	log.Printf("Logging initialized (level: %s, format: %s, output: %s)",
		cfg.Logging.Level, cfg.Logging.Format, cfg.Logging.Output)

	poll := poller.NewWithExporter(cfg, mibLoader, exporter, poller.WithLogger(logger))

	// If WebUI sync is enabled, fetch configuration from WebUI
	if webuiURL != "" {
		if err := syncConfigurationFromWebUI(ctx, poll, webuiURL); err != nil {
			log.Printf("Warning: failed to sync configuration from WebUI: %v", err)
			log.Println("Continuing with local configuration...")
		}
	}

	// Start poller
	log.Println("Starting SNMP poller...")
	if err := poll.Start(ctx); err != nil {
		return fmt.Errorf("failed to start poller: %w", err)
	}
	defer func() {
		log.Println("Stopping poller...")
		if err := poll.Stop(); err != nil {
			log.Printf("Error stopping poller: %v", err)
		}
	}()

	// Get enabled devices count
	enabledDevices := cfg.GetEnabledDevices()
	log.Printf("Poller started successfully with %d enabled devices", len(enabledDevices))

	// Create poller handler with status tracker (needed for processPollResults)
	pollerHandler := api.NewPollerControlHandler(poll, cfg)
	statusTracker := pollerHandler.GetStatusTracker()

	// Start background goroutine to process poll results
	go processPollResults(ctx, poll, statusTracker)

	// Use errgroup for managing HTTP servers
	g, gCtx := errgroup.WithContext(ctx)

	// Start metrics HTTP server
	metricsServer := &http.Server{
		Addr:    cfg.Metrics.ListenAddress,
		Handler: promhttp.Handler(),
	}

	g.Go(func() error {
		log.Printf("Starting metrics server on %s", cfg.Metrics.ListenAddress)
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("metrics server error: %w", err)
		}
		return nil
	})

	// Shutdown metrics server on context cancellation
	g.Go(func() error {
		<-gCtx.Done()
		log.Println("Shutting down metrics server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return metricsServer.Shutdown(shutdownCtx)
	})

	// Start API server
	apiMux := http.NewServeMux()

	// Health check endpoint
	apiMux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"healthy","version":"%s"}`, version)
	})

	// Dashboard endpoints
	dashboardHandler := NewDashboardHandler(mibLoader, "/app/dashboards")
	apiMux.HandleFunc("/api/dashboards/generate", dashboardHandler.HandleGenerate)
	apiMux.HandleFunc("/api/dashboards/list", dashboardHandler.HandleList)

	// Poller control endpoints (pollerHandler created earlier for status tracking)
	apiMux.HandleFunc("/api/poller/devices", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			pollerHandler.HandleListDevices(w, r)
		case http.MethodPost:
			pollerHandler.HandleRegisterDevice(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Device-specific endpoints (PUT, DELETE, GET status)
	apiMux.HandleFunc("/api/poller/devices/", func(w http.ResponseWriter, r *http.Request) {
		// Check if this is a status request
		if strings.HasSuffix(r.URL.Path, "/status") {
			if r.Method == http.MethodGet {
				pollerHandler.HandleGetDeviceStatus(w, r)
			} else {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
			return
		}

		// Otherwise it's a device operation
		switch r.Method {
		case http.MethodPut:
			pollerHandler.HandleUpdateDevice(w, r)
		case http.MethodDelete:
			pollerHandler.HandleRemoveDevice(w, r)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Device endpoints (placeholder for future implementation)
	apiMux.HandleFunc("/api/devices", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		devices := cfg.GetEnabledDevices()
		fmt.Fprintf(w, `{"devices":%d}`, len(devices))
	})

	// OID Learner endpoint - displays dynamically discovered tables
	apiMux.HandleFunc("/api/oid-learner/tables", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if learner := exporter.GetOIDLearner(); learner != nil {
			tables := learner.GetAllLearnedTables()
			stats := learner.GetStats()

			// Encode response
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"tables": tables,
				"stats":  stats,
			}); err != nil {
				http.Error(w, "Failed to encode response", http.StatusInternalServerError)
			}
		} else {
			fmt.Fprintf(w, `{"tables":{},"stats":{"message":"OID learner not available"}}`)
		}
	})

	apiServer := &http.Server{
		Addr:    cfg.API.ListenAddress,
		Handler: apiMux,
	}

	g.Go(func() error {
		log.Printf("Starting API server on %s", cfg.API.ListenAddress)
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("API server error: %w", err)
		}
		return nil
	})

	// Shutdown API server on context cancellation
	g.Go(func() error {
		<-gCtx.Done()
		log.Println("Shutting down API server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return apiServer.Shutdown(shutdownCtx)
	})

	// Wait for all goroutines to complete
	if err := g.Wait(); err != nil {
		return fmt.Errorf("server error: %w", err)
	}

	return nil
}

// syncConfigurationFromWebUI fetches configuration from WebUI and applies it to the poller
func syncConfigurationFromWebUI(ctx context.Context, poll *poller.Poller, webuiURL string) error {
	log.Printf("Synchronizing configuration from WebUI: %s", webuiURL)

	// Create config sync client
	syncClient := poller.NewConfigSyncClient(webuiURL, 30*time.Second)

	// Fetch configuration with retry
	syncConfig, err := syncClient.SyncConfiguration(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch configuration: %w", err)
	}

	// Apply configuration to poller
	if err := poller.ApplyConfiguration(ctx, poll, syncConfig); err != nil {
		return fmt.Errorf("failed to apply configuration: %w", err)
	}

	log.Printf("Successfully synchronized %d devices from WebUI", len(syncConfig.Devices))
	return nil
}

// processPollResults processes polling results in the background
func processPollResults(ctx context.Context, poll *poller.Poller, statusTracker *api.StatusTracker) {
	resultChan := poll.Results()

	for {
		select {
		case <-ctx.Done():
			return
		case result, ok := <-resultChan:
			if !ok {
				// Channel closed
				return
			}

			// Process the result
			if result.Error != nil {
				pollCounter.WithLabelValues(result.DeviceName, "error").Inc()
				log.Printf("Poll error for device %s: %v", result.DeviceName, result.Error)

				// Update status tracker with failure
				if statusTracker != nil {
					statusTracker.UpdatePollError(result.DeviceName, result.Error)
				}
			} else {
				pollCounter.WithLabelValues(result.DeviceName, "success").Inc()

				// Calculate poll duration
				duration := time.Since(result.Timestamp).Seconds()
				pollDuration.WithLabelValues(result.DeviceName).Observe(duration)

				log.Printf("Poll successful for device %s: %d results in %.2fs",
					result.DeviceName, len(result.Results), duration)

				// Update status tracker with success
				if statusTracker != nil {
					statusTracker.UpdatePollSuccess(result.DeviceName, len(result.Results))
				}

				// Note: metric export success/failure is tracked by the
				// exporter itself (gonetmon_exports_total, registered in
				// internal/metrics), not here -- a successful SNMP poll
				// does not imply the results were actually exported to
				// Prometheus.
			}
		}
	}
}
