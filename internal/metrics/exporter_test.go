package metrics

import (
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/jeremyhahn/go-netmon/internal/snmp"
)

func TestConvertSNMPResultsToSamples(t *testing.T) {
	timestamp := time.Now()

	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.3.0",
			Type:      gosnmp.TimeTicks,
			Value:     uint(123456),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1",
			Type:      gosnmp.Counter32,
			Value:     uint(999999),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.1.0",
			Type:      gosnmp.OctetString,
			Value:     []byte("Test Device"),
			Timestamp: timestamp,
		},
	}

	extraLabels := map[string]string{
		"location": "datacenter-1",
		"role":     "router",
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, extraLabels)

	// Should have 3 samples: 2 numeric values + 1 info metric
	if len(samples) != 3 {
		t.Errorf("Expected 3 samples, got %d", len(samples))
	}

	// Verify first numeric sample
	if samples[0].Labels["device"] != "test-device" {
		t.Errorf("Expected device label 'test-device', got %s", samples[0].Labels["device"])
	}

	if samples[0].Labels["location"] != "datacenter-1" {
		t.Errorf("Expected location label 'datacenter-1', got %s", samples[0].Labels["location"])
	}

	if samples[0].Value != 123456 {
		t.Errorf("Expected value 123456, got %f", samples[0].Value)
	}

	if !samples[0].Timestamp.Equal(timestamp) {
		t.Error("Timestamp mismatch")
	}

	// Verify info metric (should be the last sample)
	infoMetric := samples[len(samples)-1]
	if infoMetric.Name != "snmp_system_info" {
		t.Errorf("Expected info metric name 'snmp_system_info', got %s", infoMetric.Name)
	}

	if infoMetric.Value != 1.0 {
		t.Errorf("Expected info metric value 1.0, got %f", infoMetric.Value)
	}

	if infoMetric.Labels["sys_descr"] != "Test Device" {
		t.Errorf("Expected sys_descr label 'Test Device', got %s", infoMetric.Labels["sys_descr"])
	}

	if infoMetric.Labels["device"] != "test-device" {
		t.Errorf("Expected device label in info metric, got %s", infoMetric.Labels["device"])
	}

	if infoMetric.Labels["location"] != "datacenter-1" {
		t.Errorf("Expected location label in info metric, got %s", infoMetric.Labels["location"])
	}
}

func TestConvertSNMPResultsToSamples_SystemInfoMetrics(t *testing.T) {
	timestamp := time.Now()

	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.1.0", // sysDescr
			Type:      gosnmp.OctetString,
			Value:     []byte("Cisco IOS Router"),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.2.0", // sysObjectID
			Type:      gosnmp.ObjectIdentifier,
			Value:     ".1.3.6.1.4.1.9.1.1",
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.4.0", // sysContact
			Type:      gosnmp.OctetString,
			Value:     []byte("admin@example.com"),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.5.0", // sysName
			Type:      gosnmp.OctetString,
			Value:     []byte("core-01"),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.6.0", // sysLocation
			Type:      gosnmp.OctetString,
			Value:     []byte("Datacenter A"),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, nil)

	// Should have 1 info metric with all system info labels
	if len(samples) != 1 {
		t.Fatalf("Expected 1 sample (info metric), got %d", len(samples))
	}

	infoMetric := samples[0]
	if infoMetric.Name != "snmp_system_info" {
		t.Errorf("Expected metric name 'snmp_system_info', got %s", infoMetric.Name)
	}

	if infoMetric.Value != 1.0 {
		t.Errorf("Expected value 1.0, got %f", infoMetric.Value)
	}

	// Check all system info labels
	expectedLabels := map[string]string{
		"device":        "test-device",
		"sys_descr":     "Cisco IOS Router",
		"sys_object_id": ".1.3.6.1.4.1.9.1.1",
		"sys_contact":   "admin@example.com",
		"sys_name":      "core-01",
		"sys_location":  "Datacenter A",
	}

	for key, expectedValue := range expectedLabels {
		if infoMetric.Labels[key] != expectedValue {
			t.Errorf("Expected label %s='%s', got '%s'", key, expectedValue, infoMetric.Labels[key])
		}
	}
}

