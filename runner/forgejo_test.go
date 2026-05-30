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
			_, _ = w.Write([]byte(`{"task":{"id":99,"token":"tt","image":"img","workflow":"wf"}}`))
		},
	})
	defer srv.Close()

	f := newTestFJ(srv)
	f.uuid, f.token = "u", "t"
	task, err := f.fetchTask(context.Background())
	if err != nil {
		t.Fatalf("fetchTask: %v", err)
	}
	if task == nil || task.ID != 99 || task.Token != "tt" || task.Image != "img" || task.Workflow != "wf" {
		t.Fatalf("unexpected task: %+v", task)
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
