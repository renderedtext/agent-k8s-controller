#!/usr/bin/env bash
#
# End-to-end test of the controller. It runs the controller built from this
# checkout in a throwaway k3s cluster in Docker, serving a real Semaphore agent
# type, and checks how it handles the jobs queued for that type. Something else
# queues the jobs: in CI, the other block of .semaphore/e2e.yml.
#
# Required:
#   E2E_SEMAPHORE_ENDPOINT  the organization's endpoint, e.g. example.semaphoreci.com
#   E2E_AGENT_TYPE          the self-hosted agent type to serve
#   E2E_AGENT_TOKEN         its registration token (never printed)
#   E2E_EXPECTED_JOBS       how many jobs will be queued for it
#
# Optional: E2E_PREFIX (Docker object names), E2E_AGENT_IMAGE, E2E_JOB_IMAGE,
# E2E_K3S_IMAGE, E2E_TIMEOUT (seconds), E2E_KEEP=1 to keep the cluster,
# E2E_MIN_PARALLEL (how many agent jobs must have run at once, 1 by default),
# and E2E_JUNIT, where the JUnit report goes (build/e2e-junit.xml by default).
#
# Everything runs in Docker, and kubectl only runs inside the k3s container.
# Output is never piped into `grep -q` or `head`: with pipefail, a reader that
# stops early makes the writer fail, and a match would read as a failure.
#
set -euo pipefail

: "${E2E_SEMAPHORE_ENDPOINT:?}" "${E2E_AGENT_TYPE:?}" "${E2E_AGENT_TOKEN:?}" "${E2E_EXPECTED_JOBS:?}"

PREFIX="${E2E_PREFIX:-agent-k8s-controller-e2e}"
K3S_IMAGE="${E2E_K3S_IMAGE:-rancher/k3s:v1.34.1-k3s1}"
AGENT_IMAGE="${E2E_AGENT_IMAGE:-semaphoreci/agent:v2.6.0}"
JOB_IMAGE="${E2E_JOB_IMAGE:-ubuntu:24.04}"
TIMEOUT="${E2E_TIMEOUT:-900}"
MIN_PARALLEL="${E2E_MIN_PARALLEL:-1}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
MANIFESTS="$ROOT/test/e2e/manifests"
K3S="$PREFIX-k3s"
CONTROLLER_IMAGE="$PREFIX/controller:e2e"
NS=semaphore
SECRET=e2e-agent-type
MAX_PARALLEL_JOBS=2

AGENT_PODS="semaphoreci.com/resource-type=semaphore-job"
JOB_PODS="semaphoreci.com/agent-type,!semaphoreci.com/resource-type"

REPORT="${E2E_JUNIT:-$ROOT/build/e2e-junit.xml}"
CASES="$REPORT.cases"
CURRENT_CHECK=""
CHECK_STARTED=0
FAILURE=""
K3S_ID=""
CANCELED_JOB=""

log() { echo "[e2e $(date -u +%H:%M:%S)] $*"; }
fail() { FAILURE="$*"; log "FAIL: $*"; exit 1; }
kx() { docker exec "$K3S" kubectl "$@"; }
kxi() { docker exec -i "$K3S" kubectl "$@"; }
kxn() { kx -n "$NS" "$@"; }
controller_log() { kxn logs deploy/agent-k8s-controller --tail=-1 2>/dev/null || true; }
controller_logged() { [ -n "$(controller_log | grep -E "$1" || true)" ]; }
first() { awk 'NR == 1'; }

# until_ok <seconds> <what> <command...>: retries the command every 2s.
until_ok() {
  local timeout=$1 what=$2 deadline
  shift 2
  deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    [ "$SECONDS" -lt "$deadline" ] || fail "timed out after ${timeout}s waiting for $what"
    sleep 2
  done
}

