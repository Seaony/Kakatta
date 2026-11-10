#!/bin/sh
set -eu

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

[ -f /.dockerenv ] && [ -f /tests/manifest.tsv ] || fail '此脚本只能由 test-container.sh 在临时容器里运行。'
[ "$(id -u)" -ne 0 ] || fail '测试没有经过入口脚本降权。'
[ "$HOME" = /data/home ] || fail '降权后的 HOME 不在数据目录。'
[ "$XDG_CONFIG_HOME" = /data/home/.config ] || fail '降权后的 XDG_CONFIG_HOME 不在数据目录。'
for program in google-chrome-stable Xvfb x11vnc; do
    command -v "$program" >/dev/null || fail "镜像缺少 $program。"
done
IFS= read -r init_name < /proc/1/comm
[ "$init_name" = tini ] || fail '容器没有使用 tini 回收子进程。'

display_pid=
stop_display() {
    if [ -n "$display_pid" ]; then
        kill "$display_pid" 2>/dev/null || true
        wait "$display_pid" 2>/dev/null || true
        display_pid=
    fi
}

remaining_processes() {
    for entry in /proc/[0-9]*/comm; do
        if IFS= read -r process_name 2>/dev/null < "$entry"; then
            case "$process_name" in
                chrome|chrome_crashpad*|google-chrome*|Xvfb|x11vnc)
                    process_id=${entry#/proc/}
                    printf '%s %s\n' "${process_id%/comm}" "$process_name"
                    ;;
            esac
        fi
    done
}

finish() {
    status=$?
    trap - EXIT
    stop_display
    attempt=0
    while [ "$attempt" -lt 50 ]; do
        remaining=$(remaining_processes)
        [ -n "$remaining" ] || break
        sleep 0.1
        attempt=$((attempt + 1))
    done
    if [ -n "$remaining" ]; then
        printf '测试结束后仍有浏览器或画面进程：\n%s\n' "$remaining" >&2
        status=1
    else
        printf '%s\n' '进程检查通过：没有 Chrome、crashpad、Xvfb 或 x11vnc 残留。'
    fi
    exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

start_display() {
    [ ! -e /tmp/.X101-lock ] && [ ! -e /tmp/.X11-unix/X101 ] || fail '测试用的 :101 已被占用。'
    Xvfb :101 -screen 0 1920x1080x24 -nolisten tcp > /tmp/kakatta-test-xvfb.log 2>&1 &
    display_pid=$!
    attempt=0
    while [ "$attempt" -lt 50 ]; do
        if ! kill -0 "$display_pid" 2>/dev/null; then
            cat /tmp/kakatta-test-xvfb.log >&2
            fail '测试虚拟屏幕提前退出。'
        fi
        if [ -S /tmp/.X11-unix/X101 ]; then
            return
        fi
        sleep 0.1
        attempt=$((attempt + 1))
    done
    cat /tmp/kakatta-test-xvfb.log >&2
    fail '测试虚拟屏幕没有就绪。'
}

export KAKATTA_BROWSER_TESTS=1 KAKATTA_REMOTE_X11_TEST=1
export KAKATTA_EXPERIMENT_TESTS=1 KAKATTA_WEBGL_TESTS=1 KAKATTA_REMOTE_INPUT_TEST=1
tab=$(printf '\t')
failures=0
packages=0
while IFS="$tab" read -r binary relative_dir package; do
    packages=$((packages + 1))
    printf '\n运行 %s\n' "$package"
    if [ "$binary" = - ]; then
        printf '%s\n' '此包没有测试文件，宿主编译已通过。'
        continue
    fi
    case "$relative_dir" in
        internal/browser|internal/remote|internal/xianyu/web)
            start_display
            export DISPLAY=:101
            ;;
        *) unset DISPLAY ;;
    esac
    # 按包目录运行，保证源码检查和相对路径资源也真正参与验证。
    if (cd "/source/$relative_dir" && "/tests/$binary" -test.v -test.timeout=5m); then
        :
    else
        failures=$((failures + 1))
        printf '测试包失败：%s\n' "$package" >&2
    fi
    stop_display
done < /tests/manifest.tsv
[ "$packages" -gt 0 ] || fail '测试清单为空。'
[ "$failures" -eq 0 ] || fail "$failures 个测试包失败。"
printf '\n%s 个包全部运行完毕。\n' "$packages"
