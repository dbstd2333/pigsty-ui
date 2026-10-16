package jobs

import (
	"context"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/actions"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// Simulation timing bounds: one task takes 0.35 - 1.5 seconds, and a job has
// three to five plays with three to five tasks each, so a run lasts roughly
// 10 - 30 seconds. node-add, pgsql-create and the PITR actions take longer.
const (
	minTaskDelay = 350 * time.Millisecond
	maxTaskDelay = 1500 * time.Millisecond
)

// runSimulated plays a realistic ansible event stream without touching any
// host, so the UI can be developed on a machine without ansible or Pigsty. The
// same text is appended to stdout.log, which keeps /jobs/{id}/log meaningful.
func (m *Manager) runSimulated(ctx context.Context, r *run, logFile io.Writer) {
	r.log = logFile
	rng := rand.New(rand.NewSource(int64(seedOf(r.job.ID))))
	hosts := r.job.Targets
	if len(hosts) == 0 {
		hosts = []string{"127.0.0.1"}
	}
	fail := wantsFailure(r.job.Params)
	steps := r.plan.Steps
	if len(steps) == 0 {
		steps = []actions.Command{{Display: r.job.Action, Kind: "playbook", Hosts: hosts}}
	}
	for i, step := range steps {
		if m.simulatedCanceled(ctx, r) {
			return
		}
		stepIndex := i
		r.setStep(stepIndex)
		m.emit(r, JobEvent{Type: EventStepStart, Step: &stepIndex, Msg: step.Display})
		m.logSimulated(r, "PLAY ["+step.Display+"] ***")
		code := m.simulateStep(ctx, r, rng, step, hosts, fail && i == len(steps)-1)
		end := stepIndex
		m.emit(r, JobEvent{Type: EventStepEnd, Step: &end, ExitCode: &code})
		if code != 0 {
			r.setExit(code)
			r.mu.Lock()
			r.job.Status = store.JobFailed
			msg := "最后一个任务执行失败（模拟）"
			r.job.Error = &msg
			r.mu.Unlock()
			return
		}
	}
	r.setExit(0)
}

// simulateStep plays the tasks of one command across its target hosts.
func (m *Manager) simulateStep(ctx context.Context, r *run, rng *rand.Rand, step actions.Command, hosts []string, fail bool) int {
	stepHosts := step.Hosts
	if len(stepHosts) == 0 {
		stepHosts = hosts
	}
	plays := 3 + rng.Intn(3)
	if longAction(r.job.Action) {
		plays += 2
	}
	playbook := step.Playbook
	if playbook == "" {
		playbook = r.job.Action
	}
	m.emit(r, JobEvent{Type: EventPlaybookStart, Playbook: playbook})
	stats := map[string]HostStats{}
	for _, host := range stepHosts {
		stats[host] = HostStats{}
	}
	for play := 0; play < plays; play++ {
		playName := playTitle(r.job.Action, play)
		m.emit(r, JobEvent{Type: EventPlayStart, Play: playName, Playbook: playbook})
		m.logSimulated(r, "PLAY ["+playName+"] ***")
		tasks := 3 + rng.Intn(3)
		for task := 0; task < tasks; task++ {
			if m.simulatedCanceled(ctx, r) {
				return -1
			}
			lastTask := fail && play == plays-1 && task == tasks-1
			taskName := taskTitle(r.job.Action, playName, task, play, tasks)
			taskID := fmt.Sprintf("%s-%d-%d", taskIDOf(playName), play, task)
			m.emit(r, JobEvent{Type: EventTaskStart, Play: playName, Task: taskName, TaskID: taskID, Playbook: playbook})
			for _, host := range stepHosts {
				if m.simulatedCanceled(ctx, r) {
					return -1
				}
				if err := m.sleep(ctx, r, taskDelay(rng, r.job.Action)); err != nil {
					m.markCanceled(r)
					return -1
				}
				status, msg, duration := taskOutcome(rng, r.job.Check, lastTask)
				if status == "skipped" {
					m.logSimulated(r, fmt.Sprintf("skipping: [%s] => {\"changed\": false, \"skip_reason\": \"Conditional result was False\"}", host))
				}
				m.emit(r, JobEvent{
					Type: EventRunnerResult, Play: playName, Task: taskName, TaskID: taskID, Host: host,
					Status: status, Msg: msg, DurationMs: &duration, Playbook: playbook,
				})
				recordStats(stats, host, status)
				if msg != "" {
					m.logSimulated(r, fmt.Sprintf("changed: [%s] => %s", host, msg))
				}
				if status == "failed" {
					m.logSimulated(r, "PLAY RECAP *********************************************************************")
					m.logSimulated(r, fmt.Sprintf("%s : ok=%d changed=%d unreachable=%d failed=%d skipped=%d",
						host, stats[host].OK, stats[host].Changed, stats[host].Unreachable, stats[host].Failed, stats[host].Skipped))
					r.addStats(stats)
					m.emit(r, JobEvent{Type: EventStats, Hosts: stats})
					return 2
				}
			}
		}
		if !r.job.Check {
			m.logSimulated(r, "TASK ["+playName+"] *****************************************")
		}
	}
	r.addStats(stats)
	m.emit(r, JobEvent{Type: EventStats, Hosts: stats, Playbook: playbook})
	m.logSimulated(r, "PLAY RECAP *********************************************************************")
	for _, host := range stepHosts {
		hostStats := stats[host]
		m.logSimulated(r, fmt.Sprintf("%s : ok=%d changed=%d unreachable=%d failed=%d skipped=%d",
			host, hostStats.OK, hostStats.Changed, hostStats.Unreachable, hostStats.Failed, hostStats.Skipped))
	}
	return 0
}

// logSimulated emits one log event and appends the masked line to stdout.log.
func (m *Manager) logSimulated(r *run, line string) {
	event := m.emit(r, JobEvent{Type: EventLog, Line: line})
	if r.log == nil || event.Line == "" {
		return
	}
	fmt.Fprintln(r.log, event.Line)
}

// simulatedCanceled reports whether the job was cancelled.
func (m *Manager) simulatedCanceled(ctx context.Context, r *run) bool {
	select {
	case <-r.cancelCh:
		m.markCanceled(r)
		return true
	default:
	}
	if r.isCanceled() {
		m.emit(r, JobEvent{Type: EventLog, Line: "作业已取消，等待当前任务结束"})
		m.markCanceled(r)
		return true
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// markCanceled flags the job as canceled.
func (m *Manager) markCanceled(r *run) {
	r.mu.Lock()
	r.canceled = true
	r.mu.Unlock()
}

// sleep waits for a delay or the cancellation of the job.
func (m *Manager) sleep(ctx context.Context, r *run, d time.Duration) error {
	timer := time.NewTimer(m.scaled(d))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.cancelCh:
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

// scaled applies the simulate speed factor to one delay.
func (m *Manager) scaled(d time.Duration) time.Duration {
	if m.simSpeed <= 0 || m.simSpeed == 1 {
		return d
	}
	scaled := time.Duration(float64(d) / m.simSpeed)
	if scaled < time.Millisecond {
		scaled = time.Millisecond
	}
	return scaled
}

// taskDelay returns the duration of one simulated task.
func taskDelay(rng *rand.Rand, action string) time.Duration {
	span := maxTaskDelay - minTaskDelay
	delay := minTaskDelay + time.Duration(rng.Int63n(int64(span)))
	if longAction(action) {
		delay += span / 2
	}
	return delay
}

// longAction reports whether an action is markedly slower than the average.
func longAction(action string) bool {
	switch action {
	case "node-add", "pitr-clone", "pitr-inplace", "pitr-promote", "pgsql-create", "pgsql-add-replica":
		return true
	default:
		return false
	}
}

// taskOutcome picks the result of one simulated task.
func taskOutcome(rng *rand.Rand, check, last bool) (string, string, int64) {
	duration := 120 + rng.Int63n(900)
	if last {
		return "failed", "模拟失败：命令返回非零退出码（params.fail=true）", duration
	}
	roll := rng.Intn(100)
	switch {
	case roll < 12:
		return "skipped", "", duration
	case check:
		return "ok", "", duration
	case roll < 45:
		return "changed", "配置已更新", duration
	default:
		return "ok", "", duration
	}
}

// recordStats accumulates one task result.
func recordStats(stats map[string]HostStats, host, status string) {
	current := stats[host]
	switch status {
	case "ok":
		current.OK++
	case "changed":
		current.Changed++
	case "failed":
		current.Failed++
	case "unreachable":
		current.Unreachable++
	case "skipped":
		current.Skipped++
	}
	stats[host] = current
}

// playTitle renders a plausible play name.
func playTitle(action string, index int) string {
	switch action {
	case "pgsql-user":
		return []string{"PGSQL USER", "PGSQL PGBOUNCER", "PGSQL MONITOR"}[index%3]
	case "pgsql-db":
		return []string{"PGSQL DATABASE", "PGSQL PGBOUNCER", "PGSQL MONITOR"}[index%3]
	case "backup-run", "backup-check", "restore-point":
		return []string{"PGBACKREST", "PGBACKREST VERIFY", "PGBACKREST LOG"}[index%3]
	default:
		return []string{"PGSQL", "NODE", "PGSQL MONITOR", "INFRA", "PGSQL SERVICE"}[index%5]
	}
}

// taskTitle renders a plausible task name.
func taskTitle(action, play string, task, playIndex, tasks int) string {
	switch {
	case task == 0:
		return play + " : gather node identity"
	case task == tasks-1:
		return play + " : reload service"
	default:
		return fmt.Sprintf("%s : render config (%d)", play, task)
	}
}

// taskIDOf derives a stable task id from a play name.
func taskIDOf(play string) string {
	h := fnv.New32a()
	h.Write([]byte(play))
	return fmt.Sprintf("%08x", h.Sum32())
}

// seedOf derives a stable seed from a name.
func seedOf(v string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(v))
	return h.Sum64()
}

// wantsFailure reports whether the request asked for a simulated failure.
func wantsFailure(params map[string]any) bool {
	switch v := params["fail"].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	default:
		return false
	}
}
