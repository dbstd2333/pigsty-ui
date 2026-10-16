package status

import (
	"context"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
)

// DriftFix is the pg_role rewrite suggested for a role mismatch.
type DriftFix struct {
	Primary  string   `json:"primary"`
	Replicas []string `json:"replicas"`
}

// Drift is one difference between the inventory and the live cluster state.
type Drift struct {
	Kind    string    `json:"kind" enum:"role_mismatch,missing_member,unknown_member"`
	Cluster string    `json:"cluster"`
	Host    string    `json:"host"`
	Message string    `json:"message"`
	Fix     *DriftFix `json:"fix"`
}

// clusterDrift compares one cluster: inventory roles against Patroni roles, and
// the two membership lists against each other.
func (s *Service) clusterDrift(ctx context.Context, model *inventory.Model, c inventory.Cluster) []Drift {
	out := []Drift{}
	st, err := s.Cluster(ctx, c.Identity)
	if err != nil || !st.Source.OK {
		// Without live state there is nothing to compare against.
		return out
	}
	patroniByHost := map[string]PatroniMember{}
	for _, m := range st.Members {
		patroniByHost[m.Host] = m
	}
	leader := ""
	if st.Leader != nil {
		leader = *st.Leader
	}
	for _, host := range c.Hosts {
		role := c.Roles[host]
		member, ok := patroniByHost[host]
		if !ok {
			out = append(out, Drift{
				Kind:    "missing_member",
				Cluster: c.Identity,
				Host:    host,
				Message: "inventory 中的实例 " + host + " 不在 Patroni 集群成员中",
			})
			continue
		}
		wantLeader := role == "primary"
		isLeader := member.Role == "leader" || member.Role == "standby_leader"
		if wantLeader == isLeader {
			continue
		}
		fix := &DriftFix{Primary: leader, Replicas: []string{}}
		if fix.Primary == "" {
			fix.Primary = host
		}
		for _, other := range c.Hosts {
			if other != fix.Primary {
				fix.Replicas = append(fix.Replicas, other)
			}
		}
		message := "inventory 中 " + host + " 是 " + roleText(role) + "，Patroni 中是 " + roleText(member.Role) + "；建议回写 pg_role"
		out = append(out, Drift{Kind: "role_mismatch", Cluster: c.Identity, Host: host, Message: message, Fix: fix})
	}
	for _, m := range st.Members {
		known := false
		for _, host := range c.Hosts {
			if host == m.Host {
				known = true
			}
		}
		if !known {
			out = append(out, Drift{
				Kind:    "unknown_member",
				Cluster: c.Identity,
				Host:    m.Host,
				Message: "Patroni 成员 " + m.Name + "（" + m.Host + "）不在 inventory 中",
			})
		}
	}
	return out
}

// roleText renders a role for the drift message.
func roleText(role string) string {
	switch role {
	case "primary", "leader":
		return "主库"
	case "replica":
		return "副本"
	case "offline":
		return "离线实例"
	case "":
		return "未定义角色"
	default:
		return role
	}
}
