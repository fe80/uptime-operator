#!/usr/bin/env bash
#
# Repeats CRD-level install/uninstall against a live cluster and verifies that
# - install creates the uptimechecks CRD,
# - the operator can serve one apply+delete cycle per round (incl. remote check),
# - uninstall removes the CRD cleanly without leaving cluster-scoped residue.
#
# Run with the operator already running locally (`make run`).

set -euo pipefail

NAMESPACE=${NAMESPACE:-uptime-operator-e2e}
API_URL=${API_URL:-https://sandbox.upeks.net/api/v1/}
API_BASE=${API_BASE:-https://sandbox.upeks.net}
TOKEN=${UPTIME_TOKEN:?must set UPTIME_TOKEN env var}
CYCLES=${CYCLES:-3}

red()   { printf '\033[31m%s\033[0m\n' "$*"; }
green() { printf '\033[32m%s\033[0m\n' "$*"; }
blue()  { printf '\033[34m%s\033[0m\n' "$*"; }

cleanup() {
  kubectl -n "$NAMESPACE" delete uptimechecks --all --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
}
trap cleanup EXIT

assert_no_crd() {
  if kubectl get crd uptimechecks.monitoring.uptime.com >/dev/null 2>&1; then
    red "FAIL: CRD still present after uninstall"
    return 1
  fi
}

assert_crd_present() {
  kubectl get crd uptimechecks.monitoring.uptime.com >/dev/null 2>&1 \
    || { red "FAIL: CRD missing after install"; return 1; }
}

api_get() {
  curl -sS -o /tmp/up-resp -w "%{http_code}" \
    -H "Authorization: Token $TOKEN" \
    "$API_BASE/api/v1/checks/$1/"
}

apply_one_check() {
  local cycle=$1
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
  name: crd-cycle-check
spec:
  type: HTTP
  name: crd-cycle-$cycle
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
    url: https://example.com/crd-cycle-$cycle
    statusCode: "200"
YAML
}

# Start with a clean slate: ensure the CRD is uninstalled.
if kubectl get crd uptimechecks.monitoring.uptime.com >/dev/null 2>&1; then
  kubectl -n "$NAMESPACE" delete uptimechecks --all --ignore-not-found --timeout=60s >/dev/null 2>&1 || true
  make uninstall >/dev/null
fi

for cycle in $(seq 1 "$CYCLES"); do
  blue "=== CRD cycle $cycle/$CYCLES ==="

  blue "  install"
  make install >/dev/null
  assert_crd_present
  green "  CRD installed"

  apply_one_check "$cycle"

  remote_id=""
  for _ in $(seq 1 30); do
    remote_id=$(kubectl -n "$NAMESPACE" get uptimecheck crd-cycle-check -o jsonpath='{.status.remoteCheckID}' 2>/dev/null || true)
    [ -n "$remote_id" ] && [ "$remote_id" != "0" ] && break
    sleep 2
  done
  if [ -z "$remote_id" ] || [ "$remote_id" = "0" ]; then
    red "FAIL: no remoteCheckID for crd-cycle-check"
    exit 1
  fi
  green "  remote check created: $remote_id"

  code=$(api_get "$remote_id")
  if [ "$code" != "200" ]; then
    red "FAIL: expected 200, got $code"
    cat /tmp/up-resp; echo
    exit 1
  fi

  kubectl -n "$NAMESPACE" delete uptimecheck crd-cycle-check --wait=true --timeout=60s >/dev/null
  green "  CR deleted"

  code=$(api_get "$remote_id")
  if [ "$code" != "404" ]; then
    red "FAIL: expected 404 after delete, got $code"
    cat /tmp/up-resp; echo
    exit 1
  fi
  green "  remote check $remote_id is gone"

  blue "  uninstall"
  make uninstall >/dev/null
  # CRD deletion is async; wait for it to fully disappear.
  for _ in $(seq 1 30); do
    if ! kubectl get crd uptimechecks.monitoring.uptime.com >/dev/null 2>&1; then break; fi
    sleep 1
  done
  assert_no_crd
  green "  CRD uninstalled cleanly"
done

green "ALL $CYCLES CRD CYCLES PASSED"
