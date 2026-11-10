# 参考项目

除上游 ai-goofish-monitor（见 `BACKGROUND.md`）和 Mekke 以外，值得参考的开源项目。每个都写明参考什么、不参考什么。

## Dalibre（djyde/dalibre）

轻量的 calibre 替代品，2026-11-10 查看（281 star，当天还有提交，Go 代码约 4.1 万行）。作者推荐参考。

**是什么**：Go 写的桌面应用，用 MyGo 的原生界面（README：「no webview, no cgo, one binary」），用 purego 调系统接口，SQLite 用纯 Go 的 `modernc.org/sqlite`。插件用 JavaScript 写，跑在纯 Go 的 JS 运行时 MoeForge/moejs 里。内置 MCP 服务、OPDS 书目服务。官网用 Astro + Tailwind 部署在 Cloudflare Workers。设计先在 Figma 里做，AGENTS.md 规定「the design is the spec」。

**Kakatta 直接照着做的**：

- **SQLite 用 `modernc.org/sqlite`**：已经有真实的 MyGo 应用在用，纯 Go，不需要 cgo。
- **桌面端的发布流程**（`.github/workflows/release.yml`）：
  - 推送 `v*` 标签时，先检查标签和 `mygo.json` 里的版本号是否一致，CHANGELOG 有没有这个版本的段落，再建一个草稿版本；
  - 三个平台分别用 `go tool mygo build -platform ... -upload` 构建并上传，Windows 用 NSIS 生成安装包；
  - macOS 用 Developer ID 签名，再用 App Store Connect API Key 公证；
  - 更新包用 `go tool mygo keygen` 生成的密钥签名，已安装的应用只接受这把密钥签过的更新；
  - 发布草稿后，已安装的应用自动更新。
  - Kakatta 在同一套流程里再加上多架构 Docker 镜像的构建。
- **每个版本都要写 CHANGELOG**：发布流程会检查，应用内的更新窗口也直接显示它。
- **多语言**：每种语言一个 JSON 文件（`cmd/dalibre/locales/*.json`），由 `mygo.json` 的 `i18n` 指定目录和源语言。
- **开发用的样例数据生成器**（`cmd/seed`）：Kakatta 开发时不能访问闲鱼，同样需要一个命令生成假的任务、商品和结果。
- **界面测试不需要开窗口，还能保存截图**（`DALIBRE_SHOTS=<目录> go test`），适合做界面自检。
- **系统接口用 purego 调用**，不用 cgo。

**还要讨论的**：桌面端界面用原生还是网页、MCP 服务、设计流程，见 `OPEN-QUESTIONS.md`；插件见 `DECISIONS.md` 第 25 条。

**不同之处**：Dalibre 只有桌面端，不需要网页界面；Kakatta 必须有网页端（Docker 部署后用浏览器访问），这是选界面方案时最大的区别。
