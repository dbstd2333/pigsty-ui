package backup

import (
	"fmt"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// restoreThroughput is the empirical pgBackRest restore throughput used for the
// (clearly marked as estimated) duration of a PITR run.
const restoreThroughput = 150 * 1024 * 1024 // bytes per second

// LossWindow is the interval of data an in-place restore discards.
type LossWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Assessment is the repository side evaluation of one PITR request.
type Assessment struct {
	BackupSet        *string             `json:"backupSet"`
	InWindow         bool                `json:"inWindow"`
	LossWindow       *LossWindow         `json:"lossWindow"`
	EstimatedSeconds *int                `json:"estimatedSeconds"`
	Preconditions    []plan.Precondition `json:"preconditions"`
	Gate             *plan.Gate          `json:"gate"`
}

// AssessmentOptions collects everything the PITR planner needs.
type AssessmentOptions struct {
	Cluster string
	Mode    string // clone | inplace
	Target  PitrTarget
	Action  string
	Backup  bool
	// SourceCluster is the pg_cluster the data is restored from (clone mode).
	SourceCluster string
	Info          Info
	// TargetHosts are the hosts that will receive the restored data.
	TargetHosts []string
	// SourcePrimary is the host holding a local repository in clone mode.
	SourcePrimary string
	// DiskUsage reports free bytes of the data disk and the size of the data
	// directory of one host; ok=false when the metrics are unavailable.
	DiskUsage func(host string) (freeBytes, dataBytes int64, ok bool)
	// BackupMaxAgeHours is the freshness threshold shown in the gate.
	BackupMaxAgeHours int
}

// AssessPitr evaluates a PITR request against the current backup view.
func AssessPitr(opts AssessmentOptions) Assessment {
	out := Assessment{Preconditions: []plan.Precondition{}}
	targetTime, hasTarget := targetTimeOf(opts.Target, opts.Info)

	chosen := SelectBackupSet(opts.Info.Backups, targetTime)
	if chosen != nil {
		label := chosen.Label
		out.BackupSet = &label
		size := chosen.RepoSize
		if size <= 0 {
			size = chosen.Size
		}
		seconds := int(size/restoreThroughput) + 60
		out.EstimatedSeconds = &seconds
	}

	switch {
	case opts.Info.Window == nil:
		out.Preconditions = append(out.Preconditions,
			plan.Err("backup_available", "集群 "+opts.Cluster+" 没有可用备份，无法执行 PITR"))
		out.InWindow = false
	case chosen == nil:
		out.Preconditions = append(out.Preconditions,
			plan.Err("backup_available", "恢复目标早于最早的可用备份集，无法执行 PITR"))
		out.InWindow = false
	default:
		out.InWindow = !hasTarget || targetTime == nil ||
			(!targetTime.Before(opts.Info.Window.Start) && !targetTime.After(opts.Info.Window.End))
		out.Preconditions = append(out.Preconditions,
			plan.OK("backup_available", fmt.Sprintf("将使用备份集 %s（%s，结束于 %s）",
				chosen.Label, chosen.Type, chosen.Stop.Local().Format("2006-01-02 15:04:05"))))
		if out.InWindow {
			msg := fmt.Sprintf("目标在可恢复窗口内（%s ~ %s）",
				opts.Info.Window.Start.Local().Format("2006-01-02 15:04:05"),
				opts.Info.Window.End.Local().Format("2006-01-02 15:04:05"))
			out.Preconditions = append(out.Preconditions, plan.OK("in_window", msg))
		} else {
			out.Preconditions = append(out.Preconditions, plan.Err("in_window",
				fmt.Sprintf("目标不在可恢复窗口内（%s ~ %s）",
					opts.Info.Window.Start.Local().Format("2006-01-02 15:04:05"),
					opts.Info.Window.End.Local().Format("2006-01-02 15:04:05"))))
		}
	}

	// A local (posix) repository lives on the primary of the source cluster and
	// cannot be read by any other node.
	if opts.Mode == "clone" && opts.Info.Repo.Method == "local" {
		reachable := false
		for _, host := range opts.TargetHosts {
			if host == opts.SourcePrimary {
				reachable = true
			}
		}
		if !reachable {
			out.Preconditions = append(out.Preconditions, plan.Err("repo_reachable",
				"备份仓库是源主库上的 local 仓库，新集群节点读不到；请改用 minio/s3 仓库或恢复到源主库节点上的另一个端口"))
		} else {
			out.Preconditions = append(out.Preconditions, plan.OK("repo_reachable",
				"新集群节点与源主库同机，可以访问 local 仓库"))
		}
	}

	if opts.Mode == "inplace" && hasTarget && targetTime != nil {
		out.LossWindow = &LossWindow{From: *targetTime, To: time.Now().UTC()}
	}

	// Restoring in place with backup=true copies the current data aside first.
	if opts.Mode == "inplace" && opts.Backup {
		out.Preconditions = append(out.Preconditions, diskSpacePrecondition(opts))
	}

	maxAge := opts.BackupMaxAgeHours
	if maxAge <= 0 {
		maxAge = 26
	}
	out.Gate = &plan.Gate{
		ConfirmText:       opts.Cluster,
		Checkboxes:        []string{"我已确认业务已停写或已通知相关方", "我已确认恢复到该目标点之后的数据会丢失"},
		LastBackupAt:      opts.Info.Archive.LastArchivedAt,
		BackupMaxAgeHours: maxAge,
	}
	if opts.Mode == "clone" {
		out.Gate = nil
	}
	return out
}

// diskSpacePrecondition compares the free space of the data disk with the
// current data directory size; missing metrics degrade to a warning.
func diskSpacePrecondition(opts AssessmentOptions) plan.Precondition {
	if opts.DiskUsage == nil || len(opts.TargetHosts) == 0 {
		return plan.Warn("disk_space", "取不到节点磁盘指标，无法确认剩余空间是否放得下原数据副本")
	}
	for _, host := range opts.TargetHosts {
		free, data, ok := opts.DiskUsage(host)
		if !ok {
			return plan.Warn("disk_space", host+" 取不到磁盘指标，无法确认剩余空间是否放得下原数据副本")
		}
		if free < data {
			return plan.Err("disk_space", fmt.Sprintf("%s 数据盘剩余 %.1f GiB，小于数据目录 %.1f GiB",
				host, float64(free)/(1<<30), float64(data)/(1<<30)))
		}
	}
	return plan.OK("disk_space", "数据盘剩余空间足够存放原数据副本")
}

// targetTimeOf resolves the timestamp a target refers to. The second result is
// false when the target does not name one concrete point in time.
func targetTimeOf(target PitrTarget, info Info) (*time.Time, bool) {
	switch target.Type {
	case "time":
		if ts, err := plan.ParsePitrTime(target.Time); err == nil {
			return &ts, true
		}
	case "name":
		for _, rp := range info.RestorePoints {
			if rp.Name == target.Name {
				ts := rp.Time.UTC()
				return &ts, true
			}
		}
	}
	return nil, false
}
