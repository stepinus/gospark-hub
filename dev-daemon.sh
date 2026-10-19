#!/usr/bin/env bash
# dev-daemon.sh — sahd with rebuild-on-change (Go has no dev server of its own).
# Env: PORT PUBLIC_PORT PROXY_ENABLED REGISTRY_HOME ENGINE_HOME DATA_DIR DB_PATH
set -uo pipefail
cd "$(dirname "$0")"
BIN=/tmp/gospark-sahd
STAMP=/tmp/gospark-sahd.stamp
pid=

stop() { [ -n "${pid:-}" ] && kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; pid=; }
trap 'stop; exit 0' INT TERM

build() { go build -o "$BIN" ./cmd/sahd && touch "$STAMP"; }

build || exit 1
while :; do
  "$BIN" & pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    sleep 1
    if find cmd internal -name '*.go' -newer "$STAMP" -print -quit | grep -q .; then
      echo "[dev] go change → rebuild"
      stop
      build || true
      break
    fi
  done
  wait "$pid" 2>/dev/null; pid=
  echo "[dev] daemon exited, restarting"
  sleep 1
done
