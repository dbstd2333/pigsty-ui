package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// jobState is the persisted runtime state of one job; it lets a restarted
// console find the running process and continue tailing where it stopped.
type jobState struct {
	Step          int    `json:"step"`
	PID           int    `json:"pid"`
	Unit          string `json:"unit"`
	Runner        string `json:"runner"`
	AnsibleOffset int64  `json:"ansibleOffset"`
	StdoutOffset  int64  `json:"stdoutOffset"`
}

// jobDir returns the data directory of one job.
func (m *Manager) jobDir(id string) string { return filepath.Join(m.jobsDir, id) }

// configFile returns the tracked pigsty.yml path.
func (m *Manager) configFile() string { return filepath.Join(m.home, "pigsty.yml") }

// runnerName reports how steps are launched: systemd-run when a user manager is
// available, otherwise a detached process group.
func (m *Manager) runnerName() string {
	if m.simulate {
		return "simulate"
	}
	return m.runner
}

// detectRunner picks the process launcher of this host.
func detectRunner() string {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return "process"
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "process"
	}
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		return "process"
	}
	return "systemd"
}

// plannerSnapshot reads the current configuration, which supplies the secret
// values the sanitizer masks. A missing file is not fatal: the plan was already
// computed against the submitted inventory.
func (m *Manager) plannerSnapshot(_ context.Context) (string, error) {
	data, err := os.ReadFile(m.configFile())
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read pigsty.yml: %w", err)
	}
	return string(data), nil
}

// readState loads the persisted job state, tolerating a missing file.
func readState(dir string) (jobState, error) {
	var state jobState
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return jobState{}, err
	}
	return state, nil
}

// writeState stores the runtime state of a job.
func writeState(dir string, state jobState) error {
	return writeJSON(filepath.Join(dir, "state.json"), state)
}

// writeJSON writes one JSON document to disk.
func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// stepEnv builds the environment of one ansible step.
func (m *Manager) stepEnv(r *run, dir string, cmd actions.Command) []string {
	env := []string{
		"ANSIBLE_FORCE_COLOR=0",
		"ANSIBLE_FORKS=" + strconv.Itoa(m.forks),
		"ANSIBLE_CALLBACK_PLUGINS=" + filepath.Join(m.ansibleDir(), "callback_plugins"),
		"ANSIBLE_CALLBACKS_ENABLED=pigsty_events",
		"PIGSTY_EVENTS_FILE=" + filepath.Join(dir, "ansible.ndjson"),
	}
	if cmd.Kind == "playbook" || cmd.Kind == "pitr" {
		env = append(env, "ANSIBLE_STDOUT_CALLBACK=pigsty_events")
	}
	return env
}

// ansibleDir is where the callback plugin is released.
func (m *Manager) ansibleDir() string { return filepath.Join(filepath.Dir(m.jobsDir), "ansible") }

