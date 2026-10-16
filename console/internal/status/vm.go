// Package status collects cluster and node state from VictoriaMetrics, the
// Patroni REST API and Alertmanager, on demand and with a short lived cache.
package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// httpTimeout bounds every external request of the status collectors.
const httpTimeout = 4 * time.Second

// Series is one vector sample returned by a PromQL query.
type Series struct {
	Labels map[string]string
	Value  float64
}

// VM is a minimal VictoriaMetrics / Prometheus query client.
type VM struct {
	base   string
	client *http.Client
}

// NewVM builds a client for one VictoriaMetrics base URL.
func NewVM(base string) *VM {
	return &VM{base: strings.TrimSuffix(base, "/"), client: &http.Client{Timeout: httpTimeout}}
}

// Base returns the configured base URL.
func (c *VM) Base() string { return c.base }

// Up reports whether the client has a usable address.
func (c *VM) Up() bool { return c != nil && c.base != "" }

type vmResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  []json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

// Query runs one instant PromQL query.
func (c *VM) Query(ctx context.Context, expr string) ([]Series, error) {
	if !c.Up() {
		return nil, fmt.Errorf("VictoriaMetrics 地址未配置")
	}
	q := url.Values{}
	q.Set("query", expr)
	endpoint := c.base + "/api/v1/query?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("VictoriaMetrics 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var doc vmResponse
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("解析 VictoriaMetrics 响应失败: %w", err)
	}
	if doc.Status != "success" {
		return nil, fmt.Errorf("VictoriaMetrics 查询失败: %s", doc.Error)
	}
	out := make([]Series, 0, len(doc.Data.Result))
	for _, item := range doc.Data.Result {
		if len(item.Value) != 2 {
			continue
		}
		var raw string
		if err := json.Unmarshal(item.Value[1], &raw); err != nil {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		out = append(out, Series{Labels: item.Metric, Value: value})
	}
	return out, nil
}

// InstanceIP returns the inventory IP of a series, preferring the `ip` label
// that Pigsty's scrape targets always carry.
func (s Series) InstanceIP() string {
	if ip := s.Labels["ip"]; ip != "" {
		return ip
	}
	instance := s.Labels["instance"]
	if host, _, err := splitHostPort(instance); err == nil {
		return host
	}
	return instance
}

// splitHostPort splits "10.10.10.10:9100" and strips IPv6 brackets.
func splitHostPort(v string) (string, string, error) {
	if v == "" {
		return "", "", fmt.Errorf("empty address")
	}
	i := strings.LastIndex(v, ":")
	if i < 0 {
		return strings.Trim(v, "[]"), "", nil
	}
	return strings.Trim(v[:i], "[]"), v[i+1:], nil
}

// Index maps series by their inventory IP, keeping the maximum value so that
// duplicated targets never hide a real sample.
func index(samples []Series) map[string]float64 {
	out := make(map[string]float64, len(samples))
	for _, s := range samples {
		ip := s.InstanceIP()
		if ip == "" {
			continue
		}
		if existing, ok := out[ip]; !ok || s.Value > existing {
			out[ip] = s.Value
		}
	}
	return out
}
