// Command weft-runner-forgejo wires the cobra entry point. The
// daemon/lifecycle code lives in runner/, the Forgejo CI integration in
// runner/github.go, and the per-job microVM logic in runner/job.go. See
// doc.go for the design intent.
package main

import (
	"fmt"
	"os"

	"github.com/openweft/weft-runner-forgejo/runner"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "weft-runner-forgejo",
		Short: "Self-hosted Forgejo CI runner backed by weft ephemeral microVMs",
	}
	root.AddCommand(registerCmd(), runCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// registerCmd is the one-shot "obtain a registration token and persist a
// runner config" step. Distinct from `run` so an operator can re-register
// against a different scope without leaking the old token into the daemon's
// memory.
func registerCmd() *cobra.Command {
	var owner, repo, token, configFile string
	var labels []string
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register against an org or org/repo and write a runner config to disk",
		Long: `Mints a registration token via the Forgejo REST API
(/orgs/<owner>/actions/runners/registration-token, or .../repos/<owner>/<repo>/...
for the repo-scoped variant) and writes a JSON runner config to --config.
The config is then read by ` + "`run`" + ` to dial back into Forgejo.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runner.Register(runner.RegisterOptions{
				Owner:      owner,
				Repo:       repo,
				Token:      token,
				Labels:     labels,
				ConfigFile: configFile,
			})
		},
	}
	cmd.Flags().StringVar(&owner, "owner", "", "Forgejo org or user that owns the runner scope (required)")
	cmd.Flags().StringVar(&repo, "repo", "", "Optional repo name; empty = org-wide runner")
	cmd.Flags().StringVar(&token, "token", "", "PAT or Forgejo App installation token used to mint the runner registration token (required)")
	cmd.Flags().StringSliceVar(&labels, "labels", []string{"weft", "microvm"}, "Labels exposed to workflow runs-on filters")
	cmd.Flags().StringVar(&configFile, "config", "weft-runner-forgejo.json", "Path to write the persisted runner config")
	return cmd
}

// runCmd is the long-lived daemon: read the config minted by `register`,
// long-poll Forgejo for job assignments, spawn a fresh microVM per job through
// the weft cluster, stream logs, mark done.
func runCmd() *cobra.Command {
	var configFile, weftEndpoint, image string
	var idleTimeoutSecs int
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Long-lived runner loop — poll Forgejo, dispatch jobs into microVMs",
		RunE: func(c *cobra.Command, _ []string) error {
			return runner.Run(c.Context(), runner.RunOptions{
				ConfigFile:    configFile,
				WeftEndpoint:  weftEndpoint,
				Image:         image,
				IdleTimeout:   idleTimeoutSecs,
			})
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "weft-runner-forgejo.json", "Runner config written by `register`")
	cmd.Flags().StringVar(&weftEndpoint, "weft-endpoint", "", "weft control-plane target — unix:/path or tcp:host:port (required)")
	cmd.Flags().StringVar(&image, "image", "", "OCI image ref used as the per-job microVM rootfs (required)")
	cmd.Flags().IntVar(&idleTimeoutSecs, "idle-timeout", 0, "Exit after this many seconds with no job assignment (0 = no timeout)")
	return cmd
}
