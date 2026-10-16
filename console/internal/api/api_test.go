package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/auth"
	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/configstore"
	"github.com/dbstd2333/pigsty-ui/console/internal/jobs"
	"github.com/dbstd2333/pigsty-ui/console/internal/services"
	"github.com/dbstd2333/pigsty-ui/console/internal/settings"
	"github.com/dbstd2333/pigsty-ui/console/internal/status"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

const testConfig = `---
all:
  children:
    infra:
      hosts:
        10.10.10.10: { infra_seq: 1 }
      vars:
        vmetrics_port: 8428
    pg-meta:
      hosts:
        10.10.10.10: { pg_seq: 1, pg_role: primary }
        10.10.10.11: { pg_seq: 2, pg_role: replica }
      vars:
        pg_cluster: pg-meta
        pg_users:
          - { name: dbuser_meta, password: DBUser.Meta }
        pg_databases:
          - { name: meta }
        pgbackrest_method: local
        pgbackrest_repo:
          local: { path: /pg/backup, retention_full_type: count, retention_full: 2 }
    pg-clone:
      hosts:
        10.10.10.12: { pg_seq: 1, pg_role: primary }
      vars:
        pg_cluster: pg-clone
        pgbackrest_method: local
        pgbackrest_repo:
          local: { path: /pg/backup }
  vars:
    version: v4.5.0
    admin_ip: 10.10.10.10
    pg_admin_password: DBUser.DBA
`

// testServer starts the console API on a temporary Pigsty home.
func testServer(t *testing.T) (*httptest.Server, *http.Client, string) {
	t.Helper()
	return testServerWith(t, 400)
}

