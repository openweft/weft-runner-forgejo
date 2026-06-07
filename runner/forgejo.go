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
// Register + Declare are tiny ; FetchTask / UpdateLog / UpdateTask carry
// the runtime payload. The JSON shape is now stable enough that we
// hand-code it — the upstream proto (gitea/runner-go) is a moving
// target, vendoring it would force a re-pin every Forgejo release. The
// invariant : every TaskSummary field we read has a Forgejo doc-stable
// JSON name (`id`, `workflow_payload`, `vars`) ; every UpdateLog row
// we write is one log line with a UTC timestamp.

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
	rb, readErr := io.ReadAll(r.Body)
	if readErr != nil && r.StatusCode/100 == 2 {
		return fmt.Errorf("read %s response: %w", method, readErr)
	}
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

// TaskSummary is our hand-tracked projection of RunnerService.FetchTask's
// `runner.v1.Task` message. We mirror the upstream proto field names (and
// the proto-JSON snake_case wire shape) so that, when Forgejo bumps the
// schema, the diff is a localised one-struct exercise rather than a
// codegen-graph reshuffle.
//
// Canonical proto:
//
//	https://code.forgejo.org/forgejo/runner/src/branch/main/internal/pkg/client/runner/runner.proto
//
// Look there first when the agent reports a field it doesn't understand.
// Add the field below with the matching json tag; both wire decode (in
// fetchTask) and cfg-share serialisation (in job.go) flow through this
// struct, so no other site needs touching.
//
// Image is preserved as a struct (matches proto's `Image { string name; }`)
// while staying backwards-compatible with the previous flat `image` tag —
// the older flat shape is dropped, callers using task.Image.Name. The
// in-VM agent has always read the field via its decoded value, not via
// the wire tag, so the rename is internal.
type TaskSummary struct {
	ID           int64             `json:"id"`
	Token        string            `json:"token"`
	Workflow     string            `json:"workflow_payload"` // raw YAML
	Context      map[string]any    `json:"context,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
	Machine      string            `json:"machine,omitempty"`
	Event        string            `json:"event,omitempty"`         // push|pull_request|…
	EventPayload string            `json:"event_payload,omitempty"` // raw JSON of the trigger event
	Concurrency  *Concurrency      `json:"concurrency,omitempty"`
	Image        struct {
		Name string `json:"name"`
	} `json:"image"`
}

// Concurrency mirrors the proto's nested Concurrency message (group +
// cancel-in-progress flag, same semantics as upstream GitHub Actions).
type Concurrency struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress bool   `json:"cancel_in_progress,omitempty"`
}

// fetchTask long-polls for an assigned task. Returns (nil, nil) when no
// task is available — that's idle, callers back off and retry.
//
// The TaskSummary above is hand-tracked against the upstream
// `runner.v1.Task` proto; see the comment on TaskSummary for the canonical
// URL and the protocol for adding fields.
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

// updateLog appends a chunk of in-VM stdout to the live task log. `index`
// is the running ROW offset Forgejo expects to dedup resends (NOT a byte
// offset — Forgejo's UpdateLog protocol counts rows). The caller bumps
// index by len(rows) after a successful return.
//
// The proto's `rows` slice expects one log line per entry with its own
// timestamp. We split the chunk on '\n' and stamp every line at "now"
// — for in-VM streaming the chunk is short enough that all lines share
// the same wallclock second, which is the resolution Forgejo's UI
// renders anyway. Empty trailing element after a final newline is
// dropped so the UI doesn't show phantom blank rows.
//
// A nil/empty chunk returns nil without an RPC — the caller's
// `if len(buf) > 0 { updateLog(…) }` guard is the source of truth, but
// defending here keeps a buggy caller from hammering Forgejo with empty
// rows requests.
func (f *fj) updateLog(ctx context.Context, taskID int64, chunk []byte, index int64) error {
	if len(chunk) == 0 {
		return nil
	}
	type row struct {
		Time    time.Time `json:"time"`
		Content string    `json:"content"`
	}
	now := time.Now().UTC()
	lines := strings.Split(string(chunk), "\n")
	// Drop the trailing empty element produced by Split when chunk ends
	// with '\n'. Forgejo treats every row as a rendered line break, so
	// we don't want a spurious blank at the end of every chunk.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) == 0 {
		return nil
	}
	rows := make([]row, 0, len(lines))
	for _, ln := range lines {
		rows = append(rows, row{Time: now, Content: ln})
	}
	body := struct {
		TaskID int64 `json:"task_id"`
		Index  int64 `json:"index"`
		Rows   []row `json:"rows"`
	}{
		TaskID: taskID,
		Index:  index,
		Rows:   rows,
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
