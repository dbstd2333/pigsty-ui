// Package jobs runs the action catalog: it validates requests, executes
// playbooks and ad-hoc commands, streams ansible callback events, masks secrets
// and persists the job history in SQLite.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/plan"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
	"github.com/dbstd2333/pigsty-ui/console/internal/validate"
)

// Job is one action run (API contract type).
type Job struct {
	ID           string               `json:"id"`
	Action       string               `json:"action"`
	Title        string               `json:"title"`
	Params       map[string]any       `json:"params"`
	Check        bool                 `json:"check"`
	Risk         plan.Risk            `json:"risk" enum:"readonly,low,medium,high,critical"`
	Status       string               `json:"status" enum:"queued,running,success,failed,canceled"`
	Commands     []string             `json:"commands"`
	Cluster      *string              `json:"cluster"`
	Targets      []string             `json:"targets"`
	ConfigHash   string               `json:"configHash"`
	ConfigCommit *string              `json:"configCommit"`
	User         string               `json:"user"`
	ConfirmText  *string              `json:"confirmText"`
	CreatedAt    time.Time            `json:"createdAt"`
	StartedAt    *time.Time           `json:"startedAt"`
	FinishedAt   *time.Time           `json:"finishedAt"`
	ExitCode     *int                 `json:"exitCode"`
	CurrentStep  int                  `json:"currentStep"`
	Stats        map[string]HostStats `json:"stats"`
	RestorePoint *RestorePoint        `json:"restorePoint"`
	Error        *string              `json:"error"`
}

// RestorePoint is a named restore point created for a job (API contract type).
type RestorePoint = plan.RestorePoint

// Confirm is the gate confirmation of a risky job.
type Confirm struct {
	Text       string   `json:"text"`
	Checkboxes []string `json:"checkboxes"`
}

// SubmitRequest is one POST /jobs request.
type SubmitRequest struct {
	Action  string
	Params  map[string]any
	Check   bool
	Confirm *Confirm
	User    string
}

// Planner renders the commands of an action request.
type Planner interface {
	Plan(ctx context.Context, actionID string, params map[string]any, check bool) (*actions.Plan, error)
}

// Options configures the job manager.
type Options struct {
	Home     string
	DataDir  string
	JobsDir  string
	Logger   *slog.Logger
	Store    *store.Store
	Planner  Planner
	Simulate bool
	// SimulateSpeed scales the delay between simulated events (1 = real
	// pacing, larger values make the developer mode finish faster).
	SimulateSpeed float64
	Forks         int
	// OnFinish is called after a job reached a final state, used to refresh the
	// caches of the status and backup services.
	OnFinish func(job Job)
}

// Manager owns the running jobs of this console instance.
type Manager struct {
	home     string
	jobsDir  string
	logger   *slog.Logger
	store    *store.Store
	planner  Planner
	simulate bool
	simSpeed float64
	forks    int
	runner   string
	onFinish func(job Job)

	mu      sync.Mutex
	runs    map[string]*run
	running *run
}

// run is the in-memory state of one job.
type run struct {
	job      Job
	plan     *actions.Plan
	bus      *EventBus
	sanitize *Sanitizer

	mu       sync.Mutex
	canceled bool
	pid      int
	unit     string
	runner   string
	step     int

	log       io.Writer
	done      chan struct{}
	stop      context.CancelFunc
	cancelCh  chan struct{}
	cancelOne sync.Once
	closed    bool
}

// ErrJobNotFound is returned for an unknown job id.
var ErrJobNotFound = errors.New("job not found")

// ConflictError reports that another job holds the global job lock.
type ConflictError struct {
	RunningJobID string
	Detail       string
}

// Error implements the error interface.
func (e *ConflictError) Error() string { return e.Detail }

// BlockedError reports a request rejected by its preconditions or its gate.
type BlockedError struct {
	Issues []validate.Issue
	Detail string
}

// Error implements the error interface.
func (e *BlockedError) Error() string { return e.Detail }

