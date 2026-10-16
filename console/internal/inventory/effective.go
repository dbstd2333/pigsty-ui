package inventory

import (
	"errors"
	"fmt"
	"sort"
)

// ErrHostNotFound is returned when a host is not part of the inventory.
var ErrHostNotFound = errors.New("host not found")

// Var is one effective parameter value of a host.
type Var struct {
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Source      string `json:"source" enum:"default,global,group,host"`
	SourceGroup string `json:"sourceGroup,omitempty"`
	Sensitive   bool   `json:"sensitive"`
}

// Effective computes effective values from schema defaults, global vars, group
// vars and host vars, following host > group > global > default precedence.
func (m *Model) Effective(host string, defaults map[string]any, sensitive func(string) bool) ([]Var, error) {
	hostVars, ok := m.HostVars(host)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrHostNotFound, host)
	}

	values := make(map[string]any, len(defaults))
	sources := make(map[string]string, len(defaults))
	groups := make(map[string]string, len(defaults))

	for k, v := range defaults {
		values[k] = v
		sources[k] = "default"
	}
	apply := func(vars map[string]any, source, group string) {
		for k, v := range vars {
			values[k] = v
			sources[k] = source
			groups[k] = group
		}
	}
	apply(m.vars, "global", "")
	for _, name := range m.GroupsOf(host) {
		apply(m.groups[name].Vars, "group", name)
	}
	apply(hostVars, "host", "")

	return buildVars(values, sources, groups, sensitive), nil
}

// EffectiveFromHostVars merges authoritative host vars (from ansible-inventory)
// with schema defaults, attributing each value to the layer that defines it.
func (m *Model) EffectiveFromHostVars(host string, hostVars map[string]any, defaults map[string]any,
	sensitive func(string) bool) ([]Var, error) {
	if _, ok := m.HostVars(host); !ok {
		return nil, fmt.Errorf("%w: %s", ErrHostNotFound, host)
	}
	layerOf, groupOf := m.sourceLookup(host)

	values := make(map[string]any, len(defaults)+len(hostVars))
	sources := make(map[string]string, len(defaults)+len(hostVars))
	groups := make(map[string]string, len(defaults)+len(hostVars))
	for k, v := range defaults {
		values[k] = v
		sources[k] = "default"
	}
	for k, v := range hostVars {
		values[k] = v
		sources[k] = "global"
	}
	for k := range values {
		layer, group := layerOf(k), groupOf(k)
		if layer == "" {
			// Not defined in the inventory file: it can only be a schema default.
			if _, ok := hostVars[k]; !ok {
				layer = "default"
			}
		}
		if layer != "" {
			sources[k] = layer
			groups[k] = group
		}
	}
	return buildVars(values, sources, groups, sensitive), nil
}

// sourceLookup returns helpers reporting the defining layer of a key. Groups
// are walked in ascending order because ansible merges group vars sorted by
// (depth, priority, name), so the last definition wins.
func (m *Model) sourceLookup(host string) (layerOf func(string) string, groupOf func(string) string) {
	hostVars, _ := m.HostVars(host)
	groups := m.GroupsOf(host)

	layerOf = func(key string) string {
		if _, ok := hostVars[key]; ok {
			return "host"
		}
		// Groups are walked in ascending precedence order, so the last one
		// that defines the key wins, matching Ansible's merge rules.
		winner := ""
		for _, name := range groups {
			if _, ok := m.groups[name].Vars[key]; ok {
				winner = name
			}
		}
		if winner != "" {
			return "group"
		}
		if _, ok := m.vars[key]; ok {
			return "global"
		}
		return ""
	}
	groupOf = func(key string) string {
		winner := ""
		for _, name := range groups {
			if _, ok := m.groups[name].Vars[key]; ok {
				winner = name
			}
		}
		return winner
	}
	return layerOf, groupOf
}

// buildVars converts the maps into a sorted slice for the API.
func buildVars(values map[string]any, sources map[string]string, groups map[string]string,
	sensitive func(string) bool) []Var {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Var, 0, len(keys))
	for _, k := range keys {
		v := Var{Key: k, Value: values[k], Source: sources[k], SourceGroup: groups[k]}
		if sensitive != nil {
			v.Sensitive = sensitive(k)
		}
		out = append(out, v)
	}
	return out
}
