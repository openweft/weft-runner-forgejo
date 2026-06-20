<p align="center"><img src="https://raw.githubusercontent.com/openweft/brand/main/social/openweft.png" alt="openweft" width="720"></p>

# weft-runner-forgejo

Self-hosted Forgejo CI runner backed by **weft** ephemeral microVMs.

## What it does

`weft-runner-forgejo` registers as a Forgejo Actions self-hosted runner
against an instance / organization / repository, then for each task
assigned to it:

1. Asks a weft cluster to spawn a fresh **ephemeral microVM** from an OCI rootfs
   (e.g. `code.forgejo.org/forgejo/runner-images:ubuntu-24.04`) — clean state
   per task, isolated, throwaway.
2. Runs Forgejo's `act_runner` inside that VM via a thin agent (boot-time
   bootstrap drops the runner binary + token + workdir mount).
3. Streams logs back, marks the task done, and tears the VM down.

Sibling of `weft-runner-github` and `weft-runner-gitlab` — the three share
the microVM-spawn primitive but plug into their respective CI control planes.
Each implements the platform's own protocol (Forgejo's Connect-over-JSON
here, GitHub Actions's Runtime API in the GitHub sibling, GitLab's
`/api/v4/jobs/request` in the GitLab one).

## Status

**Operational** — the seven implementation steps in `doc.go` ship :
Register + Declare via Forgejo's `runner-v1` Connect service, long-poll
loop through `FetchTask`, microVM dispatch via weft-client, per-line log
streaming via `UpdateLog` with UTC timestamps, terminal-state transition
via `UpdateTask`. See `doc.go` for the per-step status.

## Quick start

```sh
# 1. Mint a runner registration token from your Forgejo admin UI :
#    Site Administration → Actions → Runners → Create new Runner Token
#    (or the per-org / per-repo equivalent for scoped runners).
#    Forgejo uses runner registration tokens directly — there's no PAT
#    or App indirection, the token IS the bootstrap credential.

# 2. Register the runner.
weft-runner-forgejo register \
  --url https://codeberg.org \
  --registration-token $FORGEJO_RUNNER_TOKEN \
  --name weft-microvm-arm64 \
  --labels "weft,microvm,arm64"

# 3. Start polling for tasks. Each task spawns a fresh microVM on the
#    target weft cluster.
weft-runner-forgejo run \
  --weft-endpoint tcp:weft.example.com:7330 \
  --image code.forgejo.org/forgejo/runner-images:ubuntu-24.04
```

## Architecture

See `doc.go` for the design intent and component boundaries ;
`runner/runner.go` for the lifecycle layer, `runner/forgejo.go` for the
Forgejo Connect-over-JSON client.
