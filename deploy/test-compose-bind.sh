#!/bin/sh
set -eu

case "${1:-}" in
    --help|-h)
        printf '%s\n' '用法：deploy/test-compose-bind.sh [已提交的 HEAD 编号]' '只从 git archive 构建，在 build/_compose-check/ 下验证真实 Mac 挂载；运行上限三十分钟。'
        exit 0
        ;;
esac
command -v python3 >/dev/null 2>&1 || {
    printf '%s\n' '需要 Python 3 检查宿主上的 JSON、SQLite 和文件权限。' >&2
    exit 2
}
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 - "$repo" "$@" <<'PY'
import contextlib
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import signal
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time

repo = Path(sys.argv[1])
if len(sys.argv) > 3:
    sys.exit("用法：deploy/test-compose-bind.sh [已提交的 HEAD 编号]")
if platform.system() != "Darwin":
    sys.exit("本脚本验证 Mac 上的真实 bind mount，请在作者的 Mac 运行。")
if os.getuid() == 0 or os.getgid() == 0:
    sys.exit("请直接使用当前 Mac 用户运行，不要用 sudo。")
os.umask(0o077)
root = repo / "build" / "_compose-check"
root.mkdir(parents=True, exist_ok=True)
work = Path(tempfile.mkdtemp(prefix="bind-", dir=root))
data = work / "data"
data.mkdir(mode=0o700)
started = time.monotonic()
deadline = started + 29 * 60
owned = []
report = {"passed": False, "work": str(work), "uid": os.getuid(), "gid": os.getgid(), "checks": [], "containers": []}


def interrupted(_signum, _frame):
    raise TimeoutError("生命周期验证已被中断或达到三十分钟截止")


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.setitimer(signal.ITIMER_REAL, 29 * 60)


def run(args, log=None, limit=60, check=True):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("生命周期验证达到截止时间")
    result = subprocess.run(args, cwd=repo, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=min(limit, remaining))
    output = result.stdout.decode("utf-8", "replace")
    if log is not None:
        (work / log).write_text(output)
    if check and result.returncode:
        raise RuntimeError(f"命令失败（{result.returncode}），请查看 {log or args[0]}")
    return result.returncode, output.strip()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def record(name, **values):
    report["checks"].append({"name": name, "passed": True, **values})
    print(f"通过：{name}", flush=True)


def read_json(path):
    return json.loads(path.read_text())


