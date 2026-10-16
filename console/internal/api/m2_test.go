package api

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// login signs the shared test client in and returns the API base URL.
func login(t *testing.T, ts string, client *http.Client) string {
	t.Helper()
	base := ts + "/api/v1"
	call(t, client, http.MethodPost, base+"/auth/login", `{"username":"admin","password":"admin123"}`, http.StatusOK)
	return base
}

func TestActionsCatalogAndPlan(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	catalog := call(t, client, http.MethodGet, base+"/actions", "", http.StatusOK)
	items, ok := catalog["items"].([]any)
	if !ok || len(items) < 20 {
		t.Fatalf("catalog = %v", catalog)
	}
	var userDef map[string]any
	for _, raw := range items {
		item := raw.(map[string]any)
		if _, ok := item["params"].([]any); !ok {
			t.Fatalf("action %v has no params array", item["id"])
		}
		if item["gate"] == nil || item["check"] == nil || item["risk"] == nil {
			t.Fatalf("action %v is missing fields: %v", item["id"], item)
		}
		if item["id"] == "pgsql-user" {
			userDef = item
		}
	}
	if userDef == nil {
		t.Fatal("pgsql-user is missing from the catalog")
	}
	param := userDef["params"].([]any)[1].(map[string]any)
	if param["of"] != "cluster" || param["enum"] == nil {
		t.Fatalf("param shape = %v", param)
	}
	for _, key := range []string{"default", "help", "of", "enum", "type", "label", "required", "name"} {
		if _, ok := param[key]; !ok {
			t.Fatalf("param %v misses key %s", param, key)
		}
	}

	plan := call(t, client, http.MethodPost, base+"/actions/pgsql-user/plan",
		`{"params":{"cluster":"pg-meta","user":"dbuser_meta"},"check":true}`, http.StatusOK)
	commands := plan["commands"].([]any)
	if len(commands) != 1 || !strings.Contains(commands[0].(string), "-l pg-meta") {
		t.Fatalf("commands = %v", commands)
	}
	if !strings.Contains(commands[0].(string), "--check") {
		t.Fatalf("check flag missing: %v", commands)
	}
	if plan["gate"] != nil {
		t.Fatalf("low risk plan must not require a gate: %v", plan["gate"])
	}
	if targets := plan["targets"].([]any); len(targets) != 2 {
		t.Fatalf("targets = %v", targets)
	}
	if plan["configHash"] == nil || plan["risk"] != "low" {
		t.Fatalf("plan = %v", plan)
	}

	bad := call(t, client, http.MethodPost, base+"/actions/pgsql-user/plan",
		`{"params":{"cluster":"pg; rm -rf /","user":"dbuser_meta"},"check":false}`, http.StatusUnprocessableEntity)
	issues, ok := bad["issues"].([]any)
	if !ok || len(issues) == 0 {
		t.Fatalf("issues = %v", bad)
	}
	if issues[0].(map[string]any)["path"] != "cluster" {
		t.Fatalf("issue = %v", issues[0])
	}

	call(t, client, http.MethodPost, base+"/actions/nope/plan", `{"params":{},"check":false}`, http.StatusNotFound)

	pitr := call(t, client, http.MethodPost, base+"/actions/pitr-inplace/plan",
		`{"params":{"cluster":"pg-meta","target":{"type":"latest"},"action":"pause"},"check":false}`, http.StatusOK)
	gate, ok := pitr["gate"].(map[string]any)
	if !ok {
		t.Fatalf("pitr gate = %v", pitr["gate"])
	}
	if gate["confirmText"] != "pg-meta" {
		t.Fatalf("confirmText = %v", gate["confirmText"])
	}
	checkboxes := gate["checkboxes"].([]any)
	joined := ""
	for _, box := range checkboxes {
		joined += box.(string)
	}
	if !strings.Contains(joined, "业务已停写") {
		t.Fatalf("checkboxes = %v", checkboxes)
	}
	if gate["backupMaxAgeHours"] == nil {
		t.Fatalf("gate = %v", gate)
	}
}

