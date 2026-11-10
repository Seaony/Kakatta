<p align="center"><img src="assets/logo/kakatta.png" width="128" alt="Kakatta"></p>

<h1 align="center">Kakatta</h1>

<p align="center">かかった！—— 上钩了！</p>

帮你盯着闲鱼上想要的东西：按任务定时搜索，新商品先过规则和 AI 筛一遍，值得看的推送到你手上。

- **自己部署**：服务器、NAS 或自己的电脑上用 Docker 运行，浏览器访问网页端，24 小时不用开着电脑。
- **账号安全优先**：只读浏览、频率保守、遇到验证就停下交给你，不破解验证码、不改浏览器指纹。

> 项目正在按定稿文档实现，尚无可用版本。功能规格在 [`docs/`](docs/)，进度和验收记录见 [`实现记录`](docs/IMPLEMENTATION.md)。

## 连接宿主机上的 Ollama

服务商地址预填 `http://host.docker.internal:11434/v1`，本地 Ollama 无须 Key。宿主 Ollama 默认只监听 `127.0.0.1`，需设置 `OLLAMA_HOST=0.0.0.0`，并允许容器访问。Ollama 在同一 Compose 网络时，可以把地址中的主机名改成实际服务名。

仓库的 Compose 配置包含以下宿主地址映射，适用于 Linux Docker 和 Docker Desktop：

```yaml
services:
  kakatta:
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

---

Kakatta (*"got a bite!"*) watches Xianyu (闲鱼) for the things you want. It searches on a schedule, screens new listings with rules and AI, and notifies you about the ones worth a look. Self-host it with Docker on a server, NAS or your own computer, and use it from your browser. Implementation is in progress; no usable release is available yet.

For Ollama running on the Docker host, use `http://host.docker.internal:11434/v1` without a key. Set the host's `OLLAMA_HOST=0.0.0.0` and allow access from the container. The Compose file includes the `host-gateway` mapping shown above for Linux Docker and Docker Desktop. For Ollama on the same Compose network, use its actual service name instead.

## License

[MIT](LICENSE)
