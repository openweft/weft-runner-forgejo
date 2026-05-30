# weft-runner-forgejo

Self-hosted Forgejo CI runner backed by **weft** ephemeral microVMs.

## What it does

`weft-runner-forgejo` registers as a Forgejo self-hosted runner against
an org / repo / enterprise, then for each job assigned to it:

1. Asks a weft cluster to spawn a fresh **ephemeral microVM** from an OCI rootfs
   (e.g. `ghcr.io/actions/runner-images-arm64:ubuntu-24.04`) — clean state per
   job, isolated, throwaway.
2. Runs the actions/runner workflow inside that VM via a thin agent (boot-time
   bootstrap drops the runner binary + token + workdir mount).
3. Streams logs back, marks the job done, and tears the VM down.

Sibling of `weft-runner-gitlab` and `weft-runner-forgejo`; the three share the
microVM-spawn primitive but plug into their respective CI control planes.

## Status

**Bootstrap skeleton** — module layout, CLI commands, interface boundaries.
Nothing runs end-to-end yet. The Forgejo CI runner protocol integration
and the weft client wiring are the next milestones (see TODO in `doc.go`).

## Quick start (target shape)

```sh
# Register the runner against a Forgejo org (uses a registration token from a
# PAT or Forgejo App, mints an org-wide ephemeral runner config):
weft-runner-forgejo register \
  --owner my-org \
  --token $GITHUB_PAT \
  --labels "weft,microvm,arm64"

# Start polling for jobs. Each job spawns a fresh microVM on the target
# weft cluster.
weft-runner-forgejo run \
  --weft-endpoint tcp:weft.example.com:7330 \
  --image ghcr.io/actions/runner-images-arm64:ubuntu-24.04
```

## Architecture

See `doc.go` for the design intent and component boundaries; `runner/runner.go`
for the core types and stubs.