func TestJobLifecycle(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	created := call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"pgsql-svc","params":{"cluster":"pg-meta"},"check":false}`, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" || !strings.HasPrefix(id, time.Now().Format("20060102")) {
		t.Fatalf("job id = %q", id)
	}
	if created["status"] == nil || created["stats"] == nil || created["targets"] == nil {
		t.Fatalf("job = %v", created)
	}

	// The event stream replays the history and ends with the final job.
	stream, err := client.Get(base + "/jobs/" + id + "/events?after=0")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	defer stream.Body.Close()
	if ct := stream.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content type = %s", ct)
	}
	scanner := bufio.NewScanner(stream.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var sawJob, sawEnd, sawStats bool
	deadline := time.Now().Add(60 * time.Second)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: job") {
			sawJob = true
		}
		if strings.HasPrefix(line, "event: end") {
			sawEnd = true
		}
		if strings.Contains(line, `"type":"stats"`) {
			sawStats = true
		}
		if sawEnd || time.Now().After(deadline) {
			break
		}
	}
	if !sawJob || !sawEnd {
		t.Fatalf("stream ended without job/end events: job=%v end=%v", sawJob, sawEnd)
	}

	// Replaying a finished job must also end immediately (this is what a
	// reloaded job detail page does).
	replay, err := client.Get(base + "/jobs/" + id + "/events?after=0")
	if err != nil {
		t.Fatalf("replay events: %v", err)
	}
	defer replay.Body.Close()
	replayBody, err := io.ReadAll(replay.Body)
	if err != nil {
		t.Fatalf("read replay: %v", err)
	}
	if !strings.Contains(string(replayBody), "event: end") {
		t.Fatalf("finished job replay did not end: %s", replayBody)
	}
	if !sawStats {
		t.Fatal("stream is missing the stats event")
	}

	job := call(t, client, http.MethodGet, base+"/jobs/"+id, "", http.StatusOK)
	if job["status"] != "success" {
		t.Fatalf("job status = %v", job["status"])
	}
	if stats := job["stats"].(map[string]any); len(stats) == 0 {
		t.Fatalf("stats = %v", stats)
	}

	list := call(t, client, http.MethodGet, base+"/jobs?limit=10", "", http.StatusOK)
	if list["total"].(float64) < 1 {
		t.Fatalf("jobs = %v", list)
	}

	logResp, err := client.Get(base + "/jobs/" + id + "/log")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	defer logResp.Body.Close()
	if logResp.StatusCode != http.StatusOK {
		t.Fatalf("log status = %d", logResp.StatusCode)
	}
	if ct := logResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("log content type = %s", ct)
	}
	logBody, err := io.ReadAll(logResp.Body)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if text := string(logBody); !strings.Contains(text, "PLAY [") || !strings.Contains(text, "PLAY RECAP") {
		t.Fatalf("log content is not ansible-like:\n%s", text)
	}
	call(t, client, http.MethodPost, base+"/jobs/"+id+"/cancel", "", http.StatusNoContent)
	call(t, client, http.MethodGet, base+"/jobs/20260101-0001", "", http.StatusNotFound)
}

func TestSingleMutatingJobReturnsConflict(t *testing.T) {
	ts, client, _ := testServerWith(t, 0.02)
	base := login(t, ts.URL, client)
	first := call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"backup-run","params":{"cluster":"pg-meta","mode":"full"},"check":false}`, http.StatusCreated)
	if _, ok := first["id"].(string); !ok {
		t.Fatalf("job = %v", first)
	}
	conflict := call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"pgsql-svc","params":{"cluster":"pg-meta"},"check":false}`, http.StatusConflict)
	if conflict["runningJobId"] != first["id"] {
		t.Fatalf("conflict = %v", conflict)
	}
	// A read-only action does not take the lock.
	call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"backup-check","params":{"cluster":"pg-meta"},"check":false}`, http.StatusCreated)

	call(t, client, http.MethodPost, base+"/jobs/"+first["id"].(string)+"/cancel", "", http.StatusNoContent)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job := call(t, client, http.MethodGet, base+"/jobs/"+first["id"].(string), "", http.StatusOK)
		if job["status"] == "canceled" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("canceled job did not reach the canceled state")
}

