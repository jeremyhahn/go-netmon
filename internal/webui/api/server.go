// Package api implements the web UI's HTTP API: devices, profiles, MIB
// assignments, metrics queries and the configuration the poller consumes.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/config"
	"github.com/jeremyhahn/go-netmon/internal/logging"
	"github.com/jeremyhahn/go-netmon/internal/mib"
	"github.com/jeremyhahn/go-netmon/internal/promname"
	"github.com/jeremyhahn/go-netmon/internal/webui/alarm"
	"github.com/jeremyhahn/go-netmon/internal/webui/client"
	"github.com/jeremyhahn/go-netmon/internal/webui/storage"
	ws "github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// PollerClient defines the interface for interacting with the poller service
type PollerClient interface {
	RegisterDevice(ctx context.Context, device *storage.Device,
		mibNames []string, filters map[string]config.ModuleFilter) error
	UpdateDevice(ctx context.Context, device *storage.Device,
		mibNames []string, filters map[string]config.ModuleFilter) error
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

	// pollingConfig is the poller's global timing. It is what lets a device
	// that inherits the default report the interval it is really polled at.
	// Read it through s.polling(), which fills in a zero value.
	pollingConfig PollingConfig

	// logger carries the server's level-gated diagnostics. Query tracing runs
	// once per metric returned, so it must cost nothing unless debug logging
	// is on.
	logger *slog.Logger

	// alarms evaluates the operator's rules and holds what is firing. Nil on
	// a server built without any, which the bell endpoint reports as such
	// rather than as an all-clear.
	alarms *alarm.Evaluator

	// assets is the embedded static tree this server serves the UI from.
	assets fs.FS

	// catalog holds the metric catalog between reads, so a dashboard load is
	// not behind four Prometheus queries and 26,000 rows of JSON.
	catalog *catalogCache
}

// MIBLoader provides MIB loading capabilities
type MIBLoader interface {
	// LoadModule parses a module and replaces whatever the store holds for it.
	// It is the explicit reload: what an import or a re-import performs.
	LoadModule(moduleName string) error

	// EnsureModuleCurrent parses a module only if the store does not already
	// hold a copy at least as new as the file it came from. It is a startup
	// operation: the store outlives the process, so a restart should pay for
	// what has changed and nothing else.
	EnsureModuleCurrent(moduleName string) error

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

	// StreamInterval is how often the websocket hub pushes a round of updates.
	// Zero uses websocket.DefaultStreamInterval.
	StreamInterval time.Duration

	// Logger receives the server's diagnostics. A nil Logger discards them.
	Logger *slog.Logger

	// AlarmRules are the conditions to watch for. An empty set builds an
	// evaluator that fires nothing, which is different from no evaluator at
	// all: the first is an operator who has written no rules, the second is a
	// build with no bell.
	AlarmRules []alarm.Rule

	// Assets is the static asset tree the UI is served from, rooted so that
	// the landing page is "index.html".
	//
	// It is a dependency rather than something this package reaches for,
	// because the assets are compiled into the binary (internal/webui) and
	// the binary is what decides to serve them. A nil Assets serves no UI at
	// all -- deliberately, since the alternative is a fallback to some path on
	// disk, and a path on disk is the bug this replaced: the UI used to work
	// only when the process happened to be running from a source checkout.
	Assets fs.FS
}

// NewServer creates a new API server
func NewServer(cfg Config) (*Server, error) {
	// Initialize storage
	store, err := storage.New(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	logger := logging.OrDiscard(cfg.Logger)
	hub := ws.NewHub(cfg.QueryFunc, logger, ws.WithStreamInterval(cfg.StreamInterval))

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
		assets:          cfg.Assets,
	}

	// The evaluator needs the server to reach Prometheus, so it is built after
	// it. A server with no Prometheus client has nothing to evaluate against
	// and gets no evaluator, which the bell reports as such.
	if cfg.PromClient != nil {
		s.alarms = alarm.New(s.alarmSampler, cfg.AlarmRules, logger)
	}
	s.catalog = newCatalogCache(s.metricCatalog)

	s.routes()
	return s, nil
}

// Alarms is the running alarm evaluator, or nil on a server built without one.
// The caller runs its evaluation loop; the server only serves what it holds.
func (s *Server) Alarms() *alarm.Evaluator { return s.alarms }

// RefreshCatalog re-reads the metric catalog.
//
// Called when something has changed what it can describe -- a MIB imported,
// reimported or deleted -- so the next dashboard describes the metrics that
// module produces instead of waiting out the refresh interval.
func (s *Server) RefreshCatalog(ctx context.Context) {
	if s.catalog != nil {
		s.catalog.Refresh(ctx)
	}
}

// RunCatalogRefresh keeps the held metric catalog warm until ctx is done.
//
// The caller runs it, as it runs the alarm evaluator, so that what has a
// lifetime is started by whatever owns that lifetime rather than by the
// constructor.
func (s *Server) RunCatalogRefresh(ctx context.Context) {
	if s.catalog != nil {
		s.catalog.Run(ctx)
	}
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
	// WebSocket endpoint. Registered from websocket.go or websocket_fips.go:
	// the handshake RFC 6455 specifies is a SHA-1 digest, so the real handler
	// is absent from FIPS builds and a declaring one takes its place.
	s.registerWebSocketRoute()

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
	// Walking answers "what does this box actually expose?", which is what
	// decides whether a MIB is worth importing and why a polled object is empty.
	api.HandleFunc("/devices/{id:[0-9]+}/walk", s.handleWalkDevice).Methods("GET")
	api.HandleFunc("/snmp/walk", s.handleWalkDirect).Methods("POST")

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

	// What is wrong right now, for the bell.
	api.HandleFunc("/alarms", s.handleGetAlarms).Methods("GET")

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
	// The page and everything on it, served from the assets compiled into the
	// binary rather than from a path relative to the working directory.
	//
	// A server built without assets registers no catch-all at all, so an
	// unknown path gets the router's 404 rather than a handler that will
	// answer nothing. That is only reachable in tests; the binaries always
	// pass the embedded tree.
	if s.assets != nil {
		s.router.PathPrefix("/").Handler(
			noCacheMiddleware(http.FileServer(http.FS(s.assets))))
	}
}

