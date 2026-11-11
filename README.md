<p align="center"><img src="assets/logo/kakatta.png" width="128" alt="Kakatta"></p>

<h1 align="center">Kakatta</h1>

<p align="center">かかった! Got a bite!</p>

[简体中文](README.zh-CN.md)

Kakatta watches Xianyu (闲鱼) for the things you want. It searches on a schedule, screens new listings with rules and optional AI analysis, and sends notifications for the ones worth a look. Run it in one Docker container on your server, NAS, or computer and use it from a browser.

Kakatta is in development. The instructions below describe how to build from source and how to deploy a published image once a release is available. See the [implementation and test record](docs/IMPLEMENTATION.md) and the [project specifications](docs/).

## Run with Docker Compose

Use a Linux `amd64` or `arm64` Docker host, including Docker Desktop running Linux containers. Allow at least 1 GB of memory for the container and leave browser concurrency at its default of one page on small hosts.

From a source checkout, run `docker compose up -d`; the first start builds the image. Open `http://127.0.0.1:8080`, read the token from `data/初始化令牌.txt`, and create an administrator. Stop with `docker compose down` to keep your data; after updating the source, run `docker compose up -d --build`. Prefix the start command with `KAKATTA_BUILD_COMMIT=$(git rev-parse HEAD)` to record the exact commit.

