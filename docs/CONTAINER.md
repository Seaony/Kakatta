# 容器与实验工具

默认入口是正式服务，镜像另带单次可行性实验工具。当前正在按里程碑实现，网页基础页还没有接上完整产品流程，不能当作 1.0 已交付。真实账号实验由作者按 `EXPERIMENT.md` 自己运行；开发和测试只使用本地假页面。

## 构建和启动

在仓库根目录构建：

```sh
docker compose build
```

作者需要运行服务时，自己启动容器：

```sh
docker compose up
```

默认只把网页端口绑定到宿主机的 `127.0.0.1:8080`，浏览器打开 `http://127.0.0.1:8080`。首次初始化令牌只在容器日志里显示一次，30 分钟有效，建立管理员后失效；过期而尚未初始化时重启领取新令牌。正式会话使用 HttpOnly、SameSite=Lax；HTTPS 下加 Secure 和 `__Host-` 前缀，30 天不用过期，最长 90 天重新登录（第 40 条）。调试端口和 VNC 端口都没有映射到宿主机。

实验工具需要明确选择 `experiment`，不与正式服务同时运行同一份实验数据：

```sh
docker compose run --rm -p 127.0.0.1:8080:8080 kakatta experiment --data /data --listen :8080 --account 1 --keyword 相机 --details 3
```

实验工具的初始化令牌同样只显示一次且 30 分钟有效；实验会话使用 HttpOnly、SameSite=Strict，闲置 30 分钟过期（第 65 条）。打开远程画面后，扫码、短信和验证码由作者手动完成；工具不破解验证，也不做闲鱼写操作。

实验运行间隔不是循环：默认交互模式等待用户操作，点击后只跑一次搜索及两三个详情。工具参数可以这样查看：

```sh
docker compose run --rm kakatta experiment --help
```

`--webgl` 只接受 `off` 和 `llvmpipe`：默认 `off` 不追加渲染参数，`llvmpipe` 选择 Mesa 软件渲染。这只决定浏览器实际使用的渲染后端，不改页面读到的厂商、设备或能力。按 `EXPERIMENT.md` 对比两档后，再由作者的真实实验结果决定正式设置（第 66 条）。

修改过绑定地址、通过其他机器访问时，应配置反向代理和 HTTPS；不要把远程画面无认证地暴露到公网。完整服务的部署说明在后续交付。

## 仅用假页面验证

本地假页面和程序跑在同一个容器里，因此可以断开容器外网：

```sh
docker run --rm --network none --security-opt seccomp=unconfined kakatta:local experiment --fixture --once --data /data --keyword 相机 --details 3 --account 1
```

`--once` 只允许和 `--fixture` 一起使用，跑完后退出；不能用它循环访问真实网站。需要看假页面的远程画面时，作者手动运行：

```sh
docker compose run --rm -p 127.0.0.1:8080:8080 kakatta experiment --fixture --data /data --listen :8080 --keyword 相机 --details 3 --account 1
```

停止实验用 `Ctrl+C`。Go 程序先关闭浏览器，再回收虚拟屏幕和远程画面；容器里的 `tini` 负责回收孤儿进程。没有实验在跑时不常驻 Chrome。

## 断网运行全部测试

先在宿主安装项目要求的 Go 版本、准备好模块缓存，并构建上面的 `kakatta:local` 镜像。在仓库根目录运行：

```sh
./deploy/test-container.sh
```

脚本按本地镜像的架构，把 Linux 目标下 `go list ./...` 找到的全部包编译到 `build/m1/container.随机目录`，再用正式入口启动一个临时容器。容器使用 `--network none`、临时 `/data` 和只读源码、测试文件，不需要 Go、Node 或 Bun。宿主编译也禁止下载模块、工具链和镜像；缓存不全会直接失败，先补齐构建依赖再运行。

