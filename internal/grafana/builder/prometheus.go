package builder

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PrometheusClient queries Prometheus for metric availability
type PrometheusClient struct {
	baseURL string
	client  *http.Client
}

// NewPrometheusClient creates a new Prometheus client
func NewPrometheusClient(baseURL string) *PrometheusClient {
	return &PrometheusClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// prometheusResponse represents the Prometheus API response
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
		} `json:"result"`
	} `json:"data"`
}

// MetricExists checks if a specific metric exists in Prometheus
func (p *PrometheusClient) MetricExists(ctx context.Context, metricName string) (bool, error) {
	query := url.QueryEscape(metricName)
	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s", p.baseURL, query)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to query prometheus: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read response: %w", err)
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return false, fmt.Errorf("failed to parse response: %w", err)
	}

	return promResp.Status == "success" && len(promResp.Data.Result) > 0, nil
}

// GetMetricsByPattern returns all metrics matching a pattern
func (p *PrometheusClient) GetMetricsByPattern(ctx context.Context, pattern string) ([]string, error) {
	query := url.QueryEscape(fmt.Sprintf("{__name__=~%q}", pattern))
	endpoint := fmt.Sprintf("%s/api/v1/query?query=%s", p.baseURL, query)

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to query prometheus: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus query failed")
	}

	// Extract unique metric names
	metricNames := make(map[string]bool)
	for _, result := range promResp.Data.Result {
		if name, ok := result.Metric["__name__"]; ok {
			metricNames[name] = true
		}
	}

	// Convert to slice
	names := make([]string, 0, len(metricNames))
	for name := range metricNames {
		names = append(names, name)
	}

	return names, nil
}
