# 待讨论的问题

只放需要作者拍板的事：产品方向、个人偏好，以及证据互相矛盾、我定不下来的。技术和安全上的事由我研究后决定，直接写进 `DECISIONS.md`（见 `AGENTS.md`）。

定下来后移到 `DECISIONS.md`，这里删掉。

---

## 1. 域名和官网

作者说以后再定。到时候参考 Dalibre：用 Astro 做官网，部署在 Cloudflare Workers。

## 2. 要不要做两步验证（TOTP）

服务端可能暴露到公网，里面又存着闲鱼登录状态和各种 Key，登录一旦被攻破损失很大。Vaultwarden、Uptime Kuma、1Panel 都提供可选的 TOTP。这是 `PRODUCT.md` 里没有的新功能，等作者定。我的建议是做成可选项，默认关闭，重置密码的命令同时能关掉它。
