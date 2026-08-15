#!/bin/bash
set -euo pipefail

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
APP_APK=${APP_APK:-"$ROOT/dist-test/sol-vpn-debug.apk"}
PROBE_APK=${PROBE_APK:-"$ROOT/dist-test/sol-vpn-probe-debug.apk"}
TOKEN='sol-ci-token-0123456789abcdef'
SERVER_URL='ws://10.0.2.2:10000/ws'
TARGET_URL='http://sol-ci-target.invalid:18080/'

cleanup() {
    kill "${SOL_PID:-}" "${TARGET_PID:-}" 2>/dev/null || true
}
trap cleanup EXIT

fail_with_logs() {
    echo '--- SOL server log ---'
    cat /tmp/sol-ci-server.log 2>/dev/null || true
    echo '--- target server log ---'
    cat /tmp/sol-ci-target.log 2>/dev/null || true
    echo '--- Android crash log ---'
    adb logcat -d -b crash 2>/dev/null || true
    echo '--- SOL Android log ---'
    adb logcat -d 2>/dev/null | grep -E 'bond\.huggy\.sol|SolCore|SolVpnService|hev-socks5|GoLog' | tail -n 300 || true
    exit 1
}

ui_dump() {
    adb shell uiautomator dump /sdcard/sol-window.xml >/dev/null
    adb pull /sdcard/sol-window.xml /tmp/sol-window.xml >/dev/null
}

tap_node() {
    local mode=$1
    local value=$2
    ui_dump
    local coords
    coords=$(python3 - "$mode" "$value" <<'PY'
import re
import sys
import xml.etree.ElementTree as ET

mode, value = sys.argv[1], sys.argv[2]
root = ET.parse('/tmp/sol-window.xml').getroot()
for node in root.iter('node'):
    text = node.attrib.get('text', '')
    resource_id = node.attrib.get('resource-id', '')
    matched = (mode == 'text' and text == value) or (mode == 'resource-suffix' and resource_id.endswith(value))
    if not matched:
        continue
    bounds = node.attrib.get('bounds', '')
    match = re.fullmatch(r'\[(\d+),(\d+)\]\[(\d+),(\d+)\]', bounds)
    if not match:
        continue
    x1, y1, x2, y2 = map(int, match.groups())
    print((x1 + x2) // 2, (y1 + y2) // 2)
    raise SystemExit(0)
raise SystemExit(1)
PY
) || return 1
    adb shell input tap $coords
}

wait_status() {
    local expected=$1
    local i status
    for i in $(seq 1 30); do
        status=$(adb shell am broadcast \
            -n bond.huggy.sol/.DebugControlReceiver \
            -a bond.huggy.sol.DEBUG_STATUS 2>/dev/null | tr -d '\r' || true)
        echo "$status"
        if echo "$status" | grep -q "$expected"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

run_probe() {
    adb shell pm clear bond.huggy.sol.probe >/dev/null || true
    adb shell am broadcast \
        -n bond.huggy.sol.probe/.ProbeReceiver \
        -a bond.huggy.sol.probe.RUN \
        --es url "$TARGET_URL" >/dev/null

    local i prefs
    for i in $(seq 1 30); do
        prefs=$(adb shell run-as bond.huggy.sol.probe cat shared_prefs/probe.xml 2>/dev/null | tr -d '\r' || true)
        echo "$prefs"
        if echo "$prefs" | grep -q 'code=200' && echo "$prefs" | grep -q 'SOL_VPN_E2E_OK'; then
            return 0
        fi
        sleep 1
    done
    return 1
}

cd "$ROOT"

if [ ! -f "$APP_APK" ] || [ ! -f "$PROBE_APK" ]; then
    echo "Missing APKs: $APP_APK or $PROBE_APK" >&2
    exit 2
fi

sudo sh -c "grep -q 'sol-ci-target.invalid' /etc/hosts || echo '127.0.0.1 sol-ci-target.invalid' >> /etc/hosts"
mkdir -p /tmp/sol-ci-target
echo 'SOL_VPN_E2E_OK' >/tmp/sol-ci-target/index.html
python3 -m http.server 18080 --bind 127.0.0.1 --directory /tmp/sol-ci-target >/tmp/sol-ci-target.log 2>&1 &
TARGET_PID=$!

go build -o /tmp/sol-ci .
SOL_TOKEN="$TOKEN" PORT=10000 /tmp/sol-ci server >/tmp/sol-ci-server.log 2>&1 &
SOL_PID=$!

for _ in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:10000/healthz >/dev/null; then
        break
    fi
    sleep 1
done
curl -fsS http://127.0.0.1:10000/healthz >/dev/null || fail_with_logs
curl -fsS http://sol-ci-target.invalid:18080/ | grep -q 'SOL_VPN_E2E_OK' || fail_with_logs

adb install -r "$APP_APK" >/dev/null
adb install -r "$PROBE_APK" >/dev/null
adb shell pm grant bond.huggy.sol android.permission.POST_NOTIFICATIONS 2>/dev/null || true
adb logcat -c

adb shell am start -W \
    -n bond.huggy.sol/.MainActivity \
    --es debug_server "$SERVER_URL" \
    --es debug_token "$TOKEN" >/dev/null

# The activity itself loads libsolcore. Loading TProxyService also loads
# hev-socks5-tunnel and exercises its JNI_OnLoad registration.
wait_status 'solAvailable=true,solRunning=false,tproxyAvailable=true,tproxyRunning=false' || fail_with_logs

# Exercise the same user path that previously crashed: tap Connect, approve the
# Android system VPN dialog, then require both native engines to stay alive.
tap_node text Connect || fail_with_logs
sleep 1
if ! tap_node resource-suffix 'button_start_vpn'; then
    tap_node resource-suffix 'button1' || fail_with_logs
fi
wait_status 'solAvailable=true,solRunning=true,tproxyAvailable=true,tproxyRunning=true' || fail_with_logs

# End-to-end proof from a different Android UID. The hostname exists only in
# the GitHub runner's /etc/hosts, so this succeeds only if mapped DNS is restored
# to a hostname and the TCP request reaches the runner through SOL.
run_probe || fail_with_logs

# Exercise teardown and reconnect twice. This catches JNI/thread lifecycle bugs
# that a one-shot startup check misses.
for cycle in 1 2; do
    echo "Reconnect cycle $cycle"
    tap_node text Disconnect || fail_with_logs
    wait_status 'solAvailable=true,solRunning=false,tproxyAvailable=true,tproxyRunning=false' || fail_with_logs
    tap_node text Connect || fail_with_logs
    wait_status 'solAvailable=true,solRunning=true,tproxyAvailable=true,tproxyRunning=true' || fail_with_logs
    run_probe || fail_with_logs
done

# The app process must still be alive after the repeated connect/disconnect path.
adb shell pidof bond.huggy.sol >/dev/null || fail_with_logs

# Any app crash in the dedicated emulator is a hard failure.
if adb logcat -d -b crash | grep -q 'bond.huggy.sol'; then
    fail_with_logs
fi

echo 'Android VPN emulator smoke test PASS'
