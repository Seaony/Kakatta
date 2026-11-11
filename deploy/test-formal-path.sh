#!/bin/sh
set -eu

case "${1:-}" in
    --help|-h)
        printf '%s\n' '用法：deploy/test-formal-path.sh [已提交 HEAD 编号] [--base 已提交旧版本]' '只构建提交归档，三个角色断网共享回环；完整验收上限六十分钟。'
        exit 0
        ;;
esac
command -v python3 >/dev/null 2>&1 || {
    printf '%s\n' '需要 Python 3 编排 Compose 并检查报告。' >&2
    exit 2
}
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 - "$repo" "$@" <<'PY'
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time

repo = Path(sys.argv[1])
parser = argparse.ArgumentParser(description="从提交归档走通正式服务网页流程")
parser.add_argument("head", nargs="?")
parser.add_argument("--base")
options = parser.parse_args(sys.argv[2:])
if os.getuid() == 0 or os.getgid() == 0:
    sys.exit("请使用宿主普通用户运行，不要用 sudo。")
os.umask(0o077)
root = repo / "build" / "_formal-path"
root.mkdir(parents=True, exist_ok=True)
work = Path(tempfile.mkdtemp(prefix="run-", dir=root))
for name in ["data", "meta", "logs", "reports", "screens", "client-browser"]:
    (work / name).mkdir(mode=0o700)
project = "kakatta-formal-" + work.name[4:]
started = time.monotonic()
deadline = started + 59 * 60
environment = dict(os.environ)
compose_file = None
event_process = None
event_output = None
observed = set()
report = {"passed": False, "project": project, "work": str(work), "phases": [], "images": [], "snapshots": [], "commands": []}


