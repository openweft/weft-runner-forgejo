# syntax=docker/dockerfile:1.7

# In-VM runtime image for weft-runner-forgejo.
#
# Boot contract (see ../runner/job.go):
#   - weft-init mounts the cfg share at /run/weft/cfg/
#   - forgejo-task.json is written there by the host daemon — carries the
#     runner UUID + token + assigned task_id + task_token; the task body
#     itself is fetched from Forgejo over Connect-over-JSON from inside the VM.
#   - ENTRYPOINT busy-waits for that file, then drives the task.

ARG RUNNER_VERSION=6.1.0

FROM debian:bookworm-slim AS runtime
ARG TARGETARCH
ARG RUNNER_VERSION
LABEL org.opencontainers.image.source="https://github.com/openweft/weft-runner-forgejo"
LABEL org.opencontainers.image.description="In-VM rootfs for weft-runner-forgejo (forgejo-runner ${RUNNER_VERSION})"
LABEL org.opencontainers.image.licenses="Apache-2.0"

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates=\* \
        curl=\* \
        tar=\* \
        git=\* \
        jq=\* \
        bash=\* \
    && rm -rf /var/lib/apt/lists/*

# forgejo-runner ships a single static binary per arch — no tarball.
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64|arm64) : ;; \
        *) echo "unsupported arch: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /usr/local/bin/forgejo-runner \
        "https://code.forgejo.org/forgejo/runner/releases/download/v${RUNNER_VERSION}/forgejo-runner-${RUNNER_VERSION}-linux-${TARGETARCH}"; \
    chmod +x /usr/local/bin/forgejo-runner

# uid 1000: matches the conventional non-root user inside the microVM.
RUN useradd --create-home --uid 1000 --shell /bin/bash runner

COPY runner-init.sh /usr/local/bin/runner-init
RUN chmod +x /usr/local/bin/runner-init

ENTRYPOINT ["/usr/local/bin/runner-init"]
