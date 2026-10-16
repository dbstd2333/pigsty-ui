package status

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// DefaultTTL is the cache lifetime of the status collectors.
const DefaultTTL = 15 * time.Second

// MemoryTTL is the cache lifetime of the ad-hoc memory collection.
const MemoryTTL = 10 * time.Minute

// Default ports of the Pigsty infrastructure services (roles/infra/defaults).
const (
	DefaultVictoriaPort     = 8428
	DefaultAlertmanagerPort = 9059
	DefaultGrafanaPort      = 3000
	DefaultNodeExporterPort = 9100
)

// Settings are the address overrides configured in the console.
type Settings struct {
	VictoriaURL     string
	AlertmanagerURL string
	GrafanaURL      string
}

// Source supplies the console inventory, settings and Pigsty home.
type Source interface {
	Model(ctx context.Context) (*inventory.Model, error)
	Settings() Settings
	Home() string
}

// AdHocFunc runs one read-only ansible ad-hoc command on a host.
type AdHocFunc func(ctx context.Context, host, command string) (string, error)

// Options configures the status service.
type Options struct {
	Source   Source
	Logger   *slog.Logger
	Simulate bool
	AdHoc    AdHocFunc
	TTL      time.Duration
}

// Service collects external state on demand and caches it for a short time.
type Service struct {
	source   Source
	logger   *slog.Logger
	simulate bool
	adhoc    AdHocFunc
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	at    time.Time
	ttl   time.Duration
	value any
}

// New builds the status service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{source: opts.Source, logger: logger, simulate: opts.Simulate, adhoc: opts.AdHoc, ttl: ttl, cache: map[string]cacheEntry{}}
}

// NodeStatus is the API contract type of one node.
type NodeStatus struct {
	IP                string   `json:"ip"`
	Up                *bool    `json:"up"`
	CPUCores          *int     `json:"cpuCores"`
	CPUUsage          *float64 `json:"cpuUsage"`
	Load1             *float64 `json:"load1"`
	MemTotalBytes     *int64   `json:"memTotalBytes"`
	MemAvailableBytes *int64   `json:"memAvailableBytes"`
	DiskTotalBytes    *int64   `json:"diskTotalBytes"`
	DiskFreeBytes     *int64   `json:"diskFreeBytes"`
	Managed           bool     `json:"managed"`
}

// ClusterStatus is the API contract type of one PG cluster.
type ClusterStatus struct {
	Cluster string          `json:"cluster"`
	Source  plan.SourceInfo `json:"source"`
	Paused  *bool           `json:"paused"`
	Leader  *string         `json:"leader"`
	Members []PatroniMember `json:"members"`
	Healthy *bool           `json:"healthy"`
}

// MemoryService is the measured memory of one systemd unit.
type MemoryService struct {
	Name  string `json:"name"`
	Bytes *int64 `json:"bytes"`
}

// DerivedURLs are the addresses derived from the inventory.
type DerivedURLs struct {
	VictoriaURL     string
	AlertmanagerURL string
	GrafanaURL      string
}

// Nodes returns the node list of the inventory with its metrics.
func (s *Service) Nodes(ctx context.Context) (plan.SourceInfo, []NodeStatus) {
	if cached, ok := s.cached("nodes"); ok {
		entry := cached.(nodesEntry)
		return entry.source, entry.items
	}
	model, err := s.source.Model(ctx)
	if err != nil {
		src := plan.FailedSource("inventory", err.Error())
		return src, []NodeStatus{}
	}
	src, items := s.collectNodes(ctx, model)
	s.store("nodes", nodesEntry{source: src, items: items}, s.ttl)
	return src, items
}

type nodesEntry struct {
	source plan.SourceInfo
	items  []NodeStatus
}

// ManagedNodes returns the set of hosts that report node_exporter metrics.
func (s *Service) ManagedNodes(ctx context.Context) map[string]bool {
	_, items := s.Nodes(ctx)
	out := map[string]bool{}
	for _, n := range items {
		if n.Managed {
			out[n.IP] = true
		}
	}
	return out
}

// Clusters returns the status of every PG cluster; a failing cluster does not
// affect the others.
func (s *Service) Clusters(ctx context.Context) []ClusterStatus {
	model, err := s.source.Model(ctx)
	if err != nil {
		return []ClusterStatus{}
	}
	clusters := model.Clusters(inventory.ModulePGSQL)
	out := make([]ClusterStatus, len(clusters))
	var wg sync.WaitGroup
	for i, c := range clusters {
		wg.Add(1)
		go func(i int, c inventory.Cluster) {
			defer wg.Done()
			out[i] = s.clusterStatus(ctx, model, c)
		}(i, c)
	}
	wg.Wait()
	return out
}

