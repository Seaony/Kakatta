#!/bin/sh
set -eu

[ -f /.dockerenv ] && [ -f /tests/core.test ] && [ "$(id -u)" -ne 0 ]
for file in memory.peak memory.current memory.max memory.events memory.stat memory.swap.max; do
    [ -r "/sys/fs/cgroup/$file" ] || { printf '缺少 cgroup v2 记录：%s\n' "$file" >&2; exit 1; }
done
sampler_pid=

sample_loop() {
    trap - EXIT INT TERM
    mkdir -p /metrics/samples
    printf 'seq\tuptime_seconds\tcurrent_bytes\tanon_bytes\tshmem_bytes\tkernel_bytes\tslab_reclaimable_bytes\tslab_unreclaimable_bytes\n' > /metrics/samples.tsv
    sample_seq=0
    while [ ! -e /metrics/sampling.stop ]; do
        sample_seq=$((sample_seq + 1))
        sample_dir=$(printf '/metrics/samples/%06d' "$sample_seq")
        mkdir "$sample_dir"
        read -r uptime rest < /proc/uptime
        printf '%s\n' "$uptime" > "$sample_dir/uptime"
        cat /sys/fs/cgroup/memory.stat > "$sample_dir/memory.stat"
        cat /sys/fs/cgroup/memory.current > "$sample_dir/memory.current"
        cat /sys/fs/cgroup/memory.peak > "$sample_dir/memory.peak"
        anon= shmem= kernel= slab_reclaimable= slab_unreclaimable=
        while read -r key value; do
            case "$key" in
                anon) anon=$value ;; shmem) shmem=$value ;; kernel) kernel=$value ;;
                slab_reclaimable) slab_reclaimable=$value ;; slab_unreclaimable) slab_unreclaimable=$value ;;
            esac
        done < "$sample_dir/memory.stat"
        [ -n "$anon" ] && [ -n "$shmem" ] && [ -n "$kernel" ] && [ -n "$slab_reclaimable" ] && [ -n "$slab_unreclaimable" ]
        read -r current < "$sample_dir/memory.current"
        printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$sample_seq" "$uptime" "$current" "$anon" "$shmem" "$kernel" "$slab_reclaimable" "$slab_unreclaimable" >> /metrics/samples.tsv
        : > /metrics/sampling.ready
        sleep 0.5
    done
    printf '%s\n' "$sample_seq" > /metrics/sampling.complete
}

