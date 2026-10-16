package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/auth"
	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/configstore"
	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/jobs"
	"github.com/dbstd2333/pigsty-ui/console/internal/schema"
	"github.com/dbstd2333/pigsty-ui/console/internal/settings"
	"github.com/dbstd2333/pigsty-ui/console/internal/status"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// Options configures the API server.
type Options struct {
	Store        *configstore.Store
	Auth         *auth.Service
	Version      string
	Logger       *slog.Logger
	SecureCookie bool
	DevOrigins   []string

	// Services of the M2 - M5 milestones.
	DB       *store.Store
	Planner  *actions.Planner
	Jobs     *jobs.Manager
	Status   *status.Service
	Backup   *backup.Service
	Settings *settings.Service
	Simulate bool
}

// Server wires the console HTTP API and the embedded frontend.
type Server struct {
	store        *configstore.Store
	auth         *auth.Service
	version      string
	logger       *slog.Logger
	secureCookie bool
	devOrigins   []string

	db       *store.Store
	planner  *actions.Planner
	jobs     *jobs.Manager
	status   *status.Service
	backup   *backup.Service
	settings *settings.Service
	simulate bool

	api huma.API
	mux *http.ServeMux

	schemaMu      sync.Mutex
	schema        *schema.Set
	schemaVersion string
}

// session is the authenticated user and session token of one request.
type session struct {
	Username string
	Token    string
}

// sessionContextKey carries the authentication state in the request context.
type sessionContextKey struct{}

// New builds the API server, its routes and the frontend handler.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		store:        opts.Store,
		auth:         opts.Auth,
		version:      opts.Version,
		logger:       logger,
		secureCookie: opts.SecureCookie,
		devOrigins:   opts.DevOrigins,
		db:           opts.DB,
		planner:      opts.Planner,
		jobs:         opts.Jobs,
		status:       opts.Status,
		backup:       opts.Backup,
		settings:     opts.Settings,
		simulate:     opts.Simulate,
	}

	config := huma.DefaultConfig("Pigsty Console API", opts.Version)
	config.Info.Description = "Pigsty 控制台后端：配置读写、动作编排、作业执行、状态采集与 PITR 规划。"
	// The console serves its own UI and does not need the docs/schema helper
	// routes nor the $schema link transformer.
	config.DocsPath = ""
	config.SchemasPath = ""
	config.CreateHooks = nil

	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", config)
	s.api, s.mux = api, mux
	api.UseMiddleware(s.guard)

	s.registerMeta()
	s.registerAuth()
	s.registerConfig()
	s.registerInventory()
	s.registerSchema()
	s.registerActions()
	s.registerJobs()
	s.registerStatus()
	s.registerSettings()
	s.registerBackups()

	mux.Handle("/", s.uiHandler())
	return s
}

// Handler returns the root HTTP handler of the console.
func (s *Server) Handler() http.Handler { return s.mux }

// guard enforces same-origin writes and session authentication for every
// route except the public ones.
func (s *Server) guard(ctx huma.Context, next func(huma.Context)) {
	if isWriteMethod(ctx.Method()) && !s.originAllowed(ctx) {
		huma.WriteErr(s.api, ctx, http.StatusForbidden, "请求来源校验失败：Origin 与 Host 不一致")
		return
	}
	if publicPath(ctx.URL().Path) {
		next(ctx)
		return
	}
	token := s.sessionToken(ctx)
	username, ok := s.auth.Authenticate(token)
	if !ok {
		huma.WriteErr(s.api, ctx, http.StatusUnauthorized, "请先登录")
		return
	}
	next(huma.WithValue(ctx, sessionContextKey{}, session{Username: username, Token: token}))
}

// currentSession returns the authentication state of a request.
func currentSession(ctx context.Context) session {
	if v, ok := ctx.Value(sessionContextKey{}).(session); ok {
		return v
	}
	return session{}
}

// sessionToken reads the session cookie value of a request.
func (s *Server) sessionToken(ctx huma.Context) string {
	cookie, err := huma.ReadCookie(ctx, auth.CookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// publicPath reports whether a route is reachable without a session.
func publicPath(path string) bool {
	switch path {
	case "/api/v1/health", "/api/v1/auth/login":
		return true
	default:
		return false
	}
}

// isWriteMethod reports whether a method changes server state.
func isWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// originAllowed checks the Origin header of write requests. Requests without an
// Origin header (curl, scripts) are accepted because they cannot be forged by a
// browser.
func (s *Server) originAllowed(ctx huma.Context) bool {
	origin := strings.TrimSpace(ctx.Header("Origin"))
	if origin == "" {
		return true
	}
	if o, ok := originHost(origin); ok && strings.EqualFold(o, ctx.Host()) {
		return true
	}
	for _, dev := range s.devOrigins {
		if strings.EqualFold(strings.TrimSuffix(dev, "/"), origin) {
			return true
		}
	}
	return false
}

// originHost returns the host part of an Origin header value.
func originHost(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", false
	}
	return u.Host, true
}

// inventoryModel returns the parsed inventory of the current configuration.
func (s *Server) inventoryModel(ctx context.Context) (*inventory.Model, error) {
	_, hash, _, err := s.store.Current(ctx)
	if err != nil {
		return nil, err
	}
	return s.store.Inventory().Model(s.store.File(), hash)
}

// isIPv4 reports whether a value is a plain IPv4 address.
func isIPv4(value string) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	return ip != nil && ip.To4() != nil
}
