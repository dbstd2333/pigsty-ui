package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
	"github.com/dbstd2333/pigsty-ui/console/internal/status"
)

type nodesStatusInput struct{}

type nodesStatusOutput struct {
	Body struct {
		Source plan.SourceInfo     `json:"source"`
		Items  []status.NodeStatus `json:"items"`
	}
}

type clustersStatusInput struct{}

type clustersStatusOutput struct {
	Body struct {
		Items []status.ClusterStatus `json:"items"`
	}
}

type clusterStatusInput struct {
	Cluster string `path:"cluster"`
}

type clusterStatusOutput struct {
	Body status.ClusterStatus
}

type clusterConfigInput struct {
	Cluster string `path:"cluster"`
}

type clusterConfigOutput struct {
	Body struct {
		Source     plan.SourceInfo   `json:"source"`
		Parameters map[string]string `json:"parameters"`
	}
}

type driftInput struct{}

type driftOutput struct {
	Body struct {
		Items []status.Drift `json:"items"`
	}
}

type alertsInput struct{}

type alertsOutput struct {
	Body struct {
		Source plan.SourceInfo `json:"source"`
		Items  []status.Alert  `json:"items"`
	}
}

type nodeMemoryInput struct {
	IP      string `path:"ip"`
	Refresh int    `query:"refresh" default:"0" enum:"0,1" doc:"1 = bypass the ten minute cache"`
}

type nodeMemoryOutput struct {
	Body struct {
		Source   plan.SourceInfo        `json:"source"`
		Services []status.MemoryService `json:"services"`
	}
}

// registerStatus registers the state collection endpoints.
func (s *Server) registerStatus() {
	huma.Register(s.api, huma.Operation{
		OperationID: "listNodeStatus",
		Method:      http.MethodGet,
		Path:        "/status/nodes",
		Summary:     "节点状态",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *nodesStatusInput) (*nodesStatusOutput, error) {
		src, items := s.status.Nodes(ctx)
		out := &nodesStatusOutput{}
		out.Body.Source = src
		out.Body.Items = items
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listClusterStatus",
		Method:      http.MethodGet,
		Path:        "/status/clusters",
		Summary:     "PG 集群状态",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *clustersStatusInput) (*clustersStatusOutput, error) {
		out := &clustersStatusOutput{}
		out.Body.Items = s.status.Clusters(ctx)
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getClusterStatus",
		Method:      http.MethodGet,
		Path:        "/status/clusters/{cluster}",
		Summary:     "单个 PG 集群状态",
		Tags:        []string{"status"},
		Errors:      []int{401, 404, 500},
	}, func(ctx context.Context, in *clusterStatusInput) (*clusterStatusOutput, error) {
		st, err := s.status.Cluster(ctx, in.Cluster)
		if err != nil {
			return nil, problem(http.StatusNotFound, err.Error())
		}
		return &clusterStatusOutput{Body: st}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getClusterConfig",
		Method:      http.MethodGet,
		Path:        "/status/clusters/{cluster}/pgconfig",
		Summary:     "DCS 中生效的 PG 参数",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, in *clusterConfigInput) (*clusterConfigOutput, error) {
		src, params := s.status.PGConfig(ctx, in.Cluster)
		out := &clusterConfigOutput{}
		out.Body.Source = src
		out.Body.Parameters = params
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listDrift",
		Method:      http.MethodGet,
		Path:        "/status/drift",
		Summary:     "inventory 与实际状态的漂移",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *driftInput) (*driftOutput, error) {
		out := &driftOutput{}
		out.Body.Items = s.status.Drift(ctx)
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listAlerts",
		Method:      http.MethodGet,
		Path:        "/status/alerts",
		Summary:     "当前告警",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *alertsInput) (*alertsOutput, error) {
		src, items := s.status.Alerts(ctx)
		out := &alertsOutput{}
		out.Body.Source = src
		out.Body.Items = items
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getNodeMemory",
		Method:      http.MethodGet,
		Path:        "/status/nodes/{ip}/memory",
		Summary:     "节点各服务的实际内存",
		Tags:        []string{"status"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, in *nodeMemoryInput) (*nodeMemoryOutput, error) {
		ip := strings.TrimSpace(in.IP)
		src, services := s.status.Memory(ctx, ip, in.Refresh == 1)
		out := &nodeMemoryOutput{}
		out.Body.Source = src
		out.Body.Services = services
		return out, nil
	})
}
