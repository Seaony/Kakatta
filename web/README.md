# 网页基础层

使用 React、TanStack Router/Query、OpenAPI 类型和基础组件。当前首页只说明基础层状态；业务页面在接入实际接口后实现。

开发工具固定为 Bun 1.3.14，依赖版本和 `bun.lock` 一起提交。运行时由 Go 提供静态产物，镜像不需要前端运行时。

```sh
bun install --frozen-lockfile
bun run generate:api
bun run check
```

以上命令在 `web/` 执行。开发服务器由作者使用 `bun run dev` 启动。`build` 输出 `web/dist`，该目录不提交；Go 嵌入前须先完成构建。Go 嵌入及 HTTP fallback 由集成层维护。

## 接口与文案

- 类型只从仓库的 `api/openapi.json` 生成，成功返回值直接使用 schema 中的对象。API client 使用同源 Cookie，保留 AbortSignal，将 HTTP、网络和解析错误转为 `ApiError`；错误正文不进入日志。
- 字典只有 `internal/locales/zh-CN.json` 和 `en.json`，网页直接导入。平铺键、`{name}` 单次插值及 `_one/_other` 与 Go 共用；未知键、缺参数和非法 count 会抛 `TranslationError`。新增文案交字典维护方合并。
- `AppProviders` 接收唯一 router、QueryClient、初始语言和主题。语言、主题可以通过各自 hook 修改；持久设置由后续业务接口保存。
- Query 和 mutation 默认不重试，不因窗口聚焦或联网自动增加请求；页面显式管理只读数据刷新和取消。
- 所有基础组件可见文字由调用方传入。Dialog 的 `closeLabel` 必填；Dialog/ConfirmDialog 可传 trigger，外部受控打开时也归还原焦点。确认动作不自动关闭弹层，失败内容由调用方保留。

## 兼容与验证

目标包含 Safari 16.0，使用 Tailwind 3.4，JS/CSS 构建目标及 Autoprefixer 范围显式配置。颜色、间距、圆角和字号来自 CSS variables；Mekke 的数值已整理在自己的 tokens 中，构建不读取私有仓库。

`bun run check` 依次检查生成文件、双语键/参数、严格类型（包含测试）、行为测试和生产构建。API/路由生成检查在临时目录进行，不修改工作区；生成说明由脚本确定性替换成简短中文，生成正文不能手改。

行为测试使用 Happy DOM，覆盖焦点、键盘、错误、取消、输入法和恢复入口。实际排版、触摸和 Safari 16 兼容性仍需浏览器验收；DOM 模拟器不能替代这些结果。集成后的 Dia 验收包括两语言、浅深主题、320/393/768/1280px、弹层长内容和各控件状态。

## 文件范围

| 文件 | 职责 |
| --- | --- |
| `.gitignore` | 忽略依赖、产物、路由临时目录和类型缓存 |
| `package.json` | 精确依赖、Bun 版本、浏览器范围和脚本 |
| `bun.lock` | 完整可复现依赖树 |
| `bunfig.toml` | 测试 preload |
| `index.html` | 静态入口、语言与 viewport 基线 |
| `vite.config.ts` | Safari 16 构建、文件路由插件和生成说明归一 |
| `tailwind.config.ts` | 语义工具类与 CSS variables 映射 |
| `postcss.config.cjs` | Tailwind 3 与 Autoprefixer |
| `tsconfig.json` | 浏览器与工具项目引用 |
| `tsconfig.app.json` | 严格浏览器类型，隔离工具 globals |
| `tsconfig.tools.json` | 构建脚本及全部测试的严格类型 |
| `tsr.config.json` | 路由输入/输出及固定格式 |
| `scripts/generated.ts` | 从真实规范和临时路由树生成确定性内容 |
| `scripts/generate-api.ts` | 写入生成的接口类型 |
| `scripts/generate-routes.ts` | 写入生成的路由表 |
| `scripts/check-generated.ts` | 比较实际文件与重新生成结果 |
| `scripts/check-locales.ts` | 校验双语键、参数及复数对 |
| `scripts/generated.test.ts` | 生成说明和字典校验回归 |
| `src/main.tsx` | 唯一 React 根入口与应用单例 |
| `src/app/providers.tsx` | 组合语言、主题、Query 与 Router |
| `src/app/providers.test.tsx` | 根页面、未知路径和错误恢复 |
| `src/app/router.ts` | RouterContext、浏览器/测试内存路由 |
| `src/app/query-client.ts` | 唯一 Query 默认行为 |
| `src/app/query-client.test.ts` | 无隐式重试和取消信号 |
| `src/app/theme.tsx` | 显式/系统主题与订阅回收 |
| `src/app/theme.test.tsx` | 主题切换和卸载清理 |
| `src/api/client.ts` | 类型安全调用与统一错误入口 |
| `src/api/errors.ts` | 使用生成的原因/字段错误类型，校验失败响应 |
| `src/api/client.test.ts` | 同源、401/409、异常响应、网络和取消 |
| `src/api/schema.gen.d.ts` | OpenAPI 生成文件 |
| `src/routes/__root.tsx` | Outlet、正文链接、错误及未找到边界 |
| `src/routes/index.tsx` | 基础状态首页 |
| `src/routeTree.gen.ts` | Router 生成文件 |
| `src/i18n/index.ts` | 独立语言实例、严格格式化和 React 入口 |
| `src/i18n/index.test.tsx` | 插值、复数、原型键拒绝和语言切换 |
| `src/i18n/react-i18next.d.ts` | 从唯一 JSON 推导键类型 |
| `src/i18n/format.ts` | 整数分金额、整数与显式时区时间 |
| `src/i18n/format.test.ts` | 金额精度、时区和无效值 |
| `src/styles/tokens.css` | Mekke 风格的独立语义变量 |
| `src/styles/base.css` | 基础 HTML、焦点、安全区和减少动态效果 |
| `src/components/ui/button.tsx` | 原生按钮、busy 与禁重复点击 |
| `src/components/ui/text-field.tsx` | 单/多行输入、标签说明和错误关联 |
| `src/components/ui/dialog.tsx` | Dialog/ConfirmDialog、Portal 与焦点管理 |
| `src/components/ui/popover.tsx` | 弹出层、碰撞与内部滚动 |
| `src/components/ui/status-badge.tsx` | 有文字的语义状态标签 |
| `src/components/ui/state-panel.tsx` | 加载、空态、错误和恢复操作 |
| `src/components/ui/skeleton.tsx` | 结构占位且不进入无障碍内容 |
| `src/components/ui/styles.ts` | 公共类名组合与焦点样式 |
| `src/components/ui/ui.test.tsx` | 控件、输入法、弹层键盘和焦点回归 |
| `src/test/setup-dom.ts` | 本地 DOM 环境及每例清理 |

`embed.go`、`embed_test.go` 由 Go 集成方维护，不属于本基础层实现的所有权。
