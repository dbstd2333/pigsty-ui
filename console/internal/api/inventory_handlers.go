package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
)

type inventoryHostsInput struct{}

type inventoryHostsOutput struct {
	Body hostsBody
}

type inventoryEffectiveInput struct {
	Host string `query:"host" required:"true" doc:"host IP address"`
}

type inventoryEffectiveOutput struct {
	Body effectiveBody
}

// registerInventory registers the effective value endpoints.
func (s *Server) registerInventory() {
	huma.Register(s.api, huma.Operation{
		OperationID: "listInventoryHosts",
		Method:      http.MethodGet,
		Path:        "/inventory/hosts",
		Summary:     "节点与分组列表",
		Tags:        []string{"inventory"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *inventoryHostsInput) (*inventoryHostsOutput, error) {
		_, hash, _, err := s.store.Current(ctx)
		if err != nil {
			return nil, err
		}
		model, err := s.store.Inventory().Model(s.store.File(), hash)
		if err != nil {
			return nil, err
		}
		hosts, groups := model.Lists()
		return &inventoryHostsOutput{Body: hostsBody{Hosts: hosts, Groups: groups}}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getEffectiveValues",
		Method:      http.MethodGet,
		Path:        "/inventory/effective",
		Summary:     "某节点的有效参数值",
		Tags:        []string{"inventory"},
		Errors:      []int{401, 404, 500},
	}, func(ctx context.Context, in *inventoryEffectiveInput) (*inventoryEffectiveOutput, error) {
		host := strings.TrimSpace(in.Host)
		_, hash, _, err := s.store.Current(ctx)
		if err != nil {
			return nil, err
		}
		set, err := s.schemaSet()
		if err != nil {
			return nil, err
		}
		engine, vars, err := s.store.Inventory().Effective(ctx, s.store.File(), hash, host, set.Defaults(), set.Sensitive)
		if err != nil {
			if errors.Is(err, inventory.ErrHostNotFound) {
				return nil, problem(http.StatusNotFound, "节点 "+host+" 不在当前 inventory 中")
			}
			return nil, err
		}
		if vars == nil {
			vars = []inventory.Var{}
		}
		return &inventoryEffectiveOutput{Body: effectiveBody{Host: host, Engine: engine, Vars: vars}}, nil
	})
}
