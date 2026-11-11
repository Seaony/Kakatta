#!/bin/sh
set -eu

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
image=${1:-kakatta:local}
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
case "$architecture" in amd64|arm64) ;; *) printf '不支持的镜像架构：%s\n' "$architecture" >&2; exit 1 ;; esac

mkdir -p build
work=$(mktemp -d "$repo/build/core-flow.XXXXXX")
cleanup() {
    if [ -s "$work/container.id" ]; then
        docker rm -f "$(cat "$work/container.id")" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 0755 "$work"

export GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GONOPROXY=none GOTOOLCHAIN=local
go test -c -o "$work/core.test" ./cmd/kakatta
printf '断网主流程回归：%s\n' "$work"
docker run --rm --pull=never --network none --no-healthcheck --cidfile "$work/container.id" \
    --security-opt "seccomp=$repo/deploy/chrome-seccomp.json" --tmpfs /data:rw,nosuid,nodev \
    --mount "type=bind,src=$work,dst=/tests,readonly" \
    "$image" /bin/sh -eu -c '
        test "$(id -u)" -ne 0
        export KAKATTA_CORE_FLOW_TESTS=1
        status=0
        /tests/core.test -test.run "^TestCoreFlow" -test.v -test.timeout=4m || status=$?
        for attempt in 1 2 3 4 5; do
            remaining=
            for file in /proc/[0-9]*/comm; do
                if IFS= read -r name 2>/dev/null < "$file"; then
                    case "$name" in chrome|chrome_crashpad*|google-chrome*|Xvfb|x11vnc) remaining="$remaining $name" ;; esac
                fi
            done
            if [ -z "$remaining" ]; then
                printf "%s\n" "进程检查通过，浏览器和远程画面已关闭。"
                exit "$status"
            fi
            sleep 0.2
        done
        printf "测试后仍有浏览器或画面进程：%s\n" "$remaining" >&2
        exit 1
    '
