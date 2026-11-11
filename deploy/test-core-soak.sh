#!/bin/sh
set -eu

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
image=${1:-kakatta:local}
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
case "$architecture" in amd64|arm64) ;; *) printf '不支持的镜像架构：%s\n' "$architecture" >&2; exit 1 ;; esac

mkdir -p build
work=$(mktemp -d "$repo/build/core-soak.XXXXXX")
mkdir "$work/metrics"
chmod 0755 "$work" "$work/metrics"
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
go version > "$work/go-version.txt"
shasum -a 256 "$work/core.test" > "$work/test-binary.sha256"
docker image inspect "$image" --format '{{.Id}} {{.Architecture}}' > "$work/image.txt"
cat > "$work/interpretation.txt" <<'EOF'
本次是两个账号、四个任务各30次真实自动运行，任务间隔1分钟、账号最小间隔2秒。
没有用手动立即运行、假时钟或SQL提前计划。上下文55分钟、测试58分钟硬截止。
仅本机假闲鱼、假模型、假Webhook；不能据此承诺真实闲鱼页面的容量。
resource-samples.jsonl每0.5秒记录Go堆、进程RSS/PSS和cgroup anon/file/kernel/shmem。
cgroup memory.current/peak包含文件页缓存和内核计费，不是进程RSS；file增长不能直接判为泄漏。
进程RSS求和可能重复计算共享页；取不到PSS时明确记为缺失。
running样本与没有浏览器/显示进程的released样本分开解释；finish.json为图关闭后的快照。
progress.jsonl记录实际新增业务行数和SQLite页数，DB/WAL正常增长不要求严格线性。
测试记录器保留的104次模型请求、60个商品输入和80条推送也是有界的测试对象。
--memory=1g --memory-swap=1g禁止交换空间，最终以保存的memory.max和memory.swap.max核对。
EOF
printf '1 GiB 真自动长跑；预计35–45分钟，硬截止58分钟；记录：%s\n' "$work"
printf '%s\n' "$work" > build/core-soak.latest
status=0
if docker run --pull=never --network none --no-healthcheck --cidfile "$work/container.id" \
    --memory=1g --memory-swap=1g \
    --security-opt "seccomp=$repo/deploy/chrome-seccomp.json" --tmpfs /data:rw,nosuid,nodev \
    -e PUID="$(id -u)" -e PGID="$(id -g)" \
    --mount "type=bind,src=$work,dst=/tests,readonly" \
    --mount "type=bind,src=$work/metrics,dst=/metrics" \
    "$image" /bin/sh -eu -c '
        test "$(id -u)" -ne 0
        test -f /.dockerenv
        test "$(cat /sys/fs/cgroup/memory.max)" = 1073741824
        test "$(cat /sys/fs/cgroup/memory.swap.max)" = 0
        snapshot() {
            mkdir -p "/metrics/$1"
            for file in memory.current memory.peak memory.max memory.events memory.stat memory.swap.max; do
                cat "/sys/fs/cgroup/$file" > "/metrics/$1/$file"
            done
        }
        remaining_processes() {
            for file in /proc/[0-9]*/comm; do
                if IFS= read -r name 2>/dev/null < "$file"; then
                    case "$name" in
                        chrome|chrome_crashpad*|google-chrome*|Xvfb|x11vnc)
                            process=${file#/proc/}
                            printf "%s %s\n" "${process%/comm}" "$name"
                            ;;
                    esac
                fi
            done
        }
        finish() {
            status=$?
            trap - EXIT
            snapshot after-test || status=1
            remaining_processes > /metrics/processes-after-test.txt
            if [ -s /metrics/processes-after-test.txt ]; then
                status=1
                while read -r process name; do kill "$process" 2>/dev/null || true; done < /metrics/processes-after-test.txt
            fi
            for attempt in 1 2 3 4 5; do
                remaining_processes > /metrics/processes-after-cleanup.txt
                [ -s /metrics/processes-after-cleanup.txt ] || break
                sleep 0.2
            done
            if [ -s /metrics/processes-after-cleanup.txt ]; then
                while read -r process name; do kill -KILL "$process" 2>/dev/null || true; done < /metrics/processes-after-cleanup.txt
                sleep 0.2
                remaining_processes > /metrics/processes-after-cleanup.txt
            fi
            [ ! -s /metrics/processes-after-cleanup.txt ] || status=1
            snapshot after-cleanup || status=1
            while read -r key value; do
                case "$key" in oom|oom_kill|oom_group_kill) [ "$value" -eq 0 ] || status=1 ;; esac
            done < /metrics/after-cleanup/memory.events
            printf "%s\n" "$status" > /metrics/exit-status
            exit "$status"
        }
        trap finish EXIT
        trap "exit 130" INT
        trap "exit 143" TERM
        snapshot before
        export KAKATTA_CORE_FLOW_TESTS=1 KAKATTA_CORE_SOAK_TEST=1 KAKATTA_CORE_SOAK_DIR=/metrics
        /tests/core.test -test.run "^TestCoreSoakAutomaticThirtyRounds$" -test.v -test.timeout=58m
    ' > "$work/tests.log" 2>&1; then
    :
else
    status=$?
fi
cat "$work/tests.log"
if [ -s "$work/container.id" ]; then
    container=$(cat "$work/container.id")
    docker inspect "$container" --format '{{json .State}}' > "$work/docker-state.json"
    docker inspect "$container" --format 'memory={{.HostConfig.Memory}} memory_swap={{.HostConfig.MemorySwap}}' > "$work/docker-memory.txt"
    if [ "$(docker inspect "$container" --format '{{.State.OOMKilled}}')" != false ]; then
        status=1
    fi
fi
if [ ! -s "$work/metrics/exit-status" ] || [ "$(cat "$work/metrics/exit-status")" -ne 0 ]; then
    status=1
fi
printf '%s\n' "$status" > "$work/exit-status"
printf '长跑退出码：%s；原始证据：%s\n' "$status" "$work"
exit "$status"
