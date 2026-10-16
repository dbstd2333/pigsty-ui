package inventory

// Module names used by the inventory facts helpers.
const (
	ModulePGSQL = "PGSQL"
	ModuleRedis = "REDIS"
	ModuleEtcd  = "ETCD"
	ModuleMinio = "MINIO"
	ModuleInfra = "INFRA"
	ModuleNode  = "NODE"
	ModuleApp   = "APP"
)

// Cluster summarizes one module cluster (PGSQL, REDIS, ETCD, MINIO) of the
// inventory: its hosts, identity value, per-host roles and merged variables.
type Cluster struct {
	Group    string
	Identity string
	Module   string
	Hosts    []string // sorted host addresses
	Primary  string   // IP of the instance with pg_role: primary, empty when none
	Roles    map[string]string
	Vars     map[string]any // global variables overridden by group variables
}

// Clusters returns the clusters of one module in inventory order.
func (m *Model) Clusters(module string) []Cluster {
	out := []Cluster{}
	for _, g := range m.Groups() {
		if g.Module() != module {
			continue
		}
		if len(g.Hosts) == 0 {
			continue
		}
		out = append(out, m.clusterOf(g))
	}
	return out
}

// Cluster finds one cluster by group name or identity value.
func (m *Model) Cluster(module, name string) (Cluster, bool) {
	if name == "" {
		return Cluster{}, false
	}
	for _, c := range m.Clusters(module) {
		if c.Group == name || c.Identity == name {
			return c, true
		}
	}
	return Cluster{}, false
}

// Group returns one inventory group by name.
func (m *Model) Group(name string) (*Group, bool) {
	g, ok := m.groups[name]
	return g, ok
}

// HostVarsIn returns the host vars of one host inside one group. A host can
// belong to several groups (one node may be infra + pgsql + etcd at once), so
// instance level facts such as pg_role or pg_seq must be read from the owning
// group instead of from the first group that happens to list the host.
func (m *Model) HostVarsIn(group, host string) (map[string]any, bool) {
	g, ok := m.groups[group]
	if !ok {
		return nil, false
	}
	vars, ok := g.Hosts[host]
	if !ok {
		return nil, false
	}
	if vars == nil {
		return map[string]any{}, true
	}
	return vars, true
}

// clusterOf builds the fact view of one group.
func (m *Model) clusterOf(g *Group) Cluster {
	c := Cluster{
		Group:    g.Name,
		Identity: Identity(g),
		Module:   g.Module(),
		Hosts:    g.HostNames(),
		Roles:    map[string]string{},
		Vars:     MergeVars(m.GlobalVars(), g.Vars),
	}
	for _, host := range c.Hosts {
		role := m.hostRole(g, host, c.Vars)
		if role != "" {
			c.Roles[host] = role
		}
		if role == "primary" && c.Primary == "" {
			c.Primary = host
		}
	}
	return c
}

// hostRole resolves the pg_role of one host inside its own cluster group: the
// group host vars win over the group vars.
func (m *Model) hostRole(g *Group, host string, clusterVars map[string]any) string {
	if vars, ok := m.HostVarsIn(g.Name, host); ok {
		if role, ok := vars["pg_role"].(string); ok && role != "" {
			return role
		}
	}
	if role, ok := clusterVars["pg_role"].(string); ok {
		return role
	}
	return ""
}

// Identity returns the cluster identity parameter of a group.
func Identity(g *Group) string {
	for _, key := range []string{"pg_cluster", "redis_cluster", "etcd_cluster", "minio_cluster", "app", "apps"} {
		if v, ok := g.Vars[key].(string); ok && v != "" {
			return v
		}
	}
	return g.Name
}

// MergeVars merges two variable maps, the second one winning.
func MergeVars(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// Node describes one inventory host.
type Node struct {
	IP     string
	Groups []string
	Vars   map[string]any // global variables overridden by host vars
}

// Nodes returns every inventory host with its merged variables.
func (m *Model) Nodes() []Node {
	hosts := m.Hosts()
	out := make([]Node, 0, len(hosts))
	for _, host := range hosts {
		vars := MergeVars(m.GlobalVars(), nil)
		for _, gname := range m.GroupsOf(host) {
			if g, ok := m.groups[gname]; ok {
				vars = MergeVars(vars, g.Vars)
			}
		}
		if hv, ok := m.HostVars(host); ok {
			vars = MergeVars(vars, hv)
		}
		out = append(out, Node{IP: host, Groups: m.HostGroups(host), Vars: vars})
	}
	return out
}

// InfraHosts returns the hosts of the infra group.
func (m *Model) InfraHosts() []string {
	g, ok := m.groups["infra"]
	if !ok {
		return []string{}
	}
	return g.HostNames()
}
