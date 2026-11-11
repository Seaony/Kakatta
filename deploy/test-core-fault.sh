#!/bin/sh
set -eu

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
image=${1:-kakatta:local}
pattern=${2:-^TestCoreFault}
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
case "$architecture" in amd64|arm64) ;; *) printf '不支持的镜像架构：%s\n' "$architecture" >&2; exit 1 ;; esac

mkdir -p build
work=$(mktemp -d "$repo/build/core-fault.XXXXXX")
mkdir "$work/report"
chmod 0755 "$work" "$work/report"
cleanup() {
    if [ -s "$work/container.id" ]; then
        docker rm -f "$(cat "$work/container.id")" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

export GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GONOPROXY=none GOTOOLCHAIN=local
go test -c -o "$work/core.test" ./cmd/kakatta
git rev-parse HEAD > "$work/source-revision.txt"
git status --short > "$work/source-status.txt"
shasum -a 256 "$work/core.test" > "$work/test-binary.sha256"
docker image inspect "$image" --format '{{.Id}} {{.Architecture}}' > "$work/image.txt"
printf '断网故障注入，数据库容量只限制在独立64MiB tmpfs：%s\n' "$work"
status=0
if docker run --pull=never --network none --no-healthcheck --cidfile "$work/container.id" \
    --security-opt "seccomp=$repo/deploy/chrome-seccomp.json" \
    --tmpfs /data:rw,nosuid,nodev \
    --tmpfs "/fault-db:rw,nosuid,nodev,size=64m,mode=0700,uid=$(id -u),gid=$(id -g)" \
    -e PUID="$(id -u)" -e PGID="$(id -g)" \
    -e KAKATTA_CORE_FLOW_TESTS=1 -e KAKATTA_CORE_FAULT_TESTS=1 \
    -e KAKATTA_CORE_FAULT_TMPFS=/fault-db -e KAKATTA_CORE_FAULT_REPORT=/report \
    -e KAKATTA_CORE_FAULT_PATTERN="$pattern" \
    --mount "type=bind,src=$work,dst=/tests,readonly" \
    --mount "type=bind,src=$work/report,dst=/report" \
    "$image" /bin/sh -eu -c '
        test "$(id -u)" -ne 0
        cat /proc/self/mountinfo > /report/mountinfo.txt
        result=0
        /tests/core.test -test.run "$KAKATTA_CORE_FAULT_PATTERN" -test.v -test.timeout=20m || result=$?
        for attempt in 1 2 3 4 5; do
            remaining=
            for file in /proc/[0-9]*/comm; do
                if IFS= read -r name 2>/dev/null < "$file"; then
                    case "$name" in chrome|chrome_crashpad*|google-chrome*|Xvfb|x11vnc) remaining="$remaining $name" ;; esac
                fi
            done
            if [ -z "$remaining" ]; then
                printf "%s\n" "进程检查通过，故障测试的浏览器和远程画面已关闭。"
                exit "$result"
            fi
            sleep 0.2
        done
        printf "故障测试后仍有残留进程：%s\n" "$remaining" >&2
        exit 1
    ' > "$work/tests.log" 2>&1; then
    :
else
    status=$?
fi
cat "$work/tests.log"
if [ -s "$work/container.id" ]; then
    docker inspect "$(cat "$work/container.id")" --format '{{json .State}}' > "$work/docker-state.json"
fi
printf '%s\n' "$status" > "$work/exit-status"
printf '故障回归退出码：%s；记录保留在 %s\n' "$status" "$work"
exit "$status"