func TestConvertSNMPResultsToSamples_MixedNumericAndString(t *testing.T) {
	timestamp := time.Now()

	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.3.0", // sysUptime (numeric)
			Type:      gosnmp.TimeTicks,
			Value:     uint(1234567),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.5.0", // sysName (string)
			Type:      gosnmp.OctetString,
			Value:     []byte("router-01"),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1", // Interface counter (numeric)
			Type:      gosnmp.Counter32,
			Value:     uint(999999),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.6.0", // sysLocation (string)
			Type:      gosnmp.OctetString,
			Value:     []byte("Rack 42"),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("router-01", results, nil)

	// Should have 3 samples: 2 numeric + 1 info metric
	if len(samples) != 3 {
		t.Fatalf("Expected 3 samples, got %d", len(samples))
	}

	// Verify we have 2 numeric metrics
	numericCount := 0
	infoCount := 0
	for _, sample := range samples {
		if sample.Name == "snmp_system_info" {
			infoCount++
			// Verify info metric has both string values
			if sample.Labels["sys_name"] != "router-01" {
				t.Errorf("Expected sys_name='router-01', got '%s'", sample.Labels["sys_name"])
			}
			if sample.Labels["sys_location"] != "Rack 42" {
				t.Errorf("Expected sys_location='Rack 42', got '%s'", sample.Labels["sys_location"])
			}
		} else {
			numericCount++
		}
	}

	if numericCount != 2 {
		t.Errorf("Expected 2 numeric metrics, got %d", numericCount)
	}

	if infoCount != 1 {
		t.Errorf("Expected 1 info metric, got %d", infoCount)
	}
}

func TestConvertSNMPResultsToSamples_NoSystemInfo(t *testing.T) {
	timestamp := time.Now()

	// Only numeric values, no system info OIDs
	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.1",
			Type:      gosnmp.Counter32,
			Value:     uint(1000),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.2.2.1.10.2",
			Type:      gosnmp.Counter32,
			Value:     uint(2000),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, nil)

	// Should have only 2 numeric samples, no info metric
	if len(samples) != 2 {
		t.Errorf("Expected 2 samples, got %d", len(samples))
	}

	// Verify no info metric was created
	for _, sample := range samples {
		if sample.Name == "snmp_system_info" {
			t.Error("Unexpected info metric created when no system info OIDs present")
		}
	}
}

func TestConvertSNMPResultsToSamples_EmptyStringValues(t *testing.T) {
	timestamp := time.Now()

	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.5.0", // sysName with empty string
			Type:      gosnmp.OctetString,
			Value:     []byte(""),
			Timestamp: timestamp,
		},
		{
			OID:       "1.3.6.1.2.1.1.3.0", // Numeric value
			Type:      gosnmp.TimeTicks,
			Value:     uint(123),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, nil)

	// Should have only 1 sample (numeric), empty strings should not create info metrics
	if len(samples) != 1 {
		t.Errorf("Expected 1 sample, got %d", len(samples))
	}

	if samples[0].Name == "snmp_system_info" {
		t.Error("Info metric should not be created for empty string values")
	}
}

func TestConvertSNMPResultsToSamples_EmptyResults(t *testing.T) {
	samples := ConvertSNMPResultsToSamples("test-device", nil, nil)

	if len(samples) != 0 {
		t.Errorf("Expected 0 samples for empty results, got %d", len(samples))
	}
}

func TestConvertSNMPResultsToSamples_NoExtraLabels(t *testing.T) {
	timestamp := time.Now()

	results := []*snmp.Result{
		{
			OID:       "1.3.6.1.2.1.1.3.0",
			Type:      gosnmp.TimeTicks,
			Value:     uint(12345),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, nil)

	if len(samples) != 1 {
		t.Fatalf("Expected 1 sample, got %d", len(samples))
	}

	// Should have device and oid labels
	if len(samples[0].Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(samples[0].Labels))
	}

	if samples[0].Labels["device"] != "test-device" {
		t.Errorf("Expected device label 'test-device', got %s", samples[0].Labels["device"])
	}
}

