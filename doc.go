// Package main hosts the weft-runner-forgejo binary — a self-hosted Forgejo
// Actions runner that executes each incoming job in a fresh weft microVM.
//
// # Why
//
// The default Forgejo-hosted runners share resources and OS images across
// every customer, and the "runs-on: self-hosted" alternative usually means
// either persistent bare metal (slow to reset, leaks state across jobs) or a
// docker-in-docker shim (no real isolation from the host). weft-runner-forgejo
// gives each job its own VM-isolated environment by riding on the same
// microVM spawn primitive as the rest of weft (`weft microvm run`, OCI rootfs
// → boot under Apple-VZ or QEMU/KVM).
//
// # Components
//
//	[Forgejo Service] ⇄ runner/github.go ⇄ runner/runner.go ⇄ runner/job.go ⇄ [weft cluster]
//	         REST + long-poll       protocol         lifecycle           gRPC
//
//   - runner/github.go: registers the runner against an org/repo/enterprise
//     using a Personal Access Token or Forgejo App installation; long-polls the
//     Actions Runtime API for assigned jobs; reports completion status.
//   - runner/runner.go: the daemon loop — owns the connection to Forgejo, the
//     connection to weft, and the per-job state machine.
//   - runner/job.go: turns one job spec into a microVM lifecycle —
//     RegisterMicroVM → StartVM → stream output → DeleteVM — with a cancel
//     path tied to Forgejo's "cancel" event.
//
// # Sibling runners
//
// weft-runner-gitlab and weft-runner-forgejo share the lifecycle layer
// (anything that talks to weft to spawn / drive / tear down a VM); the
// per-platform code is small (each platform's polling protocol + job spec
// envelope). When two of the three diverge enough to warrant it, the shared
// "microVM job runtime" should split into its own sibling module they all
// import.
//
// # TODO (rough order)
//
//  1. Forgejo CI runner registration via REST (POST /orgs/{org}/actions/runners/registration-token).
//  2. Runner-config persistence + ephemeral-runner semantics.
//  3. Long-poll loop against the Actions Runtime; decode job assignment.
//  4. weft microVM spawn: ImageStore + RegisterMicroVM via weft-client.
//  5. In-VM agent (small Go binary baked into the rootfs) that fetches the
//     runner binary, registers with the per-job token, runs, exits.
//  6. Log streaming back to the controlling weft-runner-forgejo process and
//     thence to Forgejo via the runtime API.
//  7. Cleanup on cancel + idle timeout.
//
package main
