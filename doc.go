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
//	[Forgejo Service] ⇄ runner/forgejo.go ⇄ runner/runner.go ⇄ runner/job.go ⇄ [weft cluster]
//	         Connect-over-JSON       lifecycle         per-task            gRPC
//
//   - runner/forgejo.go: registers the runner against an instance / org / repo
//     using a runner registration token minted in the Forgejo admin UI ;
//     long-polls FetchTask on the runner-v1 Connect service ; reports
//     completion via UpdateTask.
//   - runner/runner.go: the daemon loop — owns the connection to Forgejo, the
//     connection to weft, and the per-task state machine.
//   - runner/job.go: turns one task spec into a microVM lifecycle —
//     RegisterMicroVM → StartVM → stream output → DeleteVM — with a cancel
//     path tied to Forgejo's task cancellation signal.
//
// # Sibling runners
//
// All three runners (weft-runner-github, weft-runner-gitlab,
// weft-runner-forgejo) share the lifecycle layer (anything that talks to
// weft to spawn / drive / tear down a VM); the per-platform code is small
// (each platform's polling protocol + task spec envelope). When the three
// diverge enough to warrant it, the shared "microVM job runtime" should
// split into its own sibling module they all import.
//
// # Status (2026-06)
//
//  1. ✓ Forgejo CI runner registration via Connect-over-JSON (Register
//     + Declare against the Forgejo runner-v1 service).
//  2. ✓ Runner-config persistence + ephemeral-runner semantics
//     (PersistedConfig + per-task FetchTask poll).
//  3. ✓ Long-poll loop : the Run worker calls FetchTask, dispatches to
//     a microVM, then drives UpdateLog + UpdateTask to closure.
//  4. ✓ weft microVM spawn via dispatchJob → weft-client RegisterMicroVM.
//  5. ✓ In-VM agent : the runner image ships the Forgejo act_runner
//     reading the per-task spec from /run/weft/cfg/. Image side ; this
//     daemon only puts the file there via the share.
//  6. ✓ Log streaming : updateLog splits the in-VM stdout into per-line
//     rows with UTC timestamps and ships them through Forgejo's
//     UpdateLog RPC. updateTask transitions to SUCCESS / FAILURE /
//     CANCELLED on completion.
//  7. ✓ Cleanup on cancel + idle timeout : worker goroutines honour ctx.
//
// All seven items shipped. Subsequent work focuses on observability
// (per-job timing, queue-depth metrics) and on the shared microVM-job
// runtime split mentioned above — neither is functional surface.
package main
