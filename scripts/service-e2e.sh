#!/usr/bin/env bash
# End-to-end check of the background job on a real OS (CI runners only):
# install -> engine running -> crash -> restarted -> uninstall -> stopped.
# It uses the default config/data directories of the current user, so run it
# only on a throwaway machine.
set -euo pipefail

case "$(uname -s)" in
  Linux*) os=linux; exe=e5extend ;;
  Darwin*) os=darwin; exe=e5extend ;;
  MINGW* | MSYS* | CYGWIN*) os=windows; exe=e5extend.exe ;;
  *) echo "unsupported OS"; exit 1 ;;
esac

bin="$PWD/bin"
E="$bin/$exe"
mkdir -p "$bin"
CGO_ENABLED=0 go build -o "$E" ./cmd/e5extend
if [ "$os" = windows ]; then
  # The task runs the console-less build when it sits next to e5extend.exe.
  CGO_ENABLED=0 go build -ldflags=-H=windowsgui -o "$bin/e5extend-bg.exe" ./cmd/e5extend
fi

log() { echo "== $(date -u +%H:%M:%S) $*"; }

# Windows runners ship "python", not always "python3".
PY=$(command -v python3 || command -v python)

engine_level() {
  # doctor exits 1 or 2 when something is not ok; only its JSON matters here.
  { "$E" doctor --offline --json 2>/dev/null || true; } |
    "$PY" -c 'import json,sys; print(next((c["level"] for c in json.load(sys.stdin) if c["id"]=="engine"), "missing"))' ||
    echo error
}

wait_engine() { # wait_engine <want ok|fail> <seconds>
  local want=$1 limit=$2 waited=0 got
  while [ "$waited" -lt "$limit" ]; do
    got=$(engine_level)
    if [ "$got" = "$1" ]; then
      log "engine is $want after ${waited}s"
      return 0
    fi
    sleep 5
    waited=$((waited + 5))
  done
  log "engine still '$got' after ${limit}s, wanted '$want'"
  "$E" service status || true
  return 1
}

engine_pids() {
  if [ "$os" = windows ]; then
    tasklist //FI "IMAGENAME eq e5extend-bg.exe" //FO CSV //NH | grep -i e5extend-bg | cut -d, -f2 | tr -d '"' || true
  else
    pgrep -f "$E run" || true
  fi
}

crash_engine() {
  if [ "$os" = windows ]; then
    taskkill //F //IM e5extend-bg.exe
  else
    pkill -9 -f "$E run"
  fi
}

if [ "$os" = linux ]; then
  # GitHub's Ubuntu runners have no user manager until linger is enabled.
  sudo loginctl enable-linger "$(id -un)"
  export XDG_RUNTIME_DIR="/run/user/$(id -u)"
  export DBUS_SESSION_BUS_ADDRESS="unix:path=$XDG_RUNTIME_DIR/bus"
  for _ in $(seq 1 30); do [ -S "$XDG_RUNTIME_DIR/bus" ] && break; sleep 1; done
fi

"$E" config init
log "install"
"$E" service install
"$E" service status
wait_engine ok 90

before=$(engine_pids)
log "engine pid(s): $before; simulating a crash"
crash_engine
sleep 3

case "$os" in
  windows) restart_limit=420 ;; # the repeat trigger fires every 5 minutes
  *) restart_limit=120 ;;      # systemd RestartSec=30, launchd ThrottleInterval=30
esac
# The heartbeat goes stale 2 minutes after the crash; wait for a fresh one.
sleep 130
wait_engine ok "$restart_limit"
after=$(engine_pids)
log "engine pid(s) after restart: $after"
if [ -z "$after" ] || [ "$after" = "$before" ]; then
  echo "the engine was not restarted"
  exit 1
fi

log "uninstall"
"$E" service uninstall
"$E" service status || true
sleep 5
if [ -n "$(engine_pids)" ]; then
  echo "the engine is still running after uninstall"
  exit 1
fi
log "done"
