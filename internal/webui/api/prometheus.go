package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-netmon/internal/webui/websocket"
)

// PrometheusClient queries Prometheus
type PrometheusClient struct {
	baseURL string
	client  *http.Client
}

// NewPrometheusClient creates a new Prometheus client
func NewPrometheusClient(baseURL string) *PrometheusClient {
	return &PrometheusClient{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// prometheusResponse represents a Prometheus API response
type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value,omitempty"`  // For instant queries
			Values [][]interface{}   `json:"values,omitempty"` // For range queries
		} `json:"result"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// Query executes an instant query
func (p *PrometheusClient) Query(ctx context.Context, query string) ([]websocket.MetricValue, error) {
	u := fmt.Sprintf("%s/api/v1/query", p.baseURL)

	params := url.Values{}
	params.Add("query", query)

	req, err := http.NewRequestWithContext(ctx, "GET", u+"?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s (%s)", promResp.Error, promResp.ErrorType)
	}

	metrics := make([]websocket.MetricValue, 0, len(promResp.Data.Result))
	for _, result := range promResp.Data.Result {
		if len(result.Value) != 2 {
			continue
		}

		timestamp, ok := result.Value[0].(float64)
		if !ok {
			continue
		}

		valueStr, ok := result.Value[1].(string)
		if !ok {
			continue
		}

		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}

		metrics = append(metrics, websocket.MetricValue{
			Metric:    result.Metric,
			Value:     value,
			Timestamp: int64(timestamp),
		})
	}

	return metrics, nil
}

// QueryRange executes a range query and returns raw Prometheus result
func (p *PrometheusClient) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]interface{}, error) {
	u := fmt.Sprintf("%s/api/v1/query_range", p.baseURL)

	params := url.Values{}
	params.Add("query", query)
	params.Add("start", strconv.FormatInt(start.Unix(), 10))
	params.Add("end", strconv.FormatInt(end.Unix(), 10))
	params.Add("step", fmt.Sprintf("%ds", int(step.Seconds())))

	req, err := http.NewRequestWithContext(ctx, "GET", u+"?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var promResp prometheusResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	if promResp.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s (%s)", promResp.Error, promResp.ErrorType)
	}

	// Convert to []interface{} with full time-series data
	results := make([]interface{}, 0, len(promResp.Data.Result))
	for _, result := range promResp.Data.Result {
		resultMap := map[string]interface{}{
			"metric": result.Metric,
		}

		// Handle both instant and range query responses
		if len(result.Values) > 0 {
			// Range query - include all values
			resultMap["values"] = result.Values
		} else if len(result.Value) > 0 {
			// Instant query - single value
			resultMap["value"] = result.Value
		}

		results = append(results, resultMap)
	}

	return results, nil
}

// QueryLabelValues queries Prometheus for unique values of a label
// Optionally filtered by label matchers
func (p *PrometheusClient) QueryLabelValues(ctx context.Context, labelName string, matchers map[string]string) ([]string, error) {
	// Use /api/v1/label/<name>/values endpoint
	u := fmt.Sprintf("%s/api/v1/label/%s/values", p.baseURL, url.PathEscape(labelName))

	// Build match[] parameters for filtering by matchers
	params := url.Values{}
	if len(matchers) > 0 {
		// Build PromQL selector: {device="core-01"}
		matchParts := make([]string, 0, len(matchers))
		for k, v := range matchers {
			matchParts = append(matchParts, fmt.Sprintf("%s=%q", k, v))
		}
		selector := "{" + joinStrings(matchParts, ",") + "}"
		params.Add("match[]", selector)
	}

	queryURL := u
	if len(params) > 0 {
		queryURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, "GET", queryURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Parse label values response
	var labelResp struct {
		Status string   `json:"status"`
		Data   []string `json:"data"`
		Error  string   `json:"error,omitempty"`
	}

	if err := json.Unmarshal(body, &labelResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	if labelResp.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s", labelResp.Error)
	}

	return labelResp.Data, nil
}

// SeriesResponse represents a Prometheus series API response
type seriesResponse struct {
	Status string `json:"status"`
	Data   []struct {
		Metric map[string]string `json:"metric"`
	} `json:"data"`
	Error     string `json:"error,omitempty"`
	ErrorType string `json:"errorType,omitempty"`
}

// QuerySeries queries the Prometheus series endpoint to get metric metadata
// This is useful for populating dropdowns with info metrics that may not appear in instant queries
func (p *PrometheusClient) QuerySeries(ctx context.Context, matchers map[string]string, start, end time.Time) ([]map[string]string, error) {
	u := fmt.Sprintf("%s/api/v1/series", p.baseURL)

	params := url.Values{}

	// Build match[] parameter - series endpoint requires at least one matcher
	// IMPORTANT: Only query for info metrics since that's what we need for friendly names
	if len(matchers) > 0 {
		matchParts := make([]string, 0, len(matchers))
		for k, v := range matchers {
			matchParts = append(matchParts, fmt.Sprintf("%s=%q", k, v))
		}
		// Query specifically for info metrics with the matchers
		params.Add("match[]", "{__name__=~\"snmp.*_info\","+joinStrings(matchParts, ",")+"}")
	} else {
		// If no matchers provided, query for info metrics by default
		params.Add("match[]", "{__name__=~\"snmp.*_info\"}")
	}

	// Add time range (required by series endpoint)
	params.Add("start", strconv.FormatInt(start.Unix(), 10))
	params.Add("end", strconv.FormatInt(end.Unix(), 10))

	req, err := http.NewRequestWithContext(ctx, "GET", u+"?"+params.Encode(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	// Closing the body only returns the connection to the pool; the call's
	// outcome is already determined by what was read above.
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	var seriesResp seriesResponse
	if err := json.Unmarshal(body, &seriesResp); err != nil {
		return nil, fmt.Errorf("unmarshaling response: %w", err)
	}

	if seriesResp.Status != "success" {
		return nil, fmt.Errorf("prometheus error: %s (%s)", seriesResp.Error, seriesResp.ErrorType)
	}

	// Extract metric labels
	results := make([]map[string]string, 0, len(seriesResp.Data))
	for _, series := range seriesResp.Data {
		results = append(results, series.Metric)
	}

	return results, nil
}

// joinStrings joins strings with a separator (helper function)
func joinStrings(strs []string, sep string) string {
	result := ""
	for i, s := range strs {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
