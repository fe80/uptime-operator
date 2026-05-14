#!/usr/bin/env bash
#
# End-to-end smoke test for the uptime-k8s-operator against a live cluster
# (k3s via $KUBECONFIG) and the Uptime.com sandbox API
# (https://sandbox.upeks.net).
#
# What it does, per cycle:
#   1. Apply a Secret + UptimeCheck CR in $NAMESPACE.
#   2. Wait for status.remoteCheckID to be populated by the operator.
#   3. GET that ID from the sandbox API and assert HTTP 200 + expected URL.
#   4. Delete the CR. Wait for the finalizer to disappear (= clean teardown).
#   5. GET that ID from the sandbox API and assert HTTP 404.
#
# Run with:
#   KUBECONFIG=~/.kube/k3s-config UPTIME_TOKEN=... ./test/e2e/e2e_sandbox.sh
#
# The operator itself must already be running (e.g. via `make run`).

set -euo pipefail

NAMESPACE=${NAMESPACE:-uptime-operator-e2e}
API_URL=${API_URL:-https://sandbox.upeks.net/api/v1/}
API_BASE=${API_BASE:-https://sandbox.upeks.net}
TOKEN=${UPTIME_TOKEN:?must set UPTIME_TOKEN env var}
CYCLES=${CYCLES:-3}
RUN_TAG=${RUN_TAG:-e2e-$(date +%s)}

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }

cleanup() {
  # Best-effort teardown so a half-finished cycle does not leave checks behind.
  kubectl -n "$NAMESPACE" delete uptimechecks --all --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
}
trap cleanup EXIT

api_get() {
  curl -sS -o /tmp/up-resp -w "%{http_code}" \
    -H "Authorization: Token $TOKEN" \
    "$API_BASE/api/v1/checks/$1/"
}

apply_check() {
  local name=$1 url=$2
  cat <<YAML | kubectl -n "$NAMESPACE" apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: uptime-token
type: Opaque
stringData:
  token: $TOKEN
---
apiVersion: monitoring.uptime.com/v1alpha1
kind: UptimeCheck
metadata:
  name: $name
spec:
  type: HTTP
  name: k8s-operator e2e $RUN_TAG/$name
  interval: 60
  apiURL: $API_URL
  apiTokenSecretRef:
    name: uptime-token
    key: token
  contactGroups:
    - Default
  locations:
    - US-NY-New York
  http:
    url: $url
    statusCode: "200"
    numRetries: 2
YAML
}

wait_for_remote_id() {
  local name=$1
  local id=""
  for _ in $(seq 1 30); do
    id=$(kubectl -n "$NAMESPACE" get uptimecheck "$name" -o jsonpath='{.status.remoteCheckID}' 2>/dev/null || true)
    if [ -n "$id" ] && [ "$id" != "0" ]; then
      echo "$id"
      return 0
    fi
    sleep 2
  done
  red "FAIL: $name never got remoteCheckID"
  kubectl -n "$NAMESPACE" get uptimecheck "$name" -o yaml || true
  return 1
}

wait_for_gone() {
  local name=$1
  for _ in $(seq 1 30); do
    if ! kubectl -n "$NAMESPACE" get uptimecheck "$name" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  red "FAIL: $name still present after delete"
  kubectl -n "$NAMESPACE" get uptimecheck "$name" -o yaml || true
  return 1
}

assert_remote_present() {
  local id=$1 expected_url=$2
  local code; code=$(api_get "$id")
  if [ "$code" != "200" ]; then
    red "FAIL: sandbox GET /checks/$id/ -> $code (want 200)"
    cat /tmp/up-resp
    echo
    return 1
  fi
  local got_url; got_url=$(python3 -c "import json; print(json.load(open('/tmp/up-resp')).get('msp_address',''))")
  if [ "$got_url" != "$expected_url" ]; then
    red "FAIL: remote msp_address=$got_url want=$expected_url"
    return 1
  fi
  green "  remote check $id present with url=$got_url"
}

assert_remote_absent() {
  local id=$1
  local code; code=$(api_get "$id")
  if [ "$code" != "404" ]; then
    red "FAIL: sandbox GET /checks/$id/ -> $code (want 404)"
    cat /tmp/up-resp
    echo
    return 1
  fi
  green "  remote check $id is gone"
}

for cycle in $(seq 1 "$CYCLES"); do
  blue "=== cycle $cycle/$CYCLES ==="

  apply_check check-a "https://example.com/cycle-$cycle"
  apply_check check-b "https://example.org/cycle-$cycle"

  id_a=$(wait_for_remote_id check-a)
  id_b=$(wait_for_remote_id check-b)
  green "  apply ok: check-a=$id_a check-b=$id_b"

  assert_remote_present "$id_a" "https://example.com/cycle-$cycle"
  assert_remote_present "$id_b" "https://example.org/cycle-$cycle"

  kubectl -n "$NAMESPACE" delete uptimecheck check-a check-b --wait=true --timeout=60s

  wait_for_gone check-a
  wait_for_gone check-b
  green "  k8s objects gone"

  assert_remote_absent "$id_a"
  assert_remote_absent "$id_b"
done

green "ALL $CYCLES CYCLES PASSED"
