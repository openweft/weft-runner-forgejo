// runner/job.go — per-job microVM lifecycle for Forgejo Actions.
//
// Same shape as github/gitlab siblings: shell out to `weft microvm …` for
// the VM moves, contractual cfg-share file the in-VM agent picks up. The
// difference is the cfg payload: Forgejo gives us the runner UUID + token
// + a task ID, and the in-VM agent re-uses *those* to pull the actual
// workflow definition + send back log rows over the same Connect-over-JSON
// channel.
//
// The in-VM agent contract (forgejo-task.json) mirrors `runner.v1.Task`
// (see runner/forgejo.go's TaskSummary for the proto source-of-truth):
//
//	{
//	  "url":              "<forgejo base>",
//	  "uuid":             "<runner uuid>",
//	  "token":            "<runner token>",
//	  "task_id":          <int>,
//	  "task_token":       "<task-scoped token>",
//	  "workflow_payload": "<raw workflow YAML>",
//	  "context":          { ... job-level context vars ... },
//	  "secrets":          { ... secret name → value ... },
//	  "vars":             { ... var name → value ... },
//	  "machine":          "<machine label>",
//	  "event":            "push|pull_request|…",
//	  "event_payload":    "<raw JSON of the trigger event>",
//	  "concurrency":      { "group": "...", "cancel_in_progress": <bool> }
//	}
//
// The agent reads this off `/run/weft/cfg/forgejo-task.json`, executes the
// workflow steps, and reports back over the same protocol. The daemon side
// only owns the VM lifecycle.
//
// Backwards compatibility: agents reading only the smaller five-field
// projection (url/uuid/token/task_id/task_token) continue to work — the
// new fields are additive, all `omitempty` so absent inputs stay absent.

package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// taskCfg is the JSON file we drop on the cfg share for the in-VM agent.
// Field names mirror `runner.v1.Task`'s proto-JSON shape (see TaskSummary
// in runner/forgejo.go). New fields are additive + omitempty so older
// in-VM agents that only read the five baseline fields keep working.
type taskCfg struct {
	URL          string            `json:"url"`
	UUID         string            `json:"uuid"`
	Token        string            `json:"token"`
	TaskID       int64             `json:"task_id"`
	TaskToken    string            `json:"task_token"`
	Workflow     string            `json:"workflow_payload,omitempty"`
	Context      map[string]any    `json:"context,omitempty"`
	Secrets      map[string]string `json:"secrets,omitempty"`
	Vars         map[string]string `json:"vars,omitempty"`
	Machine      string            `json:"machine,omitempty"`
	Event        string            `json:"event,omitempty"`
	EventPayload string            `json:"event_payload,omitempty"`
	Concurrency  *Concurrency      `json:"concurrency,omitempty"`
}

func dispatchJob(ctx context.Context, f *fj, weftEndpoint, image string, cfg PersistedConfig, task *TaskSummary) error {
	vmName := fmt.Sprintf("forgejo-task-%d", task.ID)
	cfgDir, err := os.MkdirTemp("", "weft-runner-forgejo-"+vmName+"-cfg-")
	if err != nil {
		return fmt.Errorf("mktemp cfg: %w", err)
	}
	defer os.RemoveAll(cfgDir)

	payload := taskCfg{
		URL:          cfg.URL,
		UUID:         cfg.UUID,
		Token:        cfg.Token,
		TaskID:       task.ID,
		TaskToken:    task.Token,
		Workflow:     task.Workflow,
		Context:      task.Context,
		Secrets:      task.Secrets,
		Vars:         task.Vars,
		Machine:      task.Machine,
		Event:        task.Event,
		EventPayload: task.EventPayload,
		Concurrency:  task.Concurrency,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal task cfg: %w", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "forgejo-task.json"), payloadBytes, 0o600); err != nil {
		return fmt.Errorf("write forgejo-task.json: %w", err)
	}

	jobImage := image
	if task.Image.Name != "" {
		jobImage = task.Image.Name
	}

	endpointFlag := "--endpoint=" + weftEndpoint
	register := exec.CommandContext(ctx, "weft", "microvm", "register",
		endpointFlag,
		"--name="+vmName,
		"--image="+jobImage,
		"--cfg="+cfgDir,
	)
	register.Stdout = os.Stderr
	register.Stderr = os.Stderr
	if err := register.Run(); err != nil {
		_ = f.updateTask(ctx, task.ID, "FAILURE")
		return fmt.Errorf("weft microvm register: %w", err)
	}
	defer func() {
		delCtx, delCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer delCancel()
		del := exec.CommandContext(delCtx, "weft", "microvm", "delete", endpointFlag, "--name="+vmName)
		del.Stderr = os.Stderr
		if err := del.Run(); err != nil {
			log.Printf("weft-runner-forgejo: delete %s failed: %v (leaked weft-side VM)", vmName, err)
		}
	}()

	start := exec.CommandContext(ctx, "weft", "microvm", "start", endpointFlag, "--name="+vmName)
	start.Stderr = os.Stderr
	if err := start.Run(); err != nil {
		_ = f.updateTask(ctx, task.ID, "FAILURE")
		return fmt.Errorf("weft microvm start: %w", err)
	}

	// In-VM agent owns log streaming over the same Connect channel; the
	// daemon doesn't proxy logs. We just wait for the VM to exit.
	wait := exec.CommandContext(ctx, "weft", "microvm", "wait", endpointFlag, "--name="+vmName)
	wait.Stderr = os.Stderr
	waitErr := wait.Run()

	result := "SUCCESS"
	if waitErr != nil {
		result = "FAILURE"
		log.Printf("weft-runner-forgejo: vm %s wait error: %v → marking task failed", vmName, waitErr)
	}
	if err := f.updateTask(ctx, task.ID, result); err != nil {
		log.Printf("weft-runner-forgejo: updateTask: %v", err)
	}
	return nil
}
