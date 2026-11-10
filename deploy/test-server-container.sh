#!/bin/sh
set -eu

image=${1:-kakatta:local}
docker run --rm --network none --security-opt seccomp=unconfined -i "$image" timeout 60s sh -eu -s <<'SH'
server_pid=
cleanup() {
    if [ -n "$server_pid" ]; then
        kill -TERM "$server_pid" 2>/dev/null || true
        wait "$server_pid" 2>/dev/null || true
    fi
}
trap cleanup EXIT HUP INT TERM

start_server() {
    kakatta --data /data --listen 127.0.0.1:18080 > /tmp/server.log 2>&1 &
    server_pid=$!
    attempt=0
    until curl --noproxy '*' -fsS http://127.0.0.1:18080/healthz > /tmp/health.json 2>/dev/null; do
        attempt=$((attempt + 1))
        [ "$attempt" -lt 50 ] || exit 1
        kill -0 "$server_pid"
        sleep 0.1
    done
    grep -q '"status":"ok"' /tmp/health.json
}

[ "$(id -u)" -ne 0 ]
start_server
token=$(sed -n 's/.*"token":"\([^"]*\)".*/\1/p' /tmp/server.log)
[ -n "$token" ]
[ "$(stat -c %a /data/secret.key)" = 600 ]
status=$(curl --noproxy '*' -sS -o /tmp/rejected.json -w '%{http_code}' http://127.0.0.1:18080/api/v1/me)
[ "$status" = 401 ]
curl --noproxy '*' -fsS http://127.0.0.1:18080/ > /tmp/index.html
grep -q 'id="root"' /tmp/index.html
printf '{"token":"%s","username":"local-test","password":"local-only-password"}' "$token" > /tmp/setup.json
curl --noproxy '*' -fsS -c /tmp/session.cookies -H 'Content-Type: application/json' \
    --data-binary @/tmp/setup.json http://127.0.0.1:18080/api/v1/setup > /tmp/identity.json
grep -q '"username":"local-test"' /tmp/identity.json
curl --noproxy '*' -fsS -b /tmp/session.cookies http://127.0.0.1:18080/api/v1/settings > /tmp/settings.json
grep -q 'ai.image_count' /tmp/settings.json
curl --noproxy '*' -fsS -b /tmp/session.cookies http://127.0.0.1:18080/api/v1/openapi.json > /tmp/openapi.json
grep -q '"openapi": "3.1.0"' /tmp/openapi.json
kill -TERM "$server_pid"
wait "$server_pid"
server_pid=
start_server
! grep -q '"token":' /tmp/server.log
curl --noproxy '*' -fsS -b /tmp/session.cookies http://127.0.0.1:18080/api/v1/me > /tmp/restarted.json
grep -q '"username":"local-test"' /tmp/restarted.json
kill -TERM "$server_pid"
wait "$server_pid"
server_pid=
kakatta reset-password --data /data --disable-totp < /dev/null > /tmp/empty-password.log 2>&1 && exit 1
printf 'new-local-password\n' | kakatta reset-password --data /data --disable-totp > /tmp/reset.log 2>&1
kakatta disable-totp --data /data > /tmp/totp.log 2>&1
for process in /proc/[0-9]*/comm; do
    [ -r "$process" ] || continue
    name=$(cat "$process" 2>/dev/null || true)
    case "$name" in chrome*|Xvfb|x11vnc) exit 1 ;; esac
done
printf '%s\n' '断网服务回归通过：初始化、认证、设置、静态资源、重启会话、维护命令和关闭清理。'
SH