xml_escape() { sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g' -e 's/"/\&quot;/g'; }

# record_case <name> <seconds> [failure message]: adds a test case to the JUnit report.
record_case() {
  local name message
  name=$(printf '%s' "$1" | xml_escape)
  if [ -z "${3:-}" ]; then
    printf '    <testcase classname="e2e" name="%s" time="%s"/>\n' "$name" "$2" >>"$CASES"
    return
  fi

  message=$(printf '%s' "$3" | xml_escape)
  printf '    <testcase classname="e2e" name="%s" time="%s">\n      <failure message="%s">%s</failure>\n    </testcase>\n' \
    "$name" "$2" "$message" "$message" >>"$CASES"
}

# check <name> <command...>: runs one check of the test and records how it went.
check() {
  CURRENT_CHECK=$1
  CHECK_STARTED=$SECONDS
  shift
  "$@"
  record_case "$CURRENT_CHECK" $((SECONDS - CHECK_STARTED))
  CURRENT_CHECK=""
}

write_report() {
  local tests failures
  tests=$(grep -c '<testcase' "$CASES" || true)
  failures=$(grep -c '<failure' "$CASES" || true)
  {
    echo '<?xml version="1.0" encoding="UTF-8"?>'
    printf '<testsuites name="E2E tests" tests="%s" failures="%s" time="%s">\n' "$tests" "$failures" "$SECONDS"
    printf '  <testsuite name="Controller in a Kubernetes cluster" tests="%s" failures="%s" time="%s">\n' "$tests" "$failures" "$SECONDS"
    cat "$CASES"
    echo '  </testsuite>'
    echo '</testsuites>'
  } >"$REPORT"
  rm -f "$CASES"
  log "JUnit report: $REPORT"
}

render() {
  sed -e "s#@@CONTROLLER_IMAGE@@#$CONTROLLER_IMAGE#g" \
    -e "s#@@AGENT_IMAGE@@#$AGENT_IMAGE#g" \
    -e "s#@@ENDPOINT@@#$E2E_SEMAPHORE_ENDPOINT#g" \
    -e "s#@@MAX_PARALLEL_JOBS@@#$MAX_PARALLEL_JOBS#g" \
    "$MANIFESTS/$1"
}

# Keeps going on errors, so the cluster is always removed. It removes the
# cluster by ID: a container with the same name may belong to another run.
on_exit() {
  local status=$?
  set +e
  if [ -n "$CURRENT_CHECK" ]; then
    record_case "$CURRENT_CHECK" $((SECONDS - CHECK_STARTED)) "${FAILURE:-exited with status $status}"
  fi
  write_report

  if [ "$status" -ne 0 ] && [ -n "$K3S_ID" ]; then
    log "diagnostics"
    kx get nodes,pods,jobs -A -o wide || true
    kx get events -A --sort-by=.lastTimestamp 2>/dev/null | tail -40 || true
    controller_log | tail -200
    for pod in $(kxn get pods -l "$AGENT_PODS" -o name 2>/dev/null || true); do
      echo "--- $pod"
      kxn logs "$pod" --tail=40 2>/dev/null | grep -aviE 'token|password|authorization|Sending Instruction' || true
    done
  fi

  if [ -n "$K3S_ID" ] && [ -z "${E2E_KEEP:-}" ]; then
    docker rm -f -v "$K3S_ID" >/dev/null 2>&1
  fi

  exit "$status"
}

build_controller() {
  local go_image
  go_image=$(awk '/^FROM/ { print $2; exit }' "$ROOT/Dockerfile.dev")
  log "building the controller with $go_image"
  # The checkout can belong to another user than the container's root,
  # and git then refuses to give the version information go build stamps.
  docker run --rm -v "$ROOT:/src" -w /src -v "$PREFIX-gomod:/go/pkg/mod" -e CGO_ENABLED=0 \
    "$go_image" go build -buildvcs=false -o build/controller .
  docker build -q -t "$CONTROLLER_IMAGE" "$ROOT" >/dev/null
}

start_cluster() {
  log "starting $K3S_IMAGE"
  K3S_ID=$(docker run -d --name "$K3S" --privileged --tmpfs /run --tmpfs /var/run "$K3S_IMAGE" \
    server --disable=traefik --disable=metrics-server)
  until_ok 180 "the node" kx wait --for=condition=Ready node --all --timeout=5s
  until_ok 180 "CoreDNS" kx -n kube-system rollout status deploy/coredns --timeout=5s
}

import_image() {
  docker image inspect "$1" >/dev/null 2>&1 || docker pull -q "$1" >/dev/null
  docker save "$1" | docker exec -i "$K3S" ctr -n k8s.io images import - >/dev/null
  log "imported $1"
}

invalid_config_rejected() {
  [ "$(kxn get pod e2e-invalid-config -o jsonpath='{.status.phase}')" = Failed ]
}

# Runs before the node is tainted: this pod tolerates nothing.
check_invalid_controller_config() {
  log "an invalid SEMAPHORE_AGENT_POD_SPEC stops the controller at startup"
  until_ok 60 "the default service account" kxn get serviceaccount default
  kxn run e2e-invalid-config --image="$CONTROLLER_IMAGE" --image-pull-policy=Never --restart=Never \
    --env="SEMAPHORE_ENDPOINT=$E2E_SEMAPHORE_ENDPOINT" \
    --env='SEMAPHORE_AGENT_POD_SPEC={"nodeSelectors":{"e2e/pool":"agents"}}' >/dev/null
  until_ok 90 "the controller to reject the configuration" invalid_config_rejected
  [ -n "$(kxn logs e2e-invalid-config | grep 'invalid SEMAPHORE_AGENT_POD_SPEC' || true)" ] ||
    fail "the controller did not say why it stopped"
  kxn delete pod e2e-invalid-config --wait=false >/dev/null
}

node_name() { kx get nodes -o jsonpath='{.items[0].metadata.name}'; }

prepare_cluster() {
  build_controller
  start_cluster
  for image in "$CONTROLLER_IMAGE" "$AGENT_IMAGE" "$JOB_IMAGE"; do
    import_image "$image"
  done

  render cluster.yaml | kxi apply -f - >/dev/null
  kx label node "$(node_name)" e2e/pool=agents --overwrite >/dev/null
}

deploy_controller() {
  kx taint node "$(node_name)" e2e/dedicated=agents:NoSchedule --overwrite >/dev/null

  #
  # The agent type starts with an agentPodSpec no node matches. It replaces
  # the controller's own spec as a whole, so its first agent pods can never
  # be scheduled.
  #
  log "configuring agent type $E2E_AGENT_TYPE (token not shown)"
  printf '%s' "$E2E_AGENT_TOKEN" | kxi -n "$NS" create secret generic "$SECRET" \
    --from-literal=agentTypeName="$E2E_AGENT_TYPE" \
    --from-literal=agentStartupParameters="--kubernetes-default-image $JOB_IMAGE --kubernetes-pod-spec e2e-job-pod-spec --kubernetes-pod-start-timeout 120" \
    --from-literal=agentPodSpec='{"nodeSelector":{"e2e/pool":"nowhere"}}' \
    --from-file=registrationToken=/dev/stdin >/dev/null
  kxn label secret "$SECRET" semaphoreci.com/resource-type=agent-type-configuration >/dev/null

  render controller.yaml | kxi apply -f - >/dev/null
  until_ok 120 "the controller" kxn rollout status deploy/agent-k8s-controller --timeout=5s
}

check_invalid_update_is_ignored() {
  log "an invalid change to the agent type secret is ignored"
  kxn patch secret "$SECRET" --type merge -p '{"stringData":{"agentPodSpec":"nodeSelectors: {}"}}' >/dev/null
  until_ok 60 "the invalid change to be logged" controller_logged 'ignoring the update'
  kxn patch secret "$SECRET" --type merge \
    -p '{"stringData":{"agentPodSpec":"{\"nodeSelector\":{\"e2e/pool\":\"nowhere\"}}"}}' >/dev/null
}

check_unschedulable_agent_pod() {
  # The agent type's spec replaces the controller's, toleration included,
  # so the taint must be part of the reason.
  local reported='job did not start in time.*was not scheduled.*untolerated taint' line
  log "phase 1: an agent pod no node can take is reported and replaced"
  until_ok 420 "an unschedulable agent pod to be reported" controller_logged "$reported"
  line=$(controller_log | grep -m1 -E "$reported" || true)
  echo "$line" | cut -c1-400
  CANCELED_JOB=$(echo "$line" | sed -E 's/.* job="([^"]+)".*/\1/')

  # The fix applies to the next agent pod, which gets the controller's spec.
  kxn patch secret "$SECRET" --type merge -p '{"data":{"agentPodSpec":null}}' >/dev/null
  log "removed agentPodSpec from the agent type secret"
}

# One line per agent Job: <name> <succeeded>x <failed>x <deletionTimestamp>x
agent_jobs() {
  kxn get jobs -l "$AGENT_PODS" \
    -o jsonpath='{range .items[*]}{.metadata.name} {.status.succeeded}x {.status.failed}x {.metadata.deletionTimestamp}x{"\n"}{end}'
}

check_agent_pod() {
  local pod=$1 spec
  spec=$(kxn get "$pod" -o jsonpath='{.spec.nodeSelector} {.spec.tolerations} {.spec.topologySpreadConstraints} {.spec.priorityClassName} {.spec.priority}')
  case "$spec" in *'"e2e/pool":"agents"'*) ;; *) fail "$pod: nodeSelector not applied: $spec" ;; esac
  case "$spec" in *'"key":"e2e/dedicated"'*) ;; *) fail "$pod: toleration not applied: $spec" ;; esac
  case "$spec" in *'"topologyKey":"kubernetes.io/hostname"'*) ;; *) fail "$pod: spread constraint not applied: $spec" ;; esac
  case "$spec" in *' e2e-agents 1000') ;; *) fail "$pod: priority class not applied: $spec" ;; esac
  log "$pod runs on the tainted node with the controller's scheduling fields"
}

