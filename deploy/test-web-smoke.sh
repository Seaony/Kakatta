#!/bin/sh
set -eu

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
image=${1:-kakatta:local}
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
case "$architecture" in amd64|arm64) ;; *) printf '不支持的镜像架构：%s\n' "$architecture" >&2; exit 1 ;; esac

mkdir -p build/m7-screens
work=$(mktemp -d "$repo/build/m7-smoke.XXXXXX")
cleanup() {
    if [ -s "$work/container.id" ]; then
        docker rm -f "$(cat "$work/container.id")" >/dev/null 2>&1 || true
    fi
    rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 0755 "$work"
mkdir "$work/screens"

export GOOS=linux GOARCH="$architecture" CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GONOPROXY=none GOTOOLCHAIN=local
go build -o "$work/seed" ./cmd/seed
go test -c -o "$work/web.test" ./cmd/kakatta

docker run --rm --pull=never --network none --no-healthcheck --cidfile "$work/container.id" \
    --security-opt seccomp=unconfined --tmpfs /data:rw,nosuid,nodev \
    -e PUID="$(id -u)" -e PGID="$(id -g)" \
    --mount "type=bind,src=$work,dst=/tests,readonly" \
    --mount "type=bind,src=$work/screens,dst=/screens" \
    "$image" /bin/sh -eu -c '
        test "$(id -u)" -ne 0
        export KAKATTA_WEB_SMOKE=1
        /tests/web.test -test.run "^TestWebSmoke$" -test.v -test.timeout=4m
        for attempt in 1 2 3 4 5; do
            remaining=
            for file in /proc/[0-9]*/comm; do
                if IFS= read -r name 2>/dev/null < "$file"; then
                    case "$name" in chrome|chrome_crashpad*|google-chrome*) remaining="$remaining $name" ;; esac
                fi
            done
            if [ -z "$remaining" ]; then
                printf "%s\n" "进程检查通过，浏览器已关闭。"
                exit 0
            fi
            sleep 0.2
        done
        printf "浏览器进程没有退出：%s\n" "$remaining" >&2
        exit 1
    '
cp -R "$work/screens/." build/m7-screens/
