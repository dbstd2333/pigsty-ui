// Package inventory parses pigsty.yml inventories and computes effective
// parameter values, using ansible-inventory when it is available.
package inventory

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// LoadError describes why an inventory file could not be parsed.
type LoadError struct {
	Message string
	Source  string // "yaml" or "inventory"
	Line    int
	Column  int
}

// Error implements the error interface.
func (e *LoadError) Error() string { return e.Message }

// Group is one entry under all.children.
type Group struct {
	Name string
	Vars map[string]any
	// Hosts maps a host address onto its host vars.
	Hosts map[string]map[string]any
	Order int
}

// Model is a parsed Pigsty inventory; groups keep their file order.
type Model struct {
	vars       map[string]any
	groups     map[string]*Group
	groupOrder []string
	hostGroups map[string][]string
}

type rawInventory struct {
	All struct {
		Vars     map[string]any `yaml:"vars"`
		Children map[string]struct {
			Hosts map[string]map[string]any `yaml:"hosts"`
			Vars  map[string]any            `yaml:"vars"`
		} `yaml:"children"`
	} `yaml:"all"`
}

var linePattern = regexp.MustCompile(`line (\d+)(?::(\d+))?`)

// Load parses a pigsty.yml inventory document.
func Load(data []byte) (*Model, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, yamlLoadError(err)
	}
	all, ok := doc["all"].(map[string]any)
	if !ok {
		return nil, &LoadError{Message: "invalid config: `all` not found", Source: "inventory"}
	}
	if _, ok := all["children"].(map[string]any); !ok {
		return nil, &LoadError{Message: "invalid config: `all.children` not found", Source: "inventory"}
	}

	var raw rawInventory
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, yamlLoadError(err)
	}

	m := &Model{
		vars:       raw.All.Vars,
		groups:     map[string]*Group{},
		hostGroups: map[string][]string{},
	}
	if m.vars == nil {
		m.vars = map[string]any{}
	}
	names := make([]string, 0, len(raw.All.Children))
	for name := range raw.All.Children {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		rc := raw.All.Children[name]
		g := &Group{Name: name, Vars: rc.Vars, Hosts: rc.Hosts, Order: i}
		if g.Vars == nil {
			g.Vars = map[string]any{}
		}
		if g.Hosts == nil {
			g.Hosts = map[string]map[string]any{}
		}
		m.groups[name] = g
		m.groupOrder = append(m.groupOrder, name)
		for host := range g.Hosts {
			m.hostGroups[host] = append(m.hostGroups[host], name)
		}
	}
	for host := range m.hostGroups {
		sort.Strings(m.hostGroups[host])
	}
	return m, nil
}

// yamlLoadError converts a yaml error into a LoadError with line information.
func yamlLoadError(err error) *LoadError {
	le := &LoadError{Message: err.Error(), Source: "yaml"}
	if m := linePattern.FindStringSubmatch(err.Error()); m != nil {
		if n, convErr := strconv.Atoi(m[1]); convErr == nil {
			le.Line = n
		}
		if m[2] != "" {
			if n, convErr := strconv.Atoi(m[2]); convErr == nil {
				le.Column = n
			}
		}
	}
	return le
}

// GlobalVars returns all.vars.
func (m *Model) GlobalVars() map[string]any { return m.vars }

// Groups returns groups in file order.
func (m *Model) Groups() []*Group {
	out := make([]*Group, 0, len(m.groupOrder))
	for _, name := range m.groupOrder {
		out = append(out, m.groups[name])
	}
	return out
}

// Hosts returns every host address, sorted as an IP list.
func (m *Model) Hosts() []string {
	out := make([]string, 0, len(m.hostGroups))
	for host := range m.hostGroups {
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool { return compareHosts(out[i], out[j]) < 0 })
	return out
}

// GroupsOf returns the groups a host belongs to, sorted by name.
func (m *Model) GroupsOf(host string) []string { return m.hostGroups[host] }