check_job_pod() {
  local pod=$1 spec
  spec=$(kxn get "$pod" -o jsonpath='{.spec.tolerations} {.metadata.ownerReferences[*].kind}')
  case "$spec" in *'"key":"e2e/dedicated"'*) ;; *) fail "$pod: job pod spec not applied: $spec" ;; esac
  case "$spec" in *' Pod') ;; *) fail "$pod: not owned by its agent pod: $spec" ;; esac
  log "$pod has the job pod spec and is owned by its agent pod"
}

# A restart or a panic means the controller crashed. Checked before the planned
# restart too, since replacing the pod resets its restart count and its log.
assert_controller_healthy() {
  local restarts
  restarts=$(kxn get pods -l app=agent-k8s-controller -o jsonpath='{.items[*].status.containerStatuses[*].restartCount}')
  [ "$restarts" = "0" ] || fail "the controller restarted on its own: '${restarts:-no controller pod}'"
  ! controller_logged 'panic' || fail "the controller panicked"
  ! controller_logged 'forbidden' ||
    fail "the controller was refused a permission: $(controller_log | grep -m1 forbidden || true)"
}

# restart_controller <job id>: restarts the controller while that job runs.
restart_controller() {
  log "restarting the controller while job $1 runs"
  assert_controller_healthy
  kxn rollout restart deploy/agent-k8s-controller >/dev/null
  until_ok 120 "the restarted controller" kxn rollout status deploy/agent-k8s-controller --timeout=5s
  until_ok 60 "the restarted controller to load job $1" controller_logged "\"Job loaded\" job=\"$1\""
}

