<p align="center"><img src="assets/logo/kakatta.png" width="128" alt="Kakatta"></p>

<h1 align="center">Kakatta</h1>

<p align="center">かかった！上钩了！</p>

[English](README.md)

帮你盯着闲鱼上想要的东西。按任务定时搜索，新商品先过规则，也可以用 AI 分析，值得看的再推送给你。在自己的服务器、NAS 或电脑上用一个 Docker 容器运行，从浏览器打开网页端。

项目仍在开发。下面说明如何从源码构建，以及正式版本发布后如何部署镜像。参见 [实现与验收记录](docs/IMPLEMENTATION.md) 和 [项目规格](docs/)。

## 用 Docker Compose 运行

使用支持 Linux 容器的 `amd64` 或 `arm64` Docker 环境，Docker Desktop 也可以。给容器至少留 1 GB 内存。内存较紧的机器建议维持默认值，一次打开一个浏览器页面。

从源码运行，在仓库目录执行 `docker compose up -d`，首次会自动构建。打开 `http://127.0.0.1:8080`，从 `data/初始化令牌.txt` 取令牌并设置管理员。停止用 `docker compose down`，数据保留；更新源码后用 `docker compose up -d --build`。需要记录准确提交号时，在启动命令前加 `KAKATTA_BUILD_COMMIT=$(git rev-parse HEAD)`。

