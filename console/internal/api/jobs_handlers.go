package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/dbstd2333/pigsty-ui/console/internal/jobs"
	"github.com/dbstd2333/pigsty-ui/console/internal/store"
)

// sseHeartbeat is the comment interval that keeps SSE connections alive.
const sseHeartbeat = 15 * time.Second

type jobCreateInput struct {
	Body struct {
		Action  string         `json:"action" required:"true"`
		Params  map[string]any `json:"params" required:"false"`
		Check   bool           `json:"check" required:"false"`
		Confirm *struct {
			Text       string   `json:"text" required:"false"`
			Checkboxes []string `json:"checkboxes" required:"false"`
		} `json:"confirm" required:"false"`
	}
}

type jobCreateOutput struct {
	Body jobs.Job
}

type jobsListInput struct {
	Limit   int    `query:"limit" default:"50" minimum:"1" maximum:"500"`
	Offset  int    `query:"offset" default:"0" minimum:"0"`
	Cluster string `query:"cluster"`
	Status  string `query:"status" enum:"queued,running,success,failed,canceled"`
}

type jobsListOutput struct {
	Body struct {
		Items []jobs.Job `json:"items"`
		Total int        `json:"total"`
	}
}

type jobGetInput struct {
	ID string `path:"id"`
}

type jobGetOutput struct {
	Body jobs.Job
}

type jobEventsInput struct {
	ID    string `path:"id"`
	After int64  `query:"after" default:"0" minimum:"0"`
}

type jobLogOutput struct {
	ContentType string `header:"Content-Type"`
	Body        []byte
}

type jobCancelOutput struct{}

// registerJobs registers the job endpoints.
func (s *Server) registerJobs() {
	huma.Register(s.api, huma.Operation{
		OperationID:   "createJob",
		Method:        http.MethodPost,
		Path:          "/jobs",
		Summary:       "提交作业",
		Tags:          []string{"jobs"},
		DefaultStatus: http.StatusCreated,
		Errors:        []int{401, 403, 404, 409, 422, 500},
	}, func(ctx context.Context, in *jobCreateInput) (*jobCreateOutput, error) {
		req := jobs.SubmitRequest{
			Action: in.Body.Action,
			Params: in.Body.Params,
			Check:  in.Body.Check,
			User:   currentSession(ctx).Username,
		}
		if in.Body.Confirm != nil {
			req.Confirm = &jobs.Confirm{Text: in.Body.Confirm.Text, Checkboxes: in.Body.Confirm.Checkboxes}
		}
		job, err := s.jobs.Submit(ctx, req)
		if err != nil {
			return nil, jobError(err)
		}
		return &jobCreateOutput{Body: job}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "listJobs",
		Method:      http.MethodGet,
		Path:        "/jobs",
		Summary:     "作业列表",
		Tags:        []string{"jobs"},
		Errors:      []int{401, 500},
	}, func(_ context.Context, in *jobsListInput) (*jobsListOutput, error) {
		items, total, err := s.jobs.List(jobs.JobFilter{
			Cluster: in.Cluster, Status: in.Status, Limit: in.Limit, Offset: in.Offset,
		})
		if err != nil {
			return nil, err
		}
		out := &jobsListOutput{}
		if items == nil {
			items = []jobs.Job{}
		}
		out.Body.Items = items
		out.Body.Total = total
		return out, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "getJob",
		Method:      http.MethodGet,
		Path:        "/jobs/{id}",
		Summary:     "作业详情",
		Tags:        []string{"jobs"},
		Errors:      []int{401, 404, 500},
	}, func(_ context.Context, in *jobGetInput) (*jobGetOutput, error) {
		job, err := s.jobs.Get(in.ID)
		if err != nil {
			return nil, jobError(err)
		}
		return &jobGetOutput{Body: job}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "jobEvents",
		Method:      http.MethodGet,
		Path:        "/jobs/{id}/events",
		Summary:     "作业事件流（SSE）",
		Tags:        []string{"jobs"},
		Errors:      []int{401, 404, 500},
	}, func(_ context.Context, in *jobEventsInput) (*huma.StreamResponse, error) {
		if _, err := s.jobs.Get(in.ID); err != nil {
			return nil, jobError(err)
		}
		history, ch, cancel, err := s.jobs.Subscribe(in.ID, in.After)
		if err != nil {
			return nil, jobError(err)
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "text/event-stream")
			hctx.SetHeader("Cache-Control", "no-cache")
			hctx.SetHeader("Connection", "keep-alive")
			hctx.SetHeader("X-Accel-Buffering", "no")
			// Commit the response so the browser opens the stream immediately.
			hctx.SetStatus(http.StatusOK)
			// Every subscription owns a buffered channel plus its history copy;
			// release them (and the memory) as soon as the client goes away.
			defer cancel()
			defer debug.FreeOSMemory()
			writer := hctx.BodyWriter()
			flush := func() {
				if f, ok := writer.(http.Flusher); ok {
					f.Flush()
				}
			}
			flush()

			ended := false
			for _, ev := range history {
				if err := writeSSE(writer, "job", ev); err != nil {
					return
				}
				ended = ended || terminalEvent(ev)
			}
			flush()
			if ended {
				s.finishStream(writer, in.ID)
				return
			}

			ticker := time.NewTicker(sseHeartbeat)
			defer ticker.Stop()
			done := s.jobs.Done(in.ID)
			for {
				select {
				case <-hctx.Context().Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					if err := writeSSE(writer, "job", ev); err != nil {
						return
					}
					flush()
					if terminalEvent(ev) {
						s.finishStream(writer, in.ID)
						return
					}
				case <-done:
					// No further events will arrive; drain what is queued.
					for {
						select {
						case ev := <-ch:
							if err := writeSSE(writer, "job", ev); err != nil {
								return
							}
						default:
							flush()
							s.finishStream(writer, in.ID)
							return
						}
					}
				case <-ticker.C:
					if _, err := io.WriteString(writer, ": keepalive\n\n"); err != nil {
						return
					}
					flush()
				}
			}
		}}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID: "jobLog",
		Method:      http.MethodGet,
		Path:        "/jobs/{id}/log",
		Summary:     "作业原始输出（已脱敏）",
		Tags:        []string{"jobs"},
		Errors:      []int{401, 404, 500},
	}, func(_ context.Context, in *jobGetInput) (*jobLogOutput, error) {
		text, err := s.jobs.Log(in.ID)
		if err != nil {
			return nil, jobError(err)
		}
		return &jobLogOutput{ContentType: "text/plain; charset=utf-8", Body: []byte(text)}, nil
	})

	huma.Register(s.api, huma.Operation{
		OperationID:   "cancelJob",
		Method:        http.MethodPost,
		Path:          "/jobs/{id}/cancel",
		Summary:       "取消作业",
		Tags:          []string{"jobs"},
		DefaultStatus: http.StatusNoContent,
		Errors:        []int{401, 404, 500},
	}, func(ctx context.Context, in *jobGetInput) (*jobCancelOutput, error) {
		if _, err := s.jobs.Get(in.ID); err != nil {
			return nil, jobError(err)
		}
		if err := s.jobs.Cancel(ctx, in.ID); err != nil {
			return nil, jobError(err)
		}
		return &jobCancelOutput{}, nil
	})
}

