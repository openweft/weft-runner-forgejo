# weft-runner-forgejo / in-VM runtime image

This directory builds the OCI image booted as a microVM by `weft-runner-forgejo`.
Each Forgejo Actions task runs in a fresh VM with this rootfs.

## Boot contract

`weft-runner-forgejo` (the host daemon) long-polls Forgejo's
`runner.v1.RunnerService.FetchTask` endpoint, then exposes the *credentials*
for that task to the VM through the cfg share. Unlike GitHub's JIT runner —
which can be handed a one-shot config blob and `run.sh --jitconfig` does the
rest — Forgejo's runner is daemon-only and authenticates with its long-lived
runner UUID + token, fetching individual tasks itself.

The cfg-share payload reflects that:

| Path inside VM                       | Producer                | Consumer                       |
| ------------------------------------ | ----------------------- | ------------------------------ |
| `/run/weft/cfg/forgejo-task.json`    | host daemon, before boot| `runner-init` (this image)     |
| `/run/weft-shutdown` (if present)    | weft-init               | `runner-init`, post-exit signal|

The JSON shape (see `../runner/job.go`):

```
{
  "url":        "<forgejo base>",
  "uuid":       "<runner uuid>",
  "token":      "<runner long-lived token>",
  "task_id":    <int>,
  "task_token": "<task-scoped token>",
  "workflow":   "<optional inline workflow yaml>"
}
```

## Protocol — Forgejo vs. GitHub

GitHub publishes a runner binary with a `--jitconfig` flag that consumes a
single-use blob, runs one job, and exits. The whole interaction with GitHub
flows through that binary.

Forgejo's `forgejo-runner` has no such mode. The host daemon and the in-VM
piece both speak **Connect-over-JSON** against
`runner.v1.RunnerService` directly — `Register`, `Declare`, `FetchTask`,
`UpdateLog`, `UpdateTask`. Today the host daemon owns all of that (see
`../runner/forgejo.go`); the cfg-share carries only credentials, not the
task body.

## runner-init today

The current entrypoint validates the boot contract end-to-end:

1. Busy-waits up to 30 s for `forgejo-task.json` to appear on the cfg share.
2. Parses `url`, `uuid`, `token`, `task_id` out of it.
3. Logs them and exits 0 (clean VM shutdown).

The real dispatch step is parked behind a `TODO(milestone-real-dispatch)`
comment in `runner-init.sh`. Two viable paths forward, both follow-ups:

  - Patch `forgejo-runner` upstream to add a one-job mode (we already have
    the runner UUID + token + the pre-assigned task_id on hand inside the VM).
  - Write a small in-VM agent that consumes the FetchTask response directly
    over Connect-over-JSON and drives `act_runner`'s executor library to run
    the steps.

## Build + push

```
docker buildx build \
    --platform linux/amd64,linux/arm64 \
    -t ghcr.io/openweft/weft-runner-forgejo:v0.1.0 \
    --push \
    image/
```

CI (`.github/workflows/image.yml`) builds + pushes on a release tag (`v*`)
or on manual dispatch. The branch-push trigger is intentionally absent —
dev commits to main don't publish images.

## Use with `weft-runner-forgejo`

```
weft-runner-forgejo register \
    --url=https://codeberg.org \
    --registration-token=<one-shot> \
    --config=/etc/weft-runner-forgejo.json
weft-runner-forgejo run \
    --config=/etc/weft-runner-forgejo.json \
    --weft-endpoint=unix:///var/run/weft/agent.sock \
    --image=ghcr.io/openweft/weft-runner-forgejo:v0.1.0
```

## forgejo-runner version

Pinned via the `RUNNER_VERSION` build arg (defaults to 6.1.0). Bumping is
a one-line change; CI will republish on the next tag.
