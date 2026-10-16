package status

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// PatroniMember is one member of a Patroni cluster (API contract type).
type PatroniMember struct {
	Name           string `json:"name"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Role           string `json:"role"`
	State          string `json:"state"`
	Timeline       *int   `json:"timeline"`
	LagBytes       *int64 `json:"lagBytes"`
	PendingRestart bool   `json:"pendingRestart"`
}

// PatroniCluster is the parsed answer of GET /cluster of the Patroni REST API.
type PatroniCluster struct {
	Members []PatroniMember
	Paused  bool
}

// Patroni is a client for the Patroni REST API of one cluster.
type Patroni struct {
	hosts    []string
	port     int
	user     string
	password string
	ssl      bool
	client   *http.Client
}

// NewPatroni builds a client for the listed candidate hosts.
func NewPatroni(hosts []string, port int, user, password string, ssl bool, caFile string) *Patroni {
	if port == 0 {
		port = 8008
	}
	client := &http.Client{Timeout: httpTimeout}
	if ssl {
		pool := x509.NewCertPool()
		// Only the CA certificate is ever read; ca.key is never touched.
		if data, err := os.ReadFile(caFile); err == nil {
			pool.AppendCertsFromPEM(data)
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}
	}
	return &Patroni{hosts: hosts, port: port, user: user, password: password, ssl: ssl, client: client}
}

// Scheme returns the URL scheme of the client.
func (p *Patroni) Scheme() string {
	if p.ssl {
		return "https"
	}
	return "http"
}

// URL renders the REST endpoint of one host.
func (p *Patroni) URL(host, path string) string {
	return fmt.Sprintf("%s://%s:%d%s", p.Scheme(), host, p.port, path)
}

// rawCluster mirrors the Patroni /cluster response.
type rawCluster struct {
	Paused  bool `json:"pause"`
	Members []struct {
		Name           string          `json:"name"`
		Role           string          `json:"role"`
		State          string          `json:"state"`
		Host           string          `json:"host"`
		Port           int             `json:"port"`
		Timeline       *int            `json:"timeline"`
		Lag            json.RawMessage `json:"lag"`
		PendingRestart bool            `json:"pending_restart"`
	} `json:"members"`
}

// Cluster queries every candidate host until one answers.
func (p *Patroni) Cluster(ctx context.Context) (PatroniCluster, string, error) {
	var lastErr error
	for _, host := range p.hosts {
		endpoint := p.URL(host, "/cluster")
		body, err := p.get(ctx, endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		var doc rawCluster
		if err := json.Unmarshal(body, &doc); err != nil {
			lastErr = fmt.Errorf("解析 Patroni /cluster 响应失败: %w", err)
			continue
		}
		out := PatroniCluster{Paused: doc.Paused, Members: make([]PatroniMember, 0, len(doc.Members))}
		for _, m := range doc.Members {
			member := PatroniMember{
				Name:           m.Name,
				Host:           m.Host,
				Port:           m.Port,
				Role:           m.Role,
				State:          m.State,
				Timeline:       m.Timeline,
				PendingRestart: m.PendingRestart,
			}
			member.LagBytes = parseLag(m.Lag)
			out.Members = append(out.Members, member)
		}
		return out, endpoint, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("集群没有任何可用的 Patroni 成员")
	}
	return PatroniCluster{}, p.URL(firstHost(p.hosts), "/cluster"), lastErr
}

// Config returns the DCS configuration of the cluster.
func (p *Patroni) Config(ctx context.Context) (map[string]any, string, error) {
	var lastErr error
	for _, host := range p.hosts {
		endpoint := p.URL(host, "/config")
		body, err := p.get(ctx, endpoint)
		if err != nil {
			lastErr = err
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(body, &doc); err != nil {
			lastErr = fmt.Errorf("解析 Patroni /config 响应失败: %w", err)
			continue
		}
		return doc, endpoint, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("集群没有任何可用的 Patroni 成员")
	}
	return nil, p.URL(firstHost(p.hosts), "/config"), lastErr
}

// get performs one authenticated Patroni request.
func (p *Patroni) get(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	if p.user != "" {
		req.SetBasicAuth(p.user, p.password)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Patroni 返回 %d", resp.StatusCode)
	}
	return body, nil
}

// parseLag converts Patroni's `lag` field, which may be a number, "unknown" or
// absent.
func parseLag(raw json.RawMessage) *int64 {
	if len(raw) == 0 {
		return nil
	}
	var num int64
	if err := json.Unmarshal(raw, &num); err == nil {
		if num < 0 {
			return nil
		}
		return &num
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil && v >= 0 {
			return &v
		}
	}
	return nil
}

// firstHost returns the first host of a list, empty when the list is empty.
func firstHost(hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	return hosts[0]
}

// PatroniPort reads the REST API port from cluster variables.
func PatroniPort(vars map[string]any) int {
	if v, ok := vars["patroni_port"]; ok {
		switch t := v.(type) {
		case int:
			return t
		case float64:
			return int(t)
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	return 8008
}

// PatroniCredentials reads the REST API credentials from cluster variables.
func PatroniCredentials(vars map[string]any) (string, string, bool) {
	ssl, _ := vars["patroni_ssl_enabled"].(bool)
	user, _ := vars["patroni_username"].(string)
	password, _ := vars["patroni_password"].(string)
	return user, password, ssl
}
