#!/usr/bin/env bash
# End-to-end check of `e5extend service install --headless` on a Windows CI
# runner (they run as an administrator): the task is registered with logon
# type S4U and a boot trigger, the DPAPI key file exists with a locked-down
# ACL, doctor accepts it, the engine process starts, and uninstall removes the
# task and the key file. It uses the default config/data directories of the
# current user, so run it only on a throwaway machine.
set -euo pipefail

case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*) ;;
  *) echo "headless mode is Windows only; nothing to do"; exit 0 ;;
esac

if ! net session >/dev/null 2>&1; then
  echo "this shell is not elevated; --headless needs an administrator prompt, skipping"
  exit 0
fi

bin="$PWD/bin"
E="$bin/e5extend.exe"
mkdir -p "$bin"
CGO_ENABLED=0 go build -o "$E" ./cmd/e5extend
CGO_ENABLED=0 go build -ldflags=-H=windowsgui -o "$bin/e5extend-bg.exe" ./cmd/e5extend

log() { echo "== $(date -u +%H:%M:%S) $*"; }
PY=$(command -v python3 || command -v python)
ps() { powershell.exe -NoProfile -NonInteractive -Command "$1" | tr -d '\r'; }
fail() { echo "FAIL: $*"; "$E" service status || true; schtasks //Query //TN e5extend //V //FO LIST || true; exit 1; }

keyfile="$(cygpath -w "$APPDATA")\\e5extend\\master.key.dpapi"

"$E" config init || true  # the earlier e2e step may have created it
# Hand the key over in the environment so the run does not depend on Credential
# Manager being reachable from the runner's service session.
MASTER=$("$PY" -c 'import os,base64;print(base64.b64encode(os.urandom(32)).decode())')

log "install --headless"
E5EXTEND_MASTER_KEY="$MASTER" "$E" service install --headless

status=$("$E" service status)
echo "$status"
echo "$status" | grep -q '^headless:  yes' || fail "service status does not say headless: yes"

log "task definition"
logon=$(ps '(Get-ScheduledTask -TaskName e5extend).Principal.LogonType')
[ "$logon" = "S4U" ] || fail "LogonType is '$logon', want S4U"
triggers=$(ps '(Get-ScheduledTask -TaskName e5extend).Triggers | ForEach-Object { $_.CimClass.CimClassName }')
for t in MSFT_TaskBootTrigger MSFT_TaskLogonTrigger MSFT_TaskTimeTrigger; do
  echo "$triggers" | grep -q "$t" || fail "trigger $t is missing (have: $triggers)"
done

log "key file"
[ -f "$keyfile" ] || fail "the key file $keyfile does not exist"
if grep -q "$MASTER" "$keyfile"; then fail "the plain key is in the key file"; fi
acl=$(icacls "$keyfile")
echo "$acl"
echo "$acl" | grep -Eqi '\(I\)' && fail "the key file still has inherited access entries"
echo "$acl" | grep -Eqi 'Everyone|BUILTIN\\Users|Authenticated Users' && fail "the key file is open to a broad group"

log "doctor"
level=$( { "$E" doctor --offline --json 2>/dev/null || true; } |
  "$PY" -c 'import json,sys; print(next((c["level"] for c in json.load(sys.stdin) if c["id"]=="headless"), "missing"))')
[ "$level" = "ok" ] || fail "doctor's headless check is '$level'"

log "engine process"
waited=0
until tasklist //FI "IMAGENAME eq e5extend-bg.exe" //FO CSV //NH | grep -qi e5extend-bg; do
  [ "$waited" -ge 120 ] && fail "e5extend-bg.exe did not start within 120s"
  sleep 5
  waited=$((waited + 5))
done
log "e5extend-bg.exe is running after ${waited}s"

log "uninstall"
"$E" service uninstall
if schtasks //Query //TN e5extend >/dev/null 2>&1; then fail "the task is still registered"; fi
[ ! -e "$keyfile" ] || fail "the key file was not removed"
log "done"
