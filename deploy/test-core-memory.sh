#!/bin/sh
set -eu

repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"
image=${1:-kakatta:local}
memory=${2:-1g}
reuse=${3:-}
case "$memory" in
    768m) memory_bytes=805306368 ;;
    896m) memory_bytes=939524096 ;;
    1g) memory_bytes=1073741824 ;;
    *) printf '内存档位只支持 768m、896m、1g。\n' >&2; exit 2 ;;
esac
architecture=$(docker image inspect "$image" --format '{{.Architecture}}')
case "$architecture" in amd64|arm64) ;; *) printf '不支持的镜像架构：%s\n' "$architecture" >&2; exit 1 ;; esac

mkdir -p build
work=$(mktemp -d "$repo/build/core-memory.XXXXXX")
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
docker image inspect "$image" --format '{{.Id}} {{.Architecture}}' > "$work/image.txt"
if [ -n "$reuse" ]; then
    cmp -s "$reuse/image.txt" "$work/image.txt" || { printf '复用记录的镜像不一致。\n' >&2; exit 1; }
    cp "$reuse/core.test" "$work/core.test"
    for file in source-revision.txt source-status.txt go-version.txt; do cp "$reuse/$file" "$work/$file"; done
    printf '%s\n' "$reuse" > "$work/reused-from.txt"
else
    go test -c -o "$work/core.test" ./cmd/kakatta
    git rev-parse HEAD > "$work/source-revision.txt"
    git status --short > "$work/source-status.txt"
    go version > "$work/go-version.txt"
fi
shasum -a 256 "$work/core.test" > "$work/test-binary.sha256"
if [ -n "$reuse" ]; then
    IFS=' ' read -r previous_hash previous_path < "$reuse/test-binary.sha256"
    IFS=' ' read -r current_hash current_path < "$work/test-binary.sha256"
    [ "$previous_hash" = "$current_hash" ] || { printf '复用后的测试二进制不一致。\n' >&2; exit 1; }
fi
printf '%s %s\n' "$memory" "$memory_bytes" > "$work/memory-limit.txt"
docker info --format 'storage_driver={{.Driver}} cgroup_version={{.CgroupVersion}}' > "$work/docker-environment.txt"
cat > "$work/interpretation.txt" <<'EOF'
memory.peak 是容器 cgroup 及其后代自创建以来的总计费内存峰值，单位 bytes；本工具不重置峰值。
它包含匿名内存、文件页缓存、tmpfs、共享内存及内核计费，不是进程 RSS。
memory.current 是快照当时的用量；memory.stat 是该时刻的分类，不是峰值时的分类。
memory.events 的 oom 是到达限制且分配将失败的次数，oom_kill 是被 OOM 杀掉的进程数。
--memory 与 --memory-swap 使用同一档位，禁用容器交换空间；实际限制由 memory.max / memory.swap.max 核对。
samples/ 保存每次采样的原始 memory.stat、current、peak、uptime，samples.tsv 是同一批读数。
采样目标间隔是 0.5 秒，sampling-summary.txt 记录实际最大间隔、超过 0.75 秒的间隔及完整性；不是实时保证。
anon、shmem、kernel 峰值来自离散采样；三者之和的峰值使用同一采样点，不叠加各自峰值。
kernel 包含可回收 slab，不能一概当成不可回收内存；分类采样也不能冒充 memory.peak 那一刻的分类。
结果仅说明本次五种本地假站测试的用量，不代表真实闲鱼页面或任意账号/任务规模的容量承诺。
依据：https://docs.kernel.org/admin-guide/cgroup-v2.html
依据：https://docs.docker.com/engine/containers/resource_constraints/#--memory-swap-details
EOF
printf '%s 断网主流程回归和原始内存记录：%s\n' "$memory" "$work"
status=0
if docker run --pull=never --network none --no-healthcheck --cidfile "$work/container.id" \
    --memory="$memory" --memory-swap="$memory" \
    --security-opt "seccomp=$repo/deploy/chrome-seccomp.json" --tmpfs /data:rw,nosuid,nodev \
    -e PUID="$(id -u)" -e PGID="$(id -g)" \
    -e KAKATTA_MEMORY_BYTES="$memory_bytes" \
    --mount "type=bind,src=$work,dst=/tests,readonly" \
    --mount "type=bind,src=$work/metrics,dst=/metrics" \
    --mount "type=bind,src=$repo/deploy/test-core-memory-run.sh,dst=/memory-run.sh,readonly" \
    "$image" /bin/sh /memory-run.sh > "$work/tests.log" 2>&1; then
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
        printf '%s\n' 'Docker 记录了 OOM，回归失败。' >&2
        status=1
    fi
fi
if [ ! -s "$work/metrics/exit-status" ]; then
    printf '%s\n' '最终内存快照未完整保存，不能报告精确最终峰值；请查 Docker 状态和已有快照。' >&2
    status=1
elif [ "$(cat "$work/metrics/exit-status")" -ne 0 ]; then
    status=1
fi
printf '%s\n' "$status" > "$work/exit-status"
printf '回归退出码：%s；全部记录保留在 %s\n' "$status" "$work"
exit "$status"