func TestConvertSNMPResultsToSamples_LeadingDotOIDs(t *testing.T) {
	timestamp := time.Now()

	// Test with leading dot in OID
	results := []*snmp.Result{
		{
			OID:       ".1.3.6.1.2.1.1.5.0", // sysName with leading dot
			Type:      gosnmp.OctetString,
			Value:     []byte("router-01"),
			Timestamp: timestamp,
		},
	}

	samples := ConvertSNMPResultsToSamples("test-device", results, nil)

	if len(samples) != 1 {
		t.Fatalf("Expected 1 sample, got %d", len(samples))
	}

	if samples[0].Name != "snmp_system_info" {
		t.Errorf("Expected info metric for OID with leading dot")
	}

	if samples[0].Labels["sys_name"] != "router-01" {
		t.Errorf("Expected sys_name label to be set for OID with leading dot")
	}
}

func TestSanitizeMetricName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"valid_metric_name", "valid_metric_name"},
		{"valid:metric:name", "valid:metric:name"},
		{"123invalid", "_23invalid"},
		{"metric-with-dashes", "metric_with_dashes"},
		{"metric.with.dots", "metric_with_dots"},
		{"metric with spaces", "metric_with_spaces"},
		{"1.3.6.1.2.1.1.3.0", "__3_6_1_2_1_1_3_0"}, // First '1' -> '_', then '.' -> '_'
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := SanitizeMetricName(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeMetricName(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestSample(t *testing.T) {
	now := time.Now()

	sample := &Sample{
		Name: "test_metric",
		Labels: map[string]string{
			"device": "router1",
			"oid":    "1.3.6.1.2.1.1.3.0",
		},
		Value:     12345.67,
		Timestamp: now,
	}

	if sample.Name != "test_metric" {
		t.Errorf("Expected name 'test_metric', got %s", sample.Name)
	}

	if sample.Value != 12345.67 {
		t.Errorf("Expected value 12345.67, got %f", sample.Value)
	}

	if !sample.Timestamp.Equal(now) {
		t.Error("Timestamp mismatch")
	}

	if len(sample.Labels) != 2 {
		t.Errorf("Expected 2 labels, got %d", len(sample.Labels))
	}
}

// BenchmarkConvertSNMPResultsToSamples benchmarks the conversion performance
func BenchmarkConvertSNMPResultsToSamples(b *testing.B) {
	timestamp := time.Now()

	// Create 100 SNMP results
	results := make([]*snmp.Result, 100)
	for i := 0; i < 100; i++ {
		results[i] = &snmp.Result{
			OID:       "1.3.6.1.2.1.2.2.1.10." + string(rune(i)),
			Type:      gosnmp.Counter32,
			Value:     uint(i * 1000),
			Timestamp: timestamp,
		}
	}

	extraLabels := map[string]string{
		"location": "datacenter-1",
		"role":     "router",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ConvertSNMPResultsToSamples("test-device", results, extraLabels)
	}
}

// BenchmarkConvertSNMPResultsToSamplesWithSystemInfo benchmarks conversion with system info
func BenchmarkConvertSNMPResultsToSamplesWithSystemInfo(b *testing.B) {
	timestamp := time.Now()

	// Create mixed numeric and string results
	results := make([]*snmp.Result, 100)

	// Add system info OIDs
	results[0] = &snmp.Result{
		OID:       "1.3.6.1.2.1.1.1.0",
		Type:      gosnmp.OctetString,
		Value:     []byte("Cisco IOS Router"),
		Timestamp: timestamp,
	}
	results[1] = &snmp.Result{
		OID:       "1.3.6.1.2.1.1.5.0",
		Type:      gosnmp.OctetString,
		Value:     []byte("router-01"),
		Timestamp: timestamp,
	}
	results[2] = &snmp.Result{
		OID:       "1.3.6.1.2.1.1.6.0",
		Type:      gosnmp.OctetString,
		Value:     []byte("Datacenter A"),
		Timestamp: timestamp,
	}

	// Add numeric results
	for i := 3; i < 100; i++ {
		results[i] = &snmp.Result{
			OID:       "1.3.6.1.2.1.2.2.1.10." + string(rune(i)),
			Type:      gosnmp.Counter32,
			Value:     uint(i * 1000),
			Timestamp: timestamp,
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ConvertSNMPResultsToSamples("test-device", results, nil)
	}
}

// BenchmarkSanitizeMetricName benchmarks metric name sanitization
func BenchmarkSanitizeMetricName(b *testing.B) {
	testName := "metric-with.various!characters@123"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeMetricName(testName)
	}
}
