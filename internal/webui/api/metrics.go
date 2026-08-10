package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/logging"
)

// MetricsQueryRequest represents a metrics query request
type MetricsQueryRequest struct {
	Metric    string `json:"metric"`
	TimeRange string `json:"time_range"` // e.g., "6h", "1d", "7d", "30d", "1y"
	Start     string `json:"start,omitempty"`
	End       string `json:"end,omitempty"`
}

// TimeSeriesData represents a single time series
type TimeSeriesData struct {
	Metric string            `json:"metric"`
	Labels map[string]string `json:"labels"`
	Values [][]interface{}   `json:"values"` // [[timestamp, value], ...]
}

// SystemInfoRequest represents a request for system info
type SystemInfoRequest struct {
	Device string   `json:"device"`
	Fields []string `json:"fields,omitempty"` // Optional: specific fields to retrieve
}

// SystemInfoResponse represents the response with system info
type SystemInfoResponse struct {
	Device    string            `json:"device"`
	Timestamp int64             `json:"timestamp"`
	Values    map[string]string `json:"values"`
}

// handleGetSystemInfo retrieves system info from the snmp_system_info metric
func (s *Server) handleGetSystemInfo(w http.ResponseWriter, r *http.Request) {
	var req SystemInfoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Device == "" {
		http.Error(w, "device is required", http.StatusBadRequest)
		return
	}

	// Query Prometheus for system info metric
	if s.promClient == nil {
		http.Error(w, "Prometheus client not initialized", http.StatusInternalServerError)
		return
	}

	// Build query for snmp_system_info metric
	query := fmt.Sprintf("snmp_system_info{device=%q}", req.Device)

	log.Printf("Querying system info for device %s: %s", req.Device, query)

	metrics, err := s.promClient.Query(r.Context(), query)
	if err != nil {
		log.Printf("ERROR querying Prometheus for system info: %v", err)
		http.Error(w, fmt.Sprintf("failed to query system info: %v", err), http.StatusInternalServerError)
		return
	}

	// Extract system info from labels
	values := make(map[string]string)
	var timestamp int64

	if len(metrics) > 0 {
		// Get the first result (should only be one for a given device)
		metric := metrics[0]
		timestamp = metric.Timestamp

		// Known system info label names
		systemInfoLabels := []string{
			"sys_name",
			"sys_descr",
			"sys_contact",
			"sys_location",
			"sys_object_id",
		}

		// Extract requested fields or all fields if none specified
		fieldsToExtract := req.Fields
		if len(fieldsToExtract) == 0 {
			fieldsToExtract = systemInfoLabels
		}

		for _, field := range fieldsToExtract {
			if value, ok := metric.Metric[field]; ok {
				values[field] = value
			}
		}
	} else {
		// No data found, return empty values
		timestamp = time.Now().Unix()
		log.Printf("No system info found for device %s", req.Device)
	}

	response := SystemInfoResponse{
		Device:    req.Device,
		Timestamp: timestamp,
		Values:    values,
	}

	respondJSON(w, http.StatusOK, response)
}

// handleMetricsQuery handles metrics queries with time-range support
func (s *Server) handleMetricsQuery(w http.ResponseWriter, r *http.Request) {
	var req MetricsQueryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Metric == "" {
		http.Error(w, "metric is required", http.StatusBadRequest)
		return
	}

	// Parse time range
	var start, end time.Time
	var step time.Duration

	if req.Start != "" && req.End != "" {
		// Custom time range
		var err error
		start, err = time.Parse(time.RFC3339, req.Start)
		if err != nil {
			http.Error(w, "invalid start time", http.StatusBadRequest)
			return
		}
		end, err = time.Parse(time.RFC3339, req.End)
		if err != nil {
			http.Error(w, "invalid end time", http.StatusBadRequest)
			return
		}
		step = calculateStep(end.Sub(start))
	} else {
		// Use predefined time range
		end = time.Now()
		var duration time.Duration

		switch req.TimeRange {
		case "6h":
			duration = 6 * time.Hour
			step = 30 * time.Second
		case "1d", "24h":
			duration = 24 * time.Hour
			step = 1 * time.Minute
		case "7d", "1w":
			duration = 7 * 24 * time.Hour
			step = 5 * time.Minute
		case "30d", "1M":
			duration = 30 * 24 * time.Hour
			step = 15 * time.Minute
		case "365d", "1y":
			duration = 365 * 24 * time.Hour
			step = 1 * time.Hour
		default:
			// Default to last 6 hours
			duration = 6 * time.Hour
			step = 30 * time.Second
		}

		start = end.Add(-duration)
	}

	log.Printf("Querying metrics: metric=%s, start=%s, end=%s, step=%s",
		req.Metric, start.Format(time.RFC3339), end.Format(time.RFC3339), step)

	// Query Prometheus
	if s.promClient == nil {
		http.Error(w, "Prometheus client not initialized", http.StatusInternalServerError)
		return
	}

	// Build Prometheus query
	ctx := r.Context()
	query := req.Metric
	s.log().DebugContext(ctx, "calling QueryRange", "query", query)

	metrics, err := s.promClient.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Printf("ERROR querying Prometheus: %v", err)
		http.Error(w, fmt.Sprintf("failed to query metrics: %v", err), http.StatusInternalServerError)
		return
	}
	s.log().DebugContext(ctx, "QueryRange returned", "results", len(metrics))

	// The sample below reaches into the first result's nested maps, so the
	// whole block is skipped rather than gated one record at a time.
	if len(metrics) > 0 && s.log().Enabled(ctx, slog.LevelDebug) {
		s.logSampleMetric(ctx, metrics[0])
	}

	seriesData := convertToTimeSeries(metrics, s.logger)
	s.log().DebugContext(ctx, "converted to time series", "series", len(seriesData))
	if len(seriesData) > 0 && s.log().Enabled(ctx, slog.LevelDebug) {
		first := seriesData[0]
		s.log().DebugContext(ctx, "first series",
			"metric", first.Metric, "labels", first.Labels, "values", len(first.Values))
		if len(first.Values) > 0 {
			s.log().DebugContext(ctx, "first series first value", "value", first.Values[0])
		}
	}

	respondJSON(w, http.StatusOK, seriesData)
}

