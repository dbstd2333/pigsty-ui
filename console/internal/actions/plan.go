package actions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/inventory"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
)

// ErrUnknownAction is returned for an action id outside the catalog.
var ErrUnknownAction = errors.New("unknown action")

// Window is the recoverable window of one cluster.
type Window struct {
	Start        time.Time
	End          time.Time
	LastBackupAt *time.Time
}

// Member is one Patroni member of a PG cluster.
type Member struct {
	Name           string
	Host           string
	Role           string
	State          string
	Timeline       int
	LagBytes       *int64
	PendingRestart bool
}

// Facts supplies the external state a plan depends on. Implementations must
// report ok=false instead of failing when a source is unreachable.
type Facts interface {
	PatroniMembers(ctx context.Context, cluster string) ([]Member, bool)
	BackupWindow(ctx context.Context, cluster string) (Window, bool)
	ManagedNodes(ctx context.Context) map[string]bool
}

// SettingsProvider supplies the configurable planning thresholds.
type SettingsProvider interface {
	BackupMaxAgeHours() int
}

// Snapshot is the configuration version a plan is computed against.
type Snapshot struct {
	Text   string
	Hash   string
	Commit string
}

// Options configures a Planner.
type Options struct {
	Snapshot func(ctx context.Context) (Snapshot, error)
	Facts    Facts
	Settings SettingsProvider
	Simulate bool
}

// Planner validates action requests, evaluates preconditions and renders the
// commands of the action catalog.
type Planner struct {
	catalog  *Catalog
	snapshot func(ctx context.Context) (Snapshot, error)
	settings SettingsProvider

	// Facts supplies the live cluster state used by preconditions.
	Facts Facts
	// Simulate allows the developer-only `fail` parameter of the simulate mode.
	Simulate bool
}

// Plan is the result of a plan request. The exported fields mirror the API
// contract; the remaining fields are used by the job runner.
type Plan struct {
	Action        string              `json:"action"`
	Risk          plan.Risk           `json:"risk" enum:"readonly,low,medium,high,critical"`
	Commands      []string            `json:"commands"`
	Targets       []string            `json:"targets"`
	Preconditions []plan.Precondition `json:"preconditions"`
	Gate          *plan.Gate          `json:"gate"`
	ConfigHash    string              `json:"configHash"`

	Title      string         `json:"-"`
	Check      bool           `json:"-"`
	Steps      []Command      `json:"-"`
	Cluster    *string        `json:"-"`
	Commit     string         `json:"-"`
	Params     map[string]any `json:"-"`
	Confirm    string         `json:"-"`
	RestorePt  bool           `json:"-"`
	ParseError string         `json:"-"`
}

// NewPlanner loads the embedded catalog and builds a planner.
func NewPlanner(opts Options) (*Planner, error) {
	catalog, err := Load()
	if err != nil {
		return nil, err
	}
	return &Planner{
		catalog:  catalog,
		snapshot: opts.Snapshot,
		settings: opts.Settings,
		Facts:    opts.Facts,
		Simulate: opts.Simulate,
	}, nil
}

// Catalog exposes the loaded action catalog.
func (p *Planner) Catalog() *Catalog { return p.catalog }

// Snapshot returns the configuration version plans are computed against.
func (p *Planner) Snapshot(ctx context.Context) (Snapshot, error) {
	if p.snapshot == nil {
		return Snapshot{}, fmt.Errorf("配置读取器未初始化")
	}
	return p.snapshot(ctx)
}

// model parses the current pigsty.yml inventory.
func (p *Planner) model() (*inventory.Model, error) {
	snap, err := p.Snapshot(context.Background())
	if err != nil {
		return nil, err
	}
	return inventory.Load([]byte(snap.Text))
}

