#!/bin/sh
set -eu

case "${1:-}" in
    --help|-h)
        printf '%s\n' '用法：deploy/test-entrypoint.sh [本地运行镜像]' '只运行用户和写入探针；临时挂载位于 build/_entrypoint/，不启动应用或浏览器。'
        exit 0
        ;;
esac
command -v python3 >/dev/null 2>&1 || { printf '%s\n' '需要 Python 3 检查入口输出和 Compose 配置。' >&2; exit 2; }
repo=$(cd -- "$(dirname -- "$0")/.." && pwd)
exec python3 - "$repo" "${1:-kakatta:local}" <<'PY'
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
root = repo / "build" / "_entrypoint"
root.mkdir(parents=True, exist_ok=True)
work = Path(tempfile.mkdtemp(prefix="run-", dir=root))
report = {"passed": False, "cases": [], "work": str(work)}
uid, gid = os.getuid(), os.getgid()
owned = []
require_image = None


def interrupted(_signum, _frame):
    raise TimeoutError("入口专项测试已中断或超过三分钟")


signal.signal(signal.SIGTERM, interrupted)
signal.signal(signal.SIGALRM, interrupted)
signal.setitimer(signal.ITIMER_REAL, 180)


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def invoke(args, name, env=None):
    result = subprocess.run(args, cwd=repo, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=30)
    output = result.stdout.decode("utf-8", "replace")
    (work / (name + ".log")).write_text(output)
    return result.returncode, output


probe = r'''
printf private > /data/entrypoint-probe
printf '{"uid":%s,"gid":%s,"data_uid":%s,"data_gid":%s,"file_mode":"%s"}\n' \
    "$(id -u)" "$(id -g)" "$(stat -c %u /data)" "$(stat -c %g /data)" "$(stat -c %a /data/entrypoint-probe)"
'''


def case(name, values=None, expected=None, error=None, readonly=False, user=None, tmpfs=False, symlink=False):
    folder = work / name
    folder.mkdir(mode=0o700)
    data = folder / "data"
    data.mkdir(mode=0o700)
    if symlink:
        os.symlink("/outside", data / "home")
    cidfile = work / (name + ".id")
    command = ["docker", "run", "--rm", "--pull=never", "--network", "none", "--no-healthcheck", "--cidfile", str(cidfile),
               "--entrypoint", "/usr/bin/tini", "--mount", f"type=bind,src={repo / 'deploy' / 'entrypoint.sh'},dst=/entrypoint-under-test,readonly"]
    if tmpfs:
        initial_uid, initial_gid = (0, 0) if tmpfs is True else tmpfs
        command.extend(["--tmpfs", f"/data:rw,mode=0700,uid={initial_uid},gid={initial_gid}"])
    else:
        command.extend(["--mount", f"type=bind,src={data},dst=/data" + (",readonly" if readonly else "")])
    if user:
        command.extend(["--user", user])
    command.extend([require_image, "--", "/bin/sh", "-ec", 'printf "初始属主=%s\\n" "$(stat -c %u:%g /data)"; exec /usr/bin/env -u PUID -u PGID "$@"', "entrypoint-env"])
    command.extend(key + "=" + value for key, value in (values or {}).items())
    command.extend(["/entrypoint-under-test", "/bin/sh", "-ec", probe])
    code, output = invoke(command, name)
    if cidfile.exists():
        owned.append(cidfile.read_text().strip())
    result = {"name": name, "exit_code": code, "passed": False}
    report["cases"].append(result)
    if expected is not None:
        require(code == 0, f"{name} 没有正常启动，请查专项日志")
        initial = next((line.split("=", 1)[1] for line in output.splitlines() if line.startswith("初始属主=")), None)
        require(initial is not None, f"{name} 没有记录容器实际看到的数据目录属主")
        if expected == "owner":
            expected = tuple(int(value) or 1000 for value in initial.split(":"))
        payload = next((json.loads(line) for line in output.splitlines() if line.startswith('{"uid":')), None)
        require(payload is not None and (payload["uid"], payload["gid"]) == expected and payload["file_mode"] == "600", f"{name} 使用了错误身份或创建了公开文件")
        require(f"UID={expected[0]} GID={expected[1]}" in output, f"{name} 没有记录实际运行编号")
        if not user:
            require((payload["data_uid"], payload["data_gid"]) == expected, f"{name} 没有按选用身份修正数据目录")
        result["identity"] = payload
        result["initial_owner"] = initial
        if not tmpfs:
            require((data / "entrypoint-probe").read_text() == "private", "宿主当前用户不能读取私有探针文件")
            result["host_readable"] = True
    else:
        require(code != 0 and error in output and not (data / "entrypoint-probe").exists(), f"{name} 没有在执行应用前拒绝，或缺少处理说明")
    result["passed"] = True
    print("通过：" + name, flush=True)


