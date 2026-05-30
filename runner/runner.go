// Package runner is the daemon side of weft-runner-forgejo.
//
// One paragraph: Register exchanges a registration token (minted on the
// Forgejo admin UI: Site Administration → Actions → Runners → Create new
// runner) for a runner UUID + long-lived token via the Connect-over-JSON
// `runner.v1.RunnerService.Register` call. Run loads that config, dials
// weft, calls Declare to advertise capabilities, then enters the FetchTask
// long-poll loop; every assigned task is dispatched into a fresh microVM
// (job.go), updateTask back to Forgejo on exit. On SIGTERM the daemon
// drains in-flight tasks.

package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"
)

// RegisterOptions are the inputs to `weft-runner-forgejo register`.
type RegisterOptions struct {
	URL               string   // https://codeberg.org or self-hosted
	RegistrationToken string   // one-shot token from the Forgejo admin UI
	Name              string
	Labels            []string
	ConfigFile        string
}

// Register exchanges a registration token for a runner UUID + token.
func Register(opts RegisterOptions) error {
	if opts.URL == "" || opts.RegistrationToken == "" {
		return errors.New("register: --url and --registration-token are required")
	}
	if opts.ConfigFile == "" {
		return errors.New("register: --config is required")
	}
	if opts.Name == "" {
		opts.Name = "weft-" + hostInfo()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	f := newFJ(opts.URL)
	resp, err := f.register(ctx, opts.RegistrationToken, opts.Name, opts.Labels)
	if err != nil {
		return fmt.Errorf("forgejo register: %w", err)
	}

	cfg := PersistedConfig{
		URL:    opts.URL,
		UUID:   resp.Runner.UUID,
		Token:  resp.Runner.Token,
		Name:   resp.Runner.Name,
		Labels: resp.Runner.Labels,
	}
	if err := writeConfig(opts.ConfigFile, cfg); err != nil {
		return err
	}
	log.Printf("weft-runner-forgejo register: uuid=%s name=%s, config %s", resp.Runner.UUID, resp.Runner.Name, opts.ConfigFile)
	return nil
}

// RunOptions configures the long-lived daemon loop.
type RunOptions struct {
	ConfigFile   string
	WeftEndpoint string
	Image        string
	Concurrency  int
	PollInterval int
}

// Run boots the daemon and serves tasks until ctx is cancelled.
func Run(ctx context.Context, opts RunOptions) error {
	if opts.ConfigFile == "" || opts.WeftEndpoint == "" || opts.Image == "" {
		return errors.New("run: --config, --weft-endpoint, --image are required")
	}
	cfg, err := readConfig(opts.ConfigFile)
	if err != nil {
		return err
	}
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	pollInterval := time.Duration(opts.PollInterval) * time.Second
	if pollInterval == 0 {
		pollInterval = 3 * time.Second
	}

	f := newFJ(cfg.URL)
	f.uuid, f.token = cfg.UUID, cfg.Token

	// Advertise capabilities — Forgejo flips the runner to "online" only
	// after a successful Declare. Non-fatal if it fails (older Forgejo
	// versions don't implement it); we just log and continue.
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	if _, err := f.declare(dctx, cfg.Labels); err != nil {
		log.Printf("weft-runner-forgejo: declare warning (continuing): %v", err)
	}
	dcancel()

	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	log.Printf("weft-runner-forgejo run: uuid=%s url=%s concurrency=%d image=%s",
		cfg.UUID, cfg.URL, concurrency, opts.Image)

	backoff := pollInterval
loop:
	for {
		if ctx.Err() != nil {
			break
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			break loop
		}
		task, err := f.fetchTask(ctx)
		if err != nil {
			<-sem
			if ctx.Err() != nil {
				break
			}
			log.Printf("weft-runner-forgejo: fetch error: %v — retrying in %s", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				break loop
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = pollInterval
		if task == nil {
			<-sem
			select {
			case <-time.After(pollInterval):
			case <-ctx.Done():
				break loop
			}
			continue
		}
		wg.Add(1)
		go func(t *TaskSummary) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := dispatchJob(ctx, f, opts.WeftEndpoint, opts.Image, cfg, t); err != nil {
				log.Printf("weft-runner-forgejo: task %d dispatch error: %v", t.ID, err)
			}
		}(task)
	}

	log.Printf("weft-runner-forgejo: ctx cancelled, draining %d in-flight task(s)", len(sem))
	wg.Wait()
	return ctx.Err()
}