// New builds the job manager.
func New(opts Options) *Manager {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	dir := opts.JobsDir
	if dir == "" {
		dir = filepath.Join(opts.DataDir, "jobs")
	}
	forks := opts.Forks
	if forks <= 0 {
		forks = 5
	}
	runner := "process"
	if !opts.Simulate {
		runner = detectRunner()
	}
	speed := opts.SimulateSpeed
	if speed <= 0 {
		speed = 1
	}
	return &Manager{
		home:     opts.Home,
		jobsDir:  dir,
		logger:   logger,
		store:    opts.Store,
		planner:  opts.Planner,
		simulate: opts.Simulate,
		simSpeed: speed,
		forks:    forks,
		runner:   runner,
		onFinish: opts.OnFinish,
		runs:     map[string]*run{},
	}
}

// Submit validates a request, records the job and starts it asynchronously.
func (m *Manager) Submit(ctx context.Context, req SubmitRequest) (Job, error) {
	if m.planner == nil {
		return Job{}, fmt.Errorf("动作计划器未初始化")
	}
	computed, err := m.planner.Plan(ctx, req.Action, req.Params, req.Check)
	if err != nil {
		return Job{}, err
	}
	if msg := computed.Blocked(); msg != "" {
		return Job{}, &BlockedError{Detail: msg, Issues: preconditionIssues(computed.Preconditions)}
	}
	if computed.Gate != nil {
		if err := checkConfirm(computed.Gate, req.Confirm); err != nil {
			return Job{}, err
		}
	}
	params := req.Params
	if params == nil {
		params = map[string]any{}
	}
	title := computed.Title
	if title == "" {
		title = req.Action
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if computed.Risk != plan.RiskReadonly && m.running != nil {
		id := m.running.job.ID
		return Job{}, &ConflictError{RunningJobID: id, Detail: "已有变更作业在运行，请等待它结束或先取消它"}
	}
	if computed.Cluster != nil {
		if id := m.lockedCluster(*computed.Cluster); id != "" {
			return Job{}, &ConflictError{RunningJobID: id,
				Detail: "集群 " + *computed.Cluster + " 正在进行原地 PITR 恢复，其他变更作业被拒绝"}
		}
	}

	id, err := m.nextJobID()
	if err != nil {
		return Job{}, err
	}
	snapshot, err := m.plannerSnapshot(ctx)
	if err != nil {
		return Job{}, err
	}
	job := Job{
		ID:          id,
		Action:      computed.Action,
		Title:       title,
		Params:      params,
		Check:       computed.Check,
		Risk:        computed.Risk,
		Status:      store.JobQueued,
		Commands:    append([]string{}, computed.Commands...),
		Cluster:     computed.Cluster,
		Targets:     append([]string{}, computed.Targets...),
		ConfigHash:  computed.ConfigHash,
		User:        req.User,
		CreatedAt:   time.Now().UTC(),
		Stats:       map[string]HostStats{},
		CurrentStep: 0,
	}
	if computed.Commit != "" {
		commit := computed.Commit
		job.ConfigCommit = &commit
	}
	if req.Confirm != nil {
		text := req.Confirm.Text
		job.ConfirmText = &text
	}
	rec, err := m.toRecord(job, computed)
	if err != nil {
		return Job{}, err
	}
	if err := m.store.InsertJob(rec); err != nil {
		return Job{}, err
	}
	r := &run{
		job:      job,
		plan:     computed,
		sanitize: NewSanitizer(snapshot),
		done:     make(chan struct{}),
		cancelCh: make(chan struct{}),
		runner:   "process",
	}
	bus, err := newEventBus(filepath.Join(m.jobDir(id), "events.jsonl"), 0)
	if err != nil {
		return Job{}, err
	}
	r.bus = bus
	m.runs[id] = r
	if computed.Risk != plan.RiskReadonly {
		m.running = r
	}
	m.logger.Info("job submitted", "id", id, "action", job.Action, "user", job.User, "risk", job.Risk, "check", job.Check)
	go m.execute(r)
	return m.copyJob(r), nil
}

// List returns the newest jobs first.
func (m *Manager) List(filter JobFilter) ([]Job, int, error) {
	records, total, err := m.store.ListJobs(store.JobFilter{
		Cluster: filter.Cluster, Status: filter.Status, Limit: filter.Limit, Offset: filter.Offset,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]Job, 0, len(records))
	for _, rec := range records {
		if r, ok := m.runOf(rec.ID); ok {
			out = append(out, m.copyJob(r))
			continue
		}
		job, err := fromRecord(rec)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, job)
	}
	return out, total, nil
}

