// Package services wires the console services together: it adapts the
// configuration store, the settings and the status and backup collectors to the
// interfaces their consumers expect.
package services

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/backup"
	"github.com/dbstd2333/pigsty-ui/console/internal/configstore"
	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
	"github.com/dbstd2333/pigsty-ui/console/internal/settings"
	"github.com/dbstd2333/pigsty-ui/console/internal/status"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// AdHocTimeout bounds one read-only ansible ad-hoc collection.
const AdHocTimeout = 20 * time.Second

// StatusSource adapts the configuration store and the settings service to the
// status package.
type StatusSource struct {
	Store       *configstore.Store
	SettingsSvc *settings.Service
}

// Model returns the parsed inventory of the current configuration.
func (s *StatusSource) Model(ctx context.Context) (*inventory.Model, error) {
	_, hash, _, err := s.Store.Current(ctx)
	if err != nil {
		return nil, err
	}
	return s.Store.Inventory().Model(s.Store.File(), hash)
}

// Settings returns the configured address overrides.
func (s *StatusSource) Settings() status.Settings {
	if s.SettingsSvc == nil {
		return status.Settings{}
	}
	value, err := s.SettingsSvc.Get(context.Background())
	if err != nil {
		return status.Settings{}
	}
	return status.Settings{
		VictoriaURL:     value.VictoriaURL,
		AlertmanagerURL: value.AlertmanagerURL,
		GrafanaURL:      value.GrafanaURL,
	}
}

// Home returns the Pigsty home directory.
func (s *StatusSource) Home() string { return s.Store.Home() }

// AdHoc runs one read-only ansible ad-hoc command on a host. Commands are
// executed through argv arrays, never through a shell.
func AdHoc(home string) status.AdHocFunc {
	return func(ctx context.Context, host, command string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, AdHocTimeout)
		defer cancel()
		argv := []string{"ansible", host, "-i", "pigsty.yml", "-b", "--become-user=postgres", "-m", "shell", "-a", command}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = home
		cmd.Env = append(cmd.Environ(), "ANSIBLE_FORCE_COLOR=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("%v: %s", err, lastNonEmptyLine(string(out)))
		}
		return string(out), nil
	}
}

// lastNonEmptyLine returns the last non-empty line of a command output.
func lastNonEmptyLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// backupFetcher collects the pgBackRest state of one cluster from the inventory
// and from the primary instance.
type BackupFetcher struct {
	Home   string
	Store  *configstore.Store
	Status *status.Service
	AdHoc  status.AdHocFunc
}

// Cluster describes one PG cluster of the current inventory.
func (f *BackupFetcher) Cluster(name string) backup.ClusterInfo {
	model, err := f.model()
	if err != nil {
		return backup.ClusterInfo{Name: name}
	}
	c, ok := model.Cluster(inventory.ModulePGSQL, name)
	if !ok {
		return backup.ClusterInfo{Name: name}
	}
	return backup.ClusterInfo{Name: c.Identity, Hosts: c.Hosts, Repo: repoOf(c.Vars), Exists: true}
}

// Info runs pgbackrest info on the primary of a cluster.
func (f *BackupFetcher) Info(ctx context.Context, cluster string) ([]byte, plan.SourceInfo, error) {
	host := f.primaryHost(ctx, cluster)
	if host == "" {
		return nil, plan.FailedSource(plan.AdhocSource(cluster), "找不到集群 "+cluster+" 的主库地址"), fmt.Errorf("找不到主库")
	}
	src := plan.AdhocSource(host)
	command := fmt.Sprintf("pgbackrest --stanza=%s info --output=json", cluster)
	out, err := f.AdHoc(ctx, host, command)
	if err != nil {
		return nil, plan.FailedSource(src, err.Error()), err
	}
	return []byte(extractJSON(out)), plan.NewSource(src), nil
}

// Archive reads the pg_stat_archiver metrics of one cluster from
// VictoriaMetrics (metric names verified against
// roles/pg_exporters/templates/pg_exporter.yml).
func (f *BackupFetcher) Archive(ctx context.Context, cluster string) *backup.ArchiveStats {
	vm := status.NewVM(f.Status.URLs(ctx).VictoriaURL)
	if !vm.Up() {
		return nil
	}
	filter := fmt.Sprintf(`{job="pgsql", cls="%s"}`, cluster)
	finish, err := vm.Query(ctx, "pg_archiver_finish_time"+filter)
	if err != nil {
		return nil
	}
	out := &backup.ArchiveStats{}
	if len(finish) > 0 {
		at := time.Unix(int64(finish[0].Value), 0).UTC()
		out.LastArchivedAt = &at
	}
	if failed, err := vm.Query(ctx, "pg_archiver_failed_count"+filter); err == nil && len(failed) > 0 {
		count := int64(failed[0].Value)
		out.FailedCount = &count
	}
	if failed, err := vm.Query(ctx, "pg_archiver_failed_time"+filter); err == nil && len(failed) > 0 && failed[0].Value > 0 {
		at := time.Unix(int64(failed[0].Value), 0).UTC()
		out.LastFailedAt = &at
	}
	return out
}

