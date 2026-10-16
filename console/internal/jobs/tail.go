package jobs

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// pollInterval is how often the tailers look for new output.
const pollInterval = 150 * time.Millisecond

// tailer watches the ansible callback file and the stdout file of a job.
type tailer struct {
	m    *Manager
	r    *run
	dir  string
	done chan struct{}
}

// startTail starts tailing the output of one job and returns its handle.
func (m *Manager) startTail(ctx context.Context, r *run, dir string, state jobState) *tailer {
	t := &tailer{m: m, r: r, dir: dir, done: make(chan struct{})}
	ndjson := &fileTail{path: filepath.Join(dir, "ansible.ndjson"), offset: state.AnsibleOffset, prepared: false}
	stdout := &fileTail{path: filepath.Join(dir, "stdout.log"), offset: state.StdoutOffset, prepared: false}
	go func() {
		defer close(t.done)
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				t.flush(ndjson, stdout, state)
				return
			case <-ticker.C:
				t.poll(ndjson, stdout, state)
			}
		}
	}()
	return t
}

// Wait blocks until the tailer stopped.
func (t *tailer) Wait() { <-t.done }

// poll reads both files and turns new lines into job events.
func (t *tailer) poll(ndjson, stdout *fileTail, state jobState) {
	changed := false
	if lines, err := ndjson.readNew(); err != nil {
		t.m.logger.Debug("tail events", "id", t.r.job.ID, "err", err)
	} else if len(lines) > 0 {
		changed = true
		for _, line := range lines {
			t.handleRaw(line)
		}
	}
	// The simulate mode writes its own stdout log and already emitted the
	// matching events, so the file must not be replayed here.
	if !t.m.simulate {
		if lines, err := stdout.readNew(); err != nil {
			t.m.logger.Debug("tail stdout", "id", t.r.job.ID, "err", err)
		} else if len(lines) > 0 {
			changed = true
			for _, line := range lines {
				t.m.emit(t.r, JobEvent{Type: EventLog, Line: line})
			}
		}
	}
	if !changed {
		return
	}
	state.AnsibleOffset = ndjson.offset
	state.StdoutOffset = stdout.offset
	if err := writeState(t.dir, state); err != nil {
		t.m.logger.Debug("persist job offsets", "id", t.r.job.ID, "err", err)
	}
}

// flush performs a last read before the tailer stops.
func (t *tailer) flush(ndjson, stdout *fileTail, state jobState) {
	t.poll(ndjson, stdout, state)
}

// handleRaw converts one ndjson line of the callback plugin into job events.
func (t *tailer) handleRaw(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var raw rawEvent
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.m.emit(t.r, JobEvent{Type: EventLog, Line: line})
		return
	}
	ev := JobEvent{
		Type:     raw.Type,
		Playbook: raw.Playbook,
		Play:     raw.Play,
		Task:     raw.Task,
		TaskID:   raw.TaskID,
		Host:     raw.Host,
		Status:   raw.Status,
		Msg:      raw.Msg,
		Hosts:    raw.Hosts,
	}
	if raw.DurationMs != nil {
		duration := int64(*raw.DurationMs)
		ev.DurationMs = &duration
	}
	if ev.Type == "" {
		ev.Type = EventLog
		ev.Line = line
	}
	if len(ev.Hosts) > 0 {
		t.r.addStats(ev.Hosts)
	}
	t.m.emit(t.r, ev)
}

// rawEvent mirrors the ndjson schema emitted by the callback plugin.
type rawEvent struct {
	TS         string               `json:"ts"`
	Type       string               `json:"type"`
	Playbook   string               `json:"playbook"`
	Play       string               `json:"play"`
	Task       string               `json:"task"`
	TaskID     string               `json:"task_id"`
	Host       string               `json:"host"`
	Status     string               `json:"status"`
	DurationMs *int                 `json:"duration_ms"`
	Msg        string               `json:"msg"`
	Hosts      map[string]HostStats `json:"hosts"`
}

// fileTail incrementally reads one growing file.
type fileTail struct {
	path     string
	offset   int64
	partial  []byte
	prepared bool
}

// readNew returns the complete lines appended since the last read.
func (t *fileTail) readNew() ([]string, error) {
	file, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	if !t.prepared {
		t.prepared = true
		info, err := file.Stat()
		if err != nil {
			return nil, err
		}
		if info.Size() < t.offset {
			// The file was rotated or truncated: start over.
			t.offset = 0
		}
	}
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	t.offset += int64(len(data))
	buffer := append(t.partial, data...)
	lines := []string{}
	for {
		index := indexByte(buffer, '\n')
		if index < 0 {
			break
		}
		lines = append(lines, string(buffer[:index]))
		buffer = buffer[index+1:]
	}
	t.partial = append([]byte{}, buffer...)
	return lines, nil
}

// indexByte returns the position of a byte in a slice, -1 when absent.
func indexByte(data []byte, b byte) int {
	for i, v := range data {
		if v == b {
			return i
		}
	}
	return -1
}