// JobFilter selects jobs for the list endpoint.
type JobFilter struct {
	Cluster string
	Status  string
	Limit   int
	Offset  int
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, error) {
	if r, ok := m.runOf(id); ok {
		return m.copyJob(r), nil
	}
	rec, err := m.store.GetJob(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Job{}, ErrJobNotFound
		}
		return Job{}, err
	}
	return fromRecord(rec)
}

// Log returns the complete sanitized output of a job.
func (m *Manager) Log(id string) (string, error) {
	if _, err := m.Get(id); err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(m.jobDir(id), "stdout.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sanitizer := m.sanitizerOf(id)
	return sanitizer.Text(string(data)), nil
}

// Subscribe returns the already produced events plus a live channel.
func (m *Manager) Subscribe(id string, after int64) ([]JobEvent, <-chan JobEvent, func(), error) {
	r, err := m.runFor(id)
	if err != nil {
		return nil, nil, nil, err
	}
	history, ch, cancel := r.bus.Subscribe(after)
	return history, ch, cancel, nil
}

// Done returns a channel closed when the job reached a final state.
func (m *Manager) Done(id string) <-chan struct{} {
	if r, ok := m.runOf(id); ok {
		return r.done
	}
	closed := make(chan struct{})
	close(closed)
	return closed
}

// Current returns the newest state of one job without touching the database.
func (m *Manager) Current(id string) (Job, bool) {
	r, ok := m.runOf(id)
	if !ok {
		return Job{}, false
	}
	return m.copyJob(r), true
}

// Cancel interrupts a running job: SIGINT first, SIGTERM ten seconds later.
func (m *Manager) Cancel(ctx context.Context, id string) error {
	r, err := m.runFor(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.canceled = true
	pid, unit, runner := r.pid, r.unit, r.runner
	r.mu.Unlock()
	// Wake the simulated runner (and any waiting step) immediately.
	r.cancelOne.Do(func() { close(r.cancelCh) })

	if m.simulate {
		// The simulate mode has no child process: closing the cancel channel is
		// all that is needed.
		return nil
	}
	if r.job.Status == store.JobQueued && pid == 0 {
		// Not started yet: mark the intent, execute() will stop immediately.
		m.emit(r, JobEvent{Type: EventLog, Line: "作业在启动前被取消"})
		return nil
	}
	m.emit(r, JobEvent{Type: EventLog, Line: "收到取消请求：先发送 SIGINT，10 秒后如仍未退出则发送 SIGTERM"})
	go func() {
		if err := m.signal(pid, unit, runner, false); err != nil {
			m.logger.Warn("cancel job", "id", id, "err", err)
		}
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.done:
			return
		}
		if err := m.signal(pid, unit, runner, true); err != nil {
			m.logger.Warn("terminate job", "id", id, "err", err)
		}
	}()
	return nil
}

// Recover reattaches the running jobs of a previous console instance.
func (m *Manager) Recover() error {
	records, err := m.store.RunningJobs()
	if err != nil {
		return err
	}
	for _, rec := range records {
		job, err := fromRecord(rec)
		if err != nil {
			m.logger.Warn("skip broken job record", "id", rec.ID, "err", err)
			continue
		}
		dir := m.jobDir(job.ID)
		events, err := loadEvents(filepath.Join(dir, "events.jsonl"))
		if err != nil {
			m.logger.Warn("load job events", "id", job.ID, "err", err)
		}
		var lastSeq int64
		if len(events) > 0 {
			lastSeq = events[len(events)-1].Seq
		}
		bus, err := newEventBus(filepath.Join(dir, "events.jsonl"), lastSeq)
		if err != nil {
			m.logger.Warn("reopen job events", "id", job.ID, "err", err)
			continue
		}
		bus.events = events
		state, _ := readState(dir)
		source, _ := os.ReadFile(m.configFile())
		r := &run{
			job:      job,
			bus:      bus,
			sanitize: NewSanitizer(string(source)),
			done:     make(chan struct{}),
			cancelCh: make(chan struct{}),
			pid:      state.PID,
			unit:     state.Unit,
			runner:   state.Runner,
			step:     state.Step,
		}
		if data, err := os.ReadFile(filepath.Join(dir, "plan.json")); err == nil {
			var stored storedSteps
			if err := json.Unmarshal(data, &stored); err == nil {
				r.plan = stored.toPlan(job)
			}
		}
		m.mu.Lock()
		m.runs[job.ID] = r
		if job.Risk != plan.RiskReadonly && m.running == nil {
			m.running = r
		}
		m.mu.Unlock()
		m.logger.Info("reattaching job", "id", job.ID, "status", job.Status, "runner", r.runner, "pid", r.pid)
		go m.resume(r, state)
	}
	return nil
}

// Shutdown stops the tailers of this instance without touching the jobs.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	runs := make([]*run, 0, len(m.runs))
	for _, r := range m.runs {
		runs = append(runs, r)
	}
	m.mu.Unlock()
	for _, r := range runs {
		if r.stop != nil {
			r.stop()
		}
	}
}

