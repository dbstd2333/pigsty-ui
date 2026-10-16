package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/configstore"
)

type configGetInput struct{}

type configGetOutput struct {
	Body configBody
}

type configPutInput struct {
	Body struct {
		Text     string `json:"text" required:"true"`
		BaseHash string `json:"baseHash" required:"true" doc:"sha256 of the version the client edited"`
		Message  string `json:"message" doc:"commit message"`
	}
}

type configPutOutput struct {
	Body saveBody
}

type configValidateInput struct {
	Body struct {
		Text string `json:"text" required:"true"`
	}
}

type configValidateOutput struct {
	Body validateBody
}

type configHistoryInput struct {
	Limit  int `query:"limit" default:"50" minimum:"1" maximum:"500"`
	Offset int `query:"offset" default:"0" minimum:"0"`
}

type configHistoryOutput struct {
	Body historyBody
}

type configHistoryDetailInput struct {
	Hash string `path:"hash" doc:"commit hash"`
}

type configHistoryDetailOutput struct {
	Body historyDetailBody
}

type configDiffInput struct {
	From string `query:"from" required:"true"`
	To   string `query:"to" required:"true" doc:"commit hash or HEAD"`
}

type configDiffOutput struct {
	Body diffBody
}

type configRevertInput struct {
	Body struct {
		Hash     string `json:"hash" required:"true"`
		BaseHash string `json:"baseHash" required:"true"`
		Message  string `json:"message" required:"false" doc:"optional commit message"`
	}
}

type configRevertOutput struct {
	Body saveBody
}

// registerConfig registers the configuration endpoints.
func (s *Server) registerConfig() {
	huma.Register(s.api, huma.Operation{
		OperationID: "getConfig",
		Method:      http.MethodGet,
		Path:        "/config",
		Summary:     "读取配置文件",
		Tags:        []string{"config"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, _ *configGetInput) (*configGetOutput, error) {
		text, hash, commit, err := s.store.Current(ctx)
		if err != nil {
			return nil, err
		}
		out := &configGetOutput{}
		out.Body.Text = text
		out.Body.Hash = hash
		out.Body.Commit = commit
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID:  "putConfig",
		Method:       http.MethodPut,
		Path:         "/config",
		Summary:      "保存配置文件",
		Tags:         []string{"config"},
		Errors:       []int{401, 403, 409, 422, 500},
		MaxBodyBytes: 8 << 20,
	}, func(ctx context.Context, in *configPutInput) (*configPutOutput, error) {
		result, err := s.store.Save(ctx, configstore.SaveRequest{
			Text:     in.Body.Text,
			BaseHash: in.Body.BaseHash,
			Message:  in.Body.Message,
			Author:   currentSession(ctx).Username,
		})
		if err != nil {
			return nil, saveProblem(err)
		}
		return &configPutOutput{Body: saveBody{Hash: result.Hash, Commit: result.Commit}}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID:  "validateConfig",
		Method:       http.MethodPost,
		Path:         "/config/validate",
		Summary:      "校验配置内容（只读）",
		Tags:         []string{"config"},
		Errors:       []int{401, 403, 500},
		MaxBodyBytes: 8 << 20,
	}, func(ctx context.Context, in *configValidateInput) (*configValidateOutput, error) {
		issues, err := s.store.ValidateText(ctx, in.Body.Text)
		if err != nil {
			return nil, err
		}
		if issues == nil {
			issues = []Issue{}
		}
		out := &configValidateOutput{}
		out.Body.Issues = issues
		out.Body.OK = !configstore.HasErrors(issues)
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getConfigHistory",
		Method:      http.MethodGet,
		Path:        "/config/history",
		Summary:     "配置历史",
		Tags:        []string{"config"},
		Errors:      []int{401, 500},
	}, func(ctx context.Context, in *configHistoryInput) (*configHistoryOutput, error) {
		items, total, err := s.store.History(ctx, in.Limit, in.Offset)
		if err != nil {
			return nil, err
		}
		if items == nil {
			items = []CommitInfo{}
		}
		out := &configHistoryOutput{}
		out.Body.Items = items
		out.Body.Total = total
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getConfigCommit",
		Method:      http.MethodGet,
		Path:        "/config/history/{hash}",
		Summary:     "查看某个版本的配置",
		Tags:        []string{"config"},
		Errors:      []int{401, 404, 500},
	}, func(ctx context.Context, in *configHistoryDetailInput) (*configHistoryDetailOutput, error) {
		commit, text, err := s.store.Show(ctx, in.Hash)
		if err != nil {
			return nil, refProblem(err)
		}
		out := &configHistoryDetailOutput{}
		out.Body.Commit = commit
		out.Body.Text = text
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "diffConfig",
		Method:      http.MethodGet,
		Path:        "/config/diff",
		Summary:     "比较两个版本",
		Tags:        []string{"config"},
		Errors:      []int{401, 404, 500},
	}, func(ctx context.Context, in *configDiffInput) (*configDiffOutput, error) {
		result, err := s.store.Diff(ctx, in.From, in.To)
		if err != nil {
			return nil, refProblem(err)
		}
		out := &configDiffOutput{}
		out.Body = diffBody(result)
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID:  "revertConfig",
		Method:       http.MethodPost,
		Path:         "/config/revert",
		Summary:      "回滚到某一版本（产生新提交）",
		Tags:         []string{"config"},
		Errors:       []int{401, 403, 404, 409, 422, 500},
		MaxBodyBytes: 8 << 20,
	}, func(ctx context.Context, in *configRevertInput) (*configRevertOutput, error) {
		result, err := s.store.Revert(ctx, in.Body.Hash, in.Body.BaseHash, in.Body.Message,
			currentSession(ctx).Username)
		if err != nil {
			return nil, saveProblem(err)
		}
		return &configRevertOutput{Body: saveBody{Hash: result.Hash, Commit: result.Commit}}, nil
	})
}

// saveProblem maps configstore errors onto problem+json responses.
func saveProblem(err error) error {
	var conflict *configstore.ConflictError
	if errors.As(err, &conflict) {
		return conflictProblem(conflict)
	}
	var invalid *configstore.ValidationError
	if errors.As(err, &invalid) {
		return validationProblem(invalid)
	}
	if errors.Is(err, configstore.ErrCommitNotFound) {
		return problem(http.StatusNotFound, "版本不存在")
	}
	return err
}

// refProblem maps a missing commit onto a 404 problem and returns nil for
// other errors so callers can pass them through unchanged.
func refProblem(err error) error {
	if errors.Is(err, configstore.ErrCommitNotFound) {
		return problem(http.StatusNotFound, "版本不存在")
	}
	return err
}