For a published image, save the following as `compose.yaml` in its own directory. In a `.env` file beside it, set `KAKATTA_IMAGE=ghcr.io/seaony/kakatta:VERSION` and replace `VERSION` with the exact version from [GitHub Releases](https://github.com/Seaony/Kakatta/releases), without the leading `v`. You can also use the Docker Hub repository announced for the release. The workflow targets both registries with exact version, major version, and `latest` tags. Pin the exact version for predictable upgrades.

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

On first start, open `http://127.0.0.1:8080` on the Docker host, or use an SSH tunnel or HTTPS proxy for a remote host. Read the one-time setup token from `data/初始化令牌.txt` or `docker compose logs kakatta` and create your administrator username and password. The file has mode 0600 and is removed after setup. If the token expires after 30 minutes before setup, restart `kakatta` for a new token. Keep the data directory and logs private. The setup guide then takes you through scanning a Xianyu login QR code, configuring optional AI and notification channels, and creating a first task.

When `PUID` and `PGID` are unset, the entrypoint uses the mounted directory's owner and group, falling back to 1000 for either ID that is 0. Set the variables only when you want explicit IDs. The entrypoint adjusts ownership under `/data` before running Kakatta and Chrome as a non-root user. `TZ` sets the container time zone and the initial application default. Change the application's time zone in Settings after setup. On macOS, start directly, or use `PUID=$(id -u) PGID=$(id -g) docker compose up -d` to set explicit ownership. Before scanning, follow the local checks and troubleshooting steps in [CONTAINER.md](docs/CONTAINER.md).

`seccomp=unconfined` relaxes Docker's syscall filter so Chrome can start with its own sandbox intact. For a narrower filter, save [chrome-seccomp.json](deploy/chrome-seccomp.json) beside your Compose file and change the entry to `seccomp=./chrome-seccomp.json`. Keep Chrome's sandbox enabled and publish only the application port.

If pulling from GHCR is slow, use the same exact version from Docker Hub with a trusted mirror configured in your Docker daemon. Docker's `registry-mirrors` setting applies to Docker Hub pulls. [Docker mirror configuration](https://docs.docker.com/docker-hub/image-library/mirror/#configure-the-docker-daemon)

### Ollama on the Docker host

For Ollama on the Docker host, use `http://host.docker.internal:11434/v1`. Local Ollama requires no API key. If it listens only on the host loopback interface and the container cannot connect, use `OLLAMA_HOST` to bind it to an address the container can reach. Restrict access to trusted clients. For Ollama on the same Compose network, use its service name. [Ollama networking](https://docs.ollama.com/faq#how-can-i-expose-ollama-on-my-network)

## HTTPS and remote access

Kakatta listens on HTTP. Terminate HTTPS at your reverse proxy and give Kakatta a hostname at the root path, such as `https://kakatta.example.com/`. Keep the host port on loopback for a proxy on the same host. Forward the whole site, including `/api/v1/events` for server-sent events (SSE) and `/remote/sessions/` for the remote browser. Preserve the original request path and host, allow WebSocket upgrades, and keep SSE responses out of proxy caches and buffers.

The examples below run the proxy directly on the Docker host. For a proxy in another container, connect the two containers to a private Docker network and use `kakatta:8080` as the upstream.

In **Settings → Security → Trusted reverse proxies**, add only the IP or narrow CIDR range of proxies you control. `KAKATTA_TRUSTED_PROXIES` takes a comma-separated list and overrides that setting. Use the immediate peer address as seen by Kakatta. Docker port publishing may make that address the bridge gateway. Trust it only if untrusted workloads cannot reach the backend through that address. The proxy must set `X-Forwarded-Host` and `X-Forwarded-Proto` from the actual external request and set or safely extend `X-Forwarded-For`. Kakatta uses these headers from trusted peers for HTTPS cookies and client IP detection.

**Caddy.** Point your hostname at the host running Caddy. Caddy handles WebSocket upgrades and streams `text/event-stream` responses. [Caddy reverse_proxy documentation](https://caddyserver.com/docs/caddyfile/directives/reverse_proxy)

```caddyfile
kakatta.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

**Nginx.** Put the map and server block inside the `http` context. Set your certificate paths. For backup restores, set `client_max_body_size` in the server block to a limit large enough for your ZIP file plus form overhead. [Nginx WebSocket proxying](https://nginx.org/en/docs/http/websocket.html) · [Nginx proxy directives](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)

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

**Cloudflare Tunnel.** Run `cloudflared` on the Docker host. Create a named tunnel with a published hostname pointing to `http://127.0.0.1:8080`. Keep the whole path, allow WebSockets, bypass cache for the app, and use the HTTPS hostname in the browser. Quick Tunnels lack SSE support. If the tunnel's upload limit is smaller than your backup, restore through a local connection. [Cloudflare Tunnel setup](https://developers.cloudflare.com/tunnel/get-started/) · [Quick Tunnel limits](https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/)

**Tailscale.** On the Docker host, use `tailscale serve --bg http://127.0.0.1:8080` and open the HTTPS URL Serve prints. This shares the app within your tailnet. [Tailscale Serve](https://tailscale.com/docs/reference/tailscale-cli/serve)

After configuring a proxy, sign in over HTTPS and check that the browser receives a Secure session cookie and that live updates reach the page. The remote view also needs a working WebSocket through the same hostname. Never expose Chrome's debug port or the internal VNC port.

## Data and keys

`./data` is the persistent mount. It holds `kakatta.db`, the default `secret.key`, browser profiles under `browser/`, the image cache, and database snapshots in `backups/`. Keep the mount private and use a separate directory for each instance. Recreating the container with the same mount retains its state.

The server generates a 32-byte key in `secret.key` when creating a new database. Keep it with the matching database. If someone obtains the entire data directory, they can obtain both the key and the encrypted credentials, as well as Xianyu sessions in the browser profiles.

To keep the key separate, mount a secret file read-only and set `KAKATTA_SECRET_KEY_FILE` to its path inside the container. The file must contain exactly 32 raw bytes and be readable by the service user. When moving an existing installation to an external key file, use the original key bytes. The alternative `KAKATTA_SECRET_KEY` takes the standard Base64 encoding of those bytes. Set only one of the two variables. Docker environment variables are visible to operators with inspection access, so prefer the file.

## Back up, restore, and upgrade

In **Settings → Backup and restore**, generate an encrypted ZIP with a backup password. Save the ZIP and its password securely outside the Docker host. The ZIP contains business data, settings, the administrator's username, password hash and two-factor settings, and the encryption key. Xianyu browser sessions, Kakatta web sessions and API tokens, access quota records, and the image cache are excluded.

1. To move to a new host, start the target instance and initialize its administrator so you can open its backup page. Use a Kakatta version that supports the backup's database schema.
2. On the target, upload the ZIP and enter its backup password. Review the backup information after validation, then confirm the full replacement. Existing business data on the target will be replaced. If you configured an external key, the restored credentials are encrypted with that target key.
3. Sign in again with the administrator password and authenticator from the backup. Recreate API tokens, check the restored tasks and settings, and scan each Xianyu account's login QR code again. If the connection drops during restore, check the target's state before retrying.

To upgrade an image deployment, read the release notes, export a backup, and keep a private copy of the complete data directory made while Kakatta is stopped. Record the old image tag and retain the matching external key if you use one. Change `KAKATTA_IMAGE` to the target version and run these commands in order.

```sh
docker compose pull kakatta
docker compose up -d kakatta
```

Startup applies database migrations and writes a pre-migration database snapshot in `data/backups/` when needed. For a rollback, stop Kakatta, move the upgraded data directory aside, and restore the pre-upgrade data copy and matching key with the old image tag. Changes made after that copy remain in the directory you set aside. An older version cannot open a newer database schema, and an older Chrome may require fresh profiles and another QR login. Apply updates manually so you can review these changes first.

If you forget the Kakatta password, run `docker compose exec --user kakatta kakatta kakatta reset-password --data /data` and enter the new password at the prompt. This revokes all Kakatta sessions and API tokens. If you also lost the authenticator, add `--disable-totp` to that command. To disable two-factor authentication alone, use `docker compose exec --user kakatta kakatta kakatta disable-totp --data /data`.

## API and MCP

Open **Settings → About** to browse the API or download `/api/v1/openapi.json`. For scripts or an MCP client, create a token in **Settings → Security** and send it as `Authorization: Bearer <token>`. Set the MCP client's Streamable HTTP URL to your Kakatta address with `/mcp` appended. The tools can read results and create, pause, resume, or run tasks, so give a token only to clients you trust. Revoke it from Security when you no longer need it.

## Account protection

Kakatta browses Xianyu in read-only mode. It does not place orders, send messages, or modify browser fingerprints. It stops on a verification challenge and lets you handle it in the authenticated remote view. Keep the default access limits when starting out. They reduce the frequency of requests, but Xianyu can still restrict an account. Review model scores and listing evidence before buying. [Crawl policy](docs/CRAWL-POLICY.md)

## Development

Use the Go version in [go.mod](go.mod) and the Bun version in [package.json](package.json). With Google Chrome installed, run this from the repository root:

```sh
bun run dev
```

Open `http://127.0.0.1:5173`. The first start seeds fictional data in `build/_dev/data`, starts local Xianyu, model, and notification fixtures, and signs you in automatically. Run “示例 · MacBook监控” from Tasks to exercise the full flow. The development panel shows backend logs and recent errors, with controls for expired login, verification, missing fields, and API errors. Vite updates the page immediately; Air compiles Go changes before the manager drains active work and switches processes. Failed builds keep the old backend running. One Ctrl+C safely closes the child processes.

For maintainer testing against the live site, use `bun run dev:real`. Go, Chrome, and Air run in a development container, with Vite on the host. It preserves the formal authentication and protection records in `./data`; first-time setup still needs a token. Scheduling starts paused every time: run a task manually or explicitly enable scheduling in the panel. Go reloads wait for active work to finish and retain login state and quotas. The first build downloads development tools and dependencies. The development image and compiler require more disk space and memory than the release image. See [development containers](docs/CONTAINER.md#开发热更新).

Run the full checks with:

```sh
bun install --cwd web --frozen-lockfile
bun run --cwd web check
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

Use `go run ./cmd/seed --data <empty-directory>` to populate a separate empty directory with fictional data. Development uses local Xianyu fixtures and mock services. The [container tests](deploy/test-container.sh) run offline. Real-account sign-in, verification, and behavior on the live site still need the maintainer's acceptance checks. [Experiment plan](docs/EXPERIMENT.md)

## License

[MIT](LICENSE). See the [reference projects](docs/REFERENCES.md) and [bundled noVNC notices](internal/remote/README.md) for their respective terms.
