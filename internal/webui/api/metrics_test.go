package api

import (
	"testing"
	"time"
)

// TestConvertToTimeSeries_WithDoubleArray tests the bug fix for type assertion
func TestConvertToTimeSeries_WithDoubleArray(t *testing.T) {
	// This simulates the actual data structure returned from prometheus.go QueryRange
	metrics := []interface{}{
		map[string]interface{}{
			"metric": map[string]string{
				"__name__": "snmp__1_3_6_1_2_1_1_3_0",
				"device":   "203.0.113.1",
				"job":      "snmp",
			},
			"values": [][]interface{}{
				{float64(1762007794), "168807634"},
				{float64(1762007824), "168810634"},
				{float64(1762007854), "168813634"},
			},
		},
	}

	result := convertToTimeSeries(metrics, nil)

	if len(result) != 1 {
		t.Fatalf("Expected 1 series, got %d", len(result))
	}

	series := result[0]

	if series.Metric != "snmp__1_3_6_1_2_1_1_3_0" {
		t.Errorf("Expected metric name 'snmp__1_3_6_1_2_1_1_3_0', got '%s'", series.Metric)
	}

	if len(series.Values) != 3 {
		t.Fatalf("Expected 3 values, got %d", len(series.Values))
	}

	// Verify first value
	if len(series.Values[0]) != 2 {
		t.Errorf("Expected value pair to have 2 elements, got %d", len(series.Values[0]))
	}

	// Check labels
	if series.Labels["device"] != "203.0.113.1" {
		t.Errorf("Expected device label '203.0.113.1', got '%s'", series.Labels["device"])
	}
}

// TestConvertToTimeSeries_WithNestedInterfaceArray tests fallback for JSON-unmarshaled data
func TestConvertToTimeSeries_WithNestedInterfaceArray(t *testing.T) {
	// This simulates data that comes from JSON unmarshaling
	metrics := []interface{}{
		map[string]interface{}{
			"metric": map[string]interface{}{
				"__name__": "test_metric",
				"label1":   "value1",
			},
			"values": []interface{}{
				[]interface{}{float64(1234567890), "100"},
				[]interface{}{float64(1234567920), "200"},
			},
		},
	}

	result := convertToTimeSeries(metrics, nil)

	if len(result) != 1 {
		t.Fatalf("Expected 1 series, got %d", len(result))
	}

	series := result[0]

	if series.Metric != "test_metric" {
		t.Errorf("Expected metric name 'test_metric', got '%s'", series.Metric)
	}

	if len(series.Values) != 2 {
		t.Fatalf("Expected 2 values, got %d", len(series.Values))
	}
}

// TestConvertToTimeSeries_WithSingleValue tests instant query response
func TestConvertToTimeSeries_WithSingleValue(t *testing.T) {
	metrics := []interface{}{
		map[string]interface{}{
			"metric": map[string]string{
				"__name__": "instant_metric",
			},
			"value": []interface{}{float64(1234567890), "42"},
		},
	}

	result := convertToTimeSeries(metrics, nil)

	if len(result) != 1 {
		t.Fatalf("Expected 1 series, got %d", len(result))
	}

	series := result[0]

	if len(series.Values) != 1 {
		t.Fatalf("Expected 1 value, got %d", len(series.Values))
	}
}

// TestConvertToTimeSeries_EmptyMetrics tests empty input
func TestConvertToTimeSeries_EmptyMetrics(t *testing.T) {
	metrics := []interface{}{}

	result := convertToTimeSeries(metrics, nil)

	if len(result) != 0 {
		t.Errorf("Expected 0 series for empty input, got %d", len(result))
	}
}

// TestConvertToTimeSeries_InvalidMetric tests invalid metric format
func TestConvertToTimeSeries_InvalidMetric(t *testing.T) {
	metrics := []interface{}{
		"not a map",
		map[string]interface{}{
			// Missing metric field and values - this creates an empty series
		},
	}

	result := convertToTimeSeries(metrics, nil)

	// The second metric will create a series but with no values
	if len(result) > 1 {
		t.Errorf("Expected at most 1 series for invalid input, got %d", len(result))
	}

	// If a series was created, it should have no values
	for _, series := range result {
		if len(series.Values) != 0 {
			t.Errorf("Expected series to have 0 values, got %d", len(series.Values))
		}
	}
}

// TestConvertToTimeSeries_MultipleMetrics tests grouping by label set
func TestConvertToTimeSeries_MultipleMetrics(t *testing.T) {
	metrics := []interface{}{
		map[string]interface{}{
			"metric": map[string]string{
				"__name__": "cpu_usage",
				"host":     "server1",
			},
			"values": [][]interface{}{
				{float64(100), "50"},
				{float64(200), "60"},
			},
		},
		map[string]interface{}{
			"metric": map[string]string{
				"__name__": "cpu_usage",
				"host":     "server2",
			},
			"values": [][]interface{}{
				{float64(100), "30"},
				{float64(200), "40"},
			},
		},
	}

	result := convertToTimeSeries(metrics, nil)

	if len(result) != 2 {
		t.Fatalf("Expected 2 series (one per host), got %d", len(result))
	}

	// Verify both series have correct value counts
	for i, series := range result {
		if len(series.Values) != 2 {
			t.Errorf("Series %d: Expected 2 values, got %d", i, len(series.Values))
		}
		if series.Metric != "cpu_usage" {
			t.Errorf("Series %d: Expected metric 'cpu_usage', got '%s'", i, series.Metric)
		}
	}
}

// TestCalculateStep tests the step calculation function
func TestCalculateStep(t *testing.T) {
	tests := []struct {
		name     string
		duration string
		expected string
	}{
		{"6 hours", "6h", "30s"},
		{"1 day", "24h", "1m0s"},
		{"1 week", "168h", "5m0s"},
		{"30 days", "720h", "15m0s"},
		{"1 year", "8760h", "1h0m0s"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			duration, _ := parseSimpleDuration(tt.duration)
			result := calculateStep(duration)
			if result.String() != tt.expected {
				t.Errorf("Expected step %s for duration %s, got %s", tt.expected, tt.duration, result)
			}
		})
	}
}

// Helper to parse simple duration strings
func parseSimpleDuration(s string) (time.Duration, error) {
	// Simple parser for test purposes
	switch s {
	case "6h":
		return 6 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	case "168h":
		return 168 * time.Hour, nil
	case "720h":
		return 720 * time.Hour, nil
	case "8760h":
		return 8760 * time.Hour, nil
	default:
		return 0, nil
	}
}
