package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
)

type actionsListInput struct{}

type actionsListOutput struct {
	Body struct {
		Items []actions.ActionDef `json:"items"`
	}
}

type actionPlanInput struct {
	ID   string `path:"id" doc:"action id"`
	Body struct {
		Params map[string]any `json:"params" required:"false" doc:"action parameters"`
		Check  bool           `json:"check" required:"false" doc:"run the playbook in --check mode"`
	}
}

type actionPlanOutput struct {
	Body *actions.Plan
}

// registerActions registers the action catalog endpoints.
func (s *Server) registerActions() {
	huma.Register(s.api, huma.Operation{
		OperationID: "listActions",
		Method:      http.MethodGet,
		Path:        "/actions",
		Summary:     "动作目录",
		Tags:        []string{"actions"},
		Errors:      []int{401, 500},
	}, func(_ context.Context, _ *actionsListInput) (*actionsListOutput, error) {
		out := &actionsListOutput{}
		out.Body.Items = s.planner.Catalog().List()
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "planAction",
		Method:      http.MethodPost,
		Path:        "/actions/{id}/plan",
		Summary:     "计算动作计划",
		Tags:        []string{"actions"},
		Errors:      []int{401, 403, 404, 422, 500},
	}, func(ctx context.Context, in *actionPlanInput) (*actionPlanOutput, error) {
		computed, err := s.planner.Plan(ctx, in.ID, in.Body.Params, in.Body.Check)
		if err != nil {
			return nil, actionError(err)
		}
		return &actionPlanOutput{Body: computed}, nil
	})
}

// actionError maps planner errors onto problem+json responses.
func actionError(err error) error {
	var paramErr *actions.ParamError
	switch {
	case errors.Is(err, actions.ErrUnknownAction):
		return problem(http.StatusNotFound, "动作不存在："+err.Error())
	case errors.As(err, &paramErr):
		return &ProblemError{
			Title:  titleFor(http.StatusUnprocessableEntity),
			Status: http.StatusUnprocessableEntity,
			Detail: "参数校验未通过",
			Issues: paramErr.Issues,
		}
	default:
		return err
	}
}