// HostVars returns the host vars of one host.
func (m *Model) HostVars(host string) (map[string]any, bool) {
	for _, name := range m.hostGroups[host] {
		if hv, ok := m.groups[name].Hosts[host]; ok {
			if hv == nil {
				return map[string]any{}, true
			}
			return hv, true
		}
	}
	return nil, false
}

// GroupHostNames returns the host addresses of a group in sorted order.
func (g *Group) HostNames() []string {
	out := make([]string, 0, len(g.Hosts))
	for host := range g.Hosts {
		out = append(out, host)
	}
	sort.Slice(out, func(i, j int) bool { return compareHosts(out[i], out[j]) < 0 })
	return out
}

// compareHosts compares two addresses numerically when both are IPv4.
func compareHosts(a, b string) int {
	pa, oka := ipv4Parts(a)
	pb, okb := ipv4Parts(b)
	if oka && okb {
		for i := 0; i < 4; i++ {
			if pa[i] != pb[i] {
				if pa[i] < pb[i] {
					return -1
				}
				return 1
			}
		}
		return 0
	}
	return strings.Compare(a, b)
}

// ipv4Parts splits a dotted quad into numbers.
func ipv4Parts(s string) ([4]int, bool) {
	var out [4]int
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// moduleOf infers the module of a group from its identity parameters.
func moduleOf(g *Group) string {
	if _, ok := g.Vars["pg_cluster"]; ok {
		return "PGSQL"
	}
	if _, ok := g.Vars["etcd_cluster"]; ok {
		return "ETCD"
	}
	if _, ok := g.Vars["minio_cluster"]; ok {
		return "MINIO"
	}
	if _, ok := g.Vars["redis_cluster"]; ok {
		return "REDIS"
	}
	if _, ok := g.Vars["apps"]; ok {
		return "APP"
	}
	if _, ok := g.Vars["app"]; ok {
		return "APP"
	}
	if _, ok := g.Vars["docker_enabled"]; ok {
		return "DOCKER"
	}
	if g.Name == "infra" {
		return "INFRA"
	}
	for host := range g.Hosts {
		if _, ok := g.Hosts[host]["infra_seq"]; ok {
			return "INFRA"
		}
	}
	return "NODE"
}

// Module returns the module of a group.
func (g *Group) Module() string { return moduleOf(g) }

// HostInfo is one entry of the inventory host list.
type HostInfo struct {
	IP     string   `json:"ip"`
	Groups []string `json:"groups"`
}

// GroupInfo is one entry of the inventory group list.
type GroupInfo struct {
	Name  string   `json:"name"`
	Hosts []string `json:"hosts"`
	// Module is nil when the group has no recognisable module.
	Module *string `json:"module"`
}

// Lists returns the hosts and groups of the inventory for the API.
func (m *Model) Lists() ([]HostInfo, []GroupInfo) {
	hosts := make([]HostInfo, 0, len(m.hostGroups))
	for _, host := range m.Hosts() {
		groups := m.HostGroups(host)
		hosts = append(hosts, HostInfo{IP: host, Groups: groups})
	}
	groups := make([]GroupInfo, 0, len(m.groupOrder))
	for _, g := range m.Groups() {
		gi := GroupInfo{Name: g.Name, Hosts: g.HostNames()}
		if len(g.Hosts) > 0 {
			mod := g.Module()
			gi.Module = &mod
		}
		groups = append(groups, gi)
	}
	return hosts, groups
}

// HostGroups returns the groups of a host, empty when the host is unknown.
func (m *Model) HostGroups(host string) []string {
	out := m.hostGroups[host]
	if out == nil {
		return []string{}
	}
	return out
}

// String renders the model size, used in logs.
func (m *Model) String() string {
	return fmt.Sprintf("inventory(%d groups, %d hosts)", len(m.groupOrder), len(m.hostGroups))
}