// Cluster returns the status of one PG cluster.
func (s *Service) Cluster(ctx context.Context, name string) (ClusterStatus, error) {
	model, err := s.source.Model(ctx)
	if err != nil {
		return ClusterStatus{}, err
	}
	c, ok := model.Cluster(inventory.ModulePGSQL, name)
	if !ok {
		return ClusterStatus{}, fmt.Errorf("inventory 中没有 PG 集群 %s", name)
	}
	if cached, ok := s.cached("cluster:" + c.Identity); ok {
		return cached.(ClusterStatus), nil
	}
	st := s.clusterStatus(ctx, model, c)
	s.store("cluster:"+c.Identity, st, s.ttl)
	return st, nil
}

// PatroniMembers returns the Patroni members of a cluster, ok=false when the
// cluster state could not be read.
func (s *Service) PatroniMembers(ctx context.Context, cluster string) ([]PatroniMember, bool) {
	st, err := s.Cluster(ctx, cluster)
	if err != nil {
		return nil, false
	}
	if !st.Source.OK {
		return st.Members, false
	}
	return st.Members, true
}

// PGConfig returns the effective PostgreSQL parameters of a cluster.
func (s *Service) PGConfig(ctx context.Context, name string) (plan.SourceInfo, map[string]string) {
	key := "pgconfig:" + name
	if cached, ok := s.cached(key); ok {
		entry := cached.(pgConfigEntry)
		return entry.source, entry.parameters
	}
	model, err := s.source.Model(ctx)
	if err != nil {
		return plan.FailedSource("inventory", err.Error()), map[string]string{}
	}
	c, ok := model.Cluster(inventory.ModulePGSQL, name)
	if !ok {
		return plan.FailedSource("inventory", "inventory 中没有 PG 集群 "+name), map[string]string{}
	}
	src, params := s.collectPGConfig(ctx, c)
	s.store(key, pgConfigEntry{source: src, parameters: params}, s.ttl)
	return src, params
}

type pgConfigEntry struct {
	source     plan.SourceInfo
	parameters map[string]string
}

// Alerts returns the currently firing Alertmanager alerts.
func (s *Service) Alerts(ctx context.Context) (plan.SourceInfo, []Alert) {
	if cached, ok := s.cached("alerts"); ok {
		entry := cached.(alertsEntry)
		return entry.source, entry.items
	}
	src, items := s.collectAlerts(ctx)
	s.store("alerts", alertsEntry{source: src, items: items}, s.ttl)
	return src, items
}

type alertsEntry struct {
	source plan.SourceInfo
	items  []Alert
}

// Drift compares the inventory with the live Patroni state.
func (s *Service) Drift(ctx context.Context) []Drift {
	model, err := s.source.Model(ctx)
	if err != nil {
		return []Drift{}
	}
	clusters := model.Clusters(inventory.ModulePGSQL)
	out := []Drift{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range clusters {
		wg.Add(1)
		go func(c inventory.Cluster) {
			defer wg.Done()
			items := s.clusterDrift(ctx, model, c)
			mu.Lock()
			out = append(out, items...)
			mu.Unlock()
		}(c)
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Cluster != out[j].Cluster {
			return out[i].Cluster < out[j].Cluster
		}
		return out[i].Host < out[j].Host
	})
	return out
}

// Derived returns the service addresses derived from the inventory.
func (s *Service) Derived(ctx context.Context) DerivedURLs {
	out := DerivedURLs{
		VictoriaURL:     fmt.Sprintf("http://%s:%d", "localhost", DefaultVictoriaPort),
		AlertmanagerURL: fmt.Sprintf("http://%s:%d", "localhost", DefaultAlertmanagerPort),
		GrafanaURL:      fmt.Sprintf("http://%s:%d", "localhost", DefaultGrafanaPort),
	}
	model, err := s.source.Model(ctx)
	if err != nil {
		return out
	}
	hosts := model.InfraHosts()
	if len(hosts) == 0 {
		return out
	}
	host := hosts[0]
	vars := model.GlobalVars()
	if g, ok := model.Group("infra"); ok {
		vars = inventory.MergeVars(vars, g.Vars)
	}
	out.VictoriaURL = fmt.Sprintf("http://%s:%d", host, portVar(vars, "vmetrics_port", DefaultVictoriaPort))
	out.AlertmanagerURL = fmt.Sprintf("http://%s:%d", host, portVar(vars, "alertmanager_port", DefaultAlertmanagerPort))
	out.GrafanaURL = fmt.Sprintf("http://%s:%d", host, portVar(vars, "grafana_port", DefaultGrafanaPort))
	return out
}

