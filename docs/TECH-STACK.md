# 技术栈分析

2026-11-10 整理。结论：服务端用 Go，浏览器自动化用 chromedp，前端用 React + Vite + TypeScript，桌面端用 MyGo。

## 选型要满足什么

1. **抓闲鱼要稳**：浏览器自动化库要成熟、一直有人维护；能读到页面自己请求的数据、点筛选、截屏传画面、转发用户的鼠标操作。
2. **能跑在各种环境里，并且尽可能轻量**：NAS（常见 ARM64、内存 2GB 左右）、云服务器、用户自己电脑上的 Docker。要省内存、镜像小、支持 amd64 和 arm64；不另外带运行时（`DECISIONS.md` 第 15、16 条）。
3. **桌面端能把服务端直接带进去**：本机模式不另外下载服务端（`VISION.md`）。
4. **长期常驻**：7×24 小时运行，不能有内存一直涨、需要定期重启的问题。
5. **开源项目能吸引贡献者**：语言要常见，工程要简单。

## 候选对比

| | Go | TypeScript（Node） | Rust | Python |
|---|---|---|---|---|
| 浏览器自动化 | chromedp：1.3 万 star，2026 年至今 100+ 次提交，2026-11-05 发布 v0.23.0。直接走 CDP，纯 Go，不依赖 Node | Playwright 官方：9.9 万 star，最好用（自动等待、录制、调试工具） | chromiumoxide：1.4 千 star，最后提交 2026-04-03，不够成熟 | Playwright 官方 Python 版（参考项目在用） |
| 桌面端 | MyGo（作者倾向）、Wails；服务端代码直接编译进去 | Electron：要带 Node 运行时，安装包和内存都大；用不了 MyGo | Tauri：要带服务端的话，还得另外打包运行时 | 打包桌面应用很难 |
| 服务器资源 | 一个静态程序，常驻内存几十 MB；交叉编译 arm64 只要一条命令 | Node 运行时常驻一两百 MB | 最省 | 运行时和依赖最重 |
| 长期常驻 | 好，并发模型适合调度器加多个抓取任务 | 可以 | 好 | 一般 |
| 贡献者 | 自托管圈很常见（AList、Memos、frp、1Panel、Gitea 都是 Go） | 最多 | 门槛高 | 多 |

## 为什么不是 TypeScript

TypeScript 唯一明显强过 Go 的地方是 Playwright 更好用。但是：

- Playwright 的 Go、Python、Java 版本底层都要带一个 Node 进程（playwright-go 也一样），换语言绕不开 Node。
- 我们的抓取流程每一步都要按频率规则主动等待，不靠 Playwright 的自动等待来提速。我们需要的能力（打开页面、等元素、点击、读网络响应、截屏流、转发鼠标操作）chromedp 在 CDP 层面都有。
- 选 TypeScript 的代价落在第 2、3 条上：NAS 上多占一两百 MB 内存，桌面端只能用 Electron。

## 为什么不是 go-rod

go-rod 的接口比 chromedp 好写，但维护基本停了：最后一个版本是 2024-07 的 v0.116.2，2026 年只有 4 次提交，还有 217 个未关 issue。抓取是项目的核心，不能押在停更的库上。

## 其他选择

- **SQLite**：用纯 Go 的 `modernc.org/sqlite`，不用 cgo。Dalibre 这个真实的 MyGo 应用也在用它（v1.62.0），见 `REFERENCES.md`。
- **前端**（网页端和桌面端共用，`DECISIONS.md` 第 34 条）：
  - React + Vite + TypeScript；
  - 路由用 TanStack Router，接口数据用 TanStack Query；
  - 接口调用代码由服务端的 OpenAPI 描述自动生成（openapi-typescript + openapi-fetch），Go 和 TypeScript 之间不用手动同步；
  - 样式用 Tailwind CSS，颜色、字号、间距、圆角全部做成 CSS 变量；交互组件用 Radix UI 的无样式基础组件，在仓库里包一层自己的组件，页面只组合这些组件。第一版不做设计、之后要整体 Redesign（`DECISIONS.md` 第 30 条），这样换皮时只改变量和组件，不动页面；
  - 多语言文件和服务端共用同一套 JSON；
  - 用 Bun 做包管理和构建，Bun 只在开发和构建时用到，不进运行时；构建产物用 `go:embed` 嵌进 Go 程序，Docker 镜像里不需要 Node 或 Bun；
  - 要兼容三种网页引擎：Safari 内核（macOS 的 WKWebView、Linux 的 WebKitGTK）和 Chromium 内核（Windows 的 WebView2、普通浏览器），构建目标按 Safari 16 来定。
- **Docker 镜像**：Go 程序 + Google Chrome 正式版 + Xvfb（需要用户操作时再临时启动 x11vnc 和 noVNC），同时发布 amd64 和 arm64。浏览器为什么这样选，见 `CRAWL-POLICY.md`。

## 有什么证据会推翻这个结论

- 动工前的实验（`CRAWL-POLICY.md` 第八节）发现 chromedp 在闲鱼的某个流程上明显做不到、而 Playwright 能做到。
- MyGo 在实际使用中有严重问题，并且 Wails 也不合适。这种情况下 Go 的优势会少一大块，需要重新比较。
