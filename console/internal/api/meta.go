package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

type healthInput struct{}

type healthOutput struct {
	Body healthBody
}

type metaInput struct{}

type metaOutput struct {
	Body metaBody
}

// registerMeta registers the health, meta and schema endpoints.
func (s *Server) registerMeta() {
	huma.Register(s.api, huma.Operation{
		OperationID: "getHealth",
		Method:      http.MethodGet,
		Path:        "/health",
		Summary:     "健康检查",
		Tags:        []string{"meta"},
	}, func(_ context.Context, _ *healthInput) (*healthOutput, error) {
		out := &healthOutput{}
		out.Body.Status = "ok"
		out.Body.Version = s.version
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getMeta",
		Method:      http.MethodGet,
		Path:        "/meta",
		Summary:     "控制台与部署元信息",
		Tags:        []string{"meta"},
		Errors:      []int{401},
	}, func(_ context.Context, _ *metaInput) (*metaOutput, error) {
		out := &metaOutput{}
		out.Body.Version = s.version
		out.Body.PigstyVersion = s.store.PigstyVersion()
		out.Body.PigstyHome = s.store.Home()
		out.Body.InventoryEngine = s.store.Inventory().Engine()
		out.Body.ValidateAvailable = s.store.Validator().Available()
		out.Body.Simulate = s.simulate
		return out, nil
	})

}