try:
    require(uid != 0 and gid != 0, "请使用普通宿主用户运行，不要用 sudo")
    code, info = invoke(["docker", "image", "inspect", image, "--format", "{{.Id}} {{.Architecture}}"], "image")
    require(code == 0, "找不到本地运行镜像；本脚本不会拉取或改写已有镜像")
    require_image, architecture = info.strip().split()
    report["image"] = {"id": require_image, "architecture": architecture}
    require(not re.search(r"^ENV\s+.*(?:PUID|PGID)=", (repo / "Dockerfile").read_text(), re.MULTILINE), "Dockerfile 仍会用固定编号掩盖未设置状态")
    for name, values in [("compose-unset", {}), ("compose-explicit", {"PUID": str(uid), "PGID": str(gid)}), ("compose-empty", {"PUID": "", "PGID": ""})]:
        env = dict(os.environ)
        env.pop("PUID", None)
        env.pop("PGID", None)
        env.update(values)
        code, body = invoke(["docker", "compose", "--env-file", "/dev/null", "config", "--format", "json"], name, env)
        require(code == 0, "单命令 Compose 配置不能正常渲染")
        effective = json.loads(body)["services"]["kakatta"]["environment"]
        require(all(effective.get(key) == values.get(key) for key in ["PUID", "PGID"]), "Compose 没有区分未设置、显式编号和显式空值")
        report["cases"].append({"name": name, "passed": True, "exit_code": code})
    case("bind-owner-default", expected="owner")
    case("bind-explicit", {"PUID": str(uid), "PGID": str(gid)}, expected=(uid, gid))
    case("root-owner-fallback", expected=(1000, 1000), tmpfs=True)
    case("nonzero-owner-default", expected=(12345, 12346), tmpfs=(12345, 12346))
    case("explicit-over-root-owner", {"PUID": str(uid), "PGID": str(gid)}, expected=(uid, gid), tmpfs=True)
    case("only-puid", {"PUID": str(uid)}, expected=(uid, 1000), tmpfs=True)
    case("only-pgid", {"PGID": str(gid)}, expected=(1000, gid), tmpfs=True)
    for index, value in enumerate(["", "0", "-1", "abc", "2147483648"]):
        case("invalid-puid-" + str(index), {"PUID": value}, error="PUID 必须")
    for index, value in enumerate(["", "0", "abc"]):
        case("invalid-pgid-" + str(index), {"PGID": value}, error="PGID 必须")
    case("bind-readonly", error="不能写入", readonly=True)
    case("bind-nonroot", expected=(uid, gid), user=f"{uid}:{gid}")
    case("nonroot-explicit-compatible", {"PUID": "1000", "PGID": "1000"}, expected=(uid, gid), user=f"{uid}:{gid}")
    case("reject-home-symlink", error="不能是符号链接", symlink=True)
    report["passed"] = True
except BaseException as failure:
    report["error"] = str(failure) or type(failure).__name__
    print("入口专项测试失败：" + report["error"], file=sys.stderr)
finally:
    signal.setitimer(signal.ITIMER_REAL, 30)
    for cidfile in work.glob("*.id"):
        cid = cidfile.read_text().strip()
        if re.fullmatch(r"[a-f0-9]{64}", cid) and cid not in owned:
            owned.append(cid)
    remaining = []
    for cid in owned:
        exists = subprocess.run(["docker", "inspect", cid], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        if exists.returncode == 0:
            subprocess.run(["docker", "rm", "-f", cid], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        exists = subprocess.run(["docker", "inspect", cid], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        if exists.returncode == 0:
            remaining.append(cid)
    report["cleanup_passed"] = not remaining
    report["remaining_containers"] = remaining
    report["passed"] = report["passed"] and not remaining
    (work / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    signal.setitimer(signal.ITIMER_REAL, 0)
    print(f"专项产物：{work}\n退出码：{0 if report['passed'] else 1}", flush=True)
sys.exit(0 if report["passed"] else 1)
PY
