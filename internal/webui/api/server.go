// Package api implements the web UI's HTTP API: devices, profiles, MIB
// assignments, metrics queries and the configuration the poller consumes.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// PollerClient defines the interface for interacting with the poller service
type PollerClient interface {
	RegisterDevice(ctx context.Context, device *storage.Device, mibNames []string) error
	UpdateDevice(ctx context.Context, device *storage.Device, mibNames []string) error
	RemoveDevice(ctx context.Context, deviceName string) error
	GetDeviceStatus(ctx context.Context, deviceName string) (*client.DeviceStatus, error)
	ListDevices(ctx context.Context) ([]client.DeviceInfo, error)
}

// Server represents the Web UI server
type Server struct {
	router          *mux.Router
	hub             *ws.Hub
	queryFunc       ws.QueryFunc
	mibProvider     MIBProvider
	mibLoader       MIBLoader
	storage         *storage.Storage
	mibStoragePath  string
	dbPath          string
	configBackups   string
	pollerClient    PollerClient
	promClient      *PrometheusClient
	goNetmonBaseURL string
	upgrader        websocket.Upgrader

	// pollingConfig is the poller's global timing. It is what lets a device
	// that inherits the default report the interval it is really polled at.
	// Read it through s.polling(), which fills in a zero value.
	pollingConfig PollingConfig

	// logger carries the server's level-gated diagnostics. Query tracing runs
	// once per metric returned, so it must cost nothing unless debug logging
	// is on.
	logger *slog.Logger
}

// MIBLoader provides MIB loading capabilities
type MIBLoader interface {
	LoadModule(moduleName string) error
	GetParser() interface{}  // Returns *parser.Parser (actual concrete type)
	GetStorage() interface{} // Returns *storage.Storage (actual concrete type)
}

// MIBProvider provides MIB information
type MIBProvider interface {
	ListMIBs() ([]MIBInfo, error)
	GetMIB(name string) (*MIBDetail, error)
}

// MIBInfo represents basic MIB information
type MIBInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ObjectCount int    `json:"objectCount"`
}

// MIBDetail represents detailed MIB information
type MIBDetail struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Objects     []MIBObject `json:"objects"`
}

