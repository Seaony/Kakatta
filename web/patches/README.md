# Vite 开发服务器补丁

`vite@8.3.7.patch` 修复 Bun 1.3.14 下固定端口启动时首连接挂起的问题。Vite 的临时端口探测器可能先接住浏览器连接，却没有 HTTP 处理器；随后 `server.close()` 撤掉监听，等待已有连接断开才完成。实际表现是页面一直等待、端口不再监听，直到客户端断开后 Vite 才继续启动。

上游位置是 [Vite 8.3.7 的 `packages/vite/src/node/http.ts`](https://github.com/vitejs/vite/blob/v8.3.7/packages/vite/src/node/http.ts)：`httpServerStart` 先调用 `isPortAvailable`，后者通过 `tryListen` 创建临时 `net.Server`。发布包对应 `dist/node/chunks/node.js`。

本补丁让非零固定端口的 `strictPort` 分支直接绑定真实 HTTP 服务，端口已占用时仍退出；非严格端口和随机端口路径保持原样。保留 Bun 生成的标准补丁、上下文与上游空白，目标文件的实际修改只涉及这段启动顺序。`.gitattributes` 只为本目录的 `.patch` 文件设置 `-whitespace`，普通源码检查规则保持；冷安装后用实际目标文件散列和首请求测试确认补丁应用正确。

截至 2026-11-11，Vite 官方仓库已有直接对应的 [PR #23737](https://github.com/vitejs/vite/pull/23737)，提出关闭探测器接纳的连接并补充回归测试，但仍处于未合并状态。检索范围为官方 issues 和 PR，关键词包括 `tryListen`、`isPortAvailable`、`Bun`、`first request`、`hang`、`port`；本次未找到包含该修复的已发布版本。

将来升级时，先核对上游发布版的修复和源码，在未应用本补丁的独立依赖副本上验证首批连接收到页面、端口占用仍失败。确认通过后，再删除 `web/package.json` 中对应的 `patchedDependencies`、此补丁文件并更新 `web/bun.lock`。如已没有其他依赖补丁，再移除 Dockerfile 对应的复制步骤。