// execute runs every step of a job in order.
func (m *Manager) execute(r *run) {
	defer close(r.done)
	defer r.bus.Close()

	dir := m.jobDir(r.job.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		m.fail(r, "无法创建作业目录："+err.Error())
		return
	}
	if r.plan == nil {
		m.fail(r, "作业计划缺失")
		return
	}
	if err := writeJSON(filepath.Join(dir, "plan.json"), storedStepsOf(r.plan)); err != nil {
		m.logger.Warn("persist job plan", "id", r.job.ID, "err", err)
	}
	state, err := readState(dir)
	if err != nil {
		state = jobState{Runner: m.runnerName()}
	}
	state.Runner = m.runnerName()
	writeState(dir, state)

	tailCtx, tailCancel := context.WithCancel(context.Background())
	runCtx, runCancel := context.WithCancel(context.Background())
	r.stop = tailCancel
	tail := m.startTail(tailCtx, r, dir, state)

	m.setStatus(r, store.JobRunning, nil)

	// Risky jobs create a restore point before anything else runs, in the
	// simulate mode as well, so the UI can show the rollback entry point.
	if err := m.createAutoRestorePoint(runCtx, r); err != nil {
		m.emit(r, JobEvent{Type: EventLog, Line: "自动恢复点创建失败（仅记录警告）：" + err.Error()})
	}

	if m.simulate {
		logFile, err := os.OpenFile(filepath.Join(dir, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			m.logger.Warn("open simulated stdout log", "id", r.job.ID, "err", err)
		} else {
			defer logFile.Close()
		}
		m.runSimulated(runCtx, r, logFile)
		runCancel()
		tailCancel()
		tail.Wait()
		m.finalize(r)
		return
	}

	exitCode := 0
	for i, cmd := range r.plan.Steps {
		if r.isCanceled() {
			exitCode = -1
			m.emit(r, JobEvent{Type: EventLog, Line: "作业已取消，停止后续步骤"})
			break
		}
		step := i
		r.setStep(step)
		state.Step = step
		state.PID, state.Unit = 0, ""
		writeState(dir, state)
		m.emit(r, JobEvent{Type: EventStepStart, Step: &step, Msg: cmd.Display})
		code, err := m.runStep(runCtx, r, dir, step, cmd, &state)
		if err != nil {
			m.emit(r, JobEvent{Type: EventLog, Line: "步骤执行失败：" + err.Error()})
		}
		exitCode = code
		end := step
		m.emit(r, JobEvent{Type: EventStepEnd, Step: &end, ExitCode: &code})
		if code != 0 {
			break
		}
	}
	runCancel()
	tailCancel()
	tail.Wait()
	if r.currentJob().Status == store.JobRunning {
		r.setExit(exitCode)
	}
	m.finalize(r)
}

// createAutoRestorePoint creates the console restore point of a risky job.
func (m *Manager) createAutoRestorePoint(ctx context.Context, r *run) error {
	if !r.plan.RestorePt || r.job.Cluster == nil {
		return nil
	}
	name := "console_job_" + r.job.ID
	m.emit(r, JobEvent{Type: EventLog, Line: "高风险作业：先在主库创建恢复点 " + name})
	lsn, err := m.restorePoint(ctx, r, name)
	if err != nil {
		return err
	}
	jobID := r.job.ID
	point := RestorePoint{Name: name, LSN: lsn, Time: time.Now().UTC(), JobID: &jobID, Cluster: *r.job.Cluster}
	r.mu.Lock()
	r.job.RestorePoint = &point
	r.mu.Unlock()
	m.persist(r)
	// The restore point exists in PostgreSQL independently of the job outcome,
	// so it is recorded right away.
	if err := m.store.AddRestorePoint(store.RestorePointRecord{
		Name: point.Name, LSN: point.LSN, Time: point.Time, JobID: point.JobID, Cluster: point.Cluster,
	}); err != nil {
		m.logger.Warn("record automatic restore point", "id", r.job.ID, "err", err)
	}
	return nil
}

// restorePoint creates the restore point on the primary and records it.
func (m *Manager) restorePoint(_ context.Context, r *run, name string) (*string, error) {
	if m.simulate {
		lsn := fmt.Sprintf("0/%X%04X", 16+r.step, time.Now().UnixNano()%0xFFFF)
		return &lsn, nil
	}
	cmd, ok := m.restorePointCommand(r, name)
	if !ok {
		return nil, fmt.Errorf("无法确定主库地址")
	}
	out, err := m.execAdhoc(r, cmd)
	if err != nil {
		return nil, err
	}
	lsn := parseLSN(out)
	return lsn, nil
}

// finalize computes the terminal state of a job and persists it.
func (m *Manager) finalize(r *run) {
	r.mu.Lock()
	job := r.job
	r.closed = true
	r.mu.Unlock()

	switch {
	case r.isCanceled():
		job.Status = store.JobCanceled
		msg := "作业已被取消"
		job.Error = &msg
	case job.Status == store.JobFailed:
	case job.ExitCode != nil && *job.ExitCode != 0:
		job.Status = store.JobFailed
		if job.Error == nil {
			msg := fmt.Sprintf("命令退出码 %d", *job.ExitCode)
			job.Error = &msg
		}
	default:
		job.Status = store.JobSuccess
	}
	now := time.Now().UTC()
	job.FinishedAt = &now
	r.mu.Lock()
	r.job = job
	r.mu.Unlock()
	m.emit(r, JobEvent{Type: EventStatus, Job: &job})
	m.persist(r)

	if job.Status == store.JobSuccess {
		m.recordSuccess(r)
	}
	if m.onFinish != nil {
		m.onFinish(job)
	}
	m.mu.Lock()
	if m.running == r {
		m.running = nil
	}
	m.mu.Unlock()
	m.logger.Info("job finished", "id", job.ID, "action", job.Action, "status", job.Status, "exit", exitText(job.ExitCode))
	// Jobs buffer a lot of short lived event data; returning it to the OS keeps
	// the idle footprint of the console small on a 1c1g admin node.
	debug.FreeOSMemory()
}

// recordSuccess writes the restore point and the applied version of a job.
func (m *Manager) recordSuccess(r *run) {
	job := r.currentJob()
	if job.Action == "restore-point" {
		name, _ := job.Params["name"].(string)
		if name != "" && job.Cluster != nil {
			point := store.RestorePointRecord{Name: name, Time: job.FinishedAtOrNow(), JobID: &job.ID, Cluster: *job.Cluster}
			point.LSN = parseLSN(m.stdoutOf(job.ID))
			if point.LSN == nil && m.simulate {
				// The simulate mode has no psql output, so it reports a stable
				// synthetic LSN instead of leaving the field empty.
				fake := fmt.Sprintf("0/%X", 0x1000000+seedOf(job.ID)%0xFFFF)
				point.LSN = &fake
			}
			if err := m.store.AddRestorePoint(point); err != nil {
				m.logger.Warn("record restore point", "id", job.ID, "err", err)
			}
		}
	}
	if job.Check || job.Cluster == nil {
		return
	}
	commit := ""
	if job.ConfigCommit != nil {
		commit = *job.ConfigCommit
	} else {
		commit = job.ConfigHash
	}
	if err := m.store.AddApplied(store.AppliedRow{
		Cluster: *job.Cluster, Action: job.Action, Commit: commit, JobID: &job.ID, At: job.FinishedAtOrNow(),
	}); err != nil {
		m.logger.Warn("record applied version", "id", job.ID, "err", err)
	}
}

// fail moves a job into the failed state.
func (m *Manager) fail(r *run, message string) {
	r.mu.Lock()
	code := 1
	r.job.Status = store.JobFailed
	r.job.ExitCode = &code
	r.job.Error = &message
	now := time.Now().UTC()
	r.job.FinishedAt = &now
	r.closed = true
	r.mu.Unlock()
	m.emit(r, JobEvent{Type: EventLog, Line: message})
	m.emit(r, JobEvent{Type: EventStatus})
	m.persist(r)
	if m.onFinish != nil {
		m.onFinish(r.currentJob())
	}
	m.mu.Lock()
	if m.running == r {
		m.running = nil
	}
	m.mu.Unlock()
}

// setStatus updates and broadcasts the job status.
func (m *Manager) setStatus(r *run, status string, err error) {
	r.mu.Lock()
	if status == store.JobRunning && r.job.StartedAt == nil {
		now := time.Now().UTC()
		r.job.StartedAt = &now
	}
	r.job.Status = status
	if err != nil {
		msg := err.Error()
		r.job.Error = &msg
	}
	job := r.job
	r.mu.Unlock()
	m.emit(r, JobEvent{Type: EventStatus, Job: &job})
	m.persist(r)
}

// emit appends one sanitized event to the stream.
func (m *Manager) emit(r *run, ev JobEvent) JobEvent {
	if ev.Line != "" {
		ev.Line = r.sanitize.Text(ev.Line)
	}
	if ev.Msg != "" {
		ev.Msg = r.sanitize.Text(ev.Msg)
	}
	if ev.Job != nil {
		ev.Job = r.sanitizeJob(ev.Job)
	}
	return r.bus.Append(ev)
}

// sanitizeJob masks the free text of a job snapshot.
func (r *run) sanitizeJob(job *Job) *Job {
	if r.sanitize == nil || job == nil {
		return job
	}
	out := *job
	if job.Error != nil {
		msg := r.sanitize.Text(*job.Error)
		out.Error = &msg
	}
	commands := make([]string, 0, len(job.Commands))
	for _, cmd := range job.Commands {
		commands = append(commands, r.sanitize.Text(cmd))
	}
	out.Commands = commands
	return &out
}

// persist writes the current job state into SQLite.
func (m *Manager) persist(r *run) {
	rec, err := m.toRecord(r.currentJob(), r.plan)
	if err != nil {
		m.logger.Warn("encode job", "id", r.job.ID, "err", err)
		return
	}
	if err := m.store.SaveJob(rec); err != nil {
		m.logger.Warn("persist job", "id", r.job.ID, "err", err)
	}
}

// nextJobID allocates the short id of a new job: YYYYMMDD-NNNN.
func (m *Manager) nextJobID() (string, error) {
	day := time.Now().Format("20060102")
	count, err := m.store.CountJobsWithPrefix(day)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%04d", day, count+1), nil
}

// lockedCluster returns the job id that currently locks a cluster.
func (m *Manager) lockedCluster(cluster string) string {
	if m.running == nil {
		return ""
	}
	job := m.running.currentJob()
	if job.Action != "pitr-inplace" || job.Cluster == nil || *job.Cluster != cluster {
		return ""
	}
	return job.ID
}

// runOf returns the in-memory state of a job.
func (m *Manager) runOf(id string) (*run, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	return r, ok
}

// runFor returns the in-memory state of a job, loading it from SQLite when it
// belongs to a previous console instance.
func (m *Manager) runFor(id string) (*run, error) {
	if r, ok := m.runOf(id); ok {
		return r, nil
	}
	rec, err := m.store.GetJob(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	job, err := fromRecord(rec)
	if err != nil {
		return nil, err
	}
	events, err := loadEvents(filepath.Join(m.jobDir(id), "events.jsonl"))
	if err != nil {
		return nil, err
	}
	var lastSeq int64
	if len(events) > 0 {
		lastSeq = events[len(events)-1].Seq
	}
	bus, err := newEventBus(filepath.Join(m.jobDir(id), "events.jsonl"), lastSeq)
	if err != nil {
		return nil, err
	}
	bus.events = events
	r := &run{job: job, bus: bus, sanitize: m.sanitizerOf(id), done: make(chan struct{}), cancelCh: make(chan struct{})}
	close(r.done)
	return r, nil
}

// sanitizerOf builds a sanitizer from the current configuration.
func (m *Manager) sanitizerOf(id string) *Sanitizer {
	source, err := os.ReadFile(m.configFile())
	if err != nil {
		return NewSanitizer("")
	}
	_ = id
	return NewSanitizer(string(source))
}

// stdoutOf reads the raw stdout of a job.
func (m *Manager) stdoutOf(id string) string {
	data, err := os.ReadFile(filepath.Join(m.jobDir(id), "stdout.log"))
	if err != nil {
		return ""
	}
	return string(data)
}

// copyJob returns a deep copy of the current job state.
func (m *Manager) copyJob(r *run) Job {
	job := r.currentJob()
	out := job
	out.Commands = append([]string{}, job.Commands...)
	out.Targets = append([]string{}, job.Targets...)
	out.Stats = map[string]HostStats{}
	for host, stats := range job.Stats {
		out.Stats[host] = stats
	}
	if job.Params != nil {
		params := make(map[string]any, len(job.Params))
		for k, v := range job.Params {
			params[k] = v
		}
		out.Params = params
	}
	return out
}

// currentJob returns a snapshot of the job state guarded by the run lock.
func (r *run) currentJob() Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job
}

// isCanceled reports whether a cancel request arrived.
func (r *run) isCanceled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canceled
}

