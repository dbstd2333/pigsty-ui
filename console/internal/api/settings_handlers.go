package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/settings"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

type settingsGetInput struct{}

type settingsGetOutput struct {
	Body settings.Settings
}

type settingsPutInput struct {
	Body settings.Patch
}

type settingsPutOutput struct {
	Body settings.Settings
}

// NodeSpec is the API contract type of the node capacity table.
type NodeSpec struct {
	IP        string    `json:"ip"`
	CPU       int       `json:"cpu"`
	MemMB     int       `json:"memMb"`
	DiskGB    int       `json:"diskGb"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type nodeSpecsInput struct{}

type nodeSpecsOutput struct {
	Body struct {
		Items []NodeSpec `json:"items"`
	}
}

type nodeSpecPutInput struct {
	IP   string `path:"ip"`
	Body struct {
		CPU    int `json:"cpu" minimum:"1" maximum:"4096"`
		MemMB  int `json:"memMb" minimum:"128" maximum:"16777216"`
		DiskGB int `json:"diskGb" minimum:"1" maximum:"1048576"`
	}
}

type nodeSpecOutput struct {
	Body NodeSpec
}

type nodeSpecDeleteInput struct {
	IP string `path:"ip"`
}

type nodeSpecDeleteOutput struct{}

// Applied is the API contract type of GET /applied.
type Applied struct {
	Cluster  string    `json:"cluster"`
	Commit   string    `json:"commit"`
	JobID    *string   `json:"jobId"`
	At       time.Time `json:"at"`
	Baseline bool      `json:"baseline"`
}

type appliedInput struct{}

type appliedOutput struct {
	Body struct {
		Items []Applied `json:"items"`
	}
}

// registerSettings registers the settings, node spec and applied endpoints.
func (s *Server) registerSettings() {
	huma.Register(s.api, huma.Operation{
		OperationID: "getSettings",
		Method:      http.MethodGet,
		Path:        "/settings",
		Summary:     "控制台设置",
		Tags:        []string{"settings"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *settingsGetInput) (*settingsGetOutput, error) {
		value, err := s.settings.Get(ctx)
		if err != nil {
			return nil, err
		}
		return &settingsGetOutput{Body: value}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "updateSettings",
		Method:      http.MethodPut,
		Path:        "/settings",
		Summary:     "更新控制台设置",
		Tags:        []string{"settings"},
		Errors:      []int{401, 403, 422, 500},
	}, func(ctx context.Context, in *settingsPutInput) (*settingsPutOutput, error) {
		value, err := s.settings.Update(ctx, in.Body)
		if err != nil {
			return nil, problem(http.StatusUnprocessableEntity, err.Error())
		}
		return &settingsPutOutput{Body: value}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listNodeSpecs",
		Method:      http.MethodGet,
		Path:        "/node-specs",
		Summary:     "节点规格（容量规划）",
		Tags:        []string{"settings"},
		Errors:      []int{401, 500},
	}, func(_ context.Context, _ *nodeSpecsInput) (*nodeSpecsOutput, error) {
		specs, err := s.db.NodeSpecs()
		if err != nil {
			return nil, err
		}
		out := &nodeSpecsOutput{}
		out.Body.Items = make([]NodeSpec, 0, len(specs))
		for _, spec := range specs {
			out.Body.Items = append(out.Body.Items, NodeSpec{
				IP: spec.IP, CPU: spec.CPU, MemMB: spec.MemMB, DiskGB: spec.DiskGB, UpdatedAt: spec.UpdatedAt,
			})
		}
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "putNodeSpec",
		Method:      http.MethodPut,
		Path:        "/node-specs/{ip}",
		Summary:     "写入节点规格",
		Tags:        []string{"settings"},
		Errors:      []int{401, 403, 422, 500},
	}, func(ctx context.Context, in *nodeSpecPutInput) (*nodeSpecOutput, error) {
		ip := strings.TrimSpace(in.IP)
		if err := validateHostIP(ctx, s, ip); err != nil {
			return nil, err
		}
		spec := store.NodeSpecRecord{
			IP: ip, CPU: in.Body.CPU, MemMB: in.Body.MemMB, DiskGB: in.Body.DiskGB, UpdatedAt: time.Now().UTC(),
		}
		if err := s.db.UpsertNodeSpec(spec); err != nil {
			return nil, err
		}
		return &nodeSpecOutput{Body: NodeSpec{
			IP: spec.IP, CPU: spec.CPU, MemMB: spec.MemMB, DiskGB: spec.DiskGB, UpdatedAt: spec.UpdatedAt,
		}}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID:   "deleteNodeSpec",
		Method:        http.MethodDelete,
		Path:          "/node-specs/{ip}",
		Summary:       "删除节点规格",
		Tags:          []string{"settings"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{401, 403, 404, 500},
	}, func(_ context.Context, in *nodeSpecDeleteInput) (*nodeSpecDeleteOutput, error) {
		if err := s.db.DeleteNodeSpec(strings.TrimSpace(in.IP)); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil, problem(http.StatusNotFound, "该节点没有规格记录")
			}
			return nil, err
		}
		return &nodeSpecDeleteOutput{}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listApplied",
		Method:      http.MethodGet,
		Path:        "/applied",
		Summary:     "各集群已应用的配置版本",
		Tags:        []string{"settings"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *appliedInput) (*appliedOutput, error) {
		items, err := s.applied(ctx)
		if err != nil {
			return nil, err
		}
		out := &appliedOutput{}
		out.Body.Items = items
		return out, nil
	})
}

// validateHostIP checks that a node spec address is a plain IPv4 address.
func validateHostIP(_ context.Context, s *Server, ip string) error {
	if ip == "" {
		return problem(http.StatusUnprocessableEntity, "缺少节点地址")
	}
	model, err := s.inventoryModel(context.Background())
	if err == nil {
		for _, host := range model.Hosts() {
			if host == ip {
				return nil
			}
		}
	}
	if !isIPv4(ip) {
		return problem(http.StatusUnprocessableEntity, "节点地址需为合法的 IPv4 地址")
	}
	return nil
}

// applied merges the successful jobs with the configuration baseline.
func (s *Server) applied(ctx context.Context) ([]Applied, error) {
	model, err := s.inventoryModel(ctx)
	if err != nil {
		return nil, err
	}
	clusters := []string{}
	for _, module := range []string{inventory.ModulePGSQL, inventory.ModuleRedis} {
		for _, c := range model.Clusters(module) {
			clusters = append(clusters, c.Identity)
		}
	}
	sort.Strings(clusters)

	rows, err := s.db.LatestApplied()
	if err != nil {
		return nil, err
	}
	firstHash, firstTime, err := s.store.FirstCommit(ctx)
	if err != nil {
		s.logger.Warn("read first commit", "err", err)
	}
	out := make([]Applied, 0, len(clusters))
	for _, cluster := range clusters {
		if row, ok := rows[cluster]; ok {
			item := Applied{Cluster: cluster, Commit: row.Commit, JobID: row.JobID, At: row.At}
			out = append(out, item)
			continue
		}
		item := Applied{Cluster: cluster, Commit: firstHash, Baseline: true}
		if firstTime != nil {
			item.At = *firstTime
		} else {
			item.At = time.Now().UTC()
		}
		out = append(out, item)
	}
	return out, nil
}
