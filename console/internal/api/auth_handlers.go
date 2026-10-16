package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/auth"
)

type loginInput struct {
	Body struct {
		Username string `json:"username" required:"true"`
		Password string `json:"password" required:"true"`
	}
}

type loginOutput struct {
	SetCookie string `header:"Set-Cookie"`
	Body      userNameBody
}

type logoutOutput struct {
	SetCookie string `header:"Set-Cookie"`
}

type meInput struct{}

type meOutput struct {
	Body userNameBody
}

type passwordInput struct {
	Body struct {
		OldPassword string `json:"oldPassword" required:"true"`
		NewPassword string `json:"newPassword" required:"true"`
	}
}

type passwordOutput struct{}

// registerAuth registers the authentication endpoints.
func (s *Server) registerAuth() {
	huma.Register(s.api, huma.Operation{
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/auth/login",
		Summary:     "登录",
		Tags:        []string{"auth"},
		Errors:      []int{401, 403},
	}, func(_ context.Context, in *loginInput) (*loginOutput, error) {
		token, err := s.auth.Login(in.Body.Username, in.Body.Password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				return nil, problem(http.StatusUnauthorized, "用户名或密码错误")
			}
			return nil, err
		}
		out := &loginOutput{}
		out.SetCookie = s.auth.Cookie(token, s.secureCookie).String()
		out.Body.Username = in.Body.Username
		s.logger.Info("login", "user", in.Body.Username)
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "logout",
		Method:      http.MethodPost,
		Path:        "/auth/logout",
		Summary:     "登出",
		Tags:        []string{"auth"},
		Errors:      []int{403},
	}, func(ctx context.Context, _ *meInput) (*logoutOutput, error) {
		if err := s.auth.Logout(currentSession(ctx).Token); err != nil {
			return nil, err
		}
		out := &logoutOutput{}
		out.SetCookie = s.auth.ExpiredCookie(s.secureCookie).String()
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "me",
		Method:      http.MethodGet,
		Path:        "/auth/me",
		Summary:     "当前用户",
		Tags:        []string{"auth"},
		Errors:      []int{401},
	}, func(ctx context.Context, _ *meInput) (*meOutput, error) {
		out := &meOutput{}
		out.Body.Username = currentSession(ctx).Username
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "changePassword",
		Method:      http.MethodPut,
		Path:        "/auth/password",
		Summary:     "修改自己的密码",
		Tags:        []string{"auth"},
		Errors:      []int{400, 401, 403},
	}, func(ctx context.Context, in *passwordInput) (*passwordOutput, error) {
		sess := currentSession(ctx)
		err := s.auth.ChangePassword(sess.Username, in.Body.OldPassword, in.Body.NewPassword, sess.Token)
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrOldPassword):
				return nil, problem(http.StatusBadRequest, "原密码不正确")
			case errors.Is(err, auth.ErrWeakPassword):
				return nil, problem(http.StatusBadRequest, err.Error())
			default:
				return nil, err
			}
		}
		s.logger.Info("password changed", "user", sess.Username)
		return &passwordOutput{}, nil
	})
}