// Plan validates a request and renders the commands of one action.
func (p *Planner) Plan(ctx context.Context, actionID string, params map[string]any, check bool) (*Plan, error) {
	a, err := p.catalog.lookup(actionID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAction, actionID)
	}
	if params == nil {
		params = map[string]any{}
	}
	if check && !a.def.Check {
		errs := &ParamError{}
		errs.issue("check", "动作 "+a.def.ID+" 不支持 --check 预演")
		return nil, errs
	}
	out := p.resolveParams(a, params)
	if len(out.errs.Issues) > 0 {
		return nil, out.errs
	}
	res := out.res

	snap, err := p.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	risk := p.upgradeRisk(res)
	result := &Plan{
		Action:        a.def.ID,
		Title:         a.title(res.params),
		Risk:          risk,
		Check:         check,
		ConfigHash:    snap.Hash,
		Commit:        snap.Commit,
		Params:        res.params,
		Commands:      []string{},
		Targets:       p.targets(res),
		Preconditions: p.preconditions(ctx, res),
	}
	if res.cluster != "" {
		cluster := res.cluster
		result.Cluster = &cluster
	}
	for _, st := range a.Steps {
		cmd, err := p.renderStep(ctx, res, st, check)
		if err != nil {
			if errors.Is(err, errNoPrimary) {
				errs := &ParamError{}
				errs.issue("cluster", err.Error())
				return nil, errs
			}
			return nil, fmt.Errorf("渲染动作 %s 的命令失败：%w", a.def.ID, err)
		}
		result.Steps = append(result.Steps, cmd)
		result.Commands = append(result.Commands, cmd.Display)
	}
	result.Gate = p.gate(ctx, res, risk)
	result.Confirm = confirmTarget(res)
	result.RestorePt = risk.AtLeast(plan.RiskHigh) && res.cluster != ""
	return result, nil
}

// Blocked returns the first error precondition that prevents execution.
func (p *Plan) Blocked() string { return plan.FirstError(p.Preconditions) }

// upgradeRisk raises the base risk when the submitted parameters are
// destructive, e.g. dropping a user or a database.
func (p *Planner) upgradeRisk(res *resolved) plan.Risk {
	risk := res.action.def.Risk
	if res.info == nil {
		return risk
	}
	vars := res.info.Vars
	switch res.action.def.ID {
	case "pgsql-user":
		if entryState(vars, "pg_users", res.user) == "absent" {
			risk = risk.Upgrade(plan.RiskHigh)
		}
	case "pgsql-db":
		switch entryState(vars, "pg_databases", res.db) {
		case "absent", "recreate":
			// Dropping (or dropping and recreating) a database destroys data.
			risk = risk.Upgrade(plan.RiskHigh)
		}
	}
	return risk
}

// targets lists the hosts an action touches.
func (p *Planner) targets(res *resolved) []string {
	switch {
	case len(res.ips) > 0:
		return append([]string{}, res.ips...)
	case res.info != nil:
		return append([]string{}, res.info.Hosts...)
	default:
		return []string{}
	}
}

// gate builds the confirmation gate of a risky action, nil for low risk ones.
func (p *Planner) gate(ctx context.Context, res *resolved, risk plan.Risk) *plan.Gate {
	if res.action.def.Gate == false && !risk.AtLeast(plan.RiskHigh) {
		return nil
	}
	maxAge := 26
	if p.settings != nil {
		maxAge = p.settings.BackupMaxAgeHours()
	}
	gate := &plan.Gate{
		ConfirmText:       confirmTarget(res),
		Checkboxes:        gateCheckboxes(res),
		BackupMaxAgeHours: maxAge,
	}
	if res.cluster != "" && p.Facts != nil {
		if window, ok := p.Facts.BackupWindow(ctx, res.cluster); ok {
			gate.LastBackupAt = window.LastBackupAt
		}
	}
	return gate
}

// gateCheckboxes returns the mandatory acknowledgements of an action.
func gateCheckboxes(res *resolved) []string {
	switch res.action.def.ID {
	case "pgsql-rm-replica":
		return []string{"我已确认目标实例不是主库", "我已确认该实例的数据可以销毁"}
	case "pg-restart":
		return []string{"我已确认重启期间该实例会短暂不可用"}
	case "pg-switchover":
		return []string{"我已确认业务可以接受一次主从切换"}
	case "pitr-inplace":
		return []string{
			"我已确认业务已停写或已通知相关方",
			"我已确认恢复到该目标点之后的数据会丢失",
		}
	case "pitr-promote":
		return []string{"我已确认恢复出的数据已验证通过"}
	default:
		return []string{"我已确认目标与影响面"}
	}
}

// confirmTarget returns the text the user has to type into the gate.
func confirmTarget(res *resolved) string {
	switch {
	case len(res.ips) > 0:
		return strings.Join(res.ips, ",")
	case res.action.def.ID == "pg-restart":
		return res.member
	case res.action.def.ID == "pg-switchover":
		return res.candidate
	default:
		return res.cluster
	}
}