// logSampleMetric traces the shape of one raw Prometheus result.
//
// Callers must check that debug logging is on first: reading the nested maps
// is work this does not want to pay for when the records are dropped.
func (s *Server) logSampleMetric(ctx context.Context, metric interface{}) {
	metricMap, ok := metric.(map[string]interface{})
	if !ok {
		return
	}

	s.log().DebugContext(ctx, "first raw metric", "metric", metricMap)
	if labels, ok := metricMap["metric"].(map[string]interface{}); ok {
		s.log().DebugContext(ctx, "first metric labels", "labels", labels)
	}
	if values, ok := metricMap["values"].([]interface{}); ok {
		s.log().DebugContext(ctx, "first metric values", "count", len(values))
		if len(values) > 0 {
			s.log().DebugContext(ctx, "first value", "value", values[0])
		}
	}
}

// calculateStep determines the appropriate step size based on the duration
func calculateStep(duration time.Duration) time.Duration {
	hours := duration.Hours()

	switch {
	case hours <= 6:
		return 30 * time.Second
	case hours <= 24:
		return 1 * time.Minute
	case hours <= 168: // 1 week
		return 5 * time.Minute
	case hours <= 720: // 30 days
		return 15 * time.Minute
	default:
		return 1 * time.Hour
	}
}

// convertToTimeSeries converts MetricValue array to time series format.
//
// logger traces the payload shapes it had to fall back on; a nil logger
// discards them.
func convertToTimeSeries(metrics []interface{}, logger *slog.Logger) []TimeSeriesData {
	logger = logging.OrDiscard(logger)
	ctx := context.Background()

	// Group metrics by label set
	seriesMap := make(map[string]*TimeSeriesData)

	for _, m := range metrics {
		// Type assert to map
		metricMap, ok := m.(map[string]interface{})
		if !ok {
			logger.DebugContext(ctx, "metric is not a map", "type", fmt.Sprintf("%T", m))
			continue
		}

		// Extract metric labels - handle both map[string]string and map[string]interface{}
		var labelMap map[string]string
		var metricName string

		// prometheus.go's QueryRange yields map[string]string; JSON-decoded
		// payloads yield map[string]interface{}.
		labelMap = make(map[string]string)
		switch labels := metricMap["metric"].(type) {
		case map[string]string:
			for k, v := range labels {
				if k == "__name__" {
					metricName = v
				} else {
					labelMap[k] = v
				}
			}
		case map[string]interface{}:
			for k, v := range labels {
				str, ok := v.(string)
				if !ok {
					continue
				}
				if k == "__name__" {
					metricName = str
				} else {
					labelMap[k] = str
				}
			}
		}

		// Create label string for grouping
		labelStr := labelsToStringFromMap(labelMap, metricName)

		// Get or create series
		series, exists := seriesMap[labelStr]
		if !exists {
			series = &TimeSeriesData{
				Metric: metricName,
				Labels: labelMap,
				Values: [][]interface{}{},
			}
			seriesMap[labelStr] = series
		}

		// Extract values array - FIX: Try [][]interface{} first since prometheus.go returns that type
		switch values := metricMap["values"].(type) {
		case [][]interface{}:
			// Direct assignment - values is already [][]interface{}
			logger.DebugContext(ctx, "values are [][]interface{}", "count", len(values))
			series.Values = append(series.Values, values...)
		case []interface{}:
			// Fallback for JSON-unmarshaled data where nested arrays become []interface{}
			logger.DebugContext(ctx, "values are []interface{}", "count", len(values))
			for _, v := range values {
				if valPair, ok := v.([]interface{}); ok && len(valPair) == 2 {
					series.Values = append(series.Values, valPair)
				} else {
					logger.DebugContext(ctx, "value is not a timestamp/value pair",
						"type", fmt.Sprintf("%T", v), "value", v)
				}
			}
		default:
			value, ok := metricMap["value"].([]interface{})
			switch {
			case ok && len(value) == 2:
				// Single value for instant queries
				logger.DebugContext(ctx, "found a single instant value")
				series.Values = append(series.Values, value)
			case logger.Enabled(ctx, slog.LevelDebug):
				// getKeys allocates, so build it only when it will be printed.
				logger.DebugContext(ctx, "no values found in metric", "keys", getKeys(metricMap))
			}
		}
	}

	// Convert map to slice, in the order of the label strings the series were
	// grouped under. Emitting them in map order gives the chart a different
	// series order — and so different colors and a different legend — on every
	// identical query.
	result := make([]TimeSeriesData, 0, len(seriesMap))
	for _, labelStr := range slices.Sorted(maps.Keys(seriesMap)) {
		result = append(result, *seriesMap[labelStr])
	}

	return result
}

// getKeys returns the keys of a map for debugging
func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// labelsToStringFromMap renders a label set as the key its series is grouped
// under.
//
// The labels are emitted in name order. Ranging over the map instead makes the
// key depend on Go's randomized map iteration, so two samples carrying the same
// labels can produce two different keys and one series is silently split in
// two — intermittently, and only for series with more than one label.
func labelsToStringFromMap(labels map[string]string, metricName string) string {
	result := fmt.Sprintf("__name__=%s,", metricName)
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		result += fmt.Sprintf("%s=%s,", k, labels[k])
	}
	return result
}
