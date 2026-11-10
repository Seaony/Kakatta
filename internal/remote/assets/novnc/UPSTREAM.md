# 上游静态库

来源：[noVNC v1.7.0](https://github.com/novnc/noVNC/tree/v1.7.0)。这里只保留 `core/`、它需要的 `vendor/pako/`、作者名单和许可证，不包含开发工具、代理服务、默认网页或运行时。

核心文件按 MPL-2.0 发布，pako 按 MIT 发布；原文见同目录许可证及 `vendor/pako/LICENSE`。本项目的 MIT 许可证不覆盖这些文件。

`core/rfb.js` 仅有两处集成改动：

- 新增构造选项 `touchGestures: false`，由外层接管触摸，不挂载上游手势识别。
- 新增 `sendPointer(x, y, buttons)`，接收画布 CSS 像素坐标和鼠标按键位，沿用原来的坐标换算与协议发送。

上游默认手势要移动约 50 像素才开始拖动，双指还会转换为 Ctrl 加滚轮。这里的改动让触摸第一刻就能在原点按下，并由外层禁止双指缩放，符合 `docs/DECISIONS.md` 第 39 条。鼠标、键盘和解码器保持上游实现。

`core/inflator.js` 另修复重置压缩流的问题：随附 pako 的 `inflateReset` 清掉 `wsize`，却保留 `window`；下一块重新解压时不会重新设置窗口大小。Tight 流重置后的第一块画面可解，第二块引用前块历史时会报错。包装层现在重建 `ZStream` 并保留输出缓冲，不改 pako 内核。回归用 Node zlib 生成同一流的两个 `Z_SYNC_FLUSH` 块，分别检查正常连续解码和重置后连续解码的所有像素。