// setStep records the currently running step.
func (r *run) setStep(step int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.step = step
	r.job.CurrentStep = step
}

// setExit records the final exit code of a job.
func (r *run) setExit(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.job.ExitCode = &code
}

// addStats merges the ansible statistics of one playbook run.
func (r *run) addStats(hosts map[string]HostStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job.Stats == nil {
		r.job.Stats = map[string]HostStats{}
	}
	for host, stats := range hosts {
		r.job.Stats[host] = stats
	}
}

// FinishedAtOrNow returns the finish time of a job.
func (j Job) FinishedAtOrNow() time.Time {
	if j.FinishedAt != nil {
		return *j.FinishedAt
	}
	return time.Now().UTC()
}

// exitText renders an optional exit code.
func exitText(code *int) string {
	if code == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *code)
}

// preconditionIssues converts unmet preconditions into validation issues.
func preconditionIssues(conds []plan.Precondition) []validate.Issue {
	out := []validate.Issue{}
	for _, c := range conds {
		if c.OK {
			continue
		}
		level := "warning"
		if c.Level == plan.LevelError {
			level = "error"
		}
		out = append(out, validate.Issue{Level: level, Source: "semantic", Message: c.Message, Path: c.ID})
	}
	return out
}

// checkConfirm verifies the gate confirmation of a risky job.
func checkConfirm(gate *plan.Gate, confirm *Confirm) error {
	issues := []validate.Issue{}
	if confirm == nil {
		issues = append(issues, validate.Issue{Level: "error", Source: "semantic", Path: "confirm.text",
			Message: "该动作需要输入 " + gate.ConfirmText + " 才能执行"})
	} else {
		if strings.TrimSpace(confirm.Text) != gate.ConfirmText {
			issues = append(issues, validate.Issue{Level: "error", Source: "semantic", Path: "confirm.text",
				Message: "确认文本不匹配，需要原样输入 " + gate.ConfirmText})
		}
		have := map[string]bool{}
		for _, item := range confirm.Checkboxes {
			have[item] = true
		}
		for _, want := range gate.Checkboxes {
			if !have[want] {
				issues = append(issues, validate.Issue{Level: "error", Source: "semantic", Path: "confirm.checkboxes",
					Message: "请勾选确认项：" + want})
			}
		}
	}
	if len(issues) > 0 {
		return &BlockedError{Issues: issues, Detail: issues[0].Message}
	}
	return nil
}

