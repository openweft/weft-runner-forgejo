package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestFJ wires the fj client at a httptest server. We construct fj
// directly rather than going through newFJ so the test owns the http.Client
// and can pin a tight timeout.
func newTestFJ(srv *httptest.Server) *fj {
	return &fj{
		client: srv.Client(),
		base:   strings.TrimRight(srv.URL, "/"),
	}
}

// mountConnect routes /api/actions/runner.v1.RunnerService/<method> to a
// per-method handler. Anything else 404s so test typos surface.
func mountConnect(t *testing.T, methods map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for m, h := range methods {
		mux.HandleFunc("/api/actions/runner.v1.RunnerService/"+m, h)
	}
	return httptest.NewServer(mux)
}

func readJSON(t *testing.T, r *http.Request, into any) {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("decode body: %v (raw=%s)", err, string(b))
	}
}

func TestRegister_HappyPath(t *testing.T) {
	var gotToken, gotName string
	var gotLabels []string
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"Register": func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Connect-Protocol-Version"); got != "1" {
				t.Errorf("Connect-Protocol-Version=%q, want 1", got)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type=%q", got)
			}
			var req registerReqMsg
			readJSON(t, r, &req)
			gotToken, gotName, gotLabels = req.Token, req.Name, req.Labels
			resp := registerResp{}
			resp.Runner.ID = 42
			resp.Runner.UUID = "uuid-abc"
			resp.Runner.Token = "tok-xyz"
			resp.Runner.Name = req.Name
			resp.Runner.Labels = req.Labels
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := f.register(ctx, "reg-tok", "host-a", []string{"linux", "amd64"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if out.Runner.UUID != "uuid-abc" || out.Runner.Token != "tok-xyz" {
		t.Fatalf("unexpected resp: %+v", out.Runner)
	}
	if gotToken != "reg-tok" || gotName != "host-a" || len(gotLabels) != 2 {
		t.Fatalf("server saw: token=%q name=%q labels=%v", gotToken, gotName, gotLabels)
	}
}

func TestRegister_MissingTokenInResponse(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"Register": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"runner":{"id":1}}`))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	_, err := f.register(context.Background(), "reg", "n", nil)
	if err == nil || !strings.Contains(err.Error(), "missing runner token/uuid") {
		t.Fatalf("expected missing token/uuid error, got %v", err)
	}
}

func TestRegister_ConnectErrorEnvelope(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"Register": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"bad registration token"}`))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	_, err := f.register(context.Background(), "reg", "n", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bad registration token") || !strings.Contains(err.Error(), "unauthenticated") {
		t.Fatalf("error did not surface envelope: %v", err)
	}
}

func TestDeclare_HappyPath(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"Declare": func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("x-runner-uuid"); got != "uuid-1" {
				t.Errorf("x-runner-uuid=%q", got)
			}
			if got := r.Header.Get("x-runner-token"); got != "tok-1" {
				t.Errorf("x-runner-token=%q", got)
			}
			var req declareReqMsg
			readJSON(t, r, &req)
			resp := declareResp{}
			resp.Runner.ID = 7
			resp.Runner.Name = "declared"
			resp.Runner.Labels = req.Labels
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	f.uuid, f.token = "uuid-1", "tok-1"
	out, err := f.declare(context.Background(), []string{"self-hosted"})
	if err != nil {
		t.Fatalf("declare: %v", err)
	}
	if out.Runner.ID != 7 || out.Runner.Name != "declared" {
		t.Fatalf("unexpected: %+v", out.Runner)
	}
}

func TestFetchTask_HappyPath(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"FetchTask": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task":{"id":99,"token":"tt","image":{"name":"img"},"workflow_payload":"wf"}}`))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	f.uuid, f.token = "u", "t"
	task, err := f.fetchTask(context.Background())
	if err != nil {
		t.Fatalf("fetchTask: %v", err)
	}
	if task == nil || task.ID != 99 || task.Token != "tt" || task.Image.Name != "img" || task.Workflow != "wf" {
		t.Fatalf("unexpected task: %+v", task)
	}
}