// URLs resolves the effective service addresses (settings override the derived
// ones).
func (s *Service) URLs(ctx context.Context) DerivedURLs {
	derived := s.Derived(ctx)
	settings := s.source.Settings()
	if settings.VictoriaURL != "" {
		derived.VictoriaURL = strings.TrimSuffix(settings.VictoriaURL, "/")
	}
	if settings.AlertmanagerURL != "" {
		derived.AlertmanagerURL = strings.TrimSuffix(settings.AlertmanagerURL, "/")
	}
	if settings.GrafanaURL != "" {
		derived.GrafanaURL = strings.TrimSuffix(settings.GrafanaURL, "/")
	}
	return derived
}

// Settings exposes the configured overrides.
func (s *Service) Settings() Settings { return s.source.Settings() }

// Home returns the Pigsty home directory.
func (s *Service) Home() string { return s.source.Home() }

// CACertPath returns the CA certificate used to verify Patroni TLS.
func (s *Service) CACertPath() string {
	return filepath.Join(s.source.Home(), "files", "pki", "ca.crt")
}

// collectNodes gathers the node metrics of the whole inventory.
func (s *Service) collectNodes(ctx context.Context, model *inventory.Model) (plan.SourceInfo, []NodeStatus) {
	nodes := model.Nodes()
	items := make([]NodeStatus, 0, len(nodes))
	for _, n := range nodes {
		items = append(items, NodeStatus{IP: n.IP})
	}
	url := s.URLs(ctx).VictoriaURL
	if s.simulate {
		return s.simulateNodes(nodes, items)
	}
	vm := NewVM(url)
	samples, err := s.nodeSamples(ctx, vm)
	if err != nil {
		return plan.FailedSource(vm.Base(), err.Error()), items
	}
	for i := range items {
		ip := items[i].IP
		if v, ok := samples.up[ip]; ok {
			up := v > 0
			items[i].Up = &up
		}
		if v, ok := samples.cores[ip]; ok {
			cores := int(v)
			items[i].CPUCores = &cores
		}
		if v, ok := samples.cpu[ip]; ok {
			usage := v
			items[i].CPUUsage = &usage
		}
		if v, ok := samples.load[ip]; ok {
			load := v
			items[i].Load1 = &load
		}
		if v, ok := samples.memTotal[ip]; ok {
			total := int64(v)
			items[i].MemTotalBytes = &total
		}
		if v, ok := samples.memAvail[ip]; ok {
			available := int64(v)
			items[i].MemAvailableBytes = &available
		}
		if v, ok := pickDisk(samples.diskTotal, ip); ok {
			total := int64(v)
			items[i].DiskTotalBytes = &total
		}
		if v, ok := pickDisk(samples.diskFree, ip); ok {
			free := int64(v)
			items[i].DiskFreeBytes = &free
		}
		items[i].Managed = items[i].Up != nil
	}
	return plan.NewSource(url), items
}

// nodeSamples holds the metric vectors used to render the node list.
type nodeSamples struct {
	up        map[string]float64
	cores     map[string]float64
	cpu       map[string]float64
	load      map[string]float64
	memTotal  map[string]float64
	memAvail  map[string]float64
	diskTotal []Series
	diskFree  []Series
}

// nodeSamples runs the Pigsty node_* queries (job="node") in parallel.
func (s *Service) nodeSamples(ctx context.Context, vm *VM) (nodeSamples, error) {
	out := nodeSamples{}
	results := make([][]Series, 8)
	exprs := []string{
		`up{job="node"}`,
		`count by (ip, instance) (node_cpu_seconds_total{mode="idle"})`,
		`1 - avg by (ip, instance) (rate(node_cpu_seconds_total{mode="idle"}[5m]))`,
		`node_load1`,
		`node_memory_MemTotal_bytes`,
		`node_memory_MemAvailable_bytes`,
		`node_filesystem_size_bytes{fstype!~"tmpfs|overlay|squashfs|ramfs"}`,
		`node_filesystem_avail_bytes{fstype!~"tmpfs|overlay|squashfs|ramfs"}`,
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		lastErr error
	)
	for i, expr := range exprs {
		wg.Add(1)
		go func(i int, expr string) {
			defer wg.Done()
			samples, err := vm.Query(ctx, expr)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if lastErr == nil {
					lastErr = err
				}
				return
			}
			results[i] = samples
		}(i, expr)
	}
	wg.Wait()
	if lastErr != nil && len(results[0]) == 0 {
		return out, lastErr
	}
	out.up = index(results[0])
	out.cores = index(results[1])
	out.cpu = index(results[2])
	out.load = index(results[3])
	out.memTotal = index(results[4])
	out.memAvail = index(results[5])
	out.diskTotal = results[6]
	out.diskFree = results[7]
	return out, nil
}

