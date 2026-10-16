package backup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// CacheTTL is how long a parsed BackupInfo stays fresh.
const CacheTTL = 10 * time.Minute

// ClusterInfo is the inventory derived part of a backup view.
type ClusterInfo struct {
	Name   string
	Hosts  []string
	Repo   Repo
	Exists bool
}

// ArchiveStats is the WAL archiver state read from VictoriaMetrics.
type ArchiveStats struct {
	LastArchivedAt *time.Time
	FailedCount    *int64
	LastFailedAt   *time.Time
}

// Fetcher collects the external state a backup view needs. Implementations must
// report errors instead of panicking; the UI shows them through source.
type Fetcher interface {
	// Info returns the JSON of pgbackrest info --output=json plus its source.
	Info(ctx context.Context, cluster string) ([]byte, plan.SourceInfo, error)
	// Archive returns the archiver statistics of a cluster, nil when unknown.
	Archive(ctx context.Context, cluster string) *ArchiveStats
	// Cluster describes a PG cluster of the current inventory.
	Cluster(cluster string) ClusterInfo
}

// Store provides the console owned timeline data of a cluster.
type Store interface {
	RestorePoints(ctx context.Context, cluster string) ([]RestorePoint, error)
	TimelineEvents(ctx context.Context, cluster string) ([]TimelineEvent, error)
}

// Options configures the backup service.
type Options struct {
	Fetcher  Fetcher
	Store    Store
	Logger   *slog.Logger
	Simulate bool
	TTL      time.Duration
}

// Service serves backup information and PITR plans for PG clusters.
type Service struct {
	fetcher  Fetcher
	store    Store
	logger   *slog.Logger
	simulate bool
	ttl      time.Duration

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	info      Info
	fetchedAt time.Time
}

// New builds the backup service.
func New(opts Options) *Service {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ttl := opts.TTL
	if ttl <= 0 {
		ttl = CacheTTL
	}
	return &Service{fetcher: opts.Fetcher, store: opts.Store, logger: logger, simulate: opts.Simulate, ttl: ttl, cache: map[string]cacheEntry{}}
}

// Info returns the backup view of one cluster, fetching at most once per TTL.
func (s *Service) Info(ctx context.Context, cluster string, refresh bool) (Info, error) {
	if cluster == "" {
		return Info{}, fmt.Errorf("缺少集群名称")
	}
	if !refresh {
		if info, ok := s.cached(cluster); ok {
			return s.withConsoleData(ctx, cluster, info), nil
		}
	}
	info, err := s.fetch(ctx, cluster)
	if err != nil {
		return Info{}, err
	}
	info.FetchedAt = time.Now().UTC()
	s.mu.Lock()
	s.cache[cluster] = cacheEntry{info: info, fetchedAt: time.Now()}
	s.mu.Unlock()
	return s.withConsoleData(ctx, cluster, info), nil
}

// Window returns the recoverable window of a cluster, ok=false when unknown.
func (s *Service) Window(ctx context.Context, cluster string) (Window, time.Time, bool) {
	info, err := s.Info(ctx, cluster, false)
	if err != nil || info.Window == nil {
		last := time.Time{}
		if info.Archive.LastArchivedAt != nil {
			last = *info.Archive.LastArchivedAt
		}
		return Window{}, last, false
	}
	last := info.Window.End
	if info.Archive.LastArchivedAt != nil {
		last = *info.Archive.LastArchivedAt
	}
	return *info.Window, last, true
}

// Invalidate drops the cached view of one cluster (after backup or PITR jobs).
func (s *Service) Invalidate(cluster string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cluster == "" {
		s.cache = map[string]cacheEntry{}
		return
	}
	delete(s.cache, cluster)
}

// cached returns the cached view while it is still fresh.
func (s *Service) cached(cluster string) (Info, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.cache[cluster]
	if !ok || time.Since(entry.fetchedAt) > s.ttl {
		return Info{}, false
	}
	return entry.info, true
}

// fetch loads the repository information of one cluster.
func (s *Service) fetch(ctx context.Context, cluster string) (Info, error) {
	info := Info{
		Cluster:       cluster,
		Stanza:        cluster,
		Backups:       []Backup{},
		RestorePoints: []RestorePoint{},
		Events:        []TimelineEvent{},
	}
	cinfo := s.clusterInfo(cluster)
	if cinfo.Exists {
		info.Repo = cinfo.Repo
	}
	if info.Repo.Method == "" {
		info.Repo.Method = "local"
	}
	if !cinfo.Exists {
		info.Source = plan.FailedSource(plan.AdhocSource(cluster), "集群 "+cluster+" 不在当前 inventory 中")
		info.FetchedAt = time.Now().UTC()
		return info, nil
	}
	if s.simulate {
		return s.simulateInfo(ctx, cluster, info), nil
	}
	data, src, err := s.fetcher.Info(ctx, cluster)
	info.Source = src
	if err != nil {
		return info, nil
	}
	parsed, err := ParseInfo(data)
	if err != nil {
		info.Source = plan.FailedSource(src.URL, err.Error())
		return info, nil
	}
	info.Stanza = parsed.Stanza
	info.Backups = parsed.Backups
	info.Status = parsed.Status
	if parsed.Cipher != "" && parsed.Cipher != "none" {
		cipher := parsed.Cipher
		info.Repo.Cipher = &cipher
	}
	info.Archive.Min = parsed.ArchiveMin
	info.Archive.Max = parsed.ArchiveMax
	if stats := s.fetcher.Archive(ctx, cluster); stats != nil {
		info.Archive.LastArchivedAt = stats.LastArchivedAt
		info.Archive.FailedCount = stats.FailedCount
		info.Archive.LastFailedAt = stats.LastFailedAt
	}
	info.Window = ComputeWindow(info.Backups, info.Archive)
	return info, nil
}

// clusterInfo describes a cluster from the inventory.
func (s *Service) clusterInfo(cluster string) ClusterInfo {
	if s.fetcher == nil {
		return ClusterInfo{Name: cluster}
	}
	return s.fetcher.Cluster(cluster)
}

// withConsoleData merges the console owned restore points and timeline events.
func (s *Service) withConsoleData(ctx context.Context, cluster string, info Info) Info {
	if info.RestorePoints == nil {
		info.RestorePoints = []RestorePoint{}
	}
	if info.Events == nil {
		info.Events = []TimelineEvent{}
	}
	if s.store == nil {
		return info
	}
	points, err := s.store.RestorePoints(ctx, cluster)
	if err != nil {
		s.logger.Warn("load restore points", "cluster", cluster, "err", err)
	} else if len(points) > 0 {
		info.RestorePoints = points
	}
	events, err := s.store.TimelineEvents(ctx, cluster)
	if err != nil {
		s.logger.Warn("load timeline events", "cluster", cluster, "err", err)
	} else if len(events) > 0 {
		info.Events = append(info.Events, events...)
	}
	return info
}
