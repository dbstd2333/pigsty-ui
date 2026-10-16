package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// preconditions evaluates the precondition list declared by one action.
func (p *Planner) preconditions(ctx context.Context, res *resolved) []plan.Precondition {
	out := make([]plan.Precondition, 0, len(res.action.Preconditions))
	for _, id := range res.action.Preconditions {
		switch id {
		case "cluster_absent":
			out = append(out, p.clusterAbsent(ctx, res))
		case "cluster_exists":
			out = append(out, p.clusterExists(ctx, res))
		case "source_exists":
			out = append(out, p.sourceExists(ctx, res))
		case "ips_in_cluster":
			out = append(out, p.ipsInCluster(res))
		case "not_primary":
			out = append(out, p.notPrimary(ctx, res))
		case "member_exists":
			out = append(out, p.memberExists(ctx, res))
		case "host_in_inventory":
			out = append(out, p.hostInInventory(res))
		case "nodes_managed":
			out = append(out, p.nodesManaged(ctx, res))
		case "backup_repo":
			out = append(out, p.backupRepo(res))
		case "pitr_window":
			out = append(out, p.pitrWindow(ctx, res))
		case "not_degraded":
			out = append(out, p.notDegraded(ctx, res))
		}
	}
	return out
}

// clusterAbsent verifies that a cluster has not been initialised yet.
func (p *Planner) clusterAbsent(ctx context.Context, res *resolved) plan.Precondition {
	if res.info == nil {
		return plan.Err("cluster_absent", "inventory 中没有集群 "+res.cluster)
	}
	if p.Facts == nil {
		return plan.Warn("cluster_absent", "无法确认集群是否已初始化（状态采集不可用）")
	}
	members, ok := p.Facts.PatroniMembers(ctx, res.cluster)
	if !ok {
		return plan.Warn("cluster_absent", "无法连接 Patroni 确认集群 "+res.cluster+" 是否已存在")
	}
	if len(members) > 0 {
		return plan.Err("cluster_absent", fmt.Sprintf("集群 %s 在 Patroni 中已有 %d 个成员，不能重复初始化", res.cluster, len(members)))
	}
	return plan.OK("cluster_absent", "集群 "+res.cluster+" 尚未初始化")
}

// clusterExists verifies that the cluster is defined and known to Patroni.
func (p *Planner) clusterExists(ctx context.Context, res *resolved) plan.Precondition {
	if res.info == nil {
		return plan.Err("cluster_exists", "inventory 中没有集群 "+res.cluster)
	}
	if p.Facts == nil {
		return plan.Warn("cluster_exists", "无法确认集群运行状态（状态采集不可用）")
	}
	members, ok := p.Facts.PatroniMembers(ctx, res.cluster)
	if !ok {
		return plan.Warn("cluster_exists", "无法连接 Patroni，仅按 inventory 中的定义继续")
	}
	if len(members) == 0 {
		return plan.Warn("cluster_exists", "Patroni 中查不到 "+res.cluster+" 的成员，集群可能尚未初始化")
	}
	return plan.OK("cluster_exists", fmt.Sprintf("集群 %s 有 %d 个 Patroni 成员", res.cluster, len(members)))
}

// sourceExists verifies that the source cluster of a clone exists.
func (p *Planner) sourceExists(_ context.Context, res *resolved) plan.Precondition {
	if res.source == "" {
		return plan.Err("source_exists", "缺少源集群")
	}
	if _, ok := res.model.Cluster(inventory.ModulePGSQL, res.source); !ok {
		return plan.Err("source_exists", "inventory 中没有源集群 "+res.source)
	}
	if res.source == res.cluster {
		return plan.Err("source_exists", "源集群与目标集群不能相同")
	}
	return plan.OK("source_exists", "源集群 "+res.source+" 存在")
}

