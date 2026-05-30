// Package runner is the daemon side of weft-runner-forgejo: it owns the link
// to Forgejo CI (registration + long-poll) and the link to a weft cluster
// (microVM spawn + lifecycle), and coordinates the per-job state machine
// between them.
//
// Today everything below is a stub with explicit boundaries — the function
// signatures are the contract main.go binds to. Each TODO marks a milestone
// from doc.go.
package runner

import (
	"context"
	"errors"
	"fmt"
)

// RegisterOptions are the inputs to `weft-runner-forgejo register`. Owner +
// Token are required. Repo selects a repo-scoped runner (empty = org-wide).
type RegisterOptions struct {
	Owner      string
	Repo       string   // empty = org-wide runner
	Token      string   // PAT or GH App installation token with enough scope to mint a runner reg token
	Labels     []string // appended to the default set Forgejo injects (self-hosted, Linux/macOS/Windows, arch)
	ConfigFile string   // destination JSON
}

// Register obtains a registration token from Forgejo and persists a runner
// config that `Run` can load.
//
// TODO(milestone-1): POST /orgs/{owner}/actions/runners/registration-token
// (or .../repos/{owner}/{repo}/...) using opts.Token; serialise the response
// + labels + scope into opts.ConfigFile.
func Register(opts RegisterOptions) error {
	if opts.Owner == "" || opts.Token == "" {
		return errors.New("register: --owner and --token are required")
	}
	return fmt.Errorf("register: not yet implemented — milestone 1 in doc.go (REST registration-token mint)")
}

// RunOptions configures the long-lived daemon loop. ConfigFile must point at
// a config persisted by Register; WeftEndpoint + Image are required so the
// per-job microVM spawn has something to dial.
type RunOptions struct {
	ConfigFile   string
	WeftEndpoint string // unix:/path or tcp:host:port — passed verbatim to weft-client.Dial
	Image        string // OCI ref the runner rootfs is materialised from
	IdleTimeout  int    // seconds; 0 = no timeout
}

// Run boots the runner daemon: load the persisted config, dial weft, register
// the long-poll session with Forgejo, and serve jobs until ctx is cancelled.
//
// TODO(milestones-2..7): every part below is a placeholder. The function
// returns an explicit "not implemented" rather than silently sleeping so a
// caller can't mistake the skeleton for a working runner.
func Run(_ context.Context, opts RunOptions) error {
	if opts.ConfigFile == "" || opts.WeftEndpoint == "" || opts.Image == "" {
		return errors.New("run: --config, --weft-endpoint, --image are required")
	}
	return fmt.Errorf(
		"run: not yet implemented — milestones 2-7 in doc.go (config load, weft dial, "+
			"GH long-poll, per-job microVM spawn, log stream, cleanup); config=%s endpoint=%s image=%s",
		opts.ConfigFile, opts.WeftEndpoint, opts.Image,
	)
}