// testServerWith starts the console API with a custom simulate speed.
func testServerWith(t *testing.T, speed float64) (*httptest.Server, *http.Client, string) {
	t.Helper()
	home := t.TempDir()
	file := filepath.Join(home, "pigsty.yml")
	if err := os.WriteFile(file, []byte(testConfig), 0o644); err != nil {
		t.Fatalf("write pigsty.yml: %v", err)
	}
	dataDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	repo, err := configstore.InitRepo(ctx, home, filepath.Join(dataDir, "config.git"))
	if err != nil {
		t.Fatalf("init repo: %v", err)
	}
	if _, err := repo.Commit(ctx, "init pigsty.yml", "tester"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	cfg, err := configstore.Open(configstore.Options{Home: home, DataDir: dataDir, Logger: logger})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	db, err := store.Open(filepath.Join(dataDir, "console.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	authService := auth.NewService(db)
	if err := authService.CreateUser("admin", "admin123"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	server := New(testOptions(t, cfg, db, logger, speed))
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return ts, &http.Client{Jar: jar}, file
}

// testOptions wires the full console service stack in simulate mode.
func testOptions(t *testing.T, cfg *configstore.Store, db *store.Store, logger *slog.Logger, speed float64) Options {
	t.Helper()
	source := &services.StatusSource{Store: cfg}
	statusService := status.New(status.Options{Source: source, Logger: logger, Simulate: true})
	settingsService := settings.New(db, func(context.Context) settings.Derived {
		derived := statusService.Derived(context.Background())
		return settings.Derived{VictoriaURL: derived.VictoriaURL, AlertmanagerURL: derived.AlertmanagerURL, GrafanaURL: derived.GrafanaURL}
	}, logger)
	source.SettingsSvc = settingsService
	backupService := backup.New(backup.Options{
		Fetcher: &services.BackupFetcher{Home: cfg.Home(), Store: cfg, Status: statusService},
		Store:   &services.BackupStore{DB: db},
		Logger:  logger, Simulate: true,
	})
	facts := &services.Facts{Status: statusService, Backup: backupService}
	planner, err := actions.NewPlanner(actions.Options{
		Facts: facts, Settings: settingsService, Simulate: true,
		Snapshot: func(ctx context.Context) (actions.Snapshot, error) {
			text, hash, commit, err := cfg.Current(ctx)
			if err != nil {
				return actions.Snapshot{}, err
			}
			snapshot := actions.Snapshot{Text: text, Hash: hash}
			if commit != nil {
				snapshot.Commit = commit.Hash
			}
			return snapshot, nil
		},
	})
	if err != nil {
		t.Fatalf("planner: %v", err)
	}
	manager := jobs.New(jobs.Options{
		Home: cfg.Home(), DataDir: filepath.Join(cfg.Home(), ".jobs"), Logger: logger, Store: db,
		Planner: planner, Simulate: true, SimulateSpeed: speed, Forks: 5,
	})
	t.Cleanup(manager.Shutdown)
	return Options{
		Store: cfg, Auth: auth.NewService(db), Version: "test", Logger: logger,
		DB: db, Planner: planner, Jobs: manager, Status: statusService,
		Backup: backupService, Settings: settingsService, Simulate: true,
	}
}

// call performs an API request, optionally with a body, and decodes the JSON.
func call(t *testing.T, client *http.Client, method, url, body string, wantStatus int) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s = %d (%s), want %d", method, url, resp.StatusCode, data, wantStatus)
	}
	if len(data) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s %s: %v (%s)", method, url, err, data)
	}
	return out
}

func TestAPIRequiresSession(t *testing.T) {
	ts, client, _ := testServer(t)
	call(t, client, http.MethodGet, ts.URL+"/api/v1/health", "", http.StatusOK)
	call(t, client, http.MethodGet, ts.URL+"/api/v1/meta", "", http.StatusUnauthorized)
	call(t, client, http.MethodGet, ts.URL+"/api/v1/config", "", http.StatusUnauthorized)
}

func TestAPILoginAndConfigFlow(t *testing.T) {
	ts, client, file := testServer(t)
	base := ts.URL + "/api/v1"

	login := call(t, client, http.MethodPost, base+"/auth/login",
		`{"username":"admin","password":"admin123"}`, http.StatusOK)
	if login["username"] != "admin" {
		t.Fatalf("login body = %v", login)
	}
	if me := call(t, client, http.MethodGet, base+"/auth/me", "", http.StatusOK); me["username"] != "admin" {
		t.Fatalf("me = %v", me)
	}
	if meta := call(t, client, http.MethodGet, base+"/meta", "", http.StatusOK); meta["inventoryEngine"] != "builtin" ||
		meta["pigstyVersion"] != "v4.5.0" {
		t.Fatalf("meta = %v", meta)
	}

	config := call(t, client, http.MethodGet, base+"/config", "", http.StatusOK)
	text, _ := config["text"].(string)
	hash, _ := config["hash"].(string)
	if text != testConfig || hash == "" {
		t.Fatalf("config = %v", config)
	}
	if commit, ok := config["commit"].(map[string]any); !ok || commit["author"] != "tester" {
		t.Fatalf("commit = %v", config["commit"])
	}

	// Conflict: the base hash does not match the file on disk.
	updated := strings.Replace(testConfig, "admin_ip: 10.10.10.10", "admin_ip: 10.10.10.11", 1)
	body, err := json.Marshal(map[string]string{"text": updated, "baseHash": "deadbeef", "message": "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	conflict := call(t, client, http.MethodPut, base+"/config", string(body), http.StatusConflict)
	if conflict["currentHash"] != hash || conflict["currentText"] != testConfig {
		t.Fatalf("conflict payload = %v", conflict)
	}

	// Validation failure: the upstream/structure checks reject the document.
	broken, err := json.Marshal(map[string]string{
		"text":     strings.Replace(testConfig, "children:", "not_children:", 1),
		"baseHash": hash, "message": "broken",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	invalid := call(t, client, http.MethodPut, base+"/config", string(broken), http.StatusUnprocessableEntity)
	if issues, ok := invalid["issues"].([]any); !ok || len(issues) == 0 {
		t.Fatalf("422 payload = %v", invalid)
	}

	// Successful save.
	valid, err := json.Marshal(map[string]string{"text": updated, "baseHash": hash, "message": "change admin ip"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	saved := call(t, client, http.MethodPut, base+"/config", string(valid), http.StatusOK)
	commit, ok := saved["commit"].(map[string]any)
	if !ok || commit["author"] != "admin" || commit["message"] != "change admin ip" {
		t.Fatalf("save payload = %v", saved)
	}
	onDisk, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != updated {
		t.Fatalf("file was not updated")
	}

	// Unchanged text must not create another commit.
	unchanged, err := json.Marshal(map[string]string{
		"text": updated, "baseHash": saved["hash"].(string), "message": "noop",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	again := call(t, client, http.MethodPut, base+"/config", string(unchanged), http.StatusOK)
	if again["hash"] != saved["hash"] {
		t.Fatalf("unchanged save returned another version: %v", again)
	}
	history := call(t, client, http.MethodGet, base+"/config/history?limit=10", "", http.StatusOK)
	if total := history["total"].(float64); total != 2 {
		t.Fatalf("history total = %v, want 2", total)
	}

	// Diff between the initial and the current version.
	items := history["items"].([]any)
	oldest := items[len(items)-1].(map[string]any)["hash"].(string)
	diff := call(t, client, http.MethodGet, base+"/config/diff?from="+oldest+"&to=HEAD", "", http.StatusOK)
	if _, ok := diff["unified"].(string); !ok || diff["toText"] != updated {
		t.Fatalf("diff = %v", diff)
	}

	// Inventory endpoints.
	hosts := call(t, client, http.MethodGet, base+"/inventory/hosts", "", http.StatusOK)
	if list := hosts["hosts"].([]any); len(list) != 3 {
		t.Fatalf("hosts = %v", hosts)
	}
	foundClusterGroup := false
	for _, raw := range hosts["groups"].([]any) {
		group := raw.(map[string]any)
		if group["name"] == "pg-meta" && group["module"] == "PGSQL" {
			foundClusterGroup = true
		}
	}
	if !foundClusterGroup {
		t.Fatalf("groups = %v", hosts)
	}
	effective := call(t, client, http.MethodGet, base+"/inventory/effective?host=10.10.10.10", "", http.StatusOK)
	if effective["engine"] != "builtin" {
		t.Fatalf("engine = %v", effective["engine"])
	}
	vars := effective["vars"].([]any)
	var foundCluster, foundPassword, foundDefault bool
	for _, raw := range vars {
		v := raw.(map[string]any)
		switch v["key"] {
		case "pg_cluster":
			foundCluster = v["value"] == "pg-meta" && v["source"] == "group" && v["sourceGroup"] == "pg-meta"
		case "pg_admin_password":
			foundPassword = v["sensitive"] == true && v["source"] == "global"
		case "pg_version":
			foundDefault = v["source"] == "default"
		}
	}
	if !foundCluster || !foundPassword || !foundDefault {
		t.Fatalf("effective values incomplete: cluster=%v password=%v default=%v", foundCluster, foundPassword, foundDefault)
	}
	call(t, client, http.MethodGet, base+"/inventory/effective?host=10.9.9.9", "", http.StatusNotFound)

	// Parameter schema.
	req, err := http.NewRequest(http.MethodGet, base+"/schema", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc struct {
		Version string `json:"version"`
		Params  map[string]struct {
			Identity bool `json:"identity"`
		} `json:"params"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	if doc.Version != "v4.5.0" || !doc.Params["pg_cluster"].Identity {
		t.Fatalf("schema document is not the generated one: %v", doc.Version)
	}

	// Logout deletes the session.
	call(t, client, http.MethodPost, base+"/auth/logout", "", http.StatusNoContent)
	call(t, client, http.MethodGet, base+"/auth/me", "", http.StatusUnauthorized)
}

func TestAPIOriginCheck(t *testing.T) {
	ts, client, _ := testServer(t)
	base := ts.URL + "/api/v1"
	call(t, client, http.MethodPost, base+"/auth/login", `{"username":"admin","password":"admin123"}`, http.StatusOK)

	body := `{"text":"x","baseHash":"y","message":"m"}`
	req, err := http.NewRequest(http.MethodPut, base+"/config", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}

	// Same-origin writes and non-browser clients keep working.
	req2, err := http.NewRequest(http.MethodPut, base+"/config", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Origin", ts.URL)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp2.StatusCode)
	}
}

func TestAPIOpenAPISpec(t *testing.T) {
	ts, client, _ := testServer(t)
	resp, err := client.Get(ts.URL + "/api/v1/openapi.json")
	if err != nil {
		t.Fatalf("get spec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var spec struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	if !strings.HasPrefix(spec.OpenAPI, "3.1") {
		t.Errorf("openapi = %q, want 3.1", spec.OpenAPI)
	}
	for _, path := range []string{"/health", "/meta", "/auth/login", "/config", "/config/history/{hash}",
		"/config/diff", "/config/revert", "/inventory/hosts", "/inventory/effective", "/schema"} {
		if spec.Paths[path] == nil {
			t.Errorf("path %s missing from the spec", path)
		}
	}
}

func TestUIDevNotice(t *testing.T) {
	ts, client, _ := testServer(t)
	resp, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get root: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "localhost:5173") {
		t.Fatalf("dev notice missing (status %d): %s", resp.StatusCode, body)
	}
	// Unknown API routes answer with problem+json rather than an HTML page.
	resp2, err := client.Get(ts.URL + "/api/v1/nope")
	if err != nil {
		t.Fatalf("get unknown api: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound || resp2.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("unknown api route = %d %s", resp2.StatusCode, resp2.Header.Get("Content-Type"))
	}
}