// finishStream sends the terminal `end` event of a job stream.
func (s *Server) finishStream(writer io.Writer, id string) {
	job, err := s.jobs.Get(id)
	if err != nil {
		return
	}
	_ = writeSSE(writer, "end", job)
	if f, ok := writer.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSE writes one server sent event.
func writeSSE(writer io.Writer, event string, data any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return nil
}

// terminalEvent reports whether an event ends the stream.
func terminalEvent(ev jobs.JobEvent) bool {
	if ev.Type != jobs.EventStatus || ev.Job == nil {
		return false
	}
	switch ev.Job.Status {
	case store.JobSuccess, store.JobFailed, store.JobCanceled:
		return true
	default:
		return false
	}
}

// jobError maps job manager errors onto problem+json responses.
func jobError(err error) error {
	var conflict *jobs.ConflictError
	var blocked *jobs.BlockedError
	switch {
	case errors.Is(err, jobs.ErrJobNotFound):
		return problem(http.StatusNotFound, "作业不存在")
	case errors.As(err, &conflict):
		return &ProblemError{
			Title:        titleFor(http.StatusConflict),
			Status:       http.StatusConflict,
			Detail:       conflict.Detail,
			RunningJobID: conflict.RunningJobID,
		}
	case errors.As(err, &blocked):
		return &ProblemError{
			Title:  titleFor(http.StatusUnprocessableEntity),
			Status: http.StatusUnprocessableEntity,
			Detail: blocked.Detail,
			Issues: blocked.Issues,
		}
	default:
		// Parameter and planning failures use the same 422 contract as the
		// plan endpoint.
		if mapped := actionError(err); mapped != err {
			return mapped
		}
		return err
	}
}