def save_json(name, value):
    (work / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def events(account):
    result = []
    for path in sorted((data / "experiment-fixture" / str(account) / "logs").glob("events-*.jsonl")):
        for line in path.read_text().splitlines():
            if line.strip():
                result.append(json.loads(line))
    return result


def journal(account):
    return read_json(data / "experiment-fixture" / str(account) / "visits.json")


def fixture(account, hostname):
    return create(f"fixture-{account}-{hostname}", hostname, ["timeout", "120s", "sh", "-eu", "-c", fixture_command, "fixture", "--data", "/data", "--config", "/data/bind-fixture-settings.json", "--fixture", "--once", "--account", str(account), "--details", "2"])


def create(name, hostname, command):
    args = ["docker", "create", "--cidfile", str(work / (name + ".id")), "--network", "none", "--no-healthcheck", "--hostname", hostname,
            "--security-opt", "seccomp=unconfined", "-e", f"PUID={os.getuid()}", "-e", f"PGID={os.getgid()}", "-e", "TZ=Asia/Shanghai",
            "--mount", f"type=bind,src={data},dst=/data"]
    for variable in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"]:
        args.extend(["-e", variable + "="])
    _, cid = run(args + [image_id] + command, name + ".create.log")
    require(re.fullmatch(r"[a-f0-9]{64}", cid), "Docker 没有返回唯一容器编号")
    owned.append(cid)
    (work / (name + ".id")).write_text(cid + "\n")
    _, mounts = run(["docker", "inspect", cid, "--format", "{{json .Mounts}}"])
    mounts = json.loads(mounts)
    require(any(m["Type"] == "bind" and m["Source"] == str(data) and m["Destination"] == "/data" and m["RW"] for m in mounts), "实际运行的容器没有使用独立的可写 bind mount")
    report["containers"].append({"name": name, "id": cid, "hostname": hostname, "mounts": mounts})
    run(["docker", "start", cid], name + ".start.log")
    return cid


def logs(cid, name):
    _, output = run(["docker", "logs", cid])
    output = re.sub(r'("token"\s*:\s*)"[^"\n]*"', r'\1"[已隐去]"', output)
    (work / (name + ".stdout.log")).write_text(output + "\n")


def wait(cid, name, expected=0):
    _, exit_code = run(["docker", "wait", cid], name + ".wait.log", limit=130)
    logs(cid, name)
    _, state = run(["docker", "inspect", cid, "--format", "{{json .State}}"])
    state = json.loads(state)
    save_json(name + ".state.json", state)
    require(int(exit_code) == expected and state["ExitCode"] == expected and not state["OOMKilled"] and not state["Running"], f"{name} 退出状态不符，请看 stdout/state 文件")


def poll(test, description, seconds=30):
    end = min(deadline, time.monotonic() + seconds)
    while time.monotonic() < end:
        if test():
            return
        time.sleep(0.02)
    raise RuntimeError("等待超时：" + description)


def private(path, directory=False):
    mode = path.stat()
    require(mode.st_uid == os.getuid() and mode.st_gid == os.getgid(), f"宿主上的属主不符：{path.relative_to(data)}")
    require(stat.S_IMODE(mode.st_mode) == (0o700 if directory else 0o600), f"私有文件或目录权限不符：{path.relative_to(data)}")
    if directory:
        list(path.iterdir())
    else:
        with path.open("rb") as stream:
            stream.read(1)


def sqlite_snapshot(name):
    db = data / "kakatta.db"
    private(db)
    for suffix in ["-wal", "-shm"]:
        private(Path(str(db) + suffix))
    with contextlib.closing(sqlite3.connect(db.as_uri() + "?mode=ro", uri=True, timeout=5)) as connection:
        result = {"journal_mode": connection.execute("PRAGMA journal_mode").fetchone()[0],
                  "integrity": connection.execute("PRAGMA integrity_check").fetchone()[0],
                  "migrations": connection.execute("SELECT version,applied_at FROM schema_migrations ORDER BY version").fetchall(),
                  "accounts": connection.execute("SELECT count(*) FROM accounts").fetchone()[0],
                  "tasks": connection.execute("SELECT count(*) FROM tasks").fetchone()[0]}
    require(result["journal_mode"] == "wal" and result["integrity"] == "ok" and result["accounts"] == result["tasks"] == 0, "宿主无法完整读取正式服务的 WAL 数据，或空服务出现了账号任务")
    save_json(name + ".sqlite.json", result)
    return result


def health(cid):
    status, body = run(["docker", "exec", "--user", f"{os.getuid()}:{os.getgid()}", cid, "curl", "--noproxy", "*", "--max-time", "2", "-fsS", "http://127.0.0.1:18080/healthz"], check=False, limit=5)
    if status:
        return False
    require(json.loads(body).get("status") == "ok", "正式服务健康检查返回异常")
    return True


fixture_command = r'''
status=0
experiment "$@" || status=$?
attempt=0
while :; do
    remaining=
    for path in /proc/[0-9]*/comm; do
        [ -r "$path" ] || continue
        IFS= read -r name < "$path" || continue
        case "$name" in chrome*|google-chrome*|Xvfb|x11vnc) remaining="$remaining $name" ;; esac
    done
    [ -n "$remaining" ] || break
    attempt=$((attempt + 1))
    [ "$attempt" -lt 50 ] || { printf '退出后仍有进程：%s\n' "$remaining" >&2; exit 1; }
    sleep 0.1
done
printf '%s\n' '进程检查通过：没有浏览器、屏幕或远程画面残留。'
exit "$status"
'''

try:
    _, revision = run(["git", "rev-parse", "HEAD"])
    if len(sys.argv) == 3:
        require(sys.argv[2] == revision, "指定的版本必须等于当前已提交 HEAD；不接受工作区内容或其他版本")
    report["revision"] = revision
    _, context = run(["docker", "context", "show"])
    _, environment = run(["docker", "info", "--format", "{{json .OperatingSystem}}"])
    report["docker"] = {"context": context, "operating_system": json.loads(environment)}
    archive = work / "source.tar"
    with archive.open("wb") as stream:
        subprocess.run(["git", "archive", "--format=tar", revision], cwd=repo, stdout=stream, check=True, timeout=30)
    source = work / "source"
    source.mkdir()
    run(["tar", "-xf", str(archive), "-C", str(source)])
    report["archive_sha256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
    image = "kakatta:bind-" + revision[:12] + "-" + work.name[5:]
    run(["docker", "build", "--build-arg", "VCS_REF=" + revision, "--tag", image, str(source)], "build.log", limit=20 * 60)
    _, image_id = run(["docker", "image", "inspect", image, "--format", "{{.Id}}"])
    _, architecture = run(["docker", "image", "inspect", image_id, "--format", "{{.Architecture}}"])
    report["image"] = {"tag": image, "id": image_id, "architecture": architecture}
    save_json("image.json", report["image"])
    record("只从已提交版本构建镜像", revision=revision, image_id=image_id)
    save_json("data/bind-fixture-settings.json", {"version": 1, "timezone": "Asia/Shanghai", "account_gap_seconds": 1, "gap_jitter_seconds": 0, "quiet_enabled": False})

    first = fixture(1, "bind-first")
    wait(first, "first")
    before = journal(1)
    before_events = events(1)
    summaries = data / "experiment-fixture" / "1" / "logs" / "summaries"
    first_summaries = {str(p): hashlib.sha256(p.read_bytes()).hexdigest() for p in summaries.glob("*/summary.json")}
    require(len(before["visits"]) > 0 and len(first_summaries) == 1 and before_events, "首次运行没有保存访问、日志或摘要")
    save_json("first.visits.json", before)
    time.sleep(1.1)
    second = fixture(1, "bind-second")
    wait(second, "second")
    after = journal(1)
    after_events = events(1)
    require(len(after["visits"]) > len(before["visits"]) and after["visits"][:len(before["visits"])] == before["visits"], "第二次运行没有保留第一次的访问额度")
    require(after_events[:len(before_events)] == before_events and len(after_events) > len(before_events), "重启后日志被清空或覆盖")
    require(all(a["sequence"] < b["sequence"] for a, b in zip(after_events, after_events[1:])), "日志序号在重启后回退或重复")
    require(len(list(summaries.glob("*/summary.json"))) == 2 and all(hashlib.sha256(Path(p).read_bytes()).hexdigest() == digest for p, digest in first_summaries.items()), "重启后摘要被覆盖")
    for path in summaries.glob("*/summary.json"):
        summary = read_json(path)
        require(summary["fixture"] and summary["phase"] == "completed" and summary["detail_count"] == 2 and summary["environment"]["commit"] == revision and summary["environment"]["chrome_version"].startswith("Chrome/"), "实际镜像版本或单次假页面流程摘要不符")
    save_json("second.visits.json", after)
    record("重复实验保留访问额度、追加日志与独立摘要", first_visits=len(before["visits"]), second_visits=len(after["visits"]), events=len(after_events))

    crashed = fixture(2, "bind-before-kill")
    profile = data / "experiment-fixture" / "2" / "browser"
    lock = profile / "SingletonLock"
    def ready_to_kill():
        try:
            state = journal(2)
            return state["in_progress"] and len(state["visits"] or []) > 0 and lock.is_symlink()
        except (OSError, ValueError):
            return False
    poll(ready_to_kill, "第二个实验账号开始访问并持有 Chrome SingletonLock")
    marker = profile / ".bind-check-marker"
    marker.write_text("bind-check\n")
    old_lock = os.readlink(lock)
    require(old_lock.startswith("bind-before-kill-"), "Chrome 锁里没有首次容器的主机名")
    run(["docker", "kill", crashed], "kill.log")
    wait(crashed, "killed", expected=137)
    crash_state = journal(2)
    require(crash_state["in_progress"] and lock.is_symlink(), "没有保留真实强杀留下的访问记录和 Chrome 旧锁")
    save_json("killed.visits.json", crash_state)
    blocked = fixture(2, "bind-recover-gap")
    wait(blocked, "recover-gap", expected=1)
    recovered = journal(2)
    recovered_at = datetime.datetime.fromisoformat(recovered["highest_at"].replace("Z", "+00:00"))
    crashed_at = datetime.datetime.fromisoformat(crash_state["highest_at"].replace("Z", "+00:00"))
    require(not recovered["in_progress"] and recovered["gap_seconds"] >= 1 and recovered["visits"] == crash_state["visits"] and recovered_at >= crashed_at, "崩溃恢复清空了访问或间隔保护")
    require(os.readlink(lock) == old_lock, "间隔被拦住时仍然启动了浏览器并改动旧锁")
    save_json("recovered.visits.json", recovered)
    ended = datetime.datetime.fromisoformat(recovered["ended_at"].replace("Z", "+00:00"))
    until = ended.timestamp() + recovered["gap_seconds"]
    time.sleep(max(0, until - time.time()) + 0.2)
    restarted = fixture(2, "bind-after-kill")
    poll(lambda: lock.is_symlink() and os.readlink(lock).startswith("bind-after-kill-"), "新的容器主机名接管本账号的旧 Chrome 锁")
    wait(restarted, "restarted")
    final_state = journal(2)
    require(not final_state["in_progress"] and len(final_state["visits"]) > len(crash_state["visits"]) and final_state["visits"][:len(crash_state["visits"])] == crash_state["visits"] and marker.read_text() == "bind-check\n", "重启没有保留浏览器目录及已用访问额度")
    record("强杀后保留保护、重新等待间隔并跨主机名恢复 Chrome", killed_visits=len(crash_state["visits"]), final_visits=len(final_state["visits"]), previous_lock=old_lock)

    server = create("server-first", "bind-service-first", ["kakatta", "--data", "/data", "--listen", "127.0.0.1:18080"])
    poll(lambda: health(server), "正式服务首次健康检查")
    _, filesystem = run(["docker", "exec", server, "stat", "-f", "-c", "%T", "/data"])
    report["mount_filesystem"] = filesystem
    private(data / "secret.key")
    secret = (data / "secret.key").read_bytes()
    initial_sqlite = sqlite_snapshot("service-first")
    run(["docker", "stop", "--time", "20", server], "server-first.stop.log", limit=30)
    wait(server, "server-first")
    server = create("server-second", "bind-service-second", ["kakatta", "--data", "/data", "--listen", "127.0.0.1:18080"])
    poll(lambda: health(server), "正式服务重启后的健康检查")
    second_sqlite = sqlite_snapshot("service-second")
    require((data / "secret.key").read_bytes() == secret and second_sqlite["migrations"] == initial_sqlite["migrations"], "正式服务重启更换了密钥或重建了数据库")
    run(["docker", "stop", "--time", "20", server], "server-second.stop.log", limit=30)
    wait(server, "server-second")
    record("宿主可读 SQLite WAL 与私有密钥，正式服务重启保留数据", filesystem=filesystem, migrations=len(initial_sqlite["migrations"]), secret_unchanged=True)

    readable = {"logs": 0, "summaries": 0, "bundles": 0}
    for path in sorted(data.rglob("*")):
        relative = path.relative_to(data)
        if "logs" in relative.parts or "diagnostics" in relative.parts:
            require(not path.is_symlink(), "诊断目录出现了不应有的符号链接")
            private(path, directory=path.is_dir())
            if path.is_file():
                path.read_bytes()
                key = "bundles" if "bundles" in relative.parts else "summaries" if "summaries" in relative.parts else "logs"
                readable[key] += 1
    for path in [data, data / "browser", data / "experiment-fixture" / "1" / "browser", profile]:
        private(path, directory=True)
    for account in [1, 2]:
        private(data / "experiment-fixture" / str(account) / "visits.json")
    require(readable["logs"] > 0 and readable["summaries"] >= 4, "宿主没有足够的落盘日志和运行摘要")
    record("当前 Mac 用户无需 sudo 即可读取所有已生成诊断文件", **readable)
    if not readable["bundles"]:
        report["bundle_note"] = "正常与整容器强杀不生成故障包；本轮未验证故障包生成，另由故障注入验收覆盖。"
    report["passed"] = True
except BaseException as failure:
    report["error"] = str(failure) or type(failure).__name__
    print("验证失败：" + report["error"], file=sys.stderr, flush=True)
finally:
    signal.setitimer(signal.ITIMER_REAL, 60)
    cleanup_ok = True
    for path in work.glob("*.id"):
        cid = path.read_text().strip()
        if re.fullmatch(r"[a-f0-9]{64}", cid) and cid not in owned:
            owned.append(cid)
    if owned:
        try:
            result = subprocess.run(["docker", "rm", "-f", *owned], stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=45)
            (work / "cleanup.log").write_bytes(result.stdout)
            cleanup_ok = result.returncode == 0
        except BaseException as failure:
            cleanup_ok = False
            (work / "cleanup.log").write_text(str(failure))
    report["cleanup_passed"] = cleanup_ok
    report["passed"] = report["passed"] and cleanup_ok
    report["duration_seconds"] = round(time.monotonic() - started, 3)
    save_json("result.json", report)
    signal.setitimer(signal.ITIMER_REAL, 0)
    print(f"完整产物：{work}\n退出码：{0 if report['passed'] else 1}", flush=True)
sys.exit(0 if report["passed"] else 1)
PY
