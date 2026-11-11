#!/bin/sh
set -eu

case "${1:-}" in
    --help|-h)
        printf '%s\n' '用法：deploy/test-dev-runtime.sh' '复制当前源码后构建独立开发镜像，只连接断网容器里的本地假服务。'
        exit 0
        ;;
esac
[ "$#" -eq 0 ] || { printf '%s\n' '本脚本不接受参数。' >&2; exit 2; }
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 - "$repo" <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time

repo = Path(sys.argv[1])
os.umask(0o077)
root = repo / "build" / "_dev-container"
root.mkdir(parents=True, exist_ok=True)
work = Path(tempfile.mkdtemp(prefix="runtime-", dir=root))
source, shared = work / "source", work / "shared"
source.mkdir()
shared.mkdir()
for name in ["data", "fixture-data", "prepare-data"]:
    (work / name).mkdir()
project = "kakatta-dev-" + work.name.replace("_", "-")
image = "kakatta:" + work.name.lower()
volumes = [project + "_gomodcache", project + "_gocache"]
report = {"passed": False, "work": str(work), "project": project, "stages": []}
compose = []
driver = None
driver_output = None


def interrupted(_signum, _frame):
    raise TimeoutError("开发容器整体验收已中断或超过二十分钟")


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.setitimer(signal.ITIMER_REAL, 1200)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def invoke(args, name, timeout=60, check=True):
    with (work / (name + ".log")).open("wb") as output:
        process = subprocess.run(args, cwd=repo, env=environment, stdout=output, stderr=subprocess.STDOUT, timeout=timeout)
    if check:
        require(process.returncode == 0, name + "失败，请查专项日志")
    return process.returncode, (work / (name + ".log")).read_text(errors="replace")


def wait_file(path, process=None, seconds=150):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if path.exists():
            return
        require(process is None or process.poll() is None, "验收驱动提前退出，请查驱动日志")
        time.sleep(0.2)
    raise TimeoutError(path.name + "等待超时")


def container_id(service):
    _, body = invoke(compose + ["ps", "--all", "--quiet", service], "id-" + service)
    cid = body.strip()
    require(re.fullmatch(r"[a-f0-9]{64}", cid), "没有找到本项目容器：" + service)
    return cid


def stopped(cid, name):
    _, body = invoke(["docker", "wait", cid], name + "-wait", timeout=180)
    require(body.strip() == "0", name + "没有正常退出")
    _, body = invoke(["docker", "inspect", cid, "--format", "{{json .State}}"], name + "-state")
    state = json.loads(body)
    require(state["ExitCode"] == 0 and not state["OOMKilled"] and not state["Running"], name + "退出时发生强杀或内存故障")
    report["stages"].append({"stage": name, "exit_code": state["ExitCode"], "oom_killed": state["OOMKilled"], "process_namespace_closed": True})


environment = dict(os.environ)
for key in ["PUID", "PGID", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "KAKATTA_SECRET_KEY", "KAKATTA_SECRET_KEY_FILE", "KAKATTA_TRUSTED_PROXIES"]:
    environment.pop(key, None)