使用正式镜像时，把下面的内容保存为独立目录里的 `compose.yaml`。在同目录的 `.env` 中填 `KAKATTA_IMAGE=ghcr.io/seaony/kakatta:VERSION`，把 `VERSION` 换成 [GitHub Releases](https://github.com/Seaony/Kakatta/releases) 公布的精确版本，去掉开头的 `v`。也可以使用发布时公布的 Docker Hub 仓库。工作流给两处镜像加上精确版本、主版本和 `latest` 标签。建议固定精确版本，升级时再明确更改。

```yaml
services:
  kakatta:
    image: "${KAKATTA_IMAGE:?Set KAKATTA_IMAGE to a published Kakatta image and tag}"
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      PUID:
      PGID:
      TZ: "${TZ:-Asia/Shanghai}"
    volumes:
      - ./data:/data
    extra_hosts:
      - "host.docker.internal:host-gateway"
    security_opt:
      - seccomp=unconfined
    stop_grace_period: 30s
    command: ["--data", "/data", "--listen", ":8080"]
```

```sh
docker compose up -d
docker compose logs kakatta
```

首次启动后，在 Docker 宿主机打开 `http://127.0.0.1:8080`。宿主机在远端时，用 SSH 隧道或 HTTPS 反向代理访问。填入 `data/初始化令牌.txt` 或 `docker compose logs kakatta` 中的一次性初始化令牌，设置管理员用户名和密码。文件权限为 0600，初始化成功后删除；令牌 30 分钟后过期，未初始化时重启 `kakatta` 可取新令牌。请保管好数据目录和日志。首次引导接下来会带你扫码登录闲鱼、选配 AI 和推送渠道，以及创建第一个任务。

不设置 `PUID` 和 `PGID` 时，入口自动采用挂载目录的用户和组编号，编号为 0 时分别回退到 1000；需要指定时再设置这两个变量。入口脚本会修正 `/data` 下的属主，随后以非 root 用户运行 Kakatta 和 Chrome。`TZ` 设置容器时区和应用的初始默认时区，初始化后可在设置里修改应用时区。 Mac 上可直接启动；需要显式指定属主时使用 `PUID=$(id -u) PGID=$(id -g) docker compose up -d`。扫码前按 [容器文档](docs/CONTAINER.md) 做本地自检；出错后的文件位置也在该文档。

`seccomp=unconfined` 放宽 Docker 的系统调用限制，让 Chrome 能保留自己的沙盒。想收窄容器权限时，把 [chrome-seccomp.json](deploy/chrome-seccomp.json) 保存到 Compose 文件旁，把该项改成 `seccomp=./chrome-seccomp.json`。请保留 Chrome 沙盒，只映射应用端口。

国内拉取 GHCR 镜像较慢时，可以改用 Docker Hub 上同一精确版本，在 Docker 守护进程里配置可信的镜像加速。`registry-mirrors` 设置适用于 Docker Hub 拉取。[Docker 镜像加速配置](https://docs.docker.com/docker-hub/image-library/mirror/#configure-the-docker-daemon)

### 连接宿主机上的 Ollama

宿主机上的 Ollama 地址填 `http://host.docker.internal:11434/v1`，本地 Ollama 不需要 API Key。若它只监听宿主机回环地址，容器又无法连接，就通过 `OLLAMA_HOST` 改成容器可访问的监听地址，并把访问范围限制在可信客户端。Ollama 在同一 Compose 网络中时，地址使用它的服务名。[Ollama 网络配置](https://docs.ollama.com/faq#how-can-i-expose-ollama-on-my-network)

## HTTPS 和远程访问

Kakatta 提供 HTTP，由反向代理处理 HTTPS。给 Kakatta 分配一个域名，从根路径访问，例如 `https://kakatta.example.com/`。代理和 Docker 在同一台宿主机时，保留本机回环端口绑定。代理整个站点，包括实时事件用的 `/api/v1/events` 和远程画面用的 `/remote/sessions/`。保留原请求路径和主机名，允许 WebSocket 升级，关闭 SSE 响应的代理缓存和缓冲。

下面的示例在 Docker 宿主机上直接运行代理。如果代理在另一个容器里，把两个容器接到同一个私有 Docker 网络，上游地址填 `kakatta:8080`。

在 **设置 → 安全 → 可信反向代理** 只添加自己控制的代理 IP 或小范围 CIDR 网段。也可以通过 `KAKATTA_TRUSTED_PROXIES` 提供逗号分隔的列表，设置后以环境变量为准。填写 Kakatta 实际看到的直连来源地址，Docker 端口映射下可能是网桥网关。仅当不受信任的程序无法经该地址访问后端时才信任它。代理应按实际外部请求设置 `X-Forwarded-Host` 和 `X-Forwarded-Proto`，并设置或正确追加 `X-Forwarded-For`。Kakatta 只采用可信来源的这些请求头来识别 HTTPS 和客户端 IP。

**Caddy。** 将域名指向运行 Caddy 的宿主机。Caddy 会处理 WebSocket 升级，并逐步转发 `text/event-stream` 响应。[Caddy 反向代理文档](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)

```caddyfile
kakatta.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

**Nginx。** 把下面的配置放进 `http` 上下文，填写自己的证书路径。恢复备份时，在 `server` 块里将 `client_max_body_size` 设为可容纳 ZIP 和表单开销的大小。[Nginx 的 WebSocket 配置](https://nginx.org/en/docs/http/websocket.html) · [Nginx 代理指令](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)

```nginx
map $http_upgrade $kakatta_connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 443 ssl;
    server_name kakatta.example.com;
    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $http_host;
        proxy_set_header X-Forwarded-Host $http_host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $kakatta_connection_upgrade;
        proxy_buffering off;
        proxy_request_buffering off;
        proxy_cache off;
        proxy_read_timeout 600s;
    }
}
```

**Cloudflare Tunnel。** 在 Docker 宿主机运行 `cloudflared`，创建命名隧道，将公开主机名指向 `http://127.0.0.1:8080`。保留完整路径，允许 WebSocket，给应用关闭缓存，从浏览器打开 HTTPS 域名。临时的 Quick Tunnel 不支持 SSE。备份超过隧道的上传上限时，改走本机连接恢复。[Cloudflare Tunnel 配置](https://developers.cloudflare.com/tunnel/get-started/) · [Quick Tunnel 限制](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)

**Tailscale。** 在 Docker 宿主机运行 `tailscale serve --bg http://127.0.0.1:8080`，用命令显示的 HTTPS 地址访问。服务仅在自己的 Tailscale 网络内共享。[Tailscale Serve 文档](https://tailscale.com/docs/reference/tailscale-cli/serve)

配好代理后，用 HTTPS 登录，检查浏览器收到的会话 Cookie 带有 `Secure`，页面也能收到实时更新。远程画面的 WebSocket 也要能通过同一个域名连接。Chrome 调试端口和容器内的 VNC 端口不得对外开放。

## 数据目录和密钥

`./data` 是持久化挂载目录，其中有 `kakatta.db`、默认的 `secret.key`、`browser/` 里的浏览器配置、图片缓存和 `backups/` 里的数据库快照。保护好这个目录，每个实例使用独立目录。用原目录重建容器即可保留状态。

创建新数据库时，服务端会在 `secret.key` 生成 32 字节的主密钥。请和对应的数据库一起保存。整个数据目录一旦泄露，别人可能同时拿到密钥、加密凭据以及浏览器配置里的闲鱼登录状态。

要把密钥分开保管，可以只读挂载密钥文件，把 `KAKATTA_SECRET_KEY_FILE` 设成容器里的文件路径。文件必须是恰好 32 字节的原始数据，且服务运行用户可读。已有实例改用外部文件时，应使用原密钥的内容。也可以用 `KAKATTA_SECRET_KEY` 提供这 32 字节的标准 Base64 编码。两个变量只设一个。有 Docker 检查权限的人能看到容器环境变量，建议优先用文件。

## 备份、恢复和升级

在 **设置 → 备份和恢复** 设置备份密码，生成加密 ZIP。把文件和密码妥善存到 Docker 宿主机以外。备份包含业务数据、设置、管理员用户名、密码哈希、两步验证设置和加密主密钥。闲鱼浏览器会话、Kakatta 网页会话和接口令牌、访问额度记录、图片缓存不在备份内。

1. 迁机时，启动目标实例，先初始化管理员，以便打开备份页面。目标版本要支持备份的数据库结构版本。
2. 在目标实例上传 ZIP，输入备份密码。校验通过后核对备份信息，再确认全量覆盖。目标的现有业务数据会被替换。配置了外部密钥时，恢复的凭据会用目标实例的密钥重新加密。
3. 用备份里的管理员密码和原验证器重新登录。重建接口令牌，核对任务和设置，再逐个重新扫码登录闲鱼。如果恢复时连接断开，先核对目标实例的状态再重试。

升级镜像前，查看发布说明并导出备份。停掉 Kakatta 后另存一份完整数据目录，记下旧镜像标签。使用外部密钥的，还要保留对应密钥。把 `KAKATTA_IMAGE` 改为目标版本，按顺序运行下面的命令。

```sh
docker compose pull kakatta
docker compose up -d kakatta
```

启动时会自动迁移数据库，需要迁移时会在 `data/backups/` 生成升级前的数据库快照。要回退，先停掉 Kakatta，把升级后的数据目录移到别处，再用旧镜像标签、升级前的数据副本和配套密钥启动。备份之后发生的更改仍留在移开的目录里。旧版本无法打开更新的数据库结构，旧 Chrome 也可能需要新建浏览器配置并重新扫码。建议手动升级，先看清版本变更。

忘记 Kakatta 密码时，运行 `docker compose exec --user kakatta kakatta kakatta reset-password --data /data`，按提示输入新密码。这会让所有 Kakatta 会话和接口令牌失效。如果也丢了验证器，在同一命令末尾加 `--disable-totp`。只关闭两步验证时，运行 `docker compose exec --user kakatta kakatta kakatta disable-totp --data /data`。

## 接口和 MCP

在 **设置 → 关于** 查看接口文档，或下载 `/api/v1/openapi.json`。脚本和 MCP 客户端需要在 **设置 → 安全** 新建接口令牌，并发送 `Authorization: Bearer <令牌>`。把 MCP 客户端的 Streamable HTTP 地址设为 Kakatta 的地址加上 `/mcp`。工具可查结果，也可新建、暂停、恢复和运行任务，只把令牌交给信任的客户端；不用时可以在安全页吊销。

## 账号保护

Kakatta 只读浏览闲鱼，不下单、不发消息、不改浏览器指纹。遇到验证会停下，由你在登录后的远程画面里处理。刚开始使用时建议维持默认访问额度。它能限制访问频率，闲鱼仍可能限制账号。购买前请自己核对商品证据，模型分数只供参考。[抓取规则](docs/CRAWL-POLICY.md)

## 本地开发

使用 [go.mod](go.mod) 指定的 Go 和 [package.json](package.json) 指定的 Bun。本机安装 Google Chrome 后，在仓库根目录执行：

```sh
bun run dev
```

打开 `http://127.0.0.1:5173`。首次自动生成样例，使用本地假闲鱼、假模型和假推送，免登录；数据固定保存在 `build/_dev/data`。在任务页手动运行「示例 · MacBook监控」可走完整流程。网页面板可看后端日志和最近错误，并切换登录失效、风控、字段缺失或接口报错。网页改动即时更新，Go 改动由 Air 编译后自动切换；运行中的任务先正常结束，编译失败继续使用旧服务。按一次 Ctrl+C 安全退出并回收子进程。

作者验证真实站点时运行 `bun run dev:real`：Go、Chrome 和 Air 在开发容器，Vite 在宿主机；沿用 `./data` 的正式登录和保护记录，首次仍需初始化令牌。调度每次启动默认暂停，页面手动运行或明确打开调度后才开始任务。修改 Go 同样等待当前运行结束再切换，不清登录和额度。首次构建需要下载开发工具和依赖；开发镜像比正式镜像大，编译也需要额外内存。详见 [开发容器](docs/CONTAINER.md#开发热更新)。

完整检查：

```sh
bun install --cwd web --frozen-lockfile
bun run --cwd web check
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

用 `go run ./cmd/seed --data <empty-directory>` 把虚构数据写入独立的空目录。开发用本地假闲鱼和模拟服务，[容器回归](deploy/test-container.sh) 在断网环境运行。真实账号登录、验证及线上页面行为仍待维护者验收。[实验计划](docs/EXPERIMENT.md)

## 许可证

[MIT](LICENSE)。[参考项目](docs/REFERENCES.md) 与 [随包提供的 noVNC](internal/remote/README.md) 各自遵守其原许可证。
