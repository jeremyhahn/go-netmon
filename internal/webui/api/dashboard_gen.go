package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/jeremyhahn/go-netmon/internal/mib/parser"
	"github.com/jeremyhahn/go-netmon/internal/promname"
)

// DashboardRequest represents a dashboard generation request
type DashboardRequest struct {
	DeviceID int64   `json:"device_id"`
	MIBIDs   []int64 `json:"mib_ids"`
	Title    string  `json:"title,omitempty"`
}

// handleGenerateDashboard generates a Grafana dashboard for a device
func (s *Server) handleGenerateDashboard(w http.ResponseWriter, r *http.Request) {
	var req DashboardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.DeviceID == 0 {
		http.Error(w, "device_id is required", http.StatusBadRequest)
		return
	}

	if len(req.MIBIDs) == 0 {
		http.Error(w, "at least one mib_id is required", http.StatusBadRequest)
		return
	}

	// Get device information
	device, err := s.storage.GetDevice(r.Context(), req.DeviceID)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	// Get MIBs
	var mibNames []string
	for _, mibID := range req.MIBIDs {
		mib, getErr := s.storage.GetMIB(r.Context(), mibID)
		if getErr != nil {
			http.Error(w, fmt.Sprintf("MIB %d not found", mibID), http.StatusNotFound)
			return
		}
		mibNames = append(mibNames, mib.Name)
	}

	// Generate dashboard
	dashboard, err := s.generateDashboard(device.Name, mibNames, req.Title)
	if err != nil {
		log.Printf("Failed to generate dashboard: %v", err)
		http.Error(w, "failed to generate dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, dashboard)
}

// handleGenerateDeviceDashboard generates a dashboard for a device using its assigned MIBs
func (s *Server) handleGenerateDeviceDashboard(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	deviceID, err := strconv.ParseInt(vars["id"], 10, 64)
	if err != nil {
		http.Error(w, "invalid device ID", http.StatusBadRequest)
		return
	}

	// Get device information
	device, err := s.storage.GetDevice(r.Context(), deviceID)
	if err != nil {
		http.Error(w, "device not found", http.StatusNotFound)
		return
	}

	// Get device MIBs
	mibs, err := s.storage.GetDeviceMIBs(r.Context(), deviceID)
	if err != nil {
		http.Error(w, "failed to get device MIBs", http.StatusInternalServerError)
		return
	}

	if len(mibs) == 0 {
		http.Error(w, "device has no assigned MIBs", http.StatusBadRequest)
		return
	}

	// Extract MIB names
	var mibNames []string
	for _, mib := range mibs {
		mibNames = append(mibNames, mib.Name)
	}

	// Generate dashboard
	dashboard, err := s.generateDashboard(device.Name, mibNames, "")
	if err != nil {
		log.Printf("Failed to generate dashboard: %v", err)
		http.Error(w, "failed to generate dashboard: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, dashboard)
}

// generateDashboard creates an ECharts dashboard configuration
func (s *Server) generateDashboard(deviceName string, mibNames []string, title string) (map[string]interface{}, error) {
	if title == "" {
		title = fmt.Sprintf("%s - %s", deviceName, strings.Join(mibNames, ", "))
	}

	// Initialize parser to get MIB details
	p := parser.New(s.mibStoragePath)
	if err := p.Init(); err != nil {
		return nil, fmt.Errorf("failed to initialize parser: %w", err)
	}
	defer p.Close()

	charts := []map[string]interface{}{}

	// Get all available metric names from Prometheus once
	availableMetrics, err := s.getAvailableMetrics()
	if err != nil {
		log.Printf("Warning: failed to get available metrics: %v", err)
		availableMetrics = make(map[string]bool) // Empty set - will filter everything
	}
	log.Printf("INFO: Found %d available metrics in Prometheus", len(availableMetrics))

	// Generate charts for each MIB
	coverage := make([]MIBCoverage, 0, len(mibNames))
	for _, mibName := range mibNames {
		// What this MIB contributed, so a dashboard that comes back with fewer
		// panels than expected says which module fell short and why.
		reached := MIBCoverage{MIB: mibName}

		// Load the module
		if _, err := p.LoadModule(mibName); err != nil {
			log.Printf("Warning: failed to load MIB %s: %v", mibName, err)
			reached.Note = fmt.Sprintf("%s could not be loaded: %v", mibName, err)
			coverage = append(coverage, reached)
			continue
		}

		// Parse module to get OIDs
		oids, err := p.ParseModule(mibName)
		if err != nil {
			log.Printf("Warning: failed to parse MIB %s: %v", mibName, err)
			reached.Note = fmt.Sprintf("%s could not be parsed: %v", mibName, err)
			coverage = append(coverage, reached)
			continue
		}

		// Create charts for pollable OIDs - only if metric data exists
		for _, oid := range oids {
			if !oid.Pollable {
				continue
			}
			reached.Pollable++

			// Determine chart type based on OID type
			chartType := "line"
			if oid.IsString() {
				chartType = "text"
			}

			// The exporter names a metric after the MIB object, and every row
			// of a table column shares that one name, so existence is a single
			// exact lookup. The old scheme named a metric after the instance
			// OID, which is why this used to try the object OID, the object OID
			// with a ".0" scalar instance, and finally a prefix scan over every
			// metric name in Prometheus to find any row of a column.
			metricName := promname.MetricName(oid.Name)
			if chartType != "line" {
				// A non-numeric column reaches Prometheus as an info metric:
				// its value is a label on a constant-1 series.
				if info := promname.InfoMetricName(oid.Name); availableMetrics[info] {
					metricName = info
				}
			}

			// Skip if the object has no data in Prometheus.
			if !availableMetrics[metricName] {
				continue
			}

			chart := map[string]interface{}{
				"id":          fmt.Sprintf("%s_%s", mibName, oid.Name),
				"title":       fmt.Sprintf("%s - %s", mibName, oid.Name),
				"type":        chartType,
				"metric":      metricName,
				"description": oid.Description,
				"unit":        oid.Units,
				"oid":         oid.OID,
			}

			charts = append(charts, chart)
			reached.Charts++
		}

		reached.Note = coverageNote(reached)
		coverage = append(coverage, reached)
	}

	dashboard := map[string]interface{}{
		"device":   deviceName,
		"title":    title,
		"charts":   charts,
		"mibs":     coverage,
		"warnings": coverageWarnings(coverage),
	}

	return dashboard, nil
}

// MIBCoverage says what one of a device's MIBs contributed to its dashboard.
//
// A panel appears only for an object that Prometheus already holds data for, so
// a freshly assigned MIB contributes nothing until the poller has run once. That
// looks identical to a MIB that will never contribute anything, and both looked
// identical to a bug. These counts separate the three.
type MIBCoverage struct {
	MIB string `json:"mib"`
	// Pollable is how many of the module's objects the poller would ask for.
	Pollable int `json:"pollable"`
	// Charts is how many of them became a panel, which requires the metric to
	// already exist in Prometheus.
	Charts int `json:"charts"`
	// Note explains a shortfall, and is empty when there is none.
	Note string `json:"note,omitempty"`
}

// coverageNote explains why a MIB contributed fewer panels than its objects.
func coverageNote(c MIBCoverage) string {
	switch {
	case c.Pollable == 0:
		return fmt.Sprintf(
			"%s has no pollable objects, so it will never add a panel. It is most "+
				"likely a registry of identifiers, or a module whose IMPORTS did not "+
				"resolve.", c.MIB)

	case c.Charts == 0:
		return fmt.Sprintf(
			"%s has %d pollable objects but none of them have reached Prometheus yet, "+
				"so it adds no panels. A panel appears once the device has been polled "+
				"and answered — wait one poll interval and generate again. If it stays "+
				"empty, the device is not answering those OIDs.",
			c.MIB, c.Pollable)

	case c.Charts < c.Pollable:
		return fmt.Sprintf(
			"%s contributed %d panels of %d pollable objects; the rest have no data in "+
				"Prometheus, either not yet polled or not answered by the device.",
			c.MIB, c.Charts, c.Pollable)

	default:
		return ""
	}
}

// coverageWarnings is every note worth showing, in the order the MIBs were read.
func coverageWarnings(coverage []MIBCoverage) []string {
	warnings := []string{}
	for _, c := range coverage {
		if c.Note != "" {
			warnings = append(warnings, c.Note)
		}
	}
	return warnings
}

// getAvailableMetrics fetches all metric names from Prometheus
func (s *Server) getAvailableMetrics() (map[string]bool, error) {
	ctx := context.Background()

	// Query Prometheus for all metric names
	// We'll use the label API to get __name__ values
	labelValues, err := s.promClient.QueryLabelValues(ctx, "__name__", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to query metric names: %w", err)
	}

	// Convert to map for fast lookup
	// Include ALL metrics - we'll filter _info metrics differently when building charts
	metrics := make(map[string]bool, len(labelValues))
	for _, name := range labelValues {
		metrics[name] = true
	}

	return metrics, nil
}