// ipsInCluster verifies that every submitted IP belongs to the cluster.
func (p *Planner) ipsInCluster(res *resolved) plan.Precondition {
	if res.info == nil || len(res.ips) == 0 {
		return plan.Err("ips_in_cluster", "缺少集群或目标 IP")
	}
	hosts := map[string]bool{}
	for _, h := range res.info.Hosts {
		hosts[h] = true
	}
	for _, ip := range res.ips {
		if !hosts[ip] {
			return plan.Err("ips_in_cluster", fmt.Sprintf("%s 不在集群 %s 的 inventory 中", ip, res.cluster))
		}
	}
	return plan.OK("ips_in_cluster", fmt.Sprintf("%s 属于集群 %s", strings.Join(res.ips, ","), res.cluster))
}

// notPrimary refuses to remove an instance that is the primary, both by
// inventory role and by Patroni leader.
func (p *Planner) notPrimary(ctx context.Context, res *resolved) plan.Precondition {
	if res.info == nil || len(res.ips) == 0 {
		return plan.Err("not_primary", "缺少集群或目标 IP")
	}
	leader := ""
	if p.Facts != nil {
		if members, ok := p.Facts.PatroniMembers(ctx, res.cluster); ok {
			for _, m := range members {
				if m.Role == "leader" || m.Role == "standby_leader" {
					leader = m.Host
				}
			}
		}
	}
	for _, ip := range res.ips {
		if p.pgRole(res, ip) == "primary" {
			return plan.Err("not_primary", fmt.Sprintf("%s 在 inventory 中的 pg_role 是 primary，不能移除主库", ip))
		}
		if leader != "" && leader == ip {
			return plan.Err("not_primary", fmt.Sprintf("%s 是 Patroni leader，不能移除主库", ip))
		}
	}
	return plan.OK("not_primary", "目标实例均不是主库")
}

// memberExists verifies that the referenced instances exist in the cluster.
func (p *Planner) memberExists(_ context.Context, res *resolved) plan.Precondition {
	if res.info == nil {
		return plan.Err("member_exists", "缺少集群")
	}
	hosts := map[string]bool{}
	for _, h := range res.info.Hosts {
		hosts[h] = true
	}
	for _, ip := range []string{res.member, res.leader, res.candidate} {
		if ip == "" {
			continue
		}
		if !hosts[ip] {
			return plan.Err("member_exists", fmt.Sprintf("%s 不在集群 %s 的 inventory 中", ip, res.cluster))
		}
	}
	if res.leader != "" && res.candidate != "" && res.leader == res.candidate {
		return plan.Err("member_exists", "当前主库与目标主库不能是同一个实例")
	}
	return plan.OK("member_exists", "目标实例存在")
}

// hostInInventory warns when a node that is about to be provisioned is not in
// the inventory yet.
func (p *Planner) hostInInventory(res *resolved) plan.Precondition {
	if len(res.ips) == 0 {
		return plan.Err("host_in_inventory", "缺少目标 IP")
	}
	known := map[string]bool{}
	for _, h := range res.model.Hosts() {
		known[h] = true
	}
	for _, ip := range res.ips {
		if !known[ip] {
			return plan.Warn("host_in_inventory", fmt.Sprintf("%s 还不在 inventory 中，请先保存配置", ip))
		}
	}
	return plan.OK("host_in_inventory", "目标节点已在 inventory 中")
}

// nodesManaged warns when the target nodes have no node_exporter metrics yet.
func (p *Planner) nodesManaged(ctx context.Context, res *resolved) plan.Precondition {
	if p.Facts == nil || len(res.ips)+len(clusterHostsOrNil(res.info)) == 0 {
		return plan.Warn("nodes_managed", "无法确认节点是否已纳管（状态采集不可用）")
	}
	managed := p.Facts.ManagedNodes(ctx)
	if len(managed) == 0 {
		return plan.Warn("nodes_managed", "没有取到任何节点指标，可能在纳管之前")
	}
	hosts := res.ips
	if len(hosts) == 0 {
		hosts = clusterHostsOrNil(res.info)
	}
	for _, ip := range hosts {
		if !managed[ip] {
			return plan.Warn("nodes_managed", ip+" 尚未纳管（没有 node_exporter 指标）")
		}
	}
	return plan.OK("nodes_managed", "目标节点已纳管")
}

