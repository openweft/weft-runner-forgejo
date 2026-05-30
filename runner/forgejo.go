// Package runner — Forgejo (and Gitea) Actions runner protocol shim.
//
// Forgejo's CI runner protocol is **Connect RPC** (formerly Twirp) under
// `/api/actions/runner.v1.RunnerService/<Method>`. Connect supports a
// JSON-over-HTTP envelope alongside the protobuf+gRPC one, which lets us
// speak it without pulling in protoc + connect-go + a generated package
// graph. We use the JSON envelope only: it's slower than proto but the
// payloads are tiny and the dependency saving is meaningful (the alternative
// is vendoring the Forgejo .proto files + a connect-go + protobuf-go
// transitive graph — non-trivial).
//
// Connect-over-JSON conventions used here:
//
//   - Method URL: `<base>/api/actions/runner.v1.RunnerService/<Method>`
//   - HTTP POST, `Content-Type: application/json`, body = JSON-encoded request
//     message (proto3 JSON shape).
//   - Auth uses the runner UUID + token via the `x-runner-token` header.
//     During *registration* we don't have a token yet; we pass the
//     registration token in the request body's `token` field instead.
//   - Responses are JSON; Connect errors arrive as `{ "code": "...",
//     "message": "..." }` with HTTP 4xx/5xx.
//
// Endpoints we wrap:
//
//   - Register   — exchange a registration token for a runner UUID + token.
//   - Declare    — optional, advertises runner capabilities after registration.
//   - FetchTask  — long-poll for an assigned Action task.
//   - UpdateLog  — append log lines to a running task.
//   - UpdateTask — terminal state transition.
//
// Today we implement Register + Declare fully (they're tiny). FetchTask /
// UpdateLog / UpdateTask have HTTP plumbing in place but the message shape
// is parked behind a TODO — the proto for those carries enough nested
// optional fields that hand-coding the JSON shape is a maintenance hazard.
// The right next step is to vendor the proto and generate the JSON tags
// from it; that's a follow-up commit.

package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// fj is a tiny Connect-over-JSON client against one Forgejo base URL.
type fj struct {
	client *http.Client
	base   string // e.g. https://codeberg.org (no trailing slash)
	// uuid + token are populated after Register. Empty during the
	// Register call itself (the registration token is sent in body).
	uuid  string
	token string
}

func newFJ(baseURL string) *fj {
	return &fj{
		client: &http.Client{Timeout: 60 * time.Second},
		base:   strings.TrimRight(baseURL, "/"),
	}
}

// connectCall is the one HTTP-level helper. All RunnerService methods go
// through here so retry/auth/error handling stays in one place.
func (f *fj) connectCall(ctx context.Context, method string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", method, err)
	}
	url := fmt.Sprintf("%s/api/actions/runner.v1.RunnerService/%s", f.base, method)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	// Connect-Protocol-Version is mandatory for the Connect envelope.
	httpReq.Header.Set("Connect-Protocol-Version", "1")
	if f.uuid != "" {
		httpReq.Header.Set("x-runner-uuid", f.uuid)
	}
	if f.token != "" {
		httpReq.Header.Set("x-runner-token", f.token)
	}
	r, err := f.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	rb, _ := io.ReadAll(r.Body)
	if r.StatusCode/100 != 2 {
		// Connect error envelope: { "code": "...", "message": "..." }
		var cErr struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(rb, &cErr) == nil && cErr.Message != "" {
			return fmt.Errorf("forgejo %s: %s (%s, HTTP %d)", method, cErr.Message, cErr.Code, r.StatusCode)
		}
		return fmt.Errorf("forgejo %s: HTTP %d: %s", method, r.StatusCode, strings.TrimSpace(string(rb)))
	}
	if resp != nil && len(rb) > 0 {
		if err := json.Unmarshal(rb, resp); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
	}
	return nil
}

// registerRequest is the body Forgejo's RunnerService.Register expects.
type registerReqMsg struct {
	Token        string   `json:"token"`                  // registration token (one-shot)
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Labels       []string `json:"labels"`
}

// registerResponse mirrors RunnerService.Register's reply. The persisted
// `Runner.token` + `Runner.uuid` are what FetchTask later authenticates with.
type registerResp struct {
	Runner struct {
		ID      int64    `json:"id"`
		UUID    string   `json:"uuid"`
		Token   string   `json:"token"`
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Labels  []string `json:"labels"`
	} `json:"runner"`
}