func TestAppliedBaseline(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	applied := call(t, client, http.MethodGet, base+"/applied", "", http.StatusOK)
	items := applied["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("applied = %v", applied)
	}
	first := map[string]any{}
	for _, raw := range items {
		item := raw.(map[string]any)
		if item["cluster"] == "pg-meta" {
			first = item
		}
	}
	if first["cluster"] != "pg-meta" || first["baseline"] != true || first["jobId"] != nil {
		t.Fatalf("baseline entry = %v", first)
	}
	if commit, _ := first["commit"].(string); commit == "" {
		t.Fatalf("baseline commit is empty: %v", first)
	}

	job := call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"pgsql-svc","params":{"cluster":"pg-meta"},"check":false}`, http.StatusCreated)
	id := job["id"].(string)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current := call(t, client, http.MethodGet, base+"/jobs/"+id, "", http.StatusOK)
		if current["status"] != "running" && current["status"] != "queued" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	applied = call(t, client, http.MethodGet, base+"/applied", "", http.StatusOK)
	item := appliedEntry(t, applied, "pg-meta")
	if item["baseline"] != false || item["jobId"] != id {
		t.Fatalf("applied after a successful job = %v", item)
	}

	// A check run never updates the applied version.
	checkJob := call(t, client, http.MethodPost, base+"/jobs",
		`{"action":"pgsql-hba","params":{"cluster":"pg-meta"},"check":true}`, http.StatusCreated)
	checkID := checkJob["id"].(string)
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current := call(t, client, http.MethodGet, base+"/jobs/"+checkID, "", http.StatusOK)
		if current["status"] != "running" && current["status"] != "queued" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	applied = call(t, client, http.MethodGet, base+"/applied", "", http.StatusOK)
	item = appliedEntry(t, applied, "pg-meta")
	if item["jobId"] != id {
		t.Fatalf("check runs must not update applied: %v", item)
	}
}

func TestStatusEndpoints(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	nodes := call(t, client, http.MethodGet, base+"/status/nodes", "", http.StatusOK)
	source := nodes["source"].(map[string]any)
	if source["ok"] != true || !strings.HasPrefix(source["url"].(string), "simulate://") {
		t.Fatalf("source = %v", source)
	}
	nodeList := nodes["items"].([]any)
	if len(nodeList) != 3 {
		t.Fatalf("nodes = %v", nodeList)
	}
	first := nodeList[0].(map[string]any)
	for _, key := range []string{"cpuCores", "cpuUsage", "load1", "memTotalBytes", "memAvailableBytes", "diskTotalBytes", "diskFreeBytes"} {
		if first[key] == nil {
			t.Fatalf("node %v misses %s: %v", first["ip"], key, first)
		}
	}
	if first["managed"] != true {
		t.Fatalf("node = %v", first)
	}

	clusters := call(t, client, http.MethodGet, base+"/status/clusters", "", http.StatusOK)
	clusterList := clusters["items"].([]any)
	if len(clusterList) != 2 {
		t.Fatalf("clusters = %v", clusters)
	}
	cluster := map[string]any{}
	for _, raw := range clusterList {
		item := raw.(map[string]any)
		if item["cluster"] == "pg-meta" {
			cluster = item
		}
	}
	if len(cluster) == 0 {
		t.Fatalf("pg-meta missing from %v", clusterList)
	}
	if cluster["leader"] == nil || cluster["paused"] == nil || cluster["healthy"] == nil {
		t.Fatalf("cluster = %v", cluster)
	}
	if members := cluster["members"].([]any); len(members) < 2 {
		t.Fatalf("members = %v", members)
	}

	single := call(t, client, http.MethodGet, base+"/status/clusters/pg-meta", "", http.StatusOK)
	if single["cluster"] != "pg-meta" {
		t.Fatalf("cluster = %v", single)
	}
	call(t, client, http.MethodGet, base+"/status/clusters/nope", "", http.StatusNotFound)

	config := call(t, client, http.MethodGet, base+"/status/clusters/pg-meta/pgconfig", "", http.StatusOK)
	params, ok := config["parameters"].(map[string]any)
	if !ok || params["shared_buffers"] == nil {
		t.Fatalf("config = %v", config)
	}

	drift := call(t, client, http.MethodGet, base+"/status/drift", "", http.StatusOK)
	items := drift["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("drift = %v", drift)
	}
	kinds := map[string]bool{}
	for _, raw := range items {
		item := raw.(map[string]any)
		kinds[item["kind"].(string)] = true
		if item["message"] == "" {
			t.Fatalf("drift = %v", item)
		}
	}
	if !kinds["role_mismatch"] {
		t.Fatalf("kinds = %v", kinds)
	}

	alerts := call(t, client, http.MethodGet, base+"/status/alerts", "", http.StatusOK)
	if items := alerts["items"].([]any); len(items) == 0 {
		t.Fatalf("alerts = %v", alerts)
	}

	memory := call(t, client, http.MethodGet, base+"/status/nodes/10.10.10.10/memory", "", http.StatusOK)
	services := memory["services"].([]any)
	if len(services) == 0 {
		t.Fatalf("memory = %v", memory)
	}
	if memory["source"].(map[string]any)["url"] != "simulate://ansible/10.10.10.10" {
		t.Fatalf("memory source = %v", memory["source"])
	}
	if first := services[0].(map[string]any); first["name"] == nil {
		t.Fatalf("service = %v", first)
	}

	meta := call(t, client, http.MethodGet, base+"/meta", "", http.StatusOK)
	if meta["simulate"] != true {
		t.Fatalf("meta = %v", meta)
	}
}

func TestSettingsAndNodeSpecs(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	current := call(t, client, http.MethodGet, base+"/settings", "", http.StatusOK)
	if current["backupMaxAgeHours"].(float64) != 26 || current["ansibleForks"].(float64) != 5 {
		t.Fatalf("defaults = %v", current)
	}
	if current["victoriaUrl"] != "" || current["memoryBaseline"] == nil {
		t.Fatalf("settings = %v", current)
	}
	derived := current["derived"].(map[string]any)
	if !strings.HasPrefix(derived["victoriaUrl"].(string), "http://10.10.10.10:8428") {
		t.Fatalf("derived = %v", derived)
	}

	updated := call(t, client, http.MethodPut, base+"/settings",
		`{"victoriaUrl":"http://10.0.0.9:8428","backupMaxAgeHours":12,"memoryBaseline":{"patroni":192}}`,
		http.StatusOK)
	if updated["backupMaxAgeHours"].(float64) != 12 {
		t.Fatalf("updated = %v", updated)
	}
	if updated["memoryBaseline"].(map[string]any)["patroni"].(float64) != 192 {
		t.Fatalf("baseline = %v", updated["memoryBaseline"])
	}
	if updated["derived"].(map[string]any)["victoriaUrl"] != "http://10.10.10.10:8428" {
		t.Fatalf("derived must stay derived: %v", updated["derived"])
	}

	call(t, client, http.MethodPut, base+"/settings", `{"victoriaUrl":"ftp://nope"}`, http.StatusUnprocessableEntity)
	call(t, client, http.MethodPut, base+"/settings", `{"ansibleForks":0}`, http.StatusUnprocessableEntity)

	empty := call(t, client, http.MethodGet, base+"/node-specs", "", http.StatusOK)
	if len(empty["items"].([]any)) != 0 {
		t.Fatalf("node specs = %v", empty)
	}
	spec := call(t, client, http.MethodPut, base+"/node-specs/10.10.10.20",
		`{"cpu":8,"memMb":32768,"diskGb":500}`, http.StatusOK)
	if spec["ip"] != "10.10.10.20" || spec["memMb"].(float64) != 32768 {
		t.Fatalf("spec = %v", spec)
	}
	call(t, client, http.MethodPut, base+"/node-specs/not-an-ip", `{"cpu":1,"memMb":1024,"diskGb":10}`,
		http.StatusUnprocessableEntity)
	listed := call(t, client, http.MethodGet, base+"/node-specs", "", http.StatusOK)
	if len(listed["items"].([]any)) != 1 {
		t.Fatalf("node specs = %v", listed)
	}
	call(t, client, http.MethodDelete, base+"/node-specs/10.10.10.20", "", http.StatusNoContent)
	call(t, client, http.MethodDelete, base+"/node-specs/10.10.10.20", "", http.StatusNotFound)
}

// appliedEntry returns the applied row of one cluster.
func appliedEntry(t *testing.T, body map[string]any, cluster string) map[string]any {
	t.Helper()
	for _, raw := range body["items"].([]any) {
		item := raw.(map[string]any)
		if item["cluster"] == cluster {
			return item
		}
	}
	t.Fatalf("applied entry for %s missing: %v", cluster, body)
	return nil
}

func TestBackupsAndPitrPlan(t *testing.T) {
	ts, client, _ := testServer(t)
	base := login(t, ts.URL, client)

	info := call(t, client, http.MethodGet, base+"/backups/pg-meta", "", http.StatusOK)
	if info["cluster"] != "pg-meta" || info["stanza"] != "pg-meta" {
		t.Fatalf("backup info = %v", info)
	}
	if info["source"].(map[string]any)["ok"] != true {
		t.Fatalf("source = %v", info["source"])
	}
	backups := info["backups"].([]any)
	if len(backups) < 3 {
		t.Fatalf("backups = %d, want a week of history", len(backups))
	}
	first := backups[0].(map[string]any)
	for _, key := range []string{"label", "type", "start", "stop", "size", "deltaSize", "repoSize", "error"} {
		if first[key] == nil {
			t.Fatalf("backup %v misses %s", first["label"], key)
		}
	}
	if info["window"] == nil {
		t.Fatalf("window = %v", info["window"])
	}
	if info["restorePoints"] == nil || info["events"] == nil {
		t.Fatalf("arrays must not be null: %v", info)
	}
	if info["repo"].(map[string]any)["method"] != "local" {
		t.Fatalf("repo = %v", info["repo"])
	}

	inplace := call(t, client, http.MethodPost, base+"/backups/pg-meta/pitr/plan",
		`{"mode":"inplace","target":{"type":"latest"},"action":"pause","backup":true}`, http.StatusOK)
	if inplace["inWindow"] != true {
		t.Fatalf("plan = %v", inplace)
	}
	if inplace["backupSet"] == nil || inplace["pgPitr"] == nil {
		t.Fatalf("plan = %v", inplace)
	}
	if commands := inplace["commands"].([]any); len(commands) == 0 ||
		!strings.Contains(commands[0].(string), "pgsql-pitr.yml") {
		t.Fatalf("commands = %v", commands)
	}
	gate, ok := inplace["gate"].(map[string]any)
	if !ok || gate["confirmText"] != "pg-meta" {
		t.Fatalf("gate = %v", inplace["gate"])
	}
	if loss := inplace["lossWindow"]; loss != nil {
		t.Fatalf("latest target must not report a loss window: %v", loss)
	}

	clone := call(t, client, http.MethodPost, base+"/backups/pg-meta/pitr/plan",
		`{"mode":"clone","target":{"type":"latest"},"action":"pause","newCluster":"pg-clone"}`, http.StatusOK)
	if clone["gate"] != nil {
		t.Fatalf("clone plan must not require the in-place gate: %v", clone["gate"])
	}
	blocked := false
	for _, raw := range clone["preconditions"].([]any) {
		item := raw.(map[string]any)
		if item["id"] == "repo_reachable" && item["ok"] == false {
			blocked = true
		}
	}
	if commands := clone["commands"].([]any); len(commands) == 0 ||
		!strings.Contains(commands[0].(string), `-l pg-clone`) {
		t.Fatalf("clone commands = %v", commands)
	}
	if !blocked {
		t.Fatalf("a local repository cannot be cloned to another node: %v", clone["preconditions"])
	}

	call(t, client, http.MethodPost, base+"/backups/pg-meta/pitr/plan",
		`{"mode":"inplace","target":{"type":"time","time":"not-a-time"},"action":"pause"}`, http.StatusUnprocessableEntity)
}
