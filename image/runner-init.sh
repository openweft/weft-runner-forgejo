#!/bin/bash
# runner-init — PID-1-adjacent entrypoint for the weft-runner-forgejo microVM.
#
# The host daemon writes the task credentials to /run/weft/cfg/forgejo-task.json
# (see ../runner/job.go for the exact contract). weft-init mounts the cfg share
# very early, but on slower hypervisors the mount can lag a few hundred ms
# behind our exec, so we busy-wait briefly before declaring it missing.

set -euo pipefail

log() { printf 'runner-init: %s\n' "$*" >&2; }

CFG_FILE=/run/weft/cfg/forgejo-task.json
SHUTDOWN_FIFO=/run/weft-shutdown

log "waiting for ${CFG_FILE}"
deadline=$(( $(date +%s) + 30 ))
while [ ! -s "${CFG_FILE}" ]; do
    if [ "$(date +%s)" -ge "${deadline}" ]; then
        log "timeout: ${CFG_FILE} never appeared after 30s; cfg share not mounted?"
        exit 1
    fi
    sleep 0.2
done

URL=$(jq -r '.url'                   <"${CFG_FILE}")
UUID=$(jq -r '.uuid'                 <"${CFG_FILE}")
TOKEN=$(jq -r '.token'               <"${CFG_FILE}")
TASK_ID=$(jq -r '.task_id'           <"${CFG_FILE}")
TASK_TOKEN=$(jq -r '.task_token // ""' <"${CFG_FILE}")
EVENT=$(jq -r '.event // ""'         <"${CFG_FILE}")
MACHINE=$(jq -r '.machine // ""'     <"${CFG_FILE}")
WORKFLOW_BYTES=$(jq -r '.workflow_payload // ""' <"${CFG_FILE}" | wc -c)
EVENT_PAYLOAD_BYTES=$(jq -r '.event_payload // ""' <"${CFG_FILE}" | wc -c)
CONTEXT_KEYS=$(jq -r '(.context // {}) | keys | join(",")' <"${CFG_FILE}")
VARS_KEYS=$(jq -r '(.vars // {}) | keys | join(",")'       <"${CFG_FILE}")
SECRETS_KEYS=$(jq -r '(.secrets // {}) | keys | join(",")' <"${CFG_FILE}")
CONCURRENCY_GROUP=$(jq -r '.concurrency.group // ""'              <"${CFG_FILE}")
CONCURRENCY_CANCEL=$(jq -r '.concurrency.cancel_in_progress // false' <"${CFG_FILE}")

log "task_id=${TASK_ID} uuid=${UUID} url=${URL} token=<${#TOKEN} chars>"
log "task_token=<${#TASK_TOKEN} chars> event=${EVENT} machine=${MACHINE}"
log "workflow_payload=<${WORKFLOW_BYTES} bytes> event_payload=<${EVENT_PAYLOAD_BYTES} bytes>"
log "context_keys=[${CONTEXT_KEYS}] vars_keys=[${VARS_KEYS}]"
# secrets are masked: only key names are logged, never values.
log "secrets=<masked> keys=[${SECRETS_KEYS}]"
log "concurrency group=${CONCURRENCY_GROUP} cancel_in_progress=${CONCURRENCY_CANCEL}"

# TODO(milestone-real-dispatch): forgejo-runner's CLI only exposes daemon mode
# (`forgejo-runner daemon`) which re-registers with the server and long-polls
# for *its own* tasks; it has no "execute exactly this one task" entry point.
# Wiring the one-shot path means either:
#   (a) patching forgejo-runner upstream to add a one-job mode (we have the
#       runner UUID + token + the specific task_id pre-assigned by the host
#       daemon), or
#   (b) writing a small in-VM agent that consumes the FetchTask response
#       directly (Connect-over-JSON, same protocol as the host shim) and
#       drives act_runner's executor library to run the steps.
# For now this image only proves the cfg-share contract end-to-end; the
# host daemon already fetches the task, so the in-VM piece is a follow-up.
log "milestone: cfg-share contract validated, task dispatch is a TODO"

if [ -e "${SHUTDOWN_FIFO}" ]; then
    log "signalling weft-init via ${SHUTDOWN_FIFO}"
    printf 'runner-exit 0\n' >"${SHUTDOWN_FIFO}" || true
fi

exit 0
