#!/bin/sh
set -eu
umask 077

fail() {
    printf '%s\n' "$1" >&2
    exit 1
}

valid_id() {
    case "$1" in ''|*[!0-9]*) return 1 ;; esac
    [ "$1" -ge 1 ] && [ "$1" -le 2147483647 ]
}

valid_id "$PUID" || fail 'PUID 必须是 1 到 2147483647 之间的用户编号。'
valid_id "$PGID" || fail 'PGID 必须是 1 到 2147483647 之间的组编号。'
[ "$HOME" = /data/home ] && [ "$XDG_CONFIG_HOME" = /data/home/.config ] \
    || fail '请不要覆盖 HOME 或 XDG_CONFIG_HOME，运行目录必须保留在 /data 内。'
[ ! -L "$HOME" ] && [ ! -L "$XDG_CONFIG_HOME" ] \
    || fail '运行目录不能是符号链接，请移除 data/home 或 data/home/.config 的链接后重试。'

if [ "$(id -u)" -eq 0 ]; then
    [ ! -L /tmp/.X11-unix ] || fail '虚拟屏幕套接字目录不能是符号链接。'
    install -d -o root -g root -m 1777 /tmp/.X11-unix
    if [ "$(id -g kakatta)" != "$PGID" ]; then
        groupmod --non-unique --gid "$PGID" kakatta
    fi
    if [ "$(id -u kakatta)" != "$PUID" ] || [ "$(id -g kakatta)" != "$PGID" ]; then
        usermod --non-unique --uid "$PUID" --gid "$PGID" kakatta
    fi
    mkdir -p /data "$HOME" "$XDG_CONFIG_HOME" \
        || fail '数据目录不能写入，请把挂载目录的属主改为 PUID/PGID，或调整目录权限。'
    # 只修数据挂载内的属主，不跟随符号链接，也不进入其他挂载。
    find /data -xdev \( ! -uid "$PUID" -o ! -gid "$PGID" \) -exec chown --no-dereference "$PUID:$PGID" {} + \
        || fail '不能修正数据目录属主，请检查挂载目录是否只读，以及 PUID/PGID 是否正确。'
    # 写入检查的变量留给降权后的 shell 展开。
    # shellcheck disable=SC2016
    gosu kakatta sh -c 'probe=$(mktemp /data/.write-test.XXXXXX) && rm -f "$probe"' \
        || fail '数据目录不能写入，请把挂载目录的属主改为 PUID/PGID，或调整目录权限。'
else
    mkdir -p "$HOME" "$XDG_CONFIG_HOME" \
        || fail '数据目录不能写入，请调整挂载目录权限后重试。'
    probe=$(mktemp /data/.write-test.XXXXXX) \
        || fail '数据目录不能写入，请调整挂载目录权限后重试。'
    rm -f "$probe"
fi

if [ "$#" -eq 0 ]; then
    set -- /usr/local/bin/kakatta --data /data --listen :8080
elif [ "${1#-}" != "$1" ]; then
    set -- /usr/local/bin/kakatta "$@"
fi

if [ "$(id -u)" -eq 0 ]; then
    exec gosu kakatta env HOME="$HOME" "$@"
fi
exec "$@"