try:
    paths = subprocess.check_output(["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=repo).decode().split("\0")
    digest = hashlib.sha256()
    for relative in sorted(filter(None, paths)):
        original = repo / relative
        if original.is_file() and not relative.startswith(("data/", "build/", ".git/")):
            destination = source / relative
            destination.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(original, destination)
            digest.update(relative.encode() + b"\0" + original.read_bytes())
    require((repo / "web/dist/index.html").is_file(), "请先生成本地网页构建产物")
    shutil.copytree(repo / "web/dist", source / "web/dist", dirs_exist_ok=True)
    report["source_sha256"] = digest.hexdigest()
    report["git_head"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=repo).decode().strip()
    report["source_kind"] = "isolated-current-source"
    invoke(["docker", "build", "--target", "dev", "--tag", image, str(source)], "build", timeout=480)
    _, body = invoke(["docker", "image", "inspect", image, "--format", "{{.Id}} {{.Architecture}}"], "image")
    image_id, architecture = body.strip().split()
    report["image"] = {"tag": image, "id": image_id, "architecture": architecture}
    print("开发镜像已构建：" + image_id, flush=True)
    for name, volume in zip(["gomodcache", "gocache"], volumes):
        invoke(["docker", "volume", "create", "--label", "com.docker.compose.project=" + project, "--label", "com.docker.compose.volume=" + name, volume], "volume-" + name)
    prepare = ["docker", "run", "--rm", "--network", "none", "--no-healthcheck", "--pull=never", "--cidfile", str(work / "prepare.id"),
               "--mount", f"type=bind,src={source},dst=/workspace", "--mount", f"type=bind,src={shared},dst=/work", "--mount", f"type=bind,src={work / 'prepare-data'},dst=/data",
               "--mount", f"type=volume,src={volumes[0]},dst=/go/pkg/mod", "--mount", f"type=volume,src={volumes[1]},dst=/go/build-cache",
               "--env", "PUID=1000", "--env", "PGID=1000", "--env", "GOPROXY=off", "--env", "GOSUMDB=off", "--entrypoint", "/bin/sh", image_id, "-ec",
               "chown -R 1000:1000 /work /workspace; exec /usr/local/bin/kakatta-dev-entrypoint go test -c -buildvcs=false -tags kakatta_dev -o /work/runtime.test ./cmd/kakatta"]
    invoke(prepare, "compile-tests", timeout=180)
    q = lambda value: json.dumps(str(value))
    override = work / "compose.runtime.yaml"
    override.write_text(f'''services:
  dev:
    build: !reset null
    image: {q(image_id)}
    ports: !reset []
    extra_hosts: !reset []
    network_mode: service:fixtures
    environment:
      PUID: "1000"
      PGID: "1000"
      GOPROXY: "off"
      GOSUMDB: "off"
    volumes: !override
      - {{type: bind, source: {q(source)}, target: /workspace, read_only: true}}
      - {{type: bind, source: {q(work / 'data')}, target: /data}}
      - {{type: bind, source: {q(shared)}, target: /work}}
      - {{type: volume, source: gomodcache, target: /go/pkg/mod}}
      - {{type: volume, source: gocache, target: /go/build-cache}}
    command: [/usr/local/bin/devhost, --mode, real, --root, /workspace, --state, /data/.development, --data, /data, --listen, "0.0.0.0:18081", --backend-listen, "127.0.0.1:8080", --proxy-listen, "0.0.0.0:18080", --air, /usr/local/bin/air, --poll, --fixture, /work/fixture.json]
    depends_on:
      fixtures: {{condition: service_healthy}}
  fixtures:
    image: {q(image_id)}
    network_mode: none
    environment: {{PUID: "1000", PGID: "1000", KAKATTA_DEV_RUNTIME: "1", KAKATTA_DEV_RUNTIME_WORK: /work}}
    volumes:
      - {{type: bind, source: {q(work / 'fixture-data')}, target: /data}}
      - {{type: bind, source: {q(shared)}, target: /work}}
    command: [/work/runtime.test, -test.run, "^TestDevelopmentRuntimeFixtures$", -test.v, -test.timeout, 15m]
    healthcheck:
      test: [CMD, curl, --noproxy, "*", -fsS, "http://127.0.0.1:19090/healthz"]
      interval: 1s
      timeout: 2s
      retries: 30
    stop_grace_period: 20s
  driver:
    image: {q(image_id)}
    network_mode: service:fixtures
    user: "1000:1000"
    entrypoint: [/usr/bin/tini, --]
    environment: {{KAKATTA_DEV_RUNTIME: "1", KAKATTA_DEV_RUNTIME_WORK: /work}}
    volumes:
      - {{type: bind, source: {q(work / 'data')}, target: /data, read_only: true}}
      - {{type: bind, source: {q(shared)}, target: /work}}
      - {{type: bind, source: {q(source)}, target: /workspace}}
    command: [/work/runtime.test, -test.run, "^TestDevelopmentRuntimeDriver$", -test.v, -test.timeout, 8m]
volumes:
  gomodcache:
    name: {q(volumes[0])}
  gocache:
    name: {q(volumes[1])}
''')
    compose = ["docker", "compose", "--project-name", project, "--env-file", "/dev/null", "-f", str(repo / "compose.dev.yaml"), "-f", str(override)]
    _, body = invoke(compose + ["config", "--format", "json"], "compose")
    effective = json.loads(body)["services"]
    require(not effective["dev"].get("ports") and effective["fixtures"]["network_mode"] == "none", "验收配置暴露了宿主端口或外部网络")
    for service in ["dev", "driver"]:
        require(effective[service]["network_mode"] == "service:fixtures", "角色未共用断网回环")
    require(all(entry.get("source") != str(repo / "data") for value in effective.values() for entry in value.get("volumes", [])), "验收错误挂载了根数据目录")
    invoke(compose + ["up", "--detach", "--no-build", "--wait", "--wait-timeout", "180", "dev"], "first-up", timeout=210)
    first_id = container_id("dev")
    invoke(compose + ["run", "--rm", "--no-deps", "--env", "KAKATTA_DEV_RUNTIME_STAGE=first", "driver"], "driver-first", timeout=300)
    first = json.loads((shared / "first.json").read_text())
    require(first["passed"] is True, "首次验收报告未通过")
    report["stages"].append(first)
    invoke(["docker", "kill", "--signal", "SIGINT", first_id], "first-interrupt")
    stopped(first_id, "first-stop")
    invoke(compose + ["logs", "--no-color", "dev"], "first-service-log")
    print("运行中热更新、正式登录和正常推送已通过；开始完整容器重启验收", flush=True)
    invoke(compose + ["up", "--detach", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "180", "dev"], "restart-up", timeout=210)
    second_id = container_id("dev")
    require(second_id != first_id, "没有真正更换开发容器")
    driver_output = (work / "driver-restart.log").open("wb")
    driver = subprocess.Popen(compose + ["run", "--rm", "--no-deps", "--env", "KAKATTA_DEV_RUNTIME_STAGE=restart", "driver"], cwd=repo, env=environment, stdout=driver_output, stderr=subprocess.STDOUT)
    wait_file(shared / "shutdown-ready.json", driver)
    invoke(["docker", "top", second_id], "active-processes")
    invoke(["docker", "kill", "--signal", "SIGINT", second_id], "active-interrupt")
    require(driver.wait(timeout=120) == 0, "重启或单次退出信号验收失败")
    driver_output.close()
    second = json.loads((shared / "restart.json").read_text())
    require(second["passed"] is True, "重启验收报告未通过")
    report["stages"].append(second)
    stopped(second_id, "active-stop")
    _, logs = invoke(compose + ["logs", "--no-color", "dev"], "last-service-log")
    require("后端与编译监听已退出" in logs, "缺少管理器安全退出完成证据")
    require((work / "data/secret.key").stat().st_mode & 0o777 == 0o600, "宿主密钥权限不正确")
    report["passed"] = True
    print("完整容器重启与活跃运行中的单次SIGINT安全退出已通过", flush=True)
except BaseException as failure:
    report["error"] = str(failure) or type(failure).__name__
    print("开发容器整体验收失败：" + report["error"], file=sys.stderr)
finally:
    signal.setitimer(signal.ITIMER_REAL, 90)
    if compose:
        invoke(compose + ["logs", "--no-color"], "all-service-logs", check=False)
        invoke(compose + ["down", "--remove-orphans", "--volumes", "--timeout", "15"], "cleanup", timeout=45, check=False)
    if driver is not None and driver.poll() is None:
        driver.terminate()
        driver.wait(timeout=10)
    if driver_output is not None:
        driver_output.close()
    prepare_id = work / "prepare.id"
    if prepare_id.exists() and re.fullmatch(r"[a-f0-9]{64}", prepare_id.read_text().strip()):
        subprocess.run(["docker", "rm", "-f", prepare_id.read_text().strip()], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
    for volume in volumes:
        subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
    remaining = subprocess.check_output(["docker", "ps", "--all", "--quiet", "--filter", "label=com.docker.compose.project=" + project]).decode().split()
    remaining += [volume for volume in volumes if subprocess.run(["docker", "volume", "inspect", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0]
    report["cleanup_passed"] = not remaining
    report["remaining_resources"] = remaining
    report["passed"] = report["passed"] and not remaining
    (work / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    signal.setitimer(signal.ITIMER_REAL, 0)
    print("整体验收产物：" + str(work), flush=True)
sys.exit(0 if report["passed"] else 1)
PY