// TestFetchTask_FullProtoShape exercises every field we mirror from
// runner.v1.Task. If Forgejo bumps the proto and we add a tag to
// TaskSummary, extend the body here too — this test is the contract.
func TestFetchTask_FullProtoShape(t *testing.T) {
	body := `{
	  "task": {
	    "id": 42,
	    "token": "tt",
	    "workflow_payload": "name: ci\non: push\njobs: {}\n",
	    "context": {"repository": "owner/repo", "run_id": 7},
	    "secrets": {"GH_TOKEN": "shhh", "NPM_TOKEN": "psst"},
	    "vars": {"ENV": "prod"},
	    "machine": "ubuntu-22.04",
	    "event": "push",
	    "event_payload": "{\"ref\":\"refs/heads/main\"}",
	    "concurrency": {"group": "ci-main", "cancel_in_progress": true},
	    "image": {"name": "ghcr.io/example/img:latest"}
	  }
	}`
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"FetchTask": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	f.uuid, f.token = "u", "t"
	task, err := f.fetchTask(context.Background())
	if err != nil {
		t.Fatalf("fetchTask: %v", err)
	}
	if task == nil {
		t.Fatal("expected task, got nil")
	}
	if task.ID != 42 || task.Token != "tt" {
		t.Errorf("id/token: got id=%d token=%q", task.ID, task.Token)
	}
	if task.Workflow != "name: ci\non: push\njobs: {}\n" {
		t.Errorf("workflow_payload: got %q", task.Workflow)
	}
	if task.Context["repository"] != "owner/repo" {
		t.Errorf("context.repository: got %v", task.Context["repository"])
	}
	// JSON numbers decode to float64 into map[string]any.
	if v, ok := task.Context["run_id"].(float64); !ok || v != 7 {
		t.Errorf("context.run_id: got %v (%T)", task.Context["run_id"], task.Context["run_id"])
	}
	if task.Secrets["GH_TOKEN"] != "shhh" || task.Secrets["NPM_TOKEN"] != "psst" {
		t.Errorf("secrets: %+v", task.Secrets)
	}
	if task.Vars["ENV"] != "prod" {
		t.Errorf("vars: %+v", task.Vars)
	}
	if task.Machine != "ubuntu-22.04" {
		t.Errorf("machine: got %q", task.Machine)
	}
	if task.Event != "push" {
		t.Errorf("event: got %q", task.Event)
	}
	if task.EventPayload != `{"ref":"refs/heads/main"}` {
		t.Errorf("event_payload: got %q", task.EventPayload)
	}
	if task.Concurrency == nil || task.Concurrency.Group != "ci-main" || !task.Concurrency.CancelInProgress {
		t.Errorf("concurrency: %+v", task.Concurrency)
	}
	if task.Image.Name != "ghcr.io/example/img:latest" {
		t.Errorf("image.name: got %q", task.Image.Name)
	}
}

func TestFetchTask_EmptyReturnsNil(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"FetchTask": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	task, err := f.fetchTask(context.Background())
	if err != nil {
		t.Fatalf("fetchTask: %v", err)
	}
	if task != nil {
		t.Fatalf("expected nil task, got %+v", task)
	}
}

func TestUpdateLog_HappyPath(t *testing.T) {
	var seen atomic.Int32
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"UpdateLog": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				TaskID int64 `json:"task_id"`
				Index  int64 `json:"index"`
				Rows   []struct {
					Time    time.Time `json:"time"`
					Content string    `json:"content"`
				} `json:"rows"`
			}
			readJSON(t, r, &body)
			if body.TaskID != 5 || body.Index != 12 {
				t.Errorf("got task_id=%d index=%d", body.TaskID, body.Index)
			}
			if len(body.Rows) != 1 || body.Rows[0].Content != "hello" {
				t.Errorf("rows=%+v", body.Rows)
			}
			seen.Add(1)
			w.WriteHeader(http.StatusOK)
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	if err := f.updateLog(context.Background(), 5, []byte("hello"), 12); err != nil {
		t.Fatalf("updateLog: %v", err)
	}
	if seen.Load() != 1 {
		t.Fatalf("server not called")
	}
}

func TestUpdateTask_HappyPath(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"UpdateTask": func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				State struct {
					ID     int64  `json:"id"`
					Result string `json:"result"`
				} `json:"state"`
			}
			readJSON(t, r, &body)
			if body.State.ID != 5 || body.State.Result != "SUCCESS" {
				t.Errorf("got id=%d result=%s", body.State.ID, body.State.Result)
			}
			w.WriteHeader(http.StatusOK)
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	if err := f.updateTask(context.Background(), 5, "SUCCESS"); err != nil {
		t.Fatalf("updateTask: %v", err)
	}
}

func TestConnectCall_NonJSONErrorBody(t *testing.T) {
	srv := mountConnect(t, map[string]http.HandlerFunc{
		"UpdateTask": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	err := f.updateTask(context.Background(), 1, "FAILURE")
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected raw HTTP error, got %v", err)
	}
}
