package status

import (
	"context"
	"strconv"
	"strings"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// memoryUnits are the systemd units the memory page reports, in display order.
var memoryUnits = []struct {
	Name string
	Unit string
}{
	{"patroni", "patroni.service"},
	{"postgresql", "postgresql.service"},
	{"pgbouncer", "pgbouncer.service"},
	{"pg_exporter", "pg_exporter.service"},
	{"pgbouncer_exporter", "pgbouncer_exporter.service"},
	{"pgbackrest_exporter", "pgbackrest_exporter.service"},
	{"etcd", "etcd.service"},
	{"minio", "minio.service"},
	{"redis", "redis.service"},
	{"redis-6379", "redis-6379.service"},
	{"vector", "vector.service"},
	{"node_exporter", "node_exporter.service"},
	{"haproxy", "haproxy.service"},
	{"vip-manager", "vip-manager.service"},
	{"grafana-server", "grafana-server.service"},
	{"victoria-metrics", "victoria-metrics.service"},
	{"victoria-logs", "victoria-logs.service"},
	{"vmalert", "vmalert.service"},
	{"nginx", "nginx.service"},
}

// memoryEntry is the cached result of one ad-hoc memory collection.
type memoryEntry struct {
	source   plan.SourceInfo
	services []MemoryService
}

// Memory returns the actual memory usage of the Pigsty services of one host.
// The result is cached for ten minutes; refresh bypasses the cache.
func (s *Service) Memory(ctx context.Context, ip string, refresh bool) (plan.SourceInfo, []MemoryService) {
	key := "memory:" + ip
	if !refresh {
		if cached, ok := s.cached(key); ok {
			entry := cached.(memoryEntry)
			return entry.source, entry.services
		}
	}
	src, services := s.collectMemory(ctx, ip)
	s.storeMemory(ip, memoryEntry{source: src, services: services})
	return src, services
}

// collectMemory runs the read-only systemctl collection through ansible ad-hoc.
func (s *Service) collectMemory(ctx context.Context, ip string) (plan.SourceInfo, []MemoryService) {
	services := make([]MemoryService, 0, len(memoryUnits))
	for _, u := range memoryUnits {
		services = append(services, MemoryService{Name: u.Name})
	}
	if s.simulate {
		return s.simulateMemory(ip, services)
	}
	host := plan.AdhocSource(ip)
	if s.adhoc == nil {
		return plan.FailedSource(host, "ansible 不可用，无法采集实际内存"), services
	}
	command := memoryCommand()
	out, err := s.adhoc(ctx, ip, command)
	if err != nil {
		return plan.FailedSource(host, err.Error()), services
	}
	measured := parseSystemctlMemory(out)
	for i, u := range memoryUnits {
		if value, ok := measured[u.Unit]; ok {
			services[i].Bytes = value
		}
	}
	return plan.NewSource(host), services
}

// memoryCommand renders the read-only systemctl show invocation.
func memoryCommand() string {
	units := make([]string, 0, len(memoryUnits))
	for _, u := range memoryUnits {
		units = append(units, u.Unit)
	}
	return "systemctl show " + strings.Join(units, " ") + " -p Id -p MemoryCurrent"
}

// parseSystemctlMemory parses `systemctl show ... -p Id -p MemoryCurrent`
// output, ignoring units that are not installed on the host.
func parseSystemctlMemory(out string) map[string]*int64 {
	result := map[string]*int64{}
	var unit string
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Id="):
			unit = strings.TrimPrefix(line, "Id=")
			if unit != "" {
				result[unit] = nil
			}
		case strings.HasPrefix(line, "MemoryCurrent="):
			if unit == "" {
				continue
			}
			value := strings.TrimPrefix(line, "MemoryCurrent=")
			if bytes, ok := parseMemoryValue(value); ok {
				result[unit] = &bytes
			}
		}
	}
	return result
}

// parseMemoryValue converts a systemd MemoryCurrent value, rejecting the
// "[not set]" and "infinite" markers.
func parseMemoryValue(value string) (int64, bool) {
	v := strings.TrimSpace(value)
	if v == "" || strings.HasPrefix(v, "[") || strings.EqualFold(v, "infinite") {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	// systemd reports uint64 max for unlimited units.
	if n >= int64(^uint64(0)>>1) {
		return 0, false
	}
	return n, true
}