// model parses the current inventory.
func (f *BackupFetcher) model() (*inventory.Model, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, hash, _, err := f.Store.Current(ctx)
	if err != nil {
		return nil, err
	}
	return f.Store.Inventory().Model(f.Store.File(), hash)
}

// primaryHost returns the primary of a cluster: Patroni leader first, then the
// pg_role declared in the inventory.
func (f *BackupFetcher) primaryHost(ctx context.Context, cluster string) string {
	if st, err := f.Status.Cluster(ctx, cluster); err == nil && st.Leader != nil && *st.Leader != "" {
		return *st.Leader
	}
	model, err := f.model()
	if err != nil {
		return ""
	}
	c, ok := model.Cluster(inventory.ModulePGSQL, cluster)
	if !ok {
		return ""
	}
	return c.Primary
}

// repoOf renders the pgBackRest repository configuration of a cluster from its
// merged inventory variables.
func repoOf(vars map[string]any) backup.Repo {
	repo := backup.Repo{Method: "local", Type: "posix"}
	if method, ok := vars["pgbackrest_method"].(string); ok && method != "" {
		repo.Method = method
	}
	repos, _ := vars["pgbackrest_repo"].(map[string]any)
	entry, _ := repos[repo.Method].(map[string]any)
	if entry == nil {
		return repo
	}
	if kind, ok := entry["type"].(string); ok && kind != "" {
		repo.Type = kind
	}
	if path, ok := entry["path"].(string); ok && path != "" {
		repo.Path = &path
	}
	if cipher, ok := entry["cipher_type"].(string); ok && cipher != "" {
		repo.Cipher = &cipher
	}
	full := intEntry(entry, "retention_full")
	kind := "count"
	if v, ok := entry["retention_full_type"].(string); ok && v != "" {
		kind = v
	}
	if full > 0 || kind != "" {
		retention := fmt.Sprintf("%s: %d", kind, full)
		if kind == "time" {
			retention = fmt.Sprintf("time: %d days", full)
		}
		repo.Retention = &retention
	}
	return repo
}

// intEntry reads an integer inventory value.
func intEntry(vars map[string]any, key string) int {
	switch v := vars[key].(type) {
	case int:
		return v
	case float64:
		return int(v)
	case string:
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// extractJSON extracts the JSON document of a command output, ignoring the
// ansible prefix lines of an ad-hoc run.
func extractJSON(out string) string {
	text := strings.TrimSpace(out)
	if index := strings.Index(text, "["); index > 0 {
		text = text[index:]
	}
	if index := strings.LastIndex(text, "]"); index >= 0 {
		text = text[:index+1]
	}
	return text
}

// backupStore adapts the SQLite store to the backup package.
type BackupStore struct {
	DB *store.Store
}

// RestorePoints returns the console restore points of a cluster.
func (s *BackupStore) RestorePoints(_ context.Context, cluster string) ([]backup.RestorePoint, error) {
	rows, err := s.DB.RestorePoints(cluster)
	if err != nil {
		return nil, err
	}
	out := make([]backup.RestorePoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, backup.RestorePoint{
			Name: row.Name, LSN: row.LSN, Time: row.Time, JobID: row.JobID, Cluster: row.Cluster,
		})
	}
	return out, nil
}

// TimelineEvents returns the console timeline events of a cluster.
func (s *BackupStore) TimelineEvents(_ context.Context, cluster string) ([]backup.TimelineEvent, error) {
	rows, err := s.DB.TimelineEvents(cluster)
	if err != nil {
		return nil, err
	}
	out := make([]backup.TimelineEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, backup.TimelineEvent{
			Time: row.Time, Kind: row.Kind, Label: row.Label, JobID: row.JobID,
		})
	}
	return out, nil
}

// factsAdapter implements actions.Facts on top of the status and backup
// services.
type Facts struct {
	Status *status.Service
	Backup *backup.Service
}

// PatroniMembers returns the Patroni members of a cluster.
func (f *Facts) PatroniMembers(ctx context.Context, cluster string) ([]actions.Member, bool) {
	members, ok := f.Status.PatroniMembers(ctx, cluster)
	out := make([]actions.Member, 0, len(members))
	for _, m := range members {
		timeline := 0
		if m.Timeline != nil {
			timeline = *m.Timeline
		}
		out = append(out, actions.Member{
			Name: m.Name, Host: m.Host, Role: m.Role, State: m.State, Timeline: timeline,
			LagBytes: m.LagBytes, PendingRestart: m.PendingRestart,
		})
	}
	return out, ok
}

// BackupWindow returns the recoverable window of a cluster.
func (f *Facts) BackupWindow(ctx context.Context, cluster string) (actions.Window, bool) {
	window, last, ok := f.Backup.Window(ctx, cluster)
	if !ok {
		return actions.Window{}, false
	}
	out := actions.Window{Start: window.Start, End: window.End}
	if !last.IsZero() {
		value := last
		out.LastBackupAt = &value
	}
	return out, true
}

// ManagedNodes returns the hosts that report node_exporter metrics.
func (f *Facts) ManagedNodes(ctx context.Context) map[string]bool {
	return f.Status.ManagedNodes(ctx)
}