finish_sampling() {
    [ -n "$sampler_pid" ] || return 1
    : > /metrics/sampling.stop
    sampler_status=0
    wait "$sampler_pid" || sampler_status=$?
    printf '%s\n' "$sampler_status" > /metrics/sampler-exit-status
    [ "$sampler_status" -eq 0 ] && [ -s /metrics/sampling.complete ] || return 1
    sample_files=0
    for sample_dir in /metrics/samples/*; do
        [ -s "$sample_dir/memory.stat" ] && [ -s "$sample_dir/memory.current" ] && [ -s "$sample_dir/memory.peak" ] && [ -s "$sample_dir/uptime" ] || return 1
        sample_files=$((sample_files + 1))
    done
    [ "$sample_files" -eq "$(cat /metrics/sampling.complete)" ] || return 1
    : > /metrics/sampling-gaps.tsv
    awk -v expected="$sample_files" '
        NR == 1 { next }
        {
            if ($1 != count+1) bad=1
            if (count) {
                gap=$2-last
                if (gap>max_gap) max_gap=gap
                if (gap>0.75) { gaps++; print $1, last, $2, gap >> "/metrics/sampling-gaps.tsv" }
                if (gap<=0) bad=1
            }
            count++; last=$2
            if ($3>current) current=$3
            if ($4>anon) anon=$4
            if ($5>shmem) shmem=$5
            if ($6>kernel) kernel=$6
            if ($7>reclaimable) reclaimable=$7
            if ($8>unreclaimable) unreclaimable=$8
            if ($4+$5+$6>combined) combined=$4+$5+$6
        }
        END {
            printf "samples=%d\nraw_samples=%d\nnominal_interval_seconds=0.5\nmax_gap_seconds=%.3f\ngaps_over_0.75_seconds=%d\n", count, expected, max_gap, gaps
            printf "current_sampled_peak_bytes=%.0f\nanon_sampled_peak_bytes=%.0f\nshmem_sampled_peak_bytes=%.0f\nkernel_sampled_peak_bytes=%.0f\n", current, anon, shmem, kernel
            printf "slab_reclaimable_sampled_peak_bytes=%.0f\nslab_unreclaimable_sampled_peak_bytes=%.0f\nanon_shmem_kernel_same_sample_sum_peak_bytes=%.0f\n", reclaimable, unreclaimable, combined
            if (!count || count!=expected || bad) { print "integrity=failed"; exit 1 }
            print "integrity=complete"
        }
    ' /metrics/samples.tsv > /metrics/sampling-summary.txt
}

snapshot() {
    directory="/metrics/$1"
    mkdir -p "$directory"
    for file in memory.peak memory.current memory.max memory.events memory.events.local memory.stat \
        memory.swap.peak memory.swap.current memory.swap.max memory.swap.events memory.pressure; do
        if [ -r "/sys/fs/cgroup/$file" ]; then
            cat "/sys/fs/cgroup/$file" > "$directory/$file" || return 1
        else
            printf '%s\n' "$file" >> "$directory/unavailable"
        fi
    done
    date -u '+%Y-%m-%dT%H:%M:%SZ' > "$directory/at"
}

remaining_processes() {
    for file in /proc/[0-9]*/comm; do
        if IFS= read -r name 2>/dev/null < "$file"; then
            case "$name" in
                chrome|chrome_crashpad*|google-chrome*|Xvfb|x11vnc)
                    process=${file#/proc/}
                    printf '%s %s\n' "${process%/comm}" "$name"
                    ;;
            esac
        fi
    done
}

finish() {
    status=$?
    trap - EXIT
    snapshot after-test || status=1
    finish_sampling || status=1
    remaining_processes > /metrics/processes-after-test.txt
    if [ -s /metrics/processes-after-test.txt ]; then
        printf '%s\n' '测试退出后仍有浏览器或画面进程，本次回归失败并清理残留。' >&2
        cat /metrics/processes-after-test.txt >&2
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
    if [ -r /metrics/after-cleanup/memory.peak ]; then
        printf '容器总计费内存峰值 bytes：%s\n' "$(cat /metrics/after-cleanup/memory.peak)"
        printf '%s\n' 'OOM 原始计数：'
        cat /metrics/after-cleanup/memory.events
    fi
    if [ -r /metrics/sampling-summary.txt ]; then cat /metrics/sampling-summary.txt; fi
    printf '%s\n' "$status" > /metrics/exit-status
    exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

snapshot before
uname -a > /metrics/kernel.txt
stat -f -c '%T' / > /metrics/root-filesystem.txt
[ "$(cat /sys/fs/cgroup/memory.max)" = "$KAKATTA_MEMORY_BYTES" ]
[ "$(cat /sys/fs/cgroup/memory.swap.max)" = 0 ]
export KAKATTA_CORE_FLOW_TESTS=1
/tests/core.test -test.list '^TestCoreFlow' > /metrics/cases.txt
[ -s /metrics/cases.txt ]
sample_loop &
sampler_pid=$!
attempt=0
while [ ! -e /metrics/sampling.ready ] && [ "$attempt" -lt 100 ]; do
    kill -0 "$sampler_pid"
    sleep 0.05
    attempt=$((attempt + 1))
done
[ -e /metrics/sampling.ready ]
/tests/core.test -test.run '^TestCoreFlow' -test.v -test.timeout=10m
