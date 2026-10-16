package status

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// GiB is the byte size of one gibibyte, used by the simulation.
const GiB = 1024 * 1024 * 1024

// seedOf derives a stable seed from a name, so simulated values never change
// between restarts.
func seedOf(v string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(v))
	return h.Sum64()
}

// simulateNodes generates stable node metrics for every inventory host.
func (s *Service) simulateNodes(nodes []inventory.Node, items []NodeStatus) (plan.SourceInfo, []NodeStatus) {
	for i := range items {
		seed := seedOf(items[i].IP)
		cores := int(2 + seed%14)          // 2 - 15 cores
		memTotal := int64(8+seed%57) * GiB // 8 - 64 GiB
		diskTotal := int64(100+seed%400) * GiB
		up := true
		usage := math.Round((0.04+float64(seed%45)/100)*1000) / 1000
		load := math.Round(usage*float64(cores)*100) / 100
		available := int64(float64(memTotal) * (0.30 + float64(seed%40)/100))
		free := int64(float64(diskTotal) * (0.20 + float64(seed%60)/100))
		items[i].Up = &up
		items[i].CPUCores = &cores
		items[i].CPUUsage = &usage
		items[i].Load1 = &load
		items[i].MemTotalBytes = &memTotal
		items[i].MemAvailableBytes = &available
		items[i].DiskTotalBytes = &diskTotal
		items[i].DiskFreeBytes = &free
		items[i].Managed = true
	}
	return plan.NewSource("simulate://victoria/nodes"), items
}

// simulateCluster generates stable Patroni data. The first PG cluster of the
// inventory deliberately drifts: its simulated leader differs from the
// pg_role declared in pigsty.yml, so the drift view has something to show.
func (s *Service) simulateCluster(model *inventory.Model, c inventory.Cluster, st ClusterStatus) ClusterStatus {
	if len(c.Hosts) == 0 {
		st.Source = plan.FailedSource("simulate://patroni/"+c.Identity, "集群没有主机")
		return st
	}
	first := false
	if clusters := model.Clusters(inventory.ModulePGSQL); len(clusters) > 0 && clusters[0].Identity == c.Identity {
		first = true
	}
	seed := seedOf(c.Identity)
	members := make([]PatroniMember, 0, len(c.Hosts)+1)
	leaderHost := c.Primary
	if leaderHost == "" {
		leaderHost = c.Hosts[0]
	}
	if first {
		leaderHost = driftingLeader(c)
	}
	for _, host := range c.Hosts {
		member := PatroniMember{Name: memberName(c, host, model), Host: host, Port: 5432, State: "streaming"}
		timeline := int(1 + seed%3)
		member.Timeline = &timeline
		switch {
		case host == leaderHost:
			member.Role = "leader"
			member.State = "running"
		case c.Roles[host] == "offline":
			member.Role = "replica"
			member.State = "stopped"
			member.Timeline = nil
		default:
			member.Role = "replica"
			lag := int64(1024 * (1 + seed%64))
			member.LagBytes = &lag
			if seed%3 == 0 {
				member.PendingRestart = true
			}
		}
		members = append(members, member)
	}
	if first && leaderHost != "" && !containsHost(c.Hosts, leaderHost) {
		// The drifting leader is not in the inventory: a perfect unknown member.
		timeline := int(1 + seed%3)
		lag := int64(0)
		members = append(members, PatroniMember{
			Name: fmt.Sprintf("%s-%d", c.Identity, len(c.Hosts)+1), Host: leaderHost, Port: 5432,
			Role: "leader", State: "running", Timeline: &timeline, LagBytes: &lag,
		})
	}
	paused := false
	healthy := true
	st.Source = plan.NewSource("simulate://patroni/" + c.Identity)
	st.Paused = &paused
	st.Members = members
	st.Leader = &leaderHost
	st.Healthy = &healthy
	return st
}

// driftingLeader returns the simulated leader of the drifting cluster: the
// second host when there is one, otherwise a synthetic neighbour address.
func driftingLeader(c inventory.Cluster) string {
	if len(c.Hosts) > 1 {
		if c.Primary == c.Hosts[0] {
			return c.Hosts[1]
		}
		return c.Hosts[0]
	}
	return neighbourIP(c.Hosts[0])
}

// neighbourIP increments the last octet of an IPv4 address.
func neighbourIP(ip string) string {
	var a, b, c, d int
	if _, err := fmt.Sscanf(ip, "%d.%d.%d.%d", &a, &b, &c, &d); err != nil {
		return ip
	}
	if d < 254 {
		d++
	}
	return fmt.Sprintf("%d.%d.%d.%d", a, b, c, d)
}

// containsHost reports whether a host list contains one address.
func containsHost(hosts []string, host string) bool {
	for _, h := range hosts {
		if h == host {
			return true
		}
	}
	return false
}