// toRecord converts a job into its storage shape.
func (m *Manager) toRecord(job Job, computed *actions.Plan) (store.JobRecord, error) {
	params, err := json.Marshal(job.Params)
	if err != nil {
		return store.JobRecord{}, err
	}
	commands, err := json.Marshal(job.Commands)
	if err != nil {
		return store.JobRecord{}, err
	}
	targets, err := json.Marshal(job.Targets)
	if err != nil {
		return store.JobRecord{}, err
	}
	stats, err := json.Marshal(job.Stats)
	if err != nil {
		return store.JobRecord{}, err
	}
	var restorePoint *string
	if job.RestorePoint != nil {
		data, err := json.Marshal(job.RestorePoint)
		if err != nil {
			return store.JobRecord{}, err
		}
		text := string(data)
		restorePoint = &text
	}
	planJSON := "[]"
	if computed != nil {
		data, err := json.Marshal(storedStepsOf(computed))
		if err != nil {
			return store.JobRecord{}, err
		}
		planJSON = string(data)
	}
	return store.JobRecord{
		ID: job.ID, Action: job.Action, Title: job.Title, Params: string(params), Check: job.Check,
		Risk: string(job.Risk), Status: job.Status, Plan: planJSON, Commands: string(commands),
		Cluster: job.Cluster, Targets: string(targets), ConfigHash: job.ConfigHash,
		ConfigCommit: job.ConfigCommit, User: job.User, ConfirmText: job.ConfirmText,
		CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		ExitCode: job.ExitCode, CurrentStep: job.CurrentStep, Stats: string(stats),
		RestorePoint: restorePoint, Error: job.Error, Runner: m.runnerName(),
	}, nil
}