// MIBObject represents a single MIB object.
//
// Beyond identity, it carries the metadata a client needs to render a raw SNMP
// value as text — the enumeration labels, the DISPLAY-HINT and the units — so
// the CLI and the web UI can format values without a second round trip or a
// private copy of the MIB.
type MIBObject struct {
	OID         string `json:"oid"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Unit        string `json:"unit,omitempty"`
	Pollable    bool   `json:"pollable"`

	Access      string           `json:"access,omitempty"`
	Status      string           `json:"status,omitempty"`
	DisplayHint string           `json:"display_hint,omitempty"`
	EnumValues  map[int64]string `json:"enum_values,omitempty"`
	IsScalar    bool             `json:"is_scalar,omitempty"`
	IsColumn    bool             `json:"is_column,omitempty"`
	IsTable     bool             `json:"is_table,omitempty"`
	IsIndex     bool             `json:"is_index,omitempty"`
	TableOID    string           `json:"table_oid,omitempty"`

	// Index names the columns of a conceptual row's INDEX clause, in INDEX
	// order. It is present only on a table entry, so it costs a few entries per
	// module rather than a field on every object.
	//
	// It is what lets a client tell a series' genuine index labels from the
	// enrichment labels the exporter attaches beside them — ifName, ifDescr and
	// the rest. The exporter labels a sample with the lowercased name of each
	// INDEX column (internal/metrics/table_index_cache.go), so a client holding
	// this list knows exactly which labels are the row's identity and needs no
	// list of label names to ignore.
	Index []string `json:"index,omitempty"`
}

// newMIBObject converts stored OID metadata to its wire representation.
func newMIBObject(oid *mib.OIDMetadata) MIBObject {
	return MIBObject{
		OID:         oid.OID,
		Name:        oid.Name,
		Type:        oid.Type,
		Description: oid.Description,
		Unit:        oid.Units,
		Pollable:    oid.Pollable,
		Access:      oid.Access,
		Status:      oid.Status,
		DisplayHint: oid.DisplayHint,
		EnumValues:  oid.EnumValues,
		IsScalar:    oid.IsScalar,
		IsColumn:    oid.IsColumn,
		IsTable:     oid.IsTable,
		IsIndex:     oid.IsIndex,
		TableOID:    oid.TableOID,
		Index:       oid.Index,
	}
}

// Config represents server configuration
type Config struct {
	QueryFunc      ws.QueryFunc
	MIBProvider    MIBProvider
	MIBLoader      MIBLoader
	DBPath         string
	MIBStoragePath string

	// ConfigBackupDir is where the configuration importer saves the
	// configuration it is about to replace. Empty puts it beside the record
	// store, which on a deployment is the web UI's own data volume.
	ConfigBackupDir string

	PollerClient    PollerClient
	PromClient      *PrometheusClient
	GoNetmonBaseURL string // Base URL for go-netmon service (e.g., http://localhost:9080)

	// Polling carries the poller's global timing settings. A zero value falls
	// back to the same defaults internal/config applies.
	Polling PollingConfig

	// Logger receives the server's diagnostics. A nil Logger discards them.
	Logger *slog.Logger
}

// NewServer creates a new API server
func NewServer(cfg Config) (*Server, error) {
	// Initialize storage
	store, err := storage.New(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	logger := logging.OrDiscard(cfg.Logger)
	hub := ws.NewHub(cfg.QueryFunc, logger)

	s := &Server{
		router:          mux.NewRouter(),
		hub:             hub,
		queryFunc:       cfg.QueryFunc,
		mibProvider:     cfg.MIBProvider,
		mibLoader:       cfg.MIBLoader,
		storage:         store,
		mibStoragePath:  cfg.MIBStoragePath,
		dbPath:          cfg.DBPath,
		configBackups:   cfg.ConfigBackupDir,
		pollerClient:    cfg.PollerClient,
		promClient:      cfg.PromClient,
		goNetmonBaseURL: cfg.GoNetmonBaseURL,
		pollingConfig:   cfg.Polling,
		logger:          logger,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(_ *http.Request) bool {
				return true // Allow all origins in development
			},
		},
	}

	s.routes()
	return s, nil
}

// discardLogger is the sink a Server built without one logs to, so that the
// zero value is usable and no call site has to nil-check.
var discardLogger = logging.Discard()

// log returns the server's logger, or a discarding one when none was injected.
func (s *Server) log() *slog.Logger {
	if s.logger == nil {
		return discardLogger
	}
	return s.logger
}

// noCacheMiddleware adds no-cache headers to prevent browser caching
func noCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// routes sets up all HTTP routes
func (s *Server) routes() {
	// WebSocket endpoint
	s.router.HandleFunc("/ws", s.handleWebSocket)

	// API routes
	api := s.router.PathPrefix("/api").Subrouter()

	// Poller configuration endpoint
	api.HandleFunc("/poller/config", s.handleGetPollerConfig).Methods("GET")

	// Configuration export and import. The import is a dry run unless it is
	// asked to apply, because applying replaces the whole configuration.
	api.HandleFunc("/config/export", s.handleExportConfig).Methods("GET")
	api.HandleFunc("/config/import", s.handleImportConfig).Methods("POST")

	// Device CRUD endpoints (specific routes first)
	api.HandleFunc("/devices", s.handleListDevices).Methods("GET")
	api.HandleFunc("/devices", s.handleCreateDevice).Methods("POST")
	api.HandleFunc("/devices/{id:[0-9]+}", s.handleGetDevice).Methods("GET")
	api.HandleFunc("/devices/{id:[0-9]+}", s.handleUpdateDevice).Methods("PUT")
	api.HandleFunc("/devices/{id:[0-9]+}", s.handleDeleteDevice).Methods("DELETE")

	// Device status endpoints. /devices/status is the collection's status, the
	// bulk counterpart of /devices/{id}/status, and answers a fleet overview in
	// one request instead of one per device.
	api.HandleFunc("/devices/{id:[0-9]+}/status", s.handleGetDeviceStatus).Methods("GET")
	api.HandleFunc("/devices/status", s.handleFleetStatus).Methods("GET")
	api.HandleFunc("/devices/status/{name}", s.handleGetDeviceStatusByName).Methods("GET")

	// SNMP connection test endpoints
	api.HandleFunc("/devices/{id:[0-9]+}/test-snmp", s.handleTestSNMPConnection).Methods("POST")
	api.HandleFunc("/snmp/test", s.handleTestSNMPDirect).Methods("POST")

	// Device MIB associations
	api.HandleFunc("/devices/{id:[0-9]+}/mibs", s.handleGetDeviceMIBs).Methods("GET")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs", s.handleAssignMIBToDevice).Methods("POST")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs/{mibId:[0-9]+}", s.handleRemoveMIBFromDevice).Methods("DELETE")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs/{mibId:[0-9]+}/assignment", s.handleGetDeviceMIBAssignment).Methods("GET")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs/{mibId:[0-9]+}/assignment", s.handleSetDeviceMIBAssignment).Methods("PUT")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs/{mibId:[0-9]+}/estimate", s.handleEstimateMIBSeries).Methods("GET")
	api.HandleFunc("/devices/{id:[0-9]+}/mibs/{mibId:[0-9]+}/objects", s.handleGetDeviceMIBObjects).Methods("GET")

	// Device profile associations
	api.HandleFunc("/devices/{id:[0-9]+}/profiles", s.handleGetDeviceProfiles).Methods("GET")
	api.HandleFunc("/devices/{deviceId:[0-9]+}/profiles/{profileId:[0-9]+}", s.handleAssignProfileToDevice).Methods("POST")
	api.HandleFunc("/devices/{deviceId:[0-9]+}/profiles/{profileId:[0-9]+}", s.handleRemoveProfileFromDevice).Methods("DELETE")

	// Device profile CRUD endpoints
	api.HandleFunc("/profiles", s.handleListProfiles).Methods("GET")
	api.HandleFunc("/profiles", s.handleCreateProfile).Methods("POST")
	api.HandleFunc("/profiles/{id:[0-9]+}", s.handleGetProfile).Methods("GET")
	api.HandleFunc("/profiles/{id:[0-9]+}", s.handleUpdateProfile).Methods("PUT")
	api.HandleFunc("/profiles/{id:[0-9]+}", s.handleDeleteProfile).Methods("DELETE")

	// Profile MIB associations
	api.HandleFunc("/profiles/{id:[0-9]+}/mibs", s.handleGetProfileMIBs).Methods("GET")
	api.HandleFunc("/profiles/{id:[0-9]+}/mibs", s.handleAssignMIBToProfile).Methods("POST")
	api.HandleFunc("/profiles/{id:[0-9]+}/mibs/{mibId:[0-9]+}", s.handleRemoveMIBFromProfile).Methods("DELETE")
	api.HandleFunc("/profiles/{id:[0-9]+}/mibs/{mibId:[0-9]+}/assignment", s.handleGetProfileMIBAssignment).Methods("GET")
	api.HandleFunc("/profiles/{id:[0-9]+}/mibs/{mibId:[0-9]+}/assignment", s.handleSetProfileMIBAssignment).Methods("PUT")
	api.HandleFunc("/profiles/{id:[0-9]+}/devices", s.handleGetProfileDevices).Methods("GET")

	// MIB endpoints (IMPORTANT: Specific routes MUST be registered BEFORE general routes!)
	api.HandleFunc("/mibs/import", s.handleImportMIB).Methods("POST")
	api.HandleFunc("/mibs", s.handleListStoredMIBs).Methods("GET") // Database-based

	// ID-based routes (numeric) MUST come before name-based routes
	api.HandleFunc("/mibs/{id:[0-9]+}", s.handleGetStoredMIB).Methods("GET")
	api.HandleFunc("/mibs/{id:[0-9]+}", s.handleDeleteStoredMIB).Methods("DELETE")
	api.HandleFunc("/mibs/{id:[0-9]+}/tags", s.handleUpdateMIBTags).Methods("PUT")
	api.HandleFunc("/mibs/{id:[0-9]+}/tables", s.handleGetMIBTables).Methods("GET")       // Table metadata
	api.HandleFunc("/mibs/{id:[0-9]+}/dashboard", s.handleGetMIBDashboard).Methods("GET") // Dashboard generation

	// Name-based route (will match any string) - MUST be last
	api.HandleFunc("/mibs/{name}", s.handleGetMIB).Methods("GET") // Legacy provider-based

	// Dashboard generation endpoints
	api.HandleFunc("/dashboards/generate", s.handleGenerateDashboard).Methods("POST")
	api.HandleFunc("/devices/{id:[0-9]+}/dashboard", s.handleGenerateDeviceDashboard).Methods("GET")

	// Metrics query endpoints
	api.HandleFunc("/metrics/query", s.handleMetricsQuery).Methods("POST")        // Time-range query
	api.HandleFunc("/metrics/instant", s.handleQueryMetrics).Methods("POST")      // Instant query (legacy)
	api.HandleFunc("/metrics/system-info", s.handleGetSystemInfo).Methods("POST") // System info query

	// OID Learner endpoint - proxies to go-netmon service
	api.HandleFunc("/learned-tables", s.handleGetLearnedTables).Methods("GET")

	// Static files with no-cache headers
	staticFS := http.FileServer(http.Dir("internal/webui/static"))
	s.router.PathPrefix("/").Handler(noCacheMiddleware(staticFS))
}

// Start starts the server
func (s *Server) Start(ctx context.Context, addr string) error {
	// Start the WebSocket hub
	go s.hub.Run(ctx)

	server := newHTTPServer(addr, s.router)

	log.Printf("Web UI server starting on %s", addr)

	// #nosec G118 -- the shutdown deadline must not inherit ctx: this goroutine
	// only wakes once ctx is already canceled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("Web UI server shutdown: %v", err)
		}
	}()

	return server.ListenAndServe()
}

// newHTTPServer builds the listener the web UI serves on.
//
// The deadlines come from the documented budget rather than from literals here
// so that the ordering the handlers rely on is stated in one place and checked
// by one test.
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  ServerReadTimeout,
		WriteTimeout: ServerWriteTimeout,
	}
}

// Close closes the server and its resources
func (s *Server) Close() error {
	return s.storage.Close()
}

// LoadStoredMIBs loads all MIBs from the database into the MIB storage
// This ensures that MIBs uploaded via the web UI are available for dashboard generation
func (s *Server) LoadStoredMIBs(ctx context.Context) error {
	// Get all MIBs from database
	mibs, err := s.storage.ListMIBs(ctx)
	if err != nil {
		return fmt.Errorf("failed to list MIBs: %w", err)
	}

	if len(mibs) == 0 {
		log.Println("No MIBs found in database")
		return nil
	}

	log.Printf("Found %d MIBs in database, loading into storage...", len(mibs))

	successCount := 0
	var loadErrors []string

	for _, mib := range mibs {
		if s.mibLoader != nil {
			if err := s.mibLoader.LoadModule(mib.Name); err != nil {
				errMsg := fmt.Sprintf("failed to load MIB module %s: %v", mib.Name, err)
				log.Printf("Warning: %s", errMsg)
				loadErrors = append(loadErrors, errMsg)
			} else {
				log.Printf("Successfully loaded MIB module %s into storage", mib.Name)
				successCount++
			}
		}
	}

	log.Printf("Loaded %d of %d MIBs into storage", successCount, len(mibs))

	if len(loadErrors) > 0 {
		log.Printf("Encountered %d errors during MIB loading (see warnings above)", len(loadErrors))
	}

	return nil
}

// LoadAndRegisterDevices loads all devices from the database and registers them with the poller
func (s *Server) LoadAndRegisterDevices(ctx context.Context) error {
	// List all devices from storage
	devices, err := s.storage.ListDevices(ctx)
	if err != nil {
		return fmt.Errorf("failed to list devices: %w", err)
	}

	if len(devices) == 0 {
		log.Println("No devices found in database")
		return nil
	}

	log.Printf("Found %d devices in database, registering with poller...", len(devices))

	successCount := 0
	for _, device := range devices {
		// Modules the device polls: direct and profile assignments resolved,
		// with disabled MIBs left out.
		mibNames, err := s.getDeviceMIBNames(ctx, device.ID)
		if err != nil {
			log.Printf("Warning: failed to get MIBs for device %s: %v", device.Name, err)
			continue
		}

		// Register device with poller
		if err := s.pollerClient.RegisterDevice(ctx, &device, mibNames); err != nil {
			log.Printf("Warning: failed to register device %s with poller: %v", device.Name, err)
			continue
		}

		log.Printf("Registered device %s with %d MIBs", device.Name, len(mibNames))
		successCount++
	}

	log.Printf("Successfully registered %d/%d devices with poller", successCount, len(devices))
	return nil
}

// handleWebSocket upgrades HTTP to WebSocket
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	wsClient := ws.NewClient(s.hub, conn)
	s.hub.RegisterClient(wsClient)

	go wsClient.WritePump()
	go wsClient.ReadPump()
}

// handleGetMIB returns detailed MIB information (legacy provider-based)
func (s *Server) handleGetMIB(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	name := vars["name"]

	detail, err := s.mibProvider.GetMIB(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, detail)
}

// QueryRequest represents a metrics query request
type QueryRequest struct {
	Query string `json:"query"`
}

// LabelValuesQuery represents a parsed label_values() query
type LabelValuesQuery struct {
	LabelName string
	Matchers  map[string]string
}

// friendlyNameLabels maps the info metric of each interface-naming column to
// the label its value is presented under. The names are derived rather than
// written out so they cannot drift from what the exporter emits.
var friendlyNameLabels = func() map[string]string {
	columns := []string{"ifName", "ifDescr", "ifAlias"}
	labels := make(map[string]string, len(columns))
	for _, column := range columns {
		labels[promname.InfoMetricName(column)] = promname.IndexLabel(column)
	}
	return labels
}()

// parseLabelValuesQuery parses a Grafana-style label_values() query
// Format: label_values({device="core-01"}, ifindex)
// Returns nil if not a label_values query
func parseLabelValuesQuery(query string) *LabelValuesQuery {
	query = strings.TrimSpace(query)

	// Check if it starts with "label_values("
	if !strings.HasPrefix(query, "label_values(") {
		return nil
	}

	// Remove "label_values(" prefix and ")" suffix
	query = strings.TrimPrefix(query, "label_values(")
	query = strings.TrimSuffix(query, ")")

	// Split by comma to get matchers and label name
	parts := strings.Split(query, ",")
	if len(parts) != 2 {
		return nil
	}

	matchersStr := strings.TrimSpace(parts[0])
	labelName := strings.TrimSpace(parts[1])

	// Parse matchers: {device="core-01"}
	matchers := make(map[string]string)
	if strings.HasPrefix(matchersStr, "{") && strings.HasSuffix(matchersStr, "}") {
		matchersStr = strings.TrimPrefix(matchersStr, "{")
		matchersStr = strings.TrimSuffix(matchersStr, "}")

		// Simple parser for key="value" pairs
		matcherPairs := strings.Split(matchersStr, ",")
		for _, pair := range matcherPairs {
			pair = strings.TrimSpace(pair)
			if idx := strings.Index(pair, "="); idx > 0 {
				key := strings.TrimSpace(pair[:idx])
				value := strings.TrimSpace(pair[idx+1:])
				value = strings.Trim(value, "\"")
				matchers[key] = value
			}
		}
	}

	return &LabelValuesQuery{
		LabelName: labelName,
		Matchers:  matchers,
	}
}

// handleQueryMetrics executes a Prometheus query
func (s *Server) handleQueryMetrics(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if this is a label_values() query (Grafana templating syntax)
	// Example: label_values({device="core-01"}, ifindex)
	if labelValues := parseLabelValuesQuery(req.Query); labelValues != nil {
		s.log().DebugContext(r.Context(), "label_values query detected",
			"label", labelValues.LabelName, "matchers", labelValues.Matchers)

		// Build PromQL query for info metrics with the matchers
		var matchParts []string
		for k, v := range labelValues.Matchers {
			matchParts = append(matchParts, fmt.Sprintf("%s=%q", k, v))
		}
		promQuery := fmt.Sprintf("{__name__=~%q,%s}", promname.Prefix+".*_info", strings.Join(matchParts, ","))

		s.log().DebugContext(r.Context(), "executing instant query", "query", promQuery)

		// Use instant query which returns current metrics
		metrics, err := s.queryFunc(r.Context(), promQuery)
		if err != nil {
			log.Printf("ERROR: instant query failed: %v", err)
			http.Error(w, fmt.Sprintf("failed to query metrics: %v", err), http.StatusInternalServerError)
			return
		}

		s.log().DebugContext(r.Context(), "instant query returned", "metrics", len(metrics))

		// Extract unique instances based on the requested label, preserving all other labels
		// Use a map keyed by the target label value to merge labels from multiple metrics
		instanceMap := make(map[string]map[string]string)

		metricsWithLabel := 0
		for _, metricValue := range metrics {
			labelValue, exists := metricValue.Metric[labelValues.LabelName]
			if !exists {
				continue
			}
			metricsWithLabel++

			// Get or create the label map for this instance
			existing, found := instanceMap[labelValue]
			if !found {
				// First time seeing this instance - copy all labels
				instanceMap[labelValue] = make(map[string]string)
				for k, v := range metricValue.Metric {
					instanceMap[labelValue][k] = v
				}
			} else {
				// Merge labels from this metric into existing map
				// This allows us to combine ifdescr from ifTable with ifname from ifXTable
				for k, v := range metricValue.Metric {
					// Only add if not already present (keep first value seen)
					if _, hasKey := existing[k]; !hasKey {
						existing[k] = v
					}
				}
			}

			// An info metric carries its column's value in the "value"
			// label, so lift it onto the instance under the column's own
			// label for the frontend to display.
			if label, isInfo := friendlyNameLabels[metricValue.Metric[promname.NameLabel]]; isInfo {
				if value, hasValue := metricValue.Metric["value"]; hasValue {
					instanceMap[labelValue][label] = value
				}
			}
		}

		// Convert map to slice of metrics, ordered by the instance identifier.
		// In map order the instance picker this feeds is rebuilt in a different
		// order on every request, which moves the entry the page selects by
		// default from one interface to another.
		result := make([]map[string]interface{}, 0, len(instanceMap))
		for _, instance := range slices.SortedFunc(maps.Keys(instanceMap), compareInstance) {
			result = append(result, map[string]interface{}{
				"metric": instanceMap[instance],
			})
		}

		s.log().DebugContext(r.Context(), "label_values response",
			"scanned", len(metrics), "with_label", metricsWithLabel,
			"label", labelValues.LabelName, "instances", len(result))

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"metrics": result,
		})
		return
	}

	// Regular PromQL query
	metrics, err := s.queryFunc(r.Context(), req.Query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"metrics": metrics,
	})
}

// handleGetLearnedTables proxies requests to the go-netmon service's OID learner endpoint
func (s *Server) handleGetLearnedTables(w http.ResponseWriter, r *http.Request) {
	// If no go-netmon base URL configured, return empty response
	if s.goNetmonBaseURL == "" {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"tables": map[string]interface{}{},
			"stats": map[string]interface{}{
				"message": "OID learner not available - go-netmon base URL not configured",
			},
		})
		return
	}

	// Build request URL
	url := fmt.Sprintf("%s/api/oid-learner/tables", s.goNetmonBaseURL)

	// Create HTTP client with timeout
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Create request with context
	req, err := http.NewRequestWithContext(r.Context(), "GET", url, http.NoBody)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create request: %v", err), http.StatusInternalServerError)
		return
	}

	// Execute request
	resp, err := httpClient.Do(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to fetch learned tables: %v", err), http.StatusBadGateway)
		return
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	// Check status code
	if resp.StatusCode != http.StatusOK {
		http.Error(w, fmt.Sprintf("go-netmon service returned status %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	// Proxy the response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	// Copy response body
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("Error copying response body: %v", err)
	}
}