// pickDisk prefers the data mount of a host over the root filesystem.
func pickDisk(samples []Series, ip string) (float64, bool) {
	best, bestRank := 0.0, -1
	for _, s := range samples {
		if s.InstanceIP() != ip {
			continue
		}
		rank := 0
		switch s.Labels["mountpoint"] {
		case "/data":
			rank = 2
		case "/":
			rank = 1
		}
		if rank > bestRank || (rank == bestRank && s.Value > best) {
			best, bestRank = s.Value, rank
		}
	}
	return best, bestRank >= 0
}

// clusterStatus queries the Patroni REST API of one cluster.
func (s *Service) clusterStatus(ctx context.Context, model *inventory.Model, c inventory.Cluster) ClusterStatus {
	st := ClusterStatus{Cluster: c.Identity, Members: []PatroniMember{}}
	if s.simulate {
		return s.simulateCluster(model, c, st)
	}
	client := s.patroniFor(model, c)
	if client == nil {
		st.Source = plan.FailedSource("patroni://"+c.Identity, "集群没有任何可用的主机")
		return st
	}
	cluster, endpoint, err := client.Cluster(ctx)
	if err != nil {
		st.Source = plan.FailedSource(endpoint, err.Error())
		return st
	}
	st.Source = plan.NewSource(endpoint)
	paused := cluster.Paused
	st.Paused = &paused
	st.Members = cluster.Members
	healthy := true
	leader := ""
	for _, m := range cluster.Members {
		if m.Role == "leader" || m.Role == "standby_leader" {
			leader = m.Host
		}
		if m.State != "running" && m.State != "streaming" {
			healthy = false
		}
	}
	if leader == "" {
		healthy = false
	} else {
		st.Leader = &leader
	}
	st.Healthy = &healthy
	return st
}

// patroniFor builds a Patroni client for one cluster from the inventory.
func (s *Service) patroniFor(model *inventory.Model, c inventory.Cluster) *Patroni {
	if len(c.Hosts) == 0 {
		return nil
	}
	user, password, ssl := PatroniCredentials(c.Vars)
	return NewPatroni(c.Hosts, PatroniPort(c.Vars), user, password, ssl, s.CACertPath())
}

// collectPGConfig reads the DCS PostgreSQL parameters of one cluster.
func (s *Service) collectPGConfig(ctx context.Context, c inventory.Cluster) (plan.SourceInfo, map[string]string) {
	if s.simulate {
		return s.simulatePGConfig(c)
	}
	model, err := s.source.Model(ctx)
	if err != nil {
		return plan.FailedSource("inventory", err.Error()), map[string]string{}
	}
	client := s.patroniFor(model, c)
	if client == nil {
		return plan.FailedSource("patroni://"+c.Identity, "集群没有任何可用的主机"), map[string]string{}
	}
	doc, endpoint, err := client.Config(ctx)
	if err != nil {
		return plan.FailedSource(endpoint, err.Error()), map[string]string{}
	}
	params := map[string]string{}
	postgres, _ := doc["postgresql"].(map[string]any)
	if raw, ok := postgres["parameters"].(map[string]any); ok {
		for k, v := range raw {
			params[k] = scalarString(v)
		}
	}
	return plan.NewSource(endpoint), params
}

// collectAlerts reads the firing alerts from Alertmanager.
func (s *Service) collectAlerts(ctx context.Context) (plan.SourceInfo, []Alert) {
	url := s.URLs(ctx).AlertmanagerURL
	if s.simulate {
		return s.simulateAlerts(ctx)
	}
	client := NewAlertmanager(url)
	items, err := client.Alerts(ctx)
	if err != nil {
		return plan.FailedSource(client.Base(), err.Error()), []Alert{}
	}
	return plan.NewSource(client.Base()), items
}

// scalarString renders an inventory or DCS scalar the way the API expects it.
func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

// portVar reads a port from variables with a default.
func portVar(vars map[string]any, key string, fallback int) int {
	if v, ok := vars[key]; ok {
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
	return fallback
}

// cached returns a fresh cache entry.
func (s *Service) cached(key string) (any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[key]
	if !ok {
		return nil, false
	}
	ttl := entry.ttl
	if ttl <= 0 {
		ttl = s.ttl
	}
	if time.Since(entry.at) > ttl {
		return nil, false
	}
	return entry.value, true
}

// store caches one value for the given lifetime.
func (s *Service) store(key string, value any, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache[key] = cacheEntry{at: time.Now(), ttl: ttl, value: value}
}

// storeMemory caches the memory collection of one host for MemoryTTL.
func (s *Service) storeMemory(ip string, entry memoryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache["memory:"+ip] = cacheEntry{at: time.Now(), ttl: MemoryTTL, value: entry}
}