// fromRecord converts a storage record back into a job.
func fromRecord(rec store.JobRecord) (Job, error) {
	job := Job{
		ID: rec.ID, Action: rec.Action, Title: rec.Title, Check: rec.Check, Risk: plan.Risk(rec.Risk),
		Status: rec.Status, Cluster: rec.Cluster, ConfigHash: rec.ConfigHash, ConfigCommit: rec.ConfigCommit,
		User: rec.User, ConfirmText: rec.ConfirmText, CreatedAt: rec.CreatedAt, StartedAt: rec.StartedAt,
		FinishedAt: rec.FinishedAt, ExitCode: rec.ExitCode, CurrentStep: rec.CurrentStep, Error: rec.Error,
		Params: map[string]any{}, Commands: []string{}, Targets: []string{}, Stats: map[string]HostStats{},
	}
	if rec.Params != "" {
		if err := json.Unmarshal([]byte(rec.Params), &job.Params); err != nil {
			return Job{}, fmt.Errorf("decode job params: %w", err)
		}
	}
	if rec.Commands != "" {
		if err := json.Unmarshal([]byte(rec.Commands), &job.Commands); err != nil {
			return Job{}, fmt.Errorf("decode job commands: %w", err)
		}
	}
	if rec.Targets != "" {
		if err := json.Unmarshal([]byte(rec.Targets), &job.Targets); err != nil {
			return Job{}, fmt.Errorf("decode job targets: %w", err)
		}
	}
	if rec.Stats != "" {
		if err := json.Unmarshal([]byte(rec.Stats), &job.Stats); err != nil {
			return Job{}, fmt.Errorf("decode job stats: %w", err)
		}
	}
	if rec.RestorePoint != nil {
		var point RestorePoint
		if err := json.Unmarshal([]byte(*rec.RestorePoint), &point); err == nil {
			job.RestorePoint = &point
		}
	}
	if job.Params == nil {
		job.Params = map[string]any{}
	}
	if job.Commands == nil {
		job.Commands = []string{}
	}
	if job.Targets == nil {
		job.Targets = []string{}
	}
	if job.Stats == nil {
		job.Stats = map[string]HostStats{}
	}
	return job, nil
}

// storedSteps is the persisted execution plan of a job.
type storedSteps struct {
	Action string            `json:"action"`
	Risk   string            `json:"risk"`
	Check  bool              `json:"check"`
	Steps  []actions.Command `json:"steps"`
}

// storedStepsOf builds the persisted plan of a job.
func storedStepsOf(p *actions.Plan) storedSteps {
	return storedSteps{Action: p.Action, Risk: string(p.Risk), Check: p.Check, Steps: p.Steps}
}

// toPlan rebuilds a plan from its persisted form.
func (s storedSteps) toPlan(job Job) *actions.Plan {
	return &actions.Plan{
		Action: s.Action, Risk: plan.Risk(s.Risk), Check: s.Check, Steps: s.Steps,
		Cluster: job.Cluster, Commands: job.Commands,
	}
}
