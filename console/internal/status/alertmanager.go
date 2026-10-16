package status

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Alert is one active Alertmanager alert (API contract type).
type Alert struct {
	Name     string            `json:"name"`
	Severity string            `json:"severity"`
	Summary  string            `json:"summary"`
	StartsAt time.Time         `json:"startsAt"`
	Labels   map[string]string `json:"labels"`
}

// Alertmanager is a client for the Alertmanager v2 API.
type Alertmanager struct {
	base   string
	client *http.Client
}

// NewAlertmanager builds a client for one Alertmanager base URL.
func NewAlertmanager(base string) *Alertmanager {
	return &Alertmanager{base: strings.TrimSuffix(base, "/"), client: &http.Client{Timeout: httpTimeout}}
}

// Base returns the configured base URL.
func (a *Alertmanager) Base() string { return a.base }

// Up reports whether the client has a usable address.
func (a *Alertmanager) Up() bool { return a != nil && a.base != "" }

// Alerts returns the currently firing alerts.
func (a *Alertmanager) Alerts(ctx context.Context) ([]Alert, error) {
	if !a.Up() {
		return nil, fmt.Errorf("Alertmanager 地址未配置")
	}
	endpoint := a.base + "/api/v2/alerts"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Alertmanager 返回 %d", resp.StatusCode)
	}
	var raw []struct {
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
		Status      struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("解析 Alertmanager 响应失败: %w", err)
	}
	out := make([]Alert, 0, len(raw))
	for _, item := range raw {
		if item.Status.State != "" && item.Status.State != "active" {
			continue
		}
		summary := item.Annotations["summary"]
		if summary == "" {
			summary = item.Annotations["description"]
		}
		out = append(out, Alert{
			Name:     item.Labels["alertname"],
			Severity: item.Labels["severity"],
			Summary:  summary,
			StartsAt: item.StartsAt,
			Labels:   item.Labels,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartsAt.Before(out[j].StartsAt) })
	return out, nil
}