// backupRepo verifies that the cluster declares a pgBackRest repository.
func (p *Planner) backupRepo(res *resolved) plan.Precondition {
	if res.info == nil {
		return plan.Err("backup_repo", "缺少集群")
	}
	vars := res.info.Vars
	method := "local"
	if v, ok := vars["pgbackrest_method"].(string); ok && v != "" {
		method = v
	}
	repos, _ := vars["pgbackrest_repo"].(map[string]any)
	if _, ok := repos[method]; !ok {
		return plan.Warn("backup_repo", fmt.Sprintf("inventory 中没有定义 pgbackrest_repo.%s，将使用 Pigsty 默认值", method))
	}
	return plan.OK("backup_repo", "备份仓库 "+method+" 已定义")
}

// pitrWindow verifies that the recovery target lies inside the recoverable
// window of the cluster.
func (p *Planner) pitrWindow(ctx context.Context, res *resolved) plan.Precondition {
	if p.Facts == nil {
		return plan.Warn("pitr_window", "无法确认可恢复窗口（备份信息不可用）")
	}
	target, specific, err := parsePlanTarget(res)
	if err != nil {
		return plan.Err("pitr_window", err.Error())
	}
	cluster := res.cluster
	if res.action.def.ID == "pitr-clone" && res.source != "" {
		cluster = res.source
	}
	window, ok := p.Facts.BackupWindow(ctx, cluster)
	if !ok {
		return plan.Err("pitr_window", "取不到集群 "+cluster+" 的备份信息，无法确认可恢复窗口")
	}
	if !specific {
		return plan.OK("pitr_window", "目标不指定具体时间点，可恢复窗口："+
			window.Start.Local().Format(time.RFC3339)+" ~ "+window.End.Local().Format(time.RFC3339))
	}
	if target.Before(window.Start) || target.After(window.End) {
		return plan.Err("pitr_window", fmt.Sprintf("目标时间 %s 不在可恢复窗口 %s ~ %s 内",
			target.Local().Format(time.RFC3339), window.Start.Local().Format(time.RFC3339), window.End.Local().Format(time.RFC3339)))
	}
	return plan.OK("pitr_window", "目标时间在可恢复窗口内")
}

// notDegraded warns when the cluster is not fully healthy before a risky
// operation such as an in-place restore.
func (p *Planner) notDegraded(ctx context.Context, res *resolved) plan.Precondition {
	if p.Facts == nil {
		return plan.Warn("not_degraded", "无法确认集群健康状态")
	}
	members, ok := p.Facts.PatroniMembers(ctx, res.cluster)
	if !ok {
		return plan.Warn("not_degraded", "无法连接 Patroni 确认集群健康状态")
	}
	if len(members) == 0 {
		return plan.Warn("not_degraded", "Patroni 中没有成员记录")
	}
	leader := false
	for _, m := range members {
		if m.Role == "leader" || m.Role == "standby_leader" {
			leader = true
		}
	}
	if !leader {
		return plan.Warn("not_degraded", "集群当前没有 leader")
	}
	return plan.OK("not_degraded", "集群有 leader，可以继续")
}

// parsePlanTarget converts the submitted PITR target into a timestamp. The
// second result reports whether the target names one concrete point in time;
// latest, immediate, name, lsn and xid targets do not.
func parsePlanTarget(res *resolved) (time.Time, bool, error) {
	target, err := backup.ParseTarget(toRawMap(res.target))
	if err != nil {
		return time.Time{}, false, err
	}
	if target.Type != "time" {
		return time.Time{}, false, nil
	}
	ts, err := plan.ParsePitrTime(target.Time)
	if err != nil {
		return time.Time{}, false, err
	}
	return ts, true, nil
}

// clusterHostsOrNil returns the hosts of a cluster, nil when there is none.
func clusterHostsOrNil(c *inventory.Cluster) []string {
	if c == nil {
		return nil
	}
	return c.Hosts
}
