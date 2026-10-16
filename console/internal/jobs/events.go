package jobs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// HostStats summarizes the task results of one host.
type HostStats struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Failed      int `json:"failed"`
	Unreachable int `json:"unreachable"`
	Skipped     int `json:"skipped"`
}

// JobEvent is one element of the job event stream (API contract type).
type JobEvent struct {
	Seq        int64                `json:"seq"`
	TS         time.Time            `json:"ts"`
	Type       string               `json:"type"`
	Step       *int                 `json:"step,omitempty"`
	ExitCode   *int                 `json:"exitCode,omitempty"`
	Playbook   string               `json:"playbook,omitempty"`
	Play       string               `json:"play,omitempty"`
	Task       string               `json:"task,omitempty"`
	TaskID     string               `json:"taskId,omitempty"`
	Host       string               `json:"host,omitempty"`
	Status     string               `json:"status,omitempty"`
	DurationMs *int64               `json:"durationMs,omitempty"`
	Msg        string               `json:"msg,omitempty"`
	Line       string               `json:"line,omitempty"`
	Hosts      map[string]HostStats `json:"hosts,omitempty"`
	Job        *Job                 `json:"job,omitempty"`
}

// Event types of the job stream.
const (
	EventStepStart     = "step_start"
	EventStepEnd       = "step_end"
	EventPlaybookStart = "playbook_start"
	EventPlayStart     = "play_start"
	EventTaskStart     = "task_start"
	EventRunnerResult  = "runner_result"
	EventStats         = "stats"
	EventLog           = "log"
	EventStatus        = "status"
	EventPlaybookEnd   = "playbook_end"
)

// subscriberBuffer is the queue depth of one SSE subscriber before events are
// dropped for that client (slow clients resync from the history on reconnect).
const subscriberBuffer = 512

// EventBus fans job events out to SSE subscribers and appends them to the job
// event file so a restarted console can continue the numbering.
type EventBus struct {
	path string

	mu     sync.Mutex
	events []JobEvent
	subs   map[int64]chan JobEvent
	nextID int64
	seq    int64
	file   *os.File
	closed bool
}

// newEventBus opens (or creates) the event file of one job.
func newEventBus(path string, startSeq int64) (*EventBus, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create job dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open event file: %w", err)
	}
	return &EventBus{path: path, subs: map[int64]chan JobEvent{}, file: file, seq: startSeq}, nil
}

// loadEvents reads the persisted event file of a job.
func loadEvents(path string) ([]JobEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	events := []JobEvent{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev JobEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

// Append numbers, persists and broadcasts one event.
func (b *EventBus) Append(ev JobEvent) JobEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ev.Seq = b.seq
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	b.events = append(b.events, ev)
	if data, err := json.Marshal(ev); err == nil {
		b.file.Write(append(data, '\n'))
	}
	for id, ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// The subscriber is too slow; drop this event for it. It will
			// resync with ?after=<seq> on reconnect.
			_ = id
		}
	}
	return ev
}

// Events returns a copy of every event so far.
func (b *EventBus) Events() []JobEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]JobEvent{}, b.events...)
}

// subscriberCount reports how many live subscribers the bus holds.
func (b *EventBus) subscriberCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Seq returns the last assigned sequence number.
func (b *EventBus) Seq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// Subscribe returns the events after `after` plus a channel of new ones.
func (b *EventBus) Subscribe(after int64) ([]JobEvent, <-chan JobEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	history := make([]JobEvent, 0, len(b.events))
	for _, ev := range b.events {
		if ev.Seq > after {
			history = append(history, ev)
		}
	}
	id := b.nextID
	b.nextID++
	ch := make(chan JobEvent, subscriberBuffer)
	b.subs[id] = ch
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(existing)
		}
	}
	return history, ch, cancel
}

// Close releases the event file.
func (b *EventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if b.file != nil {
		b.file.Close()
	}
}

// terminal reports whether a job state finishes the stream.
func terminal(status string) bool {
	switch status {
	case store.JobSuccess, store.JobFailed, store.JobCanceled:
		return true
	default:
		return false
	}
}
