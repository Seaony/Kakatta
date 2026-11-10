# 浏览器远程画面

这只负责把已有 Xvfb 的画面交给用户，不创建浏览器、不判断登录或验证结果，也不操作验证码。

`Start(ctx, display)` 在 Linux 容器里启动仅监听 `127.0.0.1` 的 x11vnc。调用方结束人工会话时调用 `Close()`；父上下文取消、x11vnc 自己退出也会关闭连接并回收进程组。`Address()` 只给同进程代码检查，不放进网页、日志或接口响应。

```go
session, err := remote.Start(ctx, display)
// 检查 err，结束时 session.Close()
mux.Handle("/remote/", http.StripPrefix("/remote", session.Handler(authorized)))
```

`authorized` 每次请求都检查现有会话，包括全部静态资产和 WebSocket。传 nil 会全部拒绝。WebSocket 只接受同站点来源和二进制数据；本地 VNC 端口不对外暴露。

`session.SetViewport(&remote.Viewport{X: x, Y: y, Width: w, Height: h})` 的坐标是固定 1920×1080 Xvfb 画面里的位置，包含 Chrome 工具栏。nil 显示全画面。页面连接后读取一次 `/remote/viewport`；后续可由同源父页发送：

```js
frame.contentWindow.postMessage({ type: 'kakatta:viewport', region: { x, y, width, height } }, location.origin);
```

手机保持至少 1:1 显示。触摸第一触点立即按下鼠标，从原点连续移动；第二触点、双指缩放与 Ctrl 滚轮不会改变远程网页。切换“移动画面”后只平移本地视野，不发送鼠标操作。鼠标和键盘继续用 noVNC 核心实现。

“输入文字”打开本地文字框，可调起手机软键盘。只有点击“发送文字”才通过 noVNC 的 `sendKey` 发送字符，输入法组合期间不会发送；退格、回车单独发送。发送后、关闭输入区、断开画面或离开页面都会清空本地文字，不记录输入内容。

## 单次实验控制页

`ControlPage()` 返回自包含页面，`ControlPagePolicy()` 返回对应固定内容哈希的 CSP。它不依赖登录前不能读取的远程资产。调用方给 `/` 返回页面及这个 CSP，并实现以下同源接口：

- `POST /auth`：表单字段 `token`。
- `POST /session`：打开独立登录流程，由服务端检查成功并关闭浏览器。
- `POST /run`：另开浏览器跑一次实验流程。
- `POST /verify`：打开或复用等待人工处理的验证画面。
- `POST /confirm`：用户完成验证后，由服务端检查结果、冷却并关闭浏览器。
- `POST /close`：结束并关闭浏览器。
- `GET /status`：`{phase,running,events:[{at,event,fields}],error,reason,next_allowed_at,verification_pending,viewport}`。

phase 支持 `idle/opening/ready/running/verification/completed/failed/closed`。`completed/failed/closed` 停止轮询，下一次操作会重新刷新；其余阶段每两秒读取本地状态。`reason`、`next_allowed_at` 直接显示服务端提示，页面不计算冷却时间。登录失效的接口返回 401。

`verification_pending: true` 会在关闭或失败后继续保留“打开验证画面”入口；确认按钮只在当前验证画面打开时显示。操作接口的 409 正文按纯文本显示具体原因。

`viewport` 变化时控制页向当前画面发送一次位置消息；相同位置不会在轮询时反复重置用户平移。重新连接的画面仍会自行读取位置接口。

## 本地验证

`go test -race ./internal/remote` 使用假 VNC 和 httptest。`node --test internal/remote/*_test.mjs` 检查触摸起点、连续移动、第二触点、取消、平移、缩放拦截，以及控制页的登录、验证和终态。

真实 x11vnc 测试只在明确设置 `KAKATTA_REMOTE_X11_TEST=1` 和 `DISPLAY` 的隔离 Linux 容器里运行；测试不访问任何外部网站。noVNC 的来源、许可和修改说明见 `assets/novnc/UPSTREAM.md`。

`KAKATTA_REMOTE_INPUT_TEST=1` 另启真实输入回归，只在断网 Linux 容器里运行：账号 460 的 Chrome/Xvfb 接收真实 RFB 鼠标和键盘事件，检查数字、中文、退格、全选清空和回车；账号 461 作为独立 noVNC 客户端，在 393×852 和 359×577 模拟视口里使用真实文字按钮发送，再从目标 Chrome 的无障碍树读取值。客户端还检查 1:1 初始画布位置及首帧实际像素。这覆盖客户端发送链路，不等于手机实机软键盘和触摸验收。