// Listen binds addr without serving on it.
//
// Binding is separated from serving because the moment the socket accepts is
// the moment the service becomes usable, and that moment has to be observable
// from outside the process: it is what the readiness notification to systemd
// reports. A caller that binds and then serves can say "ready" exactly once,
// between the two, with nothing left in the startup path that could still fail.
//
// It also moves the one startup failure worth failing the service for — the
// port is already taken — to a returned error, rather than leaving it to
// surface as a serve error minutes into a start.
//
// ctx governs the bind itself, not the listener's lifetime: a bind that cannot
// complete is abandoned with the context, and the returned listener outlives it.
func (s *Server) Listen(ctx context.Context, addr string) (net.Listener, error) {
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, &ListenError{Addr: addr, Err: err}
	}
	return listener, nil
}

// Serve serves the API on an already-bound listener until ctx is canceled.
//
// The shutdown below closes the listener, so a caller that hands one over must
// not close it itself.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	// Start the WebSocket hub
	go s.hub.Run(ctx)

	server := newHTTPServer(listener.Addr().String(), s.router)

	log.Printf("Web UI server serving on %s", listener.Addr())

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

	return server.Serve(listener)
}

// Start binds addr and serves on it: Listen followed by Serve, for a caller
// with nothing to do in between.
func (s *Server) Start(ctx context.Context, addr string) error {
	listener, err := s.Listen(ctx, addr)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
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

	// EnsureModuleCurrent, not LoadModule. This loop is the whole cold start:
	// on monitor-01 it reparsed all 176 imported modules on every restart, which is
	// the 203 seconds the web UI spent before it bound its port. The parsed
	// catalog is in Pebble and survives the process, so there is nothing to
	// rebuild unless a MIB file has been edited — and those are still reparsed.
	//
	// This runs once, from the startup path in cmd/webui, which is the
	// precondition EnsureModuleCurrent documents.
	for _, mib := range mibs {
		if s.mibLoader != nil {
			if err := s.mibLoader.EnsureModuleCurrent(mib.Name); err != nil {
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
		modules, err := s.getDeviceModules(ctx, device.ID)
		if err != nil {
			log.Printf("Warning: failed to get MIBs for device %s: %v", device.Name, err)
			continue
		}

		// Register device with poller
		if err := s.pollerClient.RegisterDevice(ctx, &device, modules.Modules, modules.Filters); err != nil {
			log.Printf("Warning: failed to register device %s with poller: %v", device.Name, err)
			continue
		}

		log.Printf("Registered device %s with %d MIBs (%d filtered)",
			device.Name, len(modules.Modules), len(modules.Filters))
		successCount++
	}

	log.Printf("Successfully registered %d/%d devices with poller", successCount, len(devices))
	return nil
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

		var matchParts []string
		for k, v := range labelValues.Matchers {
			matchParts = append(matchParts, fmt.Sprintf("%s=%q", k, v))
		}
		matchParts = append(matchParts, fmt.Sprintf("%s!=%q", labelValues.LabelName, ""))
		selector := "{" + strings.Join(matchParts, ",") + "}"

		// Every row of the table, from every series carrying the index label.
		//
		// This used to ask only for info metrics -- snmp_*_info, the ones
		// carrying a text value. A table with no text column therefore had no
		// rows to offer, so its instance picker came up empty, nothing
		// subscribed, and every one of its cards waited for data that was being
		// collected the whole time. cpmCPUTotalTable is entirely numeric and
		// this is exactly what happened to it; across five devices it left 507
		// cards waiting.
		//
		// Aggregated by the label so the cost is one row per instance rather
		// than one per series: a table of 24 interfaces with 40 columns is 24
		// rows here, not 960.
		rows, err := s.queryFunc(r.Context(),
			fmt.Sprintf("count by (%s) (%s)", labelValues.LabelName, selector))
		if err != nil {
			log.Printf("ERROR: instant query failed: %v", err)
			http.Error(w, fmt.Sprintf("failed to query metrics: %v", err), http.StatusInternalServerError)
			return
		}

		// The info metrics separately, for the friendly names an operator reads
		// a row by -- ifName and ifDescr rather than ifIndex 7. A failure here
		// costs the names, not the rows, so it is not fatal.
		named, err := s.queryFunc(r.Context(),
			fmt.Sprintf("{__name__=~%q,%s}", promname.Prefix+".*_info", strings.Join(matchParts, ",")))
		if err != nil {
			s.log().WarnContext(r.Context(), "instance names unavailable",
				"label", labelValues.LabelName, "error", err)
			named = nil
		}

		// A fresh slice rather than appending onto rows, whose backing array
		// belongs to the query that produced it.
		metrics := make([]ws.MetricValue, 0, len(rows)+len(named))
		metrics = append(metrics, rows...)
		metrics = append(metrics, named...)

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
