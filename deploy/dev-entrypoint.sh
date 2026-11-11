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

[ -d /data ] && [ ! -L /data ] || fail '数据目录必须是真实目录，不能是符号链接。'
data_owner=$(stat -c '%u:%g' /data) || fail '无法读取数据目录属主，请检查挂载和权限。'
if [ "${PUID+x}" != x ]; then
    PUID=${data_owner%:*}
    [ "$PUID" -ne 0 ] || PUID=1000
fi
if [ "${PGID+x}" != x ]; then
    PGID=${data_owner#*:}
    [ "$PGID" -ne 0 ] || PGID=1000
fi
valid_id "$PUID" || fail 'PUID 必须是 1 到 2147483647 之间的用户编号。'
valid_id "$PGID" || fail 'PGID 必须是 1 到 2147483647 之间的组编号。'
export PUID PGID

[ "${GOMODCACHE:-}" = /go/pkg/mod ] && [ "${GOCACHE:-}" = /go/build-cache ] \
    || fail '开发缓存目录必须保留为 /go/pkg/mod 和 /go/build-cache。'
for directory in /go /go/pkg /go/pkg/mod /go/build-cache; do
    [ ! -L "$directory" ] || fail '开发缓存目录不能是符号链接，请检查 Go 缓存卷。'
done
mkdir -p /go/pkg/mod /go/build-cache || fail '不能创建开发缓存目录，请检查 Go 缓存卷权限。'
if [ "$(id -u)" -eq 0 ]; then
    # 只修两个缓存卷，不进入其它挂载，也不跟随卷里的符号链接。
    find /go/pkg/mod /go/build-cache -xdev \( ! -uid "$PUID" -o ! -gid "$PGID" \) \
        -exec chown --no-dereference "$PUID:$PGID" {} + \
        || fail '不能修正 Go 缓存卷属主，请检查缓存卷是否只读。'
    for directory in /go/pkg/mod /go/build-cache; do
        # 探针由降权后的 shell 展开。
        # shellcheck disable=SC2016
        gosu "$PUID:$PGID" sh -c 'probe=$(mktemp "$1/.write-test.XXXXXX") && rm -f "$probe"' sh "$directory" \
            || fail 'Go 缓存卷不能写入，请检查 PUID/PGID 和缓存卷权限。'
    done
else
    for directory in /go/pkg/mod /go/build-cache; do
        probe=$(mktemp "$directory/.write-test.XXXXXX") \
            || fail 'Go 缓存卷不能写入，请先按容器运行用户修正缓存卷属主。'
        rm -f "$probe"
    done
fi

exec /usr/local/bin/kakatta-entrypoint "$@"
