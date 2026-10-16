// Package settings stores the console settings in SQLite and merges them with
// the values derived from the current inventory.
package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// Defaults of the configurable settings.
const (
	DefaultBackupMaxAgeHours = 26
	DefaultAnsibleForks      = 5
)

// Derived holds the service addresses derived from the inventory.
type Derived struct {
	VictoriaURL     string `json:"victoriaUrl"`
	AlertmanagerURL string `json:"alertmanagerUrl"`
	GrafanaURL      string `json:"grafanaUrl"`
}

// Settings is the API contract type of GET /settings.
type Settings struct {
	VictoriaURL       string         `json:"victoriaUrl"`
	AlertmanagerURL   string         `json:"alertmanagerUrl"`
	GrafanaURL        string         `json:"grafanaUrl"`
	BackupMaxAgeHours int            `json:"backupMaxAgeHours"`
	AnsibleForks      int            `json:"ansibleForks"`
	MemoryBaseline    map[string]int `json:"memoryBaseline"`
	Derived           Derived        `json:"derived"`
}

// Patch is one PUT /settings body; nil fields stay unchanged.
type Patch struct {
	VictoriaURL       *string         `json:"victoriaUrl,omitempty"`
	AlertmanagerURL   *string         `json:"alertmanagerUrl,omitempty"`
	GrafanaURL        *string         `json:"grafanaUrl,omitempty"`
	BackupMaxAgeHours *int            `json:"backupMaxAgeHours,omitempty"`
	AnsibleForks      *int            `json:"ansibleForks,omitempty"`
	MemoryBaseline    *map[string]int `json:"memoryBaseline,omitempty"`
}

// DerivedFunc computes the addresses derived from the inventory.
type DerivedFunc func(ctx context.Context) Derived

// Service reads and writes the console settings.
type Service struct {
	store   *store.Store
	derived DerivedFunc
	logger  *slog.Logger
}

// New builds the settings service.
func New(st *store.Store, derived DerivedFunc, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, derived: derived, logger: logger}
}

// Get returns the effective settings.
func (s *Service) Get(ctx context.Context) (Settings, error) {
	raw, err := s.store.Settings()
	if err != nil {
		return Settings{}, err
	}
	out := Settings{
		VictoriaURL:       raw["victoriaUrl"],
		AlertmanagerURL:   raw["alertmanagerUrl"],
		GrafanaURL:        raw["grafanaUrl"],
		BackupMaxAgeHours: intValue(raw["backupMaxAgeHours"], DefaultBackupMaxAgeHours),
		AnsibleForks:      intValue(raw["ansibleForks"], DefaultAnsibleForks),
		MemoryBaseline:    map[string]int{},
	}
	if baseline := raw["memoryBaseline"]; baseline != "" {
		if err := json.Unmarshal([]byte(baseline), &out.MemoryBaseline); err != nil {
			s.logger.Warn("settings memoryBaseline is not valid json", "err", err)
		}
	}
	if s.derived != nil {
		out.Derived = s.derived(ctx)
	}
	return out, nil
}

// Update applies a partial settings update and returns the new state.
func (s *Service) Update(ctx context.Context, patch Patch) (Settings, error) {
	values := map[string]string{}
	if patch.VictoriaURL != nil {
		url, err := normalizeURL(*patch.VictoriaURL)
		if err != nil {
			return Settings{}, err
		}
		values["victoriaUrl"] = url
	}
	if patch.AlertmanagerURL != nil {
		url, err := normalizeURL(*patch.AlertmanagerURL)
		if err != nil {
			return Settings{}, err
		}
		values["alertmanagerUrl"] = url
	}
	if patch.GrafanaURL != nil {
		url, err := normalizeURL(*patch.GrafanaURL)
		if err != nil {
			return Settings{}, err
		}
		values["grafanaUrl"] = url
	}
	if patch.BackupMaxAgeHours != nil {
		v := *patch.BackupMaxAgeHours
		if v < 1 || v > 8760 {
			return Settings{}, fmt.Errorf("backupMaxAgeHours 需在 1 - 8760 之间")
		}
		values["backupMaxAgeHours"] = strconv.Itoa(v)
	}
	if patch.AnsibleForks != nil {
		v := *patch.AnsibleForks
		if v < 1 || v > 100 {
			return Settings{}, fmt.Errorf("ansibleForks 需在 1 - 100 之间")
		}
		values["ansibleForks"] = strconv.Itoa(v)
	}
	if patch.MemoryBaseline != nil {
		baseline := map[string]int{}
		for key, value := range *patch.MemoryBaseline {
			if value < 0 || value > 1<<20 {
				return Settings{}, fmt.Errorf("memoryBaseline.%s 需为非负整数", key)
			}
			baseline[key] = value
		}
		data, err := json.Marshal(baseline)
		if err != nil {
			return Settings{}, err
		}
		values["memoryBaseline"] = string(data)
	}
	if err := s.store.PutSettings(values); err != nil {
		return Settings{}, err
	}
	return s.Get(ctx)
}

// BackupMaxAgeHours returns the freshness threshold of the backup gate.
func (s *Service) BackupMaxAgeHours() int {
	raw, err := s.store.Settings()
	if err != nil {
		return DefaultBackupMaxAgeHours
	}
	return intValue(raw["backupMaxAgeHours"], DefaultBackupMaxAgeHours)
}

// AnsibleForks returns the configured ansible fork count.
func (s *Service) AnsibleForks() int {
	raw, err := s.store.Settings()
	if err != nil {
		return DefaultAnsibleForks
	}
	return intValue(raw["ansibleForks"], DefaultAnsibleForks)
}

// normalizeURL validates a settings URL.
func normalizeURL(value string) (string, error) {
	url := strings.TrimSpace(value)
	if url == "" {
		return "", nil
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("地址需以 http:// 或 https:// 开头")
	}
	return strings.TrimSuffix(url, "/"), nil
}

// intValue parses a stored integer with a fallback.
func intValue(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}