run_jobs() {
  local deadline=$((SECONDS + TIMEOUT)) jobs failed finished=0 active max_active=0 pod started
  local restarted="" agent_pod_checked="" job_pod_checked=""
  log "phase 2: running $E2E_EXPECTED_JOBS jobs, at most $MAX_PARALLEL_JOBS at a time"

  while [ "$finished" -lt "$E2E_EXPECTED_JOBS" ]; do
    [ "$SECONDS" -lt "$deadline" ] || fail "timed out: $finished of $E2E_EXPECTED_JOBS jobs finished"

    jobs=$(agent_jobs)
    failed=$(echo "$jobs" | awk 'NF && $3 != "x"')
    [ -z "$failed" ] || fail "an agent Job failed: $failed"

    finished=$(echo "$jobs" | awk 'NF && $2 != "x" { n++ } END { print n + 0 }')
    active=$(echo "$jobs" | awk 'NF && $2 == "x" && $4 == "x" { n++ } END { print n + 0 }')
    [ "$active" -le "$max_active" ] || max_active=$active
    [ "$active" -le "$MAX_PARALLEL_JOBS" ] || fail "$active agent Jobs active, MAX_PARALLEL_JOBS is $MAX_PARALLEL_JOBS"

    if [ -z "$agent_pod_checked" ]; then
      pod=$(kxn get pods -l "$AGENT_PODS" --field-selector=status.phase=Running -o name | first)
      if [ -n "$pod" ]; then
        check_agent_pod "$pod"
        record_case "agent pods get the controller's scheduling fields" 0
        agent_pod_checked=1
      fi
    fi

    pod=$(kxn get pods -l "$JOB_PODS" --field-selector=status.phase=Running -o name | first)
    if [ -n "$pod" ] && [ -z "$job_pod_checked" ]; then
      check_job_pod "$pod"
      record_case "job pods get the job pod spec and are owned by their agent pod" 0
      job_pod_checked=1
    fi

    if [ -n "$pod" ] && [ -z "$restarted" ]; then
      started=$SECONDS
      restart_controller "${pod#pod/semaphore-job-}"
      record_case "the controller restarts and reloads the running jobs" $((SECONDS - started))
      restarted=1
    fi

    sleep 2
  done

  [ -n "$agent_pod_checked" ] || fail "no running agent pod was seen"
  [ -n "$job_pod_checked" ] || fail "no running job pod was seen"
  [ -n "$restarted" ] || fail "the controller was not restarted while a job ran"
  [ "$max_active" -ge "$MIN_PARALLEL" ] ||
    fail "at most $max_active agent Jobs ran at once, expected at least $MIN_PARALLEL"

  [ "$(kxn get job "semaphore-agent-$CANCELED_JOB" -o jsonpath='{.status.succeeded}')" = 1 ] ||
    fail "job $CANCELED_JOB was canceled in phase 1 and did not run again"
  record_case "a canceled job is created again and runs" 0
  log "$finished jobs finished, at most $max_active at a time"
}