真实 Chrome、Xvfb、x11vnc、远程输入及实验流程测试的门禁会全部打开，WebGL 的默认关闭与 Mesa llvmpipe 模式也参与验证。正式入口的主流程、风控、超时、重启重试和多任务整链也默认启用，该包的测试期限为十分钟，其他包仍为五分钟。需要共享屏幕的包临时使用 `:101`，实验工具和远程输入测试自己创建独立屏幕。脚本按包目录运行所有测试；没有测试文件的包也会编译并列在清单里。人工验收用的预览服务不在自动回归里启动。结束时检查 Chrome、crashpad、Xvfb、x11vnc 是否残留，测试失败或清理不完整都会返回非零退出码，临时容器随即删除。编译文件、包清单和完整日志保留在上述 `build/m1` 目录，便于复查。

默认与 compose 一样使用 `seccomp=unconfined`。验证仓库附带的严格规则用：

```sh
./deploy/test-container.sh --seccomp ./deploy/chrome-seccomp.json
```

用 `--image 本地镜像名` 可选择另一份已存在的测试底座，不改变 `kakatta:local` 标签，也不会拉取镜像；测试二进制始终从当前源码重新编译。

远程输入默认验证正式 `browser.Start` 固定环境，不需要额外变量。仅做本地输入对照时，在测试进程环境中设置 `KAKATTA_RFB_CONTRACT_MODE=baseline` 或 `xim`，可让测试临时取消或设置这两个输入法变量；`KAKATTA_RFB_CONTRACT_DIR` 可保留按键和 Navigator 原始 JSON。统一脚本不从宿主传入这些变量；它们只供测试使用，不是服务设置。基线漏字时保留失败，不通过补发输入取得成功结果。

这些命令只运行本地假页面，不访问闲鱼或调用付费服务，也不启动常驻开发服务。

### 限制内存验证主流程

```sh
./deploy/test-core-memory.sh kakatta:local 1g
```

同一断网容器在严格 seccomp、无交换空间和指定内存限制下执行五种主流程，默认 `1g`，也支持 `768m`、`896m`。每 0.5 秒左右保存 cgroup 分类原始读数、总计费峰值、实际采样间隔及 OOM 计数；结束检查进程残留，失败同样保存指标并返回非零。产物保留在 `build/core-memory.随机目录/`，第三个参数可指定先前产物目录，复用并核对同一测试二进制及镜像，避免对照时更换输入。

`memory.peak` 包含文件页缓存，不是进程 RSS；离散采样的 `anon`、`shmem`、`kernel` 也不能当作不可回收内存总量。脚本保存了原始数据和解释边界，假页面通过不能替代真实站点容量验证。

### 双账号自动长跑

```sh
./deploy/test-core-soak.sh kakatta:local
```

独立断网容器限制 1GiB、禁用交换空间，两个账号的四个任务各自动运行三十次；真实间隔为一分钟和两秒，不改数据库计划或使用假时钟加速。测试上下文五十五分钟、进程五十八分钟硬截止，退出时回收服务与容器。每 0.5 秒采集内存和进程，每轮记录数据库与日志增长，并逐项核对新品、降价、同价不重发、模型用量、访问额度及恢复标记。原始读数、推送清单、关闭后的 SQLite 和退出状态保存在 `build/core-soak.随机目录/`。

### 隔离故障注入

```sh
./deploy/test-core-fault.sh kakatta:local
```

用真实 Chrome 和本地假服务验证进程被杀、响应 JSON 截断、接口挂起、磁盘满及 SQLite 写锁。磁盘只填满该容器独立的 64MiB `/fault-db` tmpfs，并核对 ENOSPC 和真实 SQLite 错误；不填宿主磁盘。信号只发给精确匹配本次 profile、祖先链和启动时间的唯一 Chrome 主进程。存储类故障清障后由新测试子进程从同一库恢复，不绕过保护占位。

容器保持断网和严格 seccomp，整套测试二十分钟硬截止；第二个参数可传测试名正则单独排查。退出时检查浏览器及画面进程并按唯一容器 ID 删除，原始失败也保存在 `build/core-fault.随机目录/`，不能用重试后的成功覆盖首次失败。

## 数据和权限

只挂载一个数据目录到 `/data`，不要挂到程序目录。时区默认为 `Asia/Shanghai`；首次生成实验设置时采用 `TZ`，之后以 JSON 里的 `timezone` 为准。Chrome 的界面语言固定为中文，不随网页语言变化。