def interrupted(_signum, _frame):
    raise TimeoutError("正式路径验收被中断或达到六十分钟截止")


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.setitimer(signal.ITIMER_REAL, 59 * 60)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def save(name, value):
    (work / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def command(args, name, limit=60, check=True):
    remaining = deadline - time.monotonic()
    if remaining <= 0:
        raise TimeoutError("正式路径验收达到截止时间")
    before = time.monotonic()
    log = work / "logs" / (name + ".log")
    with log.open("wb") as stream:
        process = subprocess.Popen(args, cwd=repo, env=environment, stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            code = process.wait(timeout=min(limit, remaining))
        except BaseException:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
            raise
    report["commands"].append({"name": name, "exit_code": code, "duration_seconds": round(time.monotonic() - before, 3), "log": str(log.relative_to(work))})
    if check:
        require(code == 0, f"{name} 失败（退出码 {code}），请查 {log}")
    return code, log.read_text(errors="replace").strip()


def compose(*args):
    return ["docker", "compose", "--env-file", "/dev/null", "--project-name", project, "--project-directory", str(compose_file.parent.parent), "--file", str(compose_file), *args]


def archive(revision, name):
    source = work / name
    source.mkdir(mode=0o700)
    tar = work / (name + ".tar")
    command(["git", "archive", "--format=tar", "--output", str(tar), revision], name + "-archive")
    command(["tar", "-xf", str(tar), "-C", str(source)], name + "-extract")
    report.setdefault("archives", []).append({"revision": revision, "sha256": hashlib.sha256(tar.read_bytes()).hexdigest(), "source": str(source.relative_to(work))})
    return source


def select_image(source, revision, phase):
    environment.update({"KAKATTA_FORMAL_SOURCE": str(source), "KAKATTA_FORMAL_COMMIT": revision,
                        "KAKATTA_FORMAL_VERSION": f"v0.0.{0 if phase == 'base' else 1}-formal+{revision[:12]}",
                        "KAKATTA_FORMAL_IMAGE": f"kakatta:formal-{phase}-{revision[:12]}-{work.name[4:]}",
                        "KAKATTA_FORMAL_WORK": str(work), "KAKATTA_FORMAL_UID": str(os.getuid()), "KAKATTA_FORMAL_GID": str(os.getgid())})
    _, config = command(compose("--profile", "driver", "config", "--format", "json"), phase + "-compose-config")
    config = json.loads(config)
    services = config["services"]
    require(set(services) == {"fixtures", "app", "driver"}, "测试 Compose 角色不符")
    for name, service in services.items():
        require(service["network_mode"] == ("none" if name == "fixtures" else "service:fixtures") and not service.get("ports") and not service.get("networks"), "测试 Compose 出现了外部网络或宿主端口")
        require(service["build"]["context"] == str(source) and service["build"]["target"] == "formal-test", "测试镜像没有使用指定提交的源码归档")
        require(service["environment"]["KAKATTA_FORMAL_PATH"] == "1", "测试开关未启用")
        require(all(service["environment"].get(k, "") == "" for k in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"]), "测试容器继承了宿主代理")
        mounts = {entry["target"]: entry["source"] for entry in service["volumes"]}
        require(mounts == {"/data": str(work / "data"), "/work": str(work)}, "测试目录挂载不符")
    save("meta/" + phase + "-compose.json", config)


def build_image(phase):
    command(compose("build", "app"), phase + "-compose-build", limit=20 * 60)
    _, image = command(["docker", "image", "inspect", environment["KAKATTA_FORMAL_IMAGE"], "--format", "{{json .}}"], phase + "-image-inspect")
    image = json.loads(image)
    identity = {"phase": phase, "tag": environment["KAKATTA_FORMAL_IMAGE"], "id": image["Id"], "architecture": image["Architecture"], "revision": environment["KAKATTA_FORMAL_COMMIT"], "version": environment["KAKATTA_FORMAL_VERSION"]}
    report["images"].append(identity)
    save("meta/" + phase + "-image.json", identity)
    print("已构建：" + phase + " " + identity["id"], flush=True)
    return identity


def ids():
    _, output = command(["docker", "ps", "-a", "-q", "--no-trunc", "--filter", "label=com.docker.compose.project=" + project], "owned-containers")
    found = output.splitlines() if output else []
    require(all(re.fullmatch(r"[0-9a-f]{64}", cid) for cid in found), "未取得精确项目容器编号")
    observed.update(found)
    return found


def snapshot(name):
    found = ids()
    if not found:
        return []
    _, output = command(["docker", "inspect", *found], name + "-inspect")
    state = json.loads(output)
    result = []
    fixture = next((c["Id"] for c in state if c["Config"]["Labels"].get("com.docker.compose.service") == "fixtures"), None)
    require(fixture is not None, "项目没有假的外部服务角色")
    for container in state:
        role = container["Config"]["Labels"]["com.docker.compose.service"]
        mode = container["HostConfig"]["NetworkMode"]
        require(mode == ("none" if role == "fixtures" else "container:" + fixture), "实际容器没有限制在同一断网回环")
        require(not container["HostConfig"].get("PortBindings") and not container["NetworkSettings"].get("Networks"), "实际测试容器接入了外部网络或映射端口")
        require(not container["State"]["OOMKilled"], "测试容器发生 OOM")
        result.append({"id": container["Id"], "role": role, "image": container["Image"], "state": container["State"], "network_mode": mode})
    save("meta/" + name + "-containers.json", result)
    report["snapshots"].append({"name": name, "file": "meta/" + name + "-containers.json"})
    return result


def lifecycle():
    path = work / "server-lifecycle.jsonl"
    require(path.is_file(), "正式服务没有保存生命周期证据")
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def stopped_since(before):
    entries = lifecycle()[before:]
    stopped = [entry for entry in entries if entry["event"] == "stopped"]
    require(stopped and all(e.get("success") is True and e.get("process_cleanup_passed") is True for e in stopped), "正式服务停止或浏览器回收没有通过")


def journey(stage):
    name = project + "-driver-" + stage
    phase = {"stage": stage, "passed": False, "exit_code": None}
    report["phases"].append(phase)
    try:
        code, _ = command(compose("run", "--no-deps", "--pull", "never", "--interactive=false", "-T", "--name", name, "-e", "KAKATTA_FORMAL_STAGE=" + stage, "driver"), stage + "-journey", limit=16 * 60, check=False)
    except BaseException as failure:
        phase["error"] = str(failure) or type(failure).__name__
        raise
    phase["exit_code"] = code
    snapshot(stage)
    _, inspected = command(["docker", "inspect", name], stage + "-driver-inspect")
    inspected = json.loads(inspected)[0]
    observed.add(inspected["Id"])
    state = inspected["State"]
    phase.update({"container_id": inspected["Id"], "container_exit_code": state["ExitCode"], "oom_killed": state["OOMKilled"]})
    path = work / "reports" / (stage + ".json")
    require(path.is_file(), f"{stage} 没有落盘的网页流程报告")
    result = json.loads(path.read_text())
    phase.update({"report": "reports/" + stage + ".json", "browser_closed": result.get("browser_closed"), "process_cleanup_passed": result.get("process_cleanup_passed")})
    require(code == state["ExitCode"] == 0 and not state["OOMKilled"] and not state["Running"], f"{stage} 网页流程失败，请查对应 journey 日志和报告")
    require(result.get("stage") == stage and all(result.get(key) is True for key in ["passed", "browser_closed", "process_cleanup_passed"]), f"{stage} 报告未通过或浏览器未回收")
    steps = result.get("steps")
    require(isinstance(steps, list) and steps and all(isinstance(item.get("number"), int) and isinstance(item.get("result"), str) and item["result"] for item in steps), f"{stage} 缺少逐步验收结果")
    expected = set(range(1, 8)) if stage == "first" else {8}
    require({item["number"] for item in steps} == expected and len(steps) == len(expected), f"{stage} 没有逐项完成约定步骤")
    phase["passed"] = True
    print("通过网页阶段：" + stage, flush=True)


def ready():
    end = min(deadline, time.monotonic() + 90)
    while time.monotonic() < end:
        _, cid = command(compose("ps", "-q", "app"), "app-ready-id")
        if cid:
            _, state = command(["docker", "inspect", cid, "--format", "{{json .State}}"], "app-ready-state")
            state = json.loads(state)
            require(not state["OOMKilled"], "正式服务发生 OOM")
            if state.get("Health", {}).get("Status") == "healthy":
                return
        time.sleep(0.25)
    raise TimeoutError("正式服务重启后没有恢复健康")


try:
    _, head = command(["git", "rev-parse", "HEAD"], "source-head")
    require(options.head is None or options.head == head, "参数必须等于当前已提交 HEAD，不接受工作区版本")
    _, base = command(["git", "rev-parse", "--verify", "--end-of-options", (options.base or head) + "^{commit}"], "source-base")
    command(["git", "merge-base", "--is-ancestor", base, head], "base-is-ancestor")
    report.update({"head": head, "base": base, "same_source_upgrade": base == head})
    command(["git", "status", "--short"], "source-status")
    head_source = archive(head, "source-head")
    base_source = head_source if base == head else archive(base, "source-base")
    compose_file = head_source / "deploy" / "compose.formal-path.yaml"
    require(compose_file.is_file(), "已提交 HEAD 缺少正式路径 Compose，请先提交测试构建块")
    select_image(base_source, base, "base")
    first_image = build_image("base")
    event_output = (work / "meta" / "container-events.jsonl").open("wb")
    event_process = subprocess.Popen(["docker", "events", "--filter", "type=container", "--filter", "label=com.docker.compose.project=" + project, "--filter", "event=die", "--filter", "event=oom", "--format", "{{json .}}"], stdout=event_output, stderr=subprocess.STDOUT)
    command(compose("up", "-d", "--wait", "--wait-timeout", "90", "--no-build", "fixtures", "app"), "first-compose-up", limit=120)
    initial = snapshot("first-start")
    fixture_id = next(entry["id"] for entry in initial if entry["role"] == "fixtures")
    app_id = next(entry["id"] for entry in initial if entry["role"] == "app")
    journey("first")
    before = len(lifecycle())
    command(compose("restart", "--no-deps", "--timeout", "20", "app"), "app-compose-restart", limit=45)
    ready()
    stopped_since(before)
    restarted = snapshot("restarted")
    require(any(e["id"] == fixture_id and e["role"] == "fixtures" for e in restarted) and any(e["id"] == app_id and e["role"] == "app" for e in restarted), "重启改动了假的外部服务或意外重建了应用容器")
    journey("restart")
    select_image(head_source, head, "upgrade")
    upgraded_image = build_image("upgrade")
    require(upgraded_image["id"] != first_image["id"], "升级没有重新构建新镜像")
    before = len(lifecycle())
    command(compose("up", "-d", "--wait", "--wait-timeout", "90", "--no-build", "--no-deps", "--force-recreate", "app"), "upgrade-compose-up", limit=120)
    stopped_since(before)
    upgraded = snapshot("upgraded")
    require(any(e["id"] == fixture_id and e["role"] == "fixtures" for e in upgraded), "升级重启了假的外部服务")
    require(any(e["role"] == "app" and e["image"] == upgraded_image["id"] and e["id"] != app_id for e in upgraded), "正式服务没有用新镜像替换旧容器")
    journey("upgrade")
    report["passed"] = True
except BaseException as failure:
    report["error"] = str(failure) or type(failure).__name__
    print("验收失败：" + report["error"], file=sys.stderr, flush=True)
finally:
    deadline = time.monotonic() + 55
    signal.setitimer(signal.ITIMER_REAL, 60)
    cleanup_ok = True
    if compose_file is not None and environment.get("KAKATTA_FORMAL_WORK"):
        try:
            before = len(lifecycle()) if (work / "server-lifecycle.jsonl").is_file() else 0
            command(compose("stop", "--timeout", "20", "app", "fixtures"), "compose-stop", limit=30)
            snapshot("stopped")
            command(compose("logs", "--no-color", "--timestamps"), "compose-service-logs", check=False, limit=5)
            if report["passed"]:
                stopped_since(before)
        except BaseException as failure:
            cleanup_ok = False
            report["stop_error"] = str(failure) or type(failure).__name__
        try:
            command(compose("down", "--remove-orphans", "--timeout", "5"), "compose-down", limit=15)
            _, remaining = command(["docker", "ps", "-a", "-q", "--filter", "label=com.docker.compose.project=" + project], "remaining-project-containers", limit=5)
            require(not remaining, "清理后仍有本项目的容器")
        except BaseException as failure:
            cleanup_ok = False
            report["cleanup_error"] = str(failure) or type(failure).__name__
    if event_process is not None:
        event_process.terminate()
        try:
            event_process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            event_process.kill()
            event_process.wait(timeout=2)
        event_output.close()
        try:
            events = [json.loads(line) for line in (work / "meta" / "container-events.jsonl").read_text().splitlines() if line.strip()]
            dies = [event for event in events if event.get("Action") == "die"]
            require(not any(event.get("Action") == "oom" for event in events), "Docker 记录了本项目 OOM")
            if report["passed"]:
                require(observed <= {event["Actor"]["ID"] for event in dies}, "缺少本项目容器的真实退出事件")
                require(all(event["Actor"]["Attributes"].get("exitCode") == "0" for event in dies), "正式服务或测试容器退出异常（含 137）")
            report["container_die_events"] = len(dies)
        except BaseException as failure:
            cleanup_ok = False
            report["events_error"] = str(failure) or type(failure).__name__
    report["cleanup_passed"] = cleanup_ok
    report["passed"] = report["passed"] and cleanup_ok
    report["duration_seconds"] = round(time.monotonic() - started, 3)
    save("result.json", report)
    signal.setitimer(signal.ITIMER_REAL, 0)
    print(f"完整产物：{work}\n退出码：{0 if report['passed'] else 1}", flush=True)
sys.exit(0 if report["passed"] else 1)
PY