# A kubectl error must not read as "nothing left".
no_job_leftovers() {
  local pods secrets
  pods=$(kxn get pods -l "$JOB_PODS" -o name) || return 1
  secrets=$(kxn get secrets -o name) || return 1
  [ -z "$pods" ] && [ -z "$(echo "$secrets" | grep semaphore-job- || true)" ]
}

check_cleanup() {
  log "phase 3: nothing is left behind"
  until_ok 120 "job pods and secrets to be removed" no_job_leftovers
  assert_controller_healthy
}

main() {
  if docker inspect "$K3S" >/dev/null 2>&1; then
    echo "$K3S already exists; remove it or set E2E_PREFIX" >&2
    exit 1
  fi

  mkdir -p "$(dirname "$REPORT")"
  : >"$CASES"
  trap on_exit EXIT

  check "the cluster starts with the controller's images" prepare_cluster
  check "an invalid SEMAPHORE_AGENT_POD_SPEC stops the controller at startup" check_invalid_controller_config
  check "the controller starts and serves the agent type" deploy_controller
  check "an invalid change to the agent type secret is ignored" check_invalid_update_is_ignored
  check "an agent pod no node can take is reported with its reason and replaced" check_unschedulable_agent_pod
  check "the jobs finish, at most MAX_PARALLEL_JOBS at a time" run_jobs
  check "nothing is left behind" check_cleanup
  log "PASS"
}

main "$@"