首次启动会创建 `/data/experiment-settings.json`，默认挂载下对应宿主的 `./data/experiment-settings.json`，也可用 `--config` 指定文件。全部频率、额度、等待区间、账号间隔、验证保留、冷却和不运行时段都在这里设置，默认值按 `CRAWL-POLICY.md`；提高频率、缩短间隔会增加账号被限制的风险。先停止容器，再编辑 JSON，重启后生效。启动时会校验字段、数值范围和时区，格式或数值不合法就停止。

真实实验账号的访问和保护记录在 `/data/experiment/账号编号`，浏览器登录配置在 `/data/browser/账号编号`；假页面分别使用 `/data/experiment-fixture`、`/data/browser-fixture`。重启、改时区和把系统时间往回调不会恢复额度。不要删除账号记录或浏览器配置来清额度；已有记录缺失、损坏或写盘失败时，工具会停止，不能靠清空记录继续运行。

`PUID`、`PGID` 默认为 `1000`，改成宿主机拥有数据目录的用户和组编号。例如：

```yaml
environment:
  PUID: "1000"
  PGID: "1000"
  TZ: "Asia/Shanghai"
volumes:
  - ./data:/data
```

入口脚本只在启动时用 root 调整运行用户编号和数据挂载内的属主，不跟随符号链接、不进入其他挂载；随后立即降权，服务和 Chrome 都以普通用户运行。运行用户有可写的 `HOME` 和 `XDG_CONFIG_HOME`，避免崩溃报告进程因为目录不可写而反复启动。启动时会实际创建再删除一个小文件，目录不可写就停止并提示，不带着错误权限继续。

容器也支持 Docker 的 `--user 用户编号:组编号`。此时入口不能代改属主，挂载目录必须事先可写；服务仍不会提升权限。用户和组编号必须是正整数，不允许把 `PUID` 或 `PGID` 设为 `0`。

容器至少分配 1GB 内存。2GB 内存的 NAS 上默认只开一个页面，不能据此保证所有 NAS 都有足够余量。Chrome 缓存和崩溃目录位于数据目录，由浏览器模块按规格控制。

## Chrome 沙盒

保留 Chrome 沙盒，不加 `--no-sandbox`，也不在失败时自动退回无沙盒模式。默认 compose 使用已定的 `seccomp=unconfined`；需要更严格的隔离时，将这一项换成仓库提供的规则文件：

```yaml
security_opt:
  - seccomp=./deploy/chrome-seccomp.json
```

这份规则以 [moby/profiles 的默认配置](https://github.com/moby/profiles/blob/4b8214fc4971e06172938a1f1554ccff6647ee80/seccomp/default.json) 为基础，保留 `SCMP_ACT_ERRNO` 默认拒绝和全部原规则，只增加 `clone`、`setns`、`unshare` 许可，供 Chrome 创建、进入命名空间。`clone3` 保留上游的 `ENOSYS` 回退。来源提交是 `4b8214fc4971e06172938a1f1554ccff6647ee80`，原文件 SHA-256 是 `6416b47770785a41ac59073cdc77d9fe98517df2799dc83ef207e622de3053f6`，许可证放在 `deploy/LICENSE.moby-profiles`。Docker 官方说明默认规则和命名空间限制见 [seccomp 文档](https://docs.docker.com/engine/security/seccomp/)。规则文件由 Docker 客户端读取，不用挂进容器。

旧版 Docker 或系统的 libseccomp 可能不认识新规则里的系统调用，此时继续使用默认 compose 设置，或升级 Docker；不能为兼容性改成无沙盒。部分群晖 DSM 内核不支持用户命名空间，可以由用户给容器增加 `cap_add: [SYS_ADMIN]`，让 Chrome 使用 SUID 沙盒。这会扩大容器能力，需要由用户根据宿主机环境决定，默认不开。

镜像不会额外安装 noVNC 软件包、Node 或 Bun。远程画面的静态文件在构建时嵌入 Go 程序；运行时只有 Go、Chrome、Xvfb、按需启动的 x11vnc 及必要系统库。Chrome 用 Google 官方稳定版，amd64 和 arm64 使用同一套构建步骤，随镜像版本更新。只保留简体中文和英文语言包，中文字体使用 `fonts-wqy-microhei`。
