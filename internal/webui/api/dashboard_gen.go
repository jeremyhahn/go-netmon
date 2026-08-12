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
	for _, mibName := range mibNames {
		// Load the module
		if _, err := p.LoadModule(mibName); err != nil {
			log.Printf("Warning: failed to load MIB %s: %v", mibName, err)
			continue
		}

		// Parse module to get OIDs
		oids, err := p.ParseModule(mibName)
		if err != nil {
			log.Printf("Warning: failed to parse MIB %s: %v", mibName, err)
			continue
		}

		// Create charts for pollable OIDs - only if metric data exists
		for _, oid := range oids {
			if !oid.Pollable {
				continue
			}

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
		}
	}

	dashboard := map[string]interface{}{
		"device": deviceName,
		"title":  title,
		"charts": charts,
	}

	return dashboard, nil
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