// runStep launches one step and waits for its exit code.
func (m *Manager) runStep(ctx context.Context, r *run, dir string, step int, cmd actions.Command, state *jobState) (int, error) {
	self, err := os.Executable()
	if err != nil {
		return 1, fmt.Errorf("无法定位 console 可执行文件: %w", err)
	}
	exitPath := filepath.Join(dir, fmt.Sprintf("step-%d.exit", step))
	os.Remove(exitPath)
	args := []string{"exec-step", "--exit-file", exitPath, "--log-file", filepath.Join(dir, "stdout.log"), "--"}
	args = append(args, cmd.Argv...)
	env := m.stepEnv(r, dir, cmd)

	if m.runner == "systemd" {
		unit := fmt.Sprintf("pigsty-job-%s-%d", strings.ReplaceAll(r.job.ID, "/", "-"), step)
		argv := []string{"systemd-run", "--user", "--unit=" + unit, "--collect", "--working-directory=" + m.home}
		for _, kv := range env {
			argv = append(argv, "--setenv="+kv)
		}
		argv = append(argv, self)
		argv = append(argv, args...)
		out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
		if err != nil {
			return 1, fmt.Errorf("systemd-run 启动失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
		r.mu.Lock()
		r.unit, r.runner, r.pid = unit, "systemd", 0
		r.mu.Unlock()
		state.Unit, state.PID = unit, 0
	} else {
		proc := exec.Command(self, args...)
		proc.Dir = m.home
		proc.Env = append(os.Environ(), env...)
		// A new process group lets the console cancel ansible and all of its
		// children with one signal, and keeps them alive across a restart.
		proc.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := proc.Start(); err != nil {
			return 1, fmt.Errorf("启动步骤失败: %w", err)
		}
		pid := proc.Process.Pid
		go func() { _ = proc.Wait() }()
		r.mu.Lock()
		r.pid, r.runner, r.unit = pid, "process", ""
		r.mu.Unlock()
		state.PID, state.Unit = pid, ""
	}
	writeState(dir, *state)
	return m.waitStep(ctx, r, exitPath)
}

// waitStep polls until the step wrote its exit code or its process vanished.
func (m *Manager) waitStep(ctx context.Context, r *run, exitPath string) (int, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.Now().Add(8 * time.Hour)
	for {
		select {
		case <-ctx.Done():
			return 1, ctx.Err()
		case <-ticker.C:
			if code, ok := readExitCode(exitPath); ok {
				return code, nil
			}
			if !m.alive(r) {
				return 1, fmt.Errorf("步骤进程已退出但未写出退出码")
			}
			if time.Now().After(deadline) {
				return 1, fmt.Errorf("等待步骤结束超时")
			}
		}
	}
}

// readExitCode reads the exit code written by the exec-step helper.
func readExitCode(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, false
	}
	code, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return code, true
}

// alive reports whether the current step process is still running.
func (m *Manager) alive(r *run) bool {
	r.mu.Lock()
	pid, unit, runner := r.pid, r.unit, r.runner
	r.mu.Unlock()
	if runner == "systemd" && unit != "" {
		out, err := exec.Command("systemctl", "--user", "is-active", unit).Output()
		if err != nil {
			return false
		}
		switch strings.TrimSpace(string(out)) {
		case "active", "activating", "reloading":
			return true
		default:
			return false
		}
	}
	if pid > 0 {
		err := syscall.Kill(pid, 0)
		return err == nil || errors.Is(err, syscall.EPERM)
	}
	return false
}

// signal delivers SIGINT (force=false) or SIGTERM (force=true) to a step.
func (m *Manager) signal(pid int, unit, runner string, force bool) error {
	sig := syscall.SIGINT
	if force {
		sig = syscall.SIGTERM
	}
	if runner == "systemd" && unit != "" {
		name := "SIGINT"
		if force {
			name = "SIGTERM"
		}
		out, err := exec.Command("systemctl", "--user", "kill", "--signal="+name, unit).CombinedOutput()
		if err != nil {
			return fmt.Errorf("systemctl kill: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if pid <= 0 {
		return fmt.Errorf("作业没有可用的进程号")
	}
	// The negative pid targets the whole process group of the step.
	if err := syscall.Kill(-pid, sig); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}

// exitCodeOf converts a command error into an exit code.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}

// lsnPattern matches a PostgreSQL log sequence number.
var lsnPattern = regexp.MustCompile(`[0-9A-Fa-f]{1,8}/[0-9A-Fa-f]{1,8}`)

// parseLSN extracts the LSN of a psql result.
func parseLSN(text string) *string {
	match := lsnPattern.FindString(text)
	if match == "" {
		return nil
	}
	out := strings.ToUpper(match)
	return &out
}

// restorePointCommand builds the restore point ad-hoc command of a risky job.
func (m *Manager) restorePointCommand(r *run, name string) ([]string, bool) {
	if r.plan == nil || len(r.plan.Steps) == 0 {
		return nil, false
	}
	// Reuse the rendering rules of the restore-point action: the primary of the
	// cluster plus the dbsu become the target of the ad-hoc command.
	for _, step := range r.plan.Steps {
		if step.Kind != "adhoc" || len(step.Hosts) == 0 {
			continue
		}
		host := step.Hosts[0]
		dbsu := "postgres"
		if len(step.Argv) > 0 {
			for _, arg := range step.Argv {
				if strings.HasPrefix(arg, "--become-user=") {
					dbsu = strings.TrimPrefix(arg, "--become-user=")
				}
			}
		}
		command := fmt.Sprintf("psql -c \"SELECT pg_create_restore_point('%s')\"", name)
		argv := []string{"ansible", host, "-i", "pigsty.yml", "-b", "--become-user=" + dbsu, "-m", "shell", "-a", command}
		return argv, true
	}
	return nil, false
}

// execAdhoc runs one short ansible ad-hoc command and returns its output. It is
// used for the automatic restore points, which must complete before ansible is
// started.
func (m *Manager) execAdhoc(r *run, argv []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = m.home
	cmd.Env = append(os.Environ(),
		"ANSIBLE_FORCE_COLOR=0",
		"ANSIBLE_CALLBACK_PLUGINS="+filepath.Join(m.ansibleDir(), "callback_plugins"),
		"ANSIBLE_CALLBACKS_ENABLED=pigsty_events",
		"PIGSTY_EVENTS_FILE="+filepath.Join(m.jobDir(r.job.ID), "ansible.ndjson"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("%v: %s", err, lastLine(string(out)))
	}
	return string(out), nil
}

// resume reattaches a job that is still running after a console restart.
func (m *Manager) resume(r *run, state jobState) {
	defer close(r.done)
	dir := m.jobDir(r.job.ID)
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	tail := m.startTail(ctx, r, dir, state)

	// The plan of a restarted job comes from plan.json; without it the console
	// cannot know what is still running.
	if r.plan == nil || len(r.plan.Steps) == 0 {
		m.emit(r, JobEvent{Type: EventLog, Line: "无法恢复作业计划，按中断处理"})
		cancel()
		tail.Wait()
		m.fail(r, "console 重启后无法恢复该作业，请检查集群状态")
		return
	}
	if _, err := m.plannerSnapshot(context.Background()); err != nil {
		m.logger.Warn("resume job config", "id", r.job.ID, "err", err)
	}
	for step := state.Step; step < len(r.plan.Steps); step++ {
		exitPath := filepath.Join(dir, fmt.Sprintf("step-%d.exit", step))
		code, ok := readExitCode(exitPath)
		if !ok {
			// The step never finished: wait for it while the process is alive.
			code, _ = m.waitStep(ctx, r, exitPath)
		}
		r.setStep(step)
		if code != 0 {
			m.setStatus(r, store.JobFailed, fmt.Errorf("第 %d 步退出码 %d", step+1, code))
			break
		}
		state.Step = step + 1
		state.PID, state.Unit = 0, ""
		writeState(dir, state)
	}
	cancel()
	tail.Wait()
	if r.currentJob().Status == store.JobRunning {
		r.setExit(0)
	}
	m.finalize(r)
}

// lastLine returns the last non-empty line of a command output.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}