// memberName renders the default Pigsty instance name.
func memberName(c inventory.Cluster, host string, model *inventory.Model) string {
	if model != nil {
		if vars, ok := model.HostVarsIn(c.Group, host); ok {
			if seq, ok := vars["pg_seq"]; ok {
				return fmt.Sprintf("%s-%v", c.Identity, seq)
			}
		}
	}
	return fmt.Sprintf("%s-%s", c.Identity, host)
}

// simulatePGConfig renders the DCS parameters a Pigsty cluster of this shape
// would carry after initialisation.
func (s *Service) simulatePGConfig(c inventory.Cluster) (plan.SourceInfo, map[string]string) {
	seed := seedOf(c.Identity)
	memoryMB := int64(8+seed%57) * 1024
	sharedBuffers := memoryMB / 4
	maxConn := "1000"
	if c.Vars["pg_max_conn"] != nil {
		maxConn = scalarString(c.Vars["pg_max_conn"])
	}
	workMem := sharedBuffers / 1000
	if workMem < 64 {
		workMem = 64
	}
	params := map[string]string{
		"shared_buffers":               fmt.Sprintf("%dMB", sharedBuffers),
		"maintenance_work_mem":         fmt.Sprintf("%dMB", sharedBuffers/4),
		"work_mem":                     fmt.Sprintf("%dMB", workMem),
		"effective_cache_size":         fmt.Sprintf("%dMB", memoryMB-sharedBuffers),
		"max_connections":              maxConn,
		"wal_buffers":                  "16MB",
		"hash_mem_multiplier":          "8.0",
		"huge_pages":                   "try",
		"autovacuum_max_workers":       "3",
		"max_replication_slots":        "10",
		"max_wal_senders":              "10",
		"wal_level":                    "replica",
		"synchronous_commit":           "on",
		"checkpoint_timeout":           "15min",
		"checkpoint_completion_target": "0.9",
	}
	return plan.NewSource("simulate://patroni/" + c.Identity + "/config"), params
}

// simulateAlerts generates one stable alert per condition that a cluster of
// this shape would plausibly report.
func (s *Service) simulateAlerts(ctx context.Context) (plan.SourceInfo, []Alert) {
	src := plan.NewSource("simulate://alertmanager")
	model, err := s.source.Model(ctx)
	if err != nil {
		return src, []Alert{}
	}
	now := time.Now().UTC()
	out := []Alert{}
	clusters := model.Clusters(inventory.ModulePGSQL)
	if len(clusters) > 0 {
		c := clusters[0]
		host := c.Primary
		if host == "" && len(c.Hosts) > 0 {
			host = c.Hosts[0]
		}
		out = append(out, Alert{
			Name:     "PgExporterDown",
			Severity: "warning",
			Summary:  fmt.Sprintf("WARN PgExporterDown %s-%s@%s:9100", c.Identity, host, host),
			StartsAt: now.Add(-37 * time.Minute),
			Labels: map[string]string{
				"alertname": "PgExporterDown", "severity": "warning",
				"cls": c.Identity, "ins": c.Identity + "-1", "ip": host, "instance": host + ":9630",
			},
		})
	}
	for _, node := range model.Nodes() {
		seed := seedOf(node.IP)
		if seed%5 != 0 {
			continue
		}
		out = append(out, Alert{
			Name:     "NodeFsSpaceFull",
			Severity: "warning",
			Summary:  fmt.Sprintf("WARN NodeFsSpaceFull %s: 数据盘剩余空间低于 20%%", node.IP),
			StartsAt: now.Add(-2*time.Hour - time.Duration(seed%60)*time.Minute),
			Labels: map[string]string{
				"alertname": "NodeFsSpaceFull", "severity": "warning",
				"ip": node.IP, "instance": node.IP + ":9100", "mountpoint": "/data",
			},
		})
	}
	return src, out
}

// simulateMemory generates stable per service memory values of one host.
func (s *Service) simulateMemory(ip string, services []MemoryService) (plan.SourceInfo, []MemoryService) {
	base := int64(16+seedOf(ip)%48) * 1024 * 1024
	for i := range services {
		seed := seedOf(ip + "/" + services[i].Name)
		switch services[i].Name {
		case "patroni":
			value := base
			services[i].Bytes = &value
		case "postgresql":
			value := int64(64+seed%256) * 1024 * 1024
			services[i].Bytes = &value
		case "pgbouncer", "pg_exporter", "vector", "node_exporter", "haproxy", "nginx":
			value := int64(8+seed%24) * 1024 * 1024
			services[i].Bytes = &value
		case "grafana-server", "victoria-metrics", "victoria-logs", "vmalert":
			value := int64(48+seed%160) * 1024 * 1024
			services[i].Bytes = &value
		case "etcd":
			value := int64(48+seed%96) * 1024 * 1024
			services[i].Bytes = &value
		case "minio":
			value := int64(96+seed%192) * 1024 * 1024
			services[i].Bytes = &value
		default:
			if len(services[i].Name) > 5 && services[i].Name[:5] == "redis" {
				value := int64(32+seed%128) * 1024 * 1024
				services[i].Bytes = &value
			}
		}
	}
	return plan.NewSource("simulate://ansible/" + ip), services
}
