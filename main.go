// Command weft-runner-forgejo wires the cobra entry point. The daemon /
// lifecycle code lives in runner/, the Forgejo Connect RPC integration in
// runner/forgejo.go, and the per-job microVM logic in runner/job.go. See
// doc.go for the design intent.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/openweft/weft-runner-forgejo/runner"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{
		Use:   "weft-runner-forgejo",
		Short: "Self-hosted Forgejo/Gitea Actions runner backed by weft ephemeral microVMs",
	}
	root.AddCommand(registerCmd(), runCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// registerCmd exchanges a Forgejo registration token for a runner UUID +
// token via the Connect RPC RunnerService.Register endpoint and persists
// the credential to disk. The op mints the registration token in Forgejo's
// Site Administration → Actions → Runners page.
func registerCmd() *cobra.Command {
	var url, regToken, name, configFile string
	var labels []string
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Exchange a Forgejo registration token for a runner credential and persist it",
		Long: `Calls /api/actions/runner.v1.RunnerService/Register with the registration
token you minted in Forgejo's admin UI and writes the returned UUID + long-
lived token to --config. ` + "`run`" + ` reads that config to long-poll for tasks.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runner.Register(runner.RegisterOptions{
				URL:               url,
				RegistrationToken: regToken,
				Name:              name,
				Labels:            labels,
				ConfigFile:        configFile,
			})
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "Forgejo base URL (e.g. https://codeberg.org) — required")
	cmd.Flags().StringVar(&regToken, "registration-token", "", "Registration token minted in the Forgejo admin UI (required)")
	cmd.Flags().StringVar(&name, "name", "", "Runner display name (default: weft-<os>-<arch>)")
	cmd.Flags().StringSliceVar(&labels, "labels", []string{"weft", "microvm"}, "Labels matched by workflows' `runs-on:` filters")
	cmd.Flags().StringVar(&configFile, "config", "weft-runner-forgejo.json", "Path to write the persisted runner config")
	return cmd
}

// runCmd boots the long-lived daemon. SIGTERM/SIGINT trigger context
// cancellation so the runner drains in-flight tasks cleanly.
func runCmd() *cobra.Command {
	var configFile, weftEndpoint, image string
	var concurrency, pollInterval int
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Long-lived runner loop — poll Forgejo, dispatch tasks into microVMs",
		RunE: func(_ *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			return runner.Run(ctx, runner.RunOptions{
				ConfigFile:   configFile,
				WeftEndpoint: weftEndpoint,
				Image:        image,
				Concurrency:  concurrency,
				PollInterval: pollInterval,
			})
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "weft-runner-forgejo.json", "Runner config written by `register`")
	cmd.Flags().StringVar(&weftEndpoint, "weft-endpoint", "", "weft control-plane target — unix:/path or tcp:host:port (required)")
	cmd.Flags().StringVar(&image, "image", "", "OCI image ref used as the per-task microVM rootfs fallback (required)")
	cmd.Flags().IntVar(&concurrency, "concurrency", 1, "Maximum in-flight tasks (microVMs)")
	cmd.Flags().IntVar(&pollInterval, "poll-interval", 3, "Seconds between FetchTask long-polls when idle")
	return cmd
}