func (f *fj) register(ctx context.Context, regToken, name string, labels []string) (registerResp, error) {
	var out registerResp
	err := f.connectCall(ctx, "Register", registerReqMsg{
		Token:   regToken,
		Name:    name,
		Version: "weft-runner-forgejo-0.0.0",
		Labels:  labels,
	}, &out)
	if err != nil {
		return out, err
	}
	if out.Runner.Token == "" || out.Runner.UUID == "" {
		return out, fmt.Errorf("forgejo Register: response missing runner token/uuid (got %+v)", out.Runner)
	}
	return out, nil
}

// declare advertises the runner's labels + version after registration. It's
// not strictly required (Forgejo accepts task fetches without it) but the
// UI shows the runner as "online" only once Declare lands, so we always
// call it on Run startup.
type declareReqMsg struct {
	Version string   `json:"version"`
	Labels  []string `json:"labels"`
}

type declareResp struct {
	Runner struct {
		ID     int64    `json:"id"`
		Name   string   `json:"name"`
		Labels []string `json:"labels"`
	} `json:"runner"`
}

func (f *fj) declare(ctx context.Context, labels []string) (declareResp, error) {
	var out declareResp
	err := f.connectCall(ctx, "Declare", declareReqMsg{
		Version: "weft-runner-forgejo-0.0.0",
		Labels:  labels,
	}, &out)
	return out, err
}

// TaskSummary is the *minimal* projection of RunnerService.FetchTask's
// Task message — just enough to drive job.go. The real Task carries the
// workflow yaml, all variables/secrets/services, container config, etc.
// Extending this struct is a per-feature exercise once the runner picks
// up real workloads.
type TaskSummary struct {
	ID      int64  `json:"id"`
	Token   string `json:"token"`
	Image   string `json:"image"` // empty if the workflow doesn't pin one
	Workflow string `json:"workflow"`
}

// fetchTask long-polls for an assigned task. Returns (nil, nil) when no
// task is available — that's idle, callers back off and retry.
//
// TODO(milestone-real-fetchtask): the proto for FetchTask carries optional
// nested messages we don't yet model (event payload, container config,
// services, dependencies). Today we parse only the top-level fields above;
// the rest is silently dropped. Land the proto vendoring + a fuller
// TaskSummary in a follow-up before claiming we run real workloads.
func (f *fj) fetchTask(ctx context.Context) (*TaskSummary, error) {
	var out struct {
		Task *TaskSummary `json:"task"`
	}
	if err := f.connectCall(ctx, "FetchTask", struct{}{}, &out); err != nil {
		return nil, err
	}
	if out.Task == nil || out.Task.ID == 0 {
		return nil, nil
	}
	return out.Task, nil
}

// updateLog appends one chunk to the live task log. `index` is the running
// byte offset Forgejo expects to track resends idempotently.
//
// TODO(milestone-real-updatelog): proto carries `rows` (a list of log lines
// with timestamps) not a flat byte chunk. Today we pack one row per call
// containing the chunk as content. Replace with a real row stream once
// we stream the in-VM stdout properly.
func (f *fj) updateLog(ctx context.Context, taskID int64, chunk []byte, index int64) error {
	type row struct {
		Time    time.Time `json:"time"`
		Content string    `json:"content"`
	}
	body := struct {
		TaskID int64 `json:"task_id"`
		Index  int64 `json:"index"`
		Rows   []row `json:"rows"`
	}{
		TaskID: taskID,
		Index:  index,
		Rows: []row{{
			Time:    time.Now().UTC(),
			Content: string(chunk),
		}},
	}
	return f.connectCall(ctx, "UpdateLog", body, nil)
}

// updateTask transitions a task to its terminal state. `result` is one of
// `forgejo_runner_v1.Result_*`: SUCCESS, FAILURE, CANCELLED, SKIPPED.
func (f *fj) updateTask(ctx context.Context, taskID int64, result string) error {
	body := struct {
		State struct {
			ID     int64  `json:"id"`
			Result string `json:"result"`
		} `json:"state"`
	}{}
	body.State.ID = taskID
	body.State.Result = result
	return f.connectCall(ctx, "UpdateTask", body, nil)
}

// hostInfo is what we send as the runner's `info` block (used by Forgejo
// to display platform/arch in its admin UI).
func hostInfo() string {
	return fmt.Sprintf("weft-microvm/%s-%s", runtime.GOOS, runtime.GOARCH)
}
