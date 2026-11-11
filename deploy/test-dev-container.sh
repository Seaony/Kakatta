#!/bin/sh
set -eu

case "${1:-}" in
    --help|-h)
        printf '%s\n' '用法：deploy/test-dev-container.sh 本地开发镜像' '只做断网工具、编译和缓存权限探针；不启动管理器、应用或浏览器。'
        exit 0
        ;;
esac
[ "$#" -eq 1 ] || { printf '%s\n' '请传入已构建的本地开发镜像。' >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { printf '%s\n' '需要 Python 3 检查容器输出和 Compose 配置。' >&2; exit 2; }
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 - "$repo" "$1" <<'PY'
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile

repo, image = Path(sys.argv[1]), sys.argv[2]
os.umask(0o077)
root = repo / "build" / "_dev-container"
root.mkdir(parents=True, exist_ok=True)
work = Path(tempfile.mkdtemp(prefix="run-", dir=root))
report = {"passed": False, "work": str(work), "cases": []}
volumes = ["kakatta-dev-" + work.name + suffix for suffix in ["-mod", "-build"]]
uid, gid = os.getuid(), os.getgid()
image_id = ""


def interrupted(_signum, _frame):
    raise TimeoutError("开发镜像专项测试已中断或超过八分钟")


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.setitimer(signal.ITIMER_REAL, 480)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def invoke(args, name, env=None, timeout=60):
    with (work / (name + ".log")).open("wb") as output:
        result = subprocess.run(args, cwd=repo, env=env, stdout=output, stderr=subprocess.STDOUT, timeout=timeout)
    return result.returncode, (work / (name + ".log")).read_text(errors="replace")


probe = r'''
printf '初始属主=%s\n' "$(stat -c %u:%g /data)"
exec /usr/bin/env -u PUID -u PGID "$@"
'''
identity = r'''
mkdir -p /data/.development/probe
for directory in "$GOMODCACHE" "$GOCACHE"; do
    probe=$(mktemp "$directory/.probe.XXXXXX")
    rm -f "$probe"
done
printf private > /data/.development/probe/private
printf '{"uid":%s,"gid":%s,"module_owner":"%s","build_owner":"%s","private_mode":"%s"}\n' \
    "$(id -u)" "$(id -g)" "$(stat -c %u:%g "$GOMODCACHE")" "$(stat -c %u:%g "$GOCACHE")" "$(stat -c %a /data/.development/probe/private)"
'''
tools = r'''
go version
go version -m /usr/local/bin/air
go version -m /usr/local/bin/devhost
printf '机器=%s\n目标=%s/%s\n' "$(uname -m)" "$(go env GOOS)" "$(go env GOARCH)"
GOPROXY=off GOSUMDB=off go build -buildvcs=false -tags kakatta_dev -o /data/.development/probe/backend ./cmd/kakatta
test -s /data/.development/probe/backend
printf '离线编译成功\n'
'''


def case(name, values=None, expected=None, error=None, readonly=False, user=None, compile_backend=False):
    data = work / name / "data"
    data.mkdir(parents=True, mode=0o700)
    command = ["docker", "run", "--rm", "--pull=never", "--network", "none", "--no-healthcheck", "--cidfile", str(work / (name + ".id")),
               "--mount", f"type=bind,src={repo},dst=/workspace,readonly", "--mount", f"type=bind,src={data},dst=/data",
               "--mount", f"type=volume,src={volumes[0]},dst=/go/pkg/mod" + (",readonly" if readonly else ""),
               "--mount", f"type=volume,src={volumes[1]},dst=/go/build-cache" + (",readonly" if readonly else ""),
               "--entrypoint", "/usr/bin/tini"]
    if user:
        command.extend(["--user", user])
    command.extend([image_id, "--", "/bin/sh", "-ec", probe, "probe-env"])
    command.extend(key + "=" + value for key, value in (values or {}).items())
    command.extend(["/usr/local/bin/kakatta-dev-entrypoint", "/bin/sh", "-ec", identity + (tools if compile_backend else "")])
    code, output = invoke(command, name, timeout=180)
    result = {"name": name, "exit_code": code, "passed": False}
    report["cases"].append(result)
    if error:
        require(code != 0 and error in output and not (data / ".development").exists(), f"{name} 没有在执行命令前拒绝，或缺少处理说明")
    else:
        require(code == 0, f"{name} 失败，请查专项日志")
        initial = next((line.split("=", 1)[1] for line in output.splitlines() if line.startswith("初始属主=")), None)
        require(initial is not None, "没有记录容器实际看到的数据目录属主")
        if expected == "owner":
            expected = tuple(int(value) or 1000 for value in initial.split(":"))
        details = next((json.loads(line) for line in output.splitlines() if line.startswith('{"uid":')), {})
        require((details.get("uid"), details.get("gid")) == expected, f"{name} 使用了错误身份")
        require(details.get("module_owner") == f"{expected[0]}:{expected[1]}" and details.get("build_owner") == f"{expected[0]}:{expected[1]}", f"{name} 缓存卷没有跟随选用身份")
        require(details.get("private_mode") == "600" and (data / ".development/probe/private").read_text() == "private", "私有文件权限错误或宿主不能读取")
        require(f"UID={expected[0]} GID={expected[1]}" in output, "入口没有记录实际运行编号")
        result.update({"identity": details, "initial_owner": initial, "host_readable": True})
        if compile_backend:
            require(f"go version go1.27.3 linux/{report['image']['architecture']}" in output, "Go 版本或架构不符合开发镜像")
            require(re.search(r"mod\s+github.com/air-verse/air\s+v1\.67\.4", output), "Air 没有固定到 v1.67.4")
            require("-tags=kakatta_dev" in output and "离线编译成功" in output, "开发标签或预填离线编译缓存无效")
            expected_machine = {"amd64": "x86_64", "arm64": "aarch64"}[report["image"]["architecture"]]
            require(f"机器={expected_machine}" in output, "Go 工具链与运行平台不一致")
            result["offline_backend_build"] = True
    result["passed"] = True
    print("通过：" + name, flush=True)


try:
    require(uid != 0 and gid != 0, "请使用普通宿主用户运行，不要用 sudo")
    code, info = invoke(["docker", "image", "inspect", image, "--format", "{{.Id}} {{.Os}} {{.Architecture}}"], "image")
    require(code == 0, "找不到本地开发镜像；本脚本不会拉取或改写已有镜像")
    image_id, system, architecture = info.strip().split()
    require(system == "linux" and architecture in ["amd64", "arm64"], "只支持 Linux amd64 或 arm64 开发镜像")
    report["image"] = {"id": image_id, "os": system, "architecture": architecture}
    env = dict(os.environ)
    env.pop("PUID", None)
    env.pop("PGID", None)
    code, body = invoke(["docker", "compose", "--env-file", "/dev/null", "-f", "compose.dev.yaml", "config", "--format", "json"], "compose", env)
    require(code == 0, "开发 Compose 配置不能正常渲染")
    service = json.loads(body)["services"]["dev"]
    require(service["build"]["target"] == "dev" and all(service["environment"].get(key) is None for key in ["PUID", "PGID"]), "Compose 没有保留开发目标或未设置的编号")
    require({(entry["host_ip"], str(entry["published"]), entry["target"]) for entry in service["ports"]} == {("127.0.0.1", "18080", 18080), ("127.0.0.1", "18081", 18081)}, "开发端口没有只绑定宿主回环")
    mounts = {entry["target"]: entry for entry in service["volumes"]}
    require(mounts["/workspace"].get("read_only") is True and mounts["/data"]["source"] == str(repo / "data"), "源码只读或正式数据挂载路径错误")
    require(all(mounts[target]["type"] == "volume" for target in ["/go/pkg/mod", "/go/build-cache"]), "Go 缓存没有使用命名卷")
    require(not any(key in service for key in ["mem_limit", "memswap_limit", "shm_size"]), "开发 Compose 不应新增内存或共享内存限制")
    require(service["stop_grace_period"] == "20m0s", "开发 Compose 没有留出默认运行和编译收尾时间")
    require(service["security_opt"] == ["seccomp=unconfined"], "开发 Compose 没有沿用正式沙盒配置")
    require(all(service["environment"].get(key) == "" for key in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"]), "宿主代理没有清空")
    report["cases"].append({"name": "compose", "passed": True})
    for index, volume in enumerate(volumes):
        code, _ = invoke(["docker", "volume", "create", volume], "volume-" + str(index))
        require(code == 0, "不能创建独立 Go 缓存卷")
    case("owner-default-offline", expected="owner", compile_backend=True)
    case("explicit-cache-reuse", {"PUID": str(uid), "PGID": str(gid)}, expected=(uid, gid))
    case("nonroot-cache-reuse", expected=(uid, gid), user=f"{uid}:{gid}")
    case("invalid-id", {"PUID": "0"}, error="PUID 必须")
    case("readonly-cache", {"PUID": str(uid), "PGID": str(gid)}, error="Go 缓存卷", readonly=True)
    report["passed"] = True
except BaseException as failure:
    report["error"] = str(failure) or type(failure).__name__
    print("开发镜像专项测试失败：" + report["error"], file=sys.stderr)
finally:
    signal.setitimer(signal.ITIMER_REAL, 45)
    remaining = []
    for cidfile in work.glob("*.id"):
        cid = cidfile.read_text().strip()
        if re.fullmatch(r"[a-f0-9]{64}", cid):
            subprocess.run(["docker", "rm", "-f", cid], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
            if subprocess.run(["docker", "inspect", cid], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5).returncode == 0:
                remaining.append(cid)
    for volume in volumes:
        subprocess.run(["docker", "volume", "rm", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10)
        if subprocess.run(["docker", "volume", "inspect", volume], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5).returncode == 0:
            remaining.append(volume)
    report["cleanup_passed"] = not remaining
    report["remaining_resources"] = remaining
    report["passed"] = report["passed"] and not remaining
    (work / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    signal.setitimer(signal.ITIMER_REAL, 0)
    print(f"专项产物：{work}\n退出码：{0 if report['passed'] else 1}", flush=True)
sys.exit(0 if report["passed"] else 1)
PY
