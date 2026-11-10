#!/bin/bash
set -euo pipefail

usage() {
    printf '%s\n' \
        '用法：deploy/test-container.sh [--seccomp 规则文件]' \
        '默认使用本地 kakatta:local 镜像和 seccomp=unconfined。' \
        '所有包在宿主编译，测试容器断网，缺少本地依赖或镜像时直接停止。'
}

seccomp=unconfined
while [ "$#" -gt 0 ]; do
    case "$1" in
        --seccomp)
            [ "$#" -ge 2 ] || { usage >&2; exit 2; }
            seccomp=$2
            shift 2
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            usage >&2
            exit 2
            ;;
    esac
done

if [ "$seccomp" != unconfined ]; then
    [ -f "$seccomp" ] || { printf '找不到 seccomp 规则：%s\n' "$seccomp" >&2; exit 2; }
    seccomp=$(cd -- "$(dirname -- "$seccomp")" && printf '%s/%s' "$PWD" "$(basename -- "$seccomp")")
fi

repo_root=$(cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo_root"
architecture=$(docker image inspect kakatta:local --format '{{.Architecture}}')
case "$architecture" in
    amd64|arm64) ;;
    *) printf '镜像架构不支持：%s\n' "$architecture" >&2; exit 1 ;;
esac

# 缺依赖时失败，不在回归过程中下载模块或工具链。
export CGO_ENABLED=0 GOOS=linux GOARCH="$architecture"
export GOPROXY=off GOSUMDB=off GONOPROXY=none GOTOOLCHAIN=local
mkdir -p build/m1
run_dir=$(mktemp -d "$repo_root/build/m1/container.XXXXXX")
chmod 0755 "$run_dir"
printf '回归文件和日志：%s\n' "$run_dir"

cleanup() {
    if [ -f "$run_dir/container.id" ]; then
        docker rm -f "$(cat "$run_dir/container.id")" >/dev/null 2>&1 || true
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

go list -f '{{.ImportPath}}{{"\t"}}{{.Dir}}{{"\t"}}{{if or .TestGoFiles .XTestGoFiles}}test{{else}}none{{end}}' ./... > "$run_dir/packages.list"
tab=$(printf '\t')
package_count=0
test_count=0
: > "$run_dir/manifest.tsv"
while IFS="$tab" read -r package package_dir has_tests; do
    package_count=$((package_count + 1))
    relative_dir=${package_dir#"$repo_root"/}
    [ "$package_dir" != "$repo_root" ] || relative_dir=.
    binary="package-$package_count.test"
    printf '编译 %s\n' "$package"
    go test -c -o "$run_dir/$binary" "$package"
    if [ "$has_tests" = test ]; then
        [ -f "$run_dir/$binary" ] || { printf '没有生成测试文件：%s\n' "$package" >&2; exit 1; }
        chmod 0755 "$run_dir/$binary"
        test_count=$((test_count + 1))
    else
        binary=-
    fi
    printf '%s\t%s\t%s\n' "$binary" "$relative_dir" "$package" >> "$run_dir/manifest.tsv"
done < "$run_dir/packages.list"
[ "$package_count" -gt 0 ] && [ "$test_count" -gt 0 ] || { printf '%s\n' '没有找到可运行的测试包。' >&2; exit 1; }
chmod 0644 "$run_dir/manifest.tsv"

printf '在断网容器里运行 %s 个包，其中 %s 个有测试；seccomp=%s\n' "$package_count" "$test_count" "$seccomp"
if docker run --rm --pull=never --network none --no-healthcheck \
    --security-opt "seccomp=$seccomp" --tmpfs /data:rw,nosuid,nodev \
    --cidfile "$run_dir/container.id" \
    --mount "type=bind,src=$repo_root,dst=/source,readonly" \
    --mount "type=bind,src=$run_dir,dst=/tests,readonly" \
    kakatta:local /bin/sh /source/deploy/test-container-run.sh 2>&1 | tee "$run_dir/tests.log"; then
    printf '全部容器测试通过，日志：%s/tests.log\n' "$run_dir"
else
    status=$?
    printf '容器回归失败，退出码 %s；日志：%s/tests.log\n' "$status" "$run_dir" >&2
    exit "$status"
fi
