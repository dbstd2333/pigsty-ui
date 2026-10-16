package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

type backupInfoInput struct {
	Cluster string `path:"cluster"`
	Refresh int    `query:"refresh" default:"0" enum:"0,1" doc:"1 = bypass the ten minute cache"`
}

type backupInfoOutput struct {
	Body backup.Info
}

type pitrPlanInput struct {
	Cluster string `path:"cluster" doc:"source cluster holding the backup"`
	Body    struct {
		Mode       string         `json:"mode" required:"false" enum:"clone,inplace"`
		Target     map[string]any `json:"target" required:"false"`
		Action     string         `json:"action" required:"false" enum:"pause,promote,shutdown"`
		Backup     bool           `json:"backup" required:"false"`
		NewCluster string         `json:"newCluster" required:"false"`
	}
}

// PitrPlan is the API contract type of POST /backups/{cluster}/pitr/plan.
type PitrPlan struct {
	BackupSet        *string             `json:"backupSet"`
	InWindow         bool                `json:"inWindow"`
	LossWindow       *backup.LossWindow  `json:"lossWindow"`
	EstimatedSeconds *int                `json:"estimatedSeconds"`
	PgPitr           map[string]any      `json:"pgPitr"`
	Commands         []string            `json:"commands"`
	Preconditions    []plan.Precondition `json:"preconditions"`
	Gate             *plan.Gate          `json:"gate"`
}

type pitrPlanOutput struct {
	Body PitrPlan
}

// registerBackups registers the backup and PITR endpoints.
func (s *Server) registerBackups() {
	huma.Register(s.api, huma.Operation{
		OperationID: "getBackups",
		Method:      http.MethodGet,
		Path:        "/backups/{cluster}",
		Summary:     "集群备份信息",
		Tags:        []string{"backup"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, in *backupInfoInput) (*backupInfoOutput, error) {
		info, err := s.backup.Info(ctx, strings.TrimSpace(in.Cluster), in.Refresh == 1)
		if err != nil {
			return nil, err
		}
		return &backupInfoOutput{Body: info}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "planPitr",
		Method:      http.MethodPost,
		Path:        "/backups/{cluster}/pitr/plan",
		Summary:     "PITR 计划",
		Tags:        []string{"backup"},
		Errors:      []int{401, 403, 404, 422, 500},
	}, func(ctx context.Context, in *pitrPlanInput) (*pitrPlanOutput, error) {
		body := in.Body
		source := strings.TrimSpace(in.Cluster)
		if body.Mode == "" {
			body.Mode = "inplace"
		}
		if body.Action == "" {
			body.Action = "pause"
		}
		target, err := backup.ParseTarget(backup.RawTarget(body.Target))
		if err != nil {
			return nil, problem(http.StatusUnprocessableEntity, err.Error())
		}
		info, err := s.backup.Info(ctx, source, false)
		if err != nil {
			return nil, err
		}

		// The action planner renders the exact commands, so the wizard and the
		// job submission always agree on what will run.
		params := map[string]any{"target": body.Target, "action": body.Action}
		actionID := "pitr-inplace"
		switch body.Mode {
		case "clone":
			actionID = "pitr-clone"
			params["cluster"] = body.NewCluster
			params["source"] = source
		case "inplace":
			params["cluster"] = source
			params["backup"] = body.Backup
		default:
			return nil, problem(http.StatusUnprocessableEntity, "mode 只能是 clone 或 inplace")
		}
		computed, err := s.planner.Plan(ctx, actionID, params, false)
		if err != nil {
			return nil, actionError(err)
		}

		targetHosts := computed.Targets
		assessment := backup.AssessPitr(backup.AssessmentOptions{
			Cluster:           source,
			Mode:              body.Mode,
			Target:            target,
			Action:            body.Action,
			Backup:            body.Backup && body.Mode == "inplace",
			SourceCluster:     source,
			Info:              info,
			TargetHosts:       targetHosts,
			SourcePrimary:     s.primaryHost(ctx, source),
			DiskUsage:         s.diskUsage(ctx, info),
			BackupMaxAgeHours: s.settings.BackupMaxAgeHours(),
		})
		pgPitr := backup.PgPitr(target, body.Action, body.Backup && body.Mode == "inplace", cloneSource(body.Mode, source))
		gate := assessment.Gate
		if gate == nil && computed.Gate != nil {
			gate = computed.Gate
		}
		out := &pitrPlanOutput{}
		out.Body = PitrPlan{
			BackupSet:        assessment.BackupSet,
			InWindow:         assessment.InWindow,
			LossWindow:       assessment.LossWindow,
			EstimatedSeconds: assessment.EstimatedSeconds,
			PgPitr:           pgPitr,
			Commands:         computed.Commands,
			Preconditions:    mergePreconditions(assessment.Preconditions, computed.Preconditions),
			Gate:             gate,
		}
		return out, nil
	})
}

// cloneSource returns the extra pg_pitr cluster value of a clone plan.
func cloneSource(mode, source string) string {
	if mode == "clone" {
		return source
	}
	return ""
}

// diskUsage builds the disk space check of an in-place PITR. The data directory
// size is approximated by the size of the newest backup, which is what
// pgBackRest copied from the database.
func (s *Server) diskUsage(ctx context.Context, info backup.Info) func(host string) (int64, int64, bool) {
	latest := backup.LatestBackup(info.Backups)
	_, nodes := s.status.Nodes(ctx)
	free := map[string]int64{}
	for _, node := range nodes {
		if node.DiskFreeBytes != nil {
			free[node.IP] = *node.DiskFreeBytes
		}
	}
	return func(host string) (int64, int64, bool) {
		if latest == nil {
			return 0, 0, false
		}
		freeBytes, ok := free[host]
		if !ok {
			return 0, 0, false
		}
		return freeBytes, latest.Size, true
	}
}

// primaryHost returns the primary of a cluster, empty when unknown.
func (s *Server) primaryHost(ctx context.Context, cluster string) string {
	st, err := s.status.Cluster(ctx, cluster)
	if err != nil || st.Leader == nil {
		return ""
	}
	return *st.Leader
}

// mergePreconditions concatenates precondition lists, keeping the stricter
// entry when an id appears twice.
func mergePreconditions(lists ...[]plan.Precondition) []plan.Precondition {
	out := []plan.Precondition{}
	seen := map[string]int{}
	for _, list := range lists {
		for _, item := range list {
			if index, ok := seen[item.ID]; ok {
				if out[index].OK && !item.OK {
					out[index] = item
				}
				continue
			}
			seen[item.ID] = len(out)
			out = append(out, item)
		}
	}
	return out
}
