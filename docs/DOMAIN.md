# 业务规则与数据模型

2026-11-10 整理。写代码时照着这份文档实现：核心概念、数据库表、一次运行怎么走、怎么判断、怎么调度、怎么调用 AI、怎么推送、数据怎么清理、怎么导入导出。

内容来源：

- 作者之前的 Mac 版闲鱼监控 Mekke（私有仓库）的产品文档、技术文档和实施记录。其中用得上的规则、提示词和边界情况，已经完整抄进本文，不需要再去看 Mekke。
- 上游 [ai-goofish-monitor](https://github.com/usagi-org/ai-goofish-monitor)（MIT，提交 `77f8d75`）的源码，引用时写明文件和行号。
- 各推送服务的官方文档，链接写在对应的小节里。

和 Kakatta 已定的文档冲突时，以 `PRODUCT.md`、`DECISIONS.md`、`CRAWL-POLICY.md` 为准；本文和 Mekke 不一样的地方，列在最后一节。闲鱼的页面地址、选择器、接口名和字段路径不在本文里，见 `XIANYU.md`。

---

## 0. 约定

- **时间**：数据库里一律存 UTC 的 Unix 毫秒（`INTEGER`）。「今天」「自然日」「每天 01:00」都按设置项 `general.timezone` 的时区算。
- **金额**：数据库里一律存「分」（`INTEGER`），界面显示成元。闲鱼的价格文字去掉 `¥` 和逗号，带「万」的乘以 10000，解析不出来时为空。
- **配置项**：本文写成 `crawl.max_details_per_run` 这样的键名，指第 3 节设置表里的值。所有数值都不写死。
- **随机等待**：在区间里均匀取值，结果永远不低于区间下限。
- **一个「访问」**：程序做的、会让闲鱼产生页面加载或数据接口请求的一个动作。每个动作产生什么请求，以 `XIANYU.md` 5.8 节的表为准：
  - 算：打开首页（包括登录时程序打开的首页）、在搜索框提交关键词、发布时间选项、个人闲置、包邮、地区的提交、价格提交、翻页和翻页重试、打开详情页、打开卖家主页、卖家主页里切到「信用及评价」、登录后打开自己的主页、验证时重开浏览器打开触发验证的网址（7.2）。表里写着「待用真实页面确认」的动作（比如地区弹层里点省、市），确认之前也算。
  - 不算：表里写着「一般没有请求」的动作（打开「新发布」菜单、关广告），以及聚焦输入框。
  - 用户自己在远程画面里的操作（扫码、过验证）不是程序做的，不算。

  额度（7.3 节）按访问计数。首页、搜索、筛选、翻页算搜索类；详情页、卖家主页、卖家主页里的「信用及评价」、自己的主页算详情类。「信用及评价」不是新页面，只计一次详情类额度，不额外占「每次运行最多打开几个」（`crawl.max_details_per_run`）的名额（4.9）。

---

## 1. 核心概念

| 概念 | 说明 |
|---|---|
| 任务 | 用户的一个「想要」：一组搜索条件、一种判断方式（AI 或关键词）、运行间隔和通知设置。比如「MacBook Pro M3 16G，5000–7000 元，只看个人闲置」。每个任务固定用一个闲鱼账号。 |
| 闲鱼账号 | 用来搜索的闲鱼账号。每个账号有自己长期保留的浏览器配置目录，登录状态由浏览器自己保存。可以有多个。 |
| 代理 | 访问闲鱼时走的代理。每个闲鱼账号固定绑定一个代理，或者不用代理，不轮换。另外可以给 AI 请求单独设一个代理。 |
| 商品 | 闲鱼上的一个商品，用闲鱼商品 ID 唯一标识。同一个商品可能被几个任务搜到，商品信息只存一份。 |
| 见过 | 某个任务在搜索结果里出现过某个商品。见过的记录永久保留（只随任务删除），所以老商品不会重新变成新商品。 |
| 新商品 | 对某个任务来说，从没见过的商品。同一个商品对另一个任务仍然可能是新商品，要用那个任务的标准重新判断。 |
| 发现 | 某个任务第一次见到某个新商品时产生的一条记录，带判断结果、是否已读、是否通知过。结果列表展示的就是发现。 |
| 推荐 | AI 任务里档位是捡漏或划算（80 分及以上），或者无法比价且不是不建议；同时没超出预算，商品没被标为不感兴趣，卖家没被屏蔽。关键词任务里命中规则（`decision = matched`）。统计和概览用这个口径；推不推送看任务的推送档位（13.2）。 |
| 需求清单 | AI 任务里，由用户的大白话需求整理成的一行一条的清单：可以接受的规格、必须满足、加分项、风险检查项和态度（2.6）。 |
| 配置 | 需求清单里规格值的一种组合，如「iPad mini 6 / 256G」。比价按配置分组。 |
| 合理价 | 同配置近 14 天在售价格的中位数（12.1）。 |
| 值得分和档位 | 程序按比合理价便宜多少、加分项、风险和卖家算出的 0–100 分，加上 AI 在 −15 到 +8 之间的综合判断；档位是捡漏、划算、价格合理、偏贵或有顾虑、不建议（5.4）。AI 不直接打分。 |
| 分层判断 | 第一层硬条件和排除词（免费，所有任务）→ 第二层批量整理标题：识别垃圾、抽规格（便宜，AI 任务固定开启）→ 第三层多模态模型看图和描述、提取事实（贵）→ 程序算分。前一层通过才进下一层，详情页只给通过前两层的商品打开。 |
| 基线 | 任务第一次运行（或改了搜索条件后的第一次运行）看到的商品，只记录、不通知。 |
| 补处理队列 | 上次没处理完（额度不够、超出本次上限）、可以重试的失败发现，以及用户点了「重新分析」的发现，由之后的运行接着处理（顺序见 4.8）。 |
| 运行 | 任务的一次执行：搜索 → 找出新商品 → 分层判断 → 通知 → 记日志。 |
| 访问额度 | 每个闲鱼账号每小时、每天能访问闲鱼的次数上限。 |
| 风控冷却 | 某个账号遇到人机验证后，用户完成验证，账号还要暂停一段时间才自动运行，时长逐次加倍。 |
| 推送渠道 | 一个配置好的推送目标，比如某个 Telegram 机器人。同一种渠道可以配多个，不同任务可以推到不同的渠道。 |
| AI 服务商 | 一个 OpenAI 兼容接口的配置（地址、Key、模型）。可以配多个，按顺序使用，当前这个出错时换下一个。 |
| 市场行情 | 某个任务近 14 天搜到的商品按配置的价格统计，以及可选的 AI 行情解读。 |

---

## 2. 数据模型（SQLite）

### 2.1 通用规则

- 驱动用 `modernc.org/sqlite`。连接打开后执行 `PRAGMA journal_mode=WAL`、`PRAGMA foreign_keys=ON`、`PRAGMA busy_timeout=5000`。
- 数据库文件放在数据目录 `<data>/kakatta.db`。同一目录下还有：`secret.key`（加密主密钥）、`browser/<账号 id>/`（每个账号的浏览器配置目录）、`cache/images/`（缩略图缓存）。
- 服务端自己下载商品图片（第三层看图、缓存缩略图）时，走这个任务所用账号的代理（账号没绑代理就直连），不带 Cookie。
- 主键一律 `INTEGER PRIMARY KEY`。布尔值存 `0/1`。列表、对象存 JSON 文本，列名以 `_json` 结尾。
- 表结构用编号的迁移脚本升级，当前版本记在 `schema_migrations`。
- **给用户看的原因一律存原因代码加参数**，不存成句子：`findings.reason`、`findings.notify_skip_reason`、`accounts.state_reason`、`ai_providers.error_state`、`tasks.last_error`、`run_records.message` 和 `warnings_json` 里的每一条、`run_steps.message` 和 `detail`、`system_events.message`、`deliveries.error`、`channels.last_error`，都存成 `{"code": "await_ai", "args": {"cause": "no_provider"}}` 这样的 JSON，显示时按界面语言翻译，中英文都要完整（`DECISIONS.md` 第 22 条）。本文写成原因「不感兴趣」「等待分析：……」的地方，存的是对应的代码，引号里是中文界面显示的文字。程序判断状态（比如「等待分析」）只看代码，不看文字。AI 自己写的话（`summary`、`title_screen_reason`、AI 综合判断的理由）不翻译，原样存、原样显示；AI 服务商返回的原始错误（8.7 的「详细信息」）放在参数里，遮掉 Key 后原样显示。

### 2.2 密钥加密

API Key、推送 Token、Webhook 请求头、代理地址（可能带密码）、邮箱密码、两步验证密钥都加密保存，列名以 `_enc` 结尾，类型 `BLOB`。

- 主密钥：第一次启动时生成 32 字节随机数，写进 `<data>/secret.key`，文件权限 0600。也可以用环境变量 `KAKATTA_SECRET_KEY`（base64）指定，或者用 `KAKATTA_SECRET_KEY_FILE` 指向一个文件（配合 Docker secrets），指定了就不读 `secret.key`。两个环境变量同时设置时拒绝启动，日志里说清楚「只能设其中一个」，不猜用哪个。主密钥丢了，所有加密的值都要重新填。
- 能防什么、不能防什么（文档如实写）：主密钥和数据在同一个目录时，加密只挡得住「只有数据库文件泄露」，挡不住整个数据目录被拿走（`DECISIONS.md` 第 40 条）。
- 算法：AES-256-GCM。存储格式是 `12 字节随机 nonce + 密文`。附加数据（AAD）用 `表名:列名:这一行的唯一键`，防止把一行的密文挪到另一行用。不用行 id：插入之前还拿不到它。唯一键必须在插入前就有、之后不会变（用户能改的名字不算）：`admin` 用固定的 `1`；`proxies`、`ai_providers`、`channels` 没有这样的自然唯一键，各加一列 `uuid`（插入前生成）。
- 接口永远不返回明文。界面上只显示「已设置」，用户要改就整个重填。日志、运行记录、错误信息里都不能出现明文，报错时先把当前 Key 遮掉再截取。

### 2.3 登录 Kakatta

**admin**（只有一行）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | 固定为 1，`CHECK (id = 1)` |
| username | TEXT NOT NULL | |
| password_hash | TEXT NOT NULL | Argon2id，PHC 字符串格式（含参数和盐） |
| totp_secret_enc | BLOB | 两步验证（TOTP）的密钥，没开启时为空 |
| totp_enabled_at | INTEGER | 开启两步验证的时间，没开启时为空 |
| created_at / password_changed_at | INTEGER NOT NULL | |

- 还没有这一行时，只有填对容器日志里的一次性初始化令牌才能创建（30 分钟有效，过期重启容器再拿新的）。令牌只存在内存里，不写数据库（`DECISIONS.md` 第 40 条）。
- 登录失败按 IP 递增延迟，另有全局失败次数上限；真实 IP 只从可信代理的请求头里读（`security.trusted_proxies`，3.1；`DECISIONS.md` 第 40 条）。失败次数达到全局上限时新建 `login_failures` 系统事件并推送（2.11）。
- **两步验证**（可选，默认关闭，`DECISIONS.md` 第 44 条）：在「设置 → 安全」里开启，填对验证器 App 里的 6 位数字才算开启成功，然后写 `totp_secret_enc` 和 `totp_enabled_at`。开启后登录分两步：用户名和密码通过后，得到一个 5 分钟有效的待验证状态（只存在内存里，不发会话）；再填 6 位数字，对了才发会话。填错和密码填错一样计入递增延迟和全局失败次数上限；5 分钟内没填完要从头登录。不做恢复码：丢了验证器，用容器里的命令关掉两步验证（清空这两个字段，`PRODUCT.md` 第 11 节）。

**auth_tokens**：网页端的 Cookie 会话和 API 令牌都在这里。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| kind | TEXT NOT NULL | `web_session` / `api_token` |
| token_hash | BLOB NOT NULL UNIQUE | 令牌的 SHA-256，明文只在发放时返回一次 |
| name | TEXT NOT NULL | 网页会话取浏览器的 User-Agent 摘要；API 令牌是用户新建时填的名字 |
| created_at / last_used_at | INTEGER | |
| expires_at | INTEGER | 网页会话 30 天不用就过期；API 令牌为空，直到吊销 |
| revoked_at | INTEGER | |

- 改密码时吊销除当前会话以外的全部会话和令牌。容器里的「重置密码」命令吊销全部。
- 索引：`(kind, revoked_at)`。

**API 令牌和 MCP**：

- API 令牌在「设置 → 安全」里新建：填一个名字，明文只显示这一次，之后只能看到名字、创建时间和最近使用时间；可以吊销。不分权限：只有一个用户（`DECISIONS.md` 第 13 条），令牌能做的和登录后一样。
- 脚本和 MCP 都在请求头里用 `Authorization: Bearer <令牌>` 带这个令牌。
- MCP 走 HTTP（Streamable HTTP），路径 `/mcp`。提供的工具：列出任务、查结果、新建任务、暂停和恢复任务、立即运行。每个工具都调用和网页端同一套服务端接口，规则（校验、额度、前置检查）完全一样，不另写一套逻辑。

### 2.4 设置与提示词

**settings**：`key TEXT PK`、`value_json TEXT NOT NULL`、`updated_at INTEGER`。没有这一行就用第 3 节的默认值。

**prompts**：`key TEXT PK`（`analysis` / `title_screen` / `profile` / `market`）、`content TEXT NOT NULL`、`updated_at`。没有这一行就用第 8 节的内置原文。「还原」就是删掉这一行；保存的内容和内置原文一样时也直接删掉，系统状态里就不会误报「已自定义」。

### 2.5 闲鱼账号与代理

**proxies**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| uuid | TEXT NOT NULL UNIQUE | 插入前生成，加密的附加数据用（2.2） |
| name | TEXT NOT NULL UNIQUE | |
| url_enc | BLOB NOT NULL | `http://[用户名:密码@]host:port` 或 `socks5://[用户名:密码@]host:port`。带用户名密码的怎么给 Chrome 用，见 7.4 |
| created_at | INTEGER | |

被账号或 `ai.proxy_id` 使用的代理不能删，先解除绑定。

**accounts**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | 浏览器配置目录 `browser/<id>/`；这个账号的 Chrome 用的 Xvfb 显示号（`:100 + id`）和调试端口（`20000 + id`）也按 id 分配（`CRAWL-POLICY.md` 第二节） |
| name | TEXT NOT NULL | 只用于区分 |
| proxy_id | INTEGER REFERENCES proxies | 为空就是不用代理 |
| nickname / avatar_url | TEXT | 登录后读到的闲鱼昵称和头像 |
| state | TEXT NOT NULL | `signed_out` 未登录 / `active` 可用 / `cooling` 冷却中 / `needs_verification` 需要验证 / `denied` 被拒绝访问 / `expired` 已失效 |
| state_reason | TEXT | 给用户看的说明（原因代码加参数，2.1） |
| state_changed_at | INTEGER | |
| cooldown_until | INTEGER | `cooling` 和 `denied` 时有值 |
| verify_level | INTEGER NOT NULL DEFAULT 0 | 连续第几次人机验证，用来算加倍冷却（7.2） |
| verify_url | TEXT | `needs_verification` 时，触发验证的那个网址；用户点「去验证」时重开浏览器打开它（7.2） |
| last_cooldown_end_at | INTEGER | 上一次验证冷却的结束时间；冷却被新的验证打断时，写打断的时间 |
| cooldown_manual_used | INTEGER NOT NULL DEFAULT 0 | 本次冷却里是否已经用掉那一次手动运行 |
| last_run_ended_at | INTEGER | 用来算同账号两次运行的最小间隔 |
| next_gap_seconds | INTEGER | 上次运行结束时抽好的间隔（3 分钟加随机 0–60 秒），重启也不重新抽 |
| last_used_at / created_at | INTEGER | |

- 不保存 Cookie，也不导入导出 Cookie。删除账号时一并删除它的浏览器配置目录。
- 账号状态怎么变，见第 7 节。

**quota_events**：每次访问闲鱼前先记一条，用来算每小时和每天的额度。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| account_id | INTEGER NOT NULL REFERENCES accounts ON DELETE CASCADE | |
| kind | TEXT NOT NULL | `search`（首页、搜索、筛选、翻页）/ `detail`（详情页、卖家主页、卖家主页里的「信用及评价」、自己的主页） |
| run_id | INTEGER | |
| at | INTEGER NOT NULL | |

索引：`(account_id, at)`。保留 48 小时。

### 2.6 任务

**tasks**

| 字段 | 类型 | 默认 | 说明 |
|---|---|---|---|
| id | INTEGER PK | | |
| name | TEXT NOT NULL | 关键词 | 1–30 字 |
| keyword | TEXT NOT NULL | | 1–50 字，存之前去掉首尾空白 |
| enabled | INTEGER NOT NULL | 1 | |
| account_id | INTEGER REFERENCES accounts ON DELETE SET NULL | 自动分配 | |
| account_pinned | INTEGER NOT NULL | 0 | 1 = 用户指定了账号，0 = 自动分配的。指定的账号被删除时改回 0，按自动分配处理（7.1） |
| min_price / max_price | INTEGER | 空 | 分；都有值时 `min <= max`，都不能为负 |
| personal_only | INTEGER NOT NULL | 1 | 只看个人闲置 |
| free_shipping_only | INTEGER NOT NULL | 0 | 只看包邮 |
| publish_window | TEXT NOT NULL | `any` | `any` / `day1` / `day3` / `day7` / `day14`。页面上的发布时间筛选一律只点「最新」，搜索结果按最新发布排序；「N 天内」完全交给本地补筛（5.1）。「1 天内」这类选项选了以后是否还按最新排序没有依据，而默认只翻 1 页的前提就是按最新排序 |
| region_province / region_city / region_district | TEXT | 空 | 必须能在随程序发布的地区数据里选到 |
| max_pages | INTEGER NOT NULL | `crawl.max_pages` | ≥ 1，只拦小于 1 的；超过 3 时界面提示风险，但允许保存（`DECISIONS.md` 第 17 条） |
| interval_minutes | INTEGER NOT NULL | `crawl.task_interval_minutes` | ≥ 1 |
| crawl_overrides_json | TEXT NOT NULL | `{}` | 本任务单独的频率设置，见 3.2 |
| judge_mode | TEXT NOT NULL | 配了 AI 时 `ai`，否则 `keyword` | |
| requirement | TEXT NOT NULL | `""` | AI 任务的大白话需求 |
| category | TEXT NOT NULL | `general` | 品类，决定带出哪套风险检查项（5.7） |
| profile_json | TEXT NOT NULL | `{}` | 需求清单，见下文；AI 任务至少要有一项规格或一条必须满足 |
| budget_min / budget_max | INTEGER | 空 | 分；大概预算，不是硬上限：打分不看，超出上限的不推送（13.2） |
| match_keywords_json | TEXT NOT NULL | `[]` | 关键词任务的匹配词 |
| exclude_keywords_json | TEXT NOT NULL | `[]` | 排除词，所有任务都可以填 |
| analyze_on_first_run | INTEGER NOT NULL | 1 | 首次运行也分析最新的 10 个（不通知） |
| notify_policy | TEXT NOT NULL | AI 任务 `good`，关键词任务 `recommended` | AI 任务：`steal` 只推捡漏 / `good` 划算及以上 / `fair` 价格合理及以上 / `all_new` 所有新商品 / `none` 不推送。关键词任务：`recommended` 命中就推 / `all_new` / `none`（13.2）。切换判断方式（AI / 关键词）后，原来的值对新方式不合法时（如关键词任务的 `good`），改成新方式的默认值 |
| notify_price_drop | INTEGER NOT NULL | 1 | 推荐过的商品降价时通知 |
| search_generation | INTEGER NOT NULL | 1 | 搜索条件每改一次加 1，见 4.6 |
| baseline_pending | INTEGER NOT NULL | 1 | 1 = 下一次成功运行是基线运行 |
| sort_index | INTEGER NOT NULL | | |
| created_at / updated_at | INTEGER | | |
| last_run_at / last_success_at / next_run_at | INTEGER | | |
| last_run_outcome | TEXT | | `success` / `failed` / `skipped` |
| last_error | TEXT | | 原因代码加参数（2.1） |
| consecutive_failures | INTEGER NOT NULL | 0 | |
| failure_paused_until | INTEGER | | 失败保护 |
| no_good_hint_dismissed_at | INTEGER | | 用户在「长期没有推荐」的提示上点「知道了」的时间（5.6） |

索引：`(enabled, next_run_at)`、`(account_id)`。

**需求清单 `profile_json`**（`DECISIONS.md` 第 48 条）：

```json
{
  "summary": "会帮你找：iPad mini 6 或 7，256G 或 512G，WiFi 版，不要有锁和监管机",
  "specs": [
    {"name": "型号", "options": ["iPad mini 5", "iPad mini 6", "iPad mini 7"], "values": ["iPad mini 6", "iPad mini 7"], "source": "user", "edited": false},
    {"name": "存储", "options": ["64G", "128G", "256G", "512G"], "values": ["256G", "512G"], "source": "user", "edited": false},
    {"name": "网络", "options": ["WiFi", "蜂窝"], "values": ["WiFi"], "source": "ai", "edited": false}
  ],
  "musts": [{"id": "m1", "text": "国行", "source": "user", "edited": false}],
  "bonuses": [{"id": "b2", "text": "电池 85% 以上", "source": "user", "edited": false}],
  "risks": [
    {"id": "id_lock", "attitude": "reject", "source": "user", "edited": false},
    {"id": "battery_replaced", "attitude": "mind", "source": "ai", "edited": false},
    {"id": "c3", "custom": "屏幕有烧屏", "attitude": "mind", "source": "user", "edited": false}
  ],
  "allowed_kinds": [],
  "next_no": 4
}
```

- `specs` 的顺序就是重要程度，越靠前越重要；比价样本不够时先去掉最后一项（12.1）。`values` 是可以接受的值，可以多选；为空表示「都行」。`options` 是给用户点选的候选值，用户也可以加自己的值。
- **固定编号**：必须满足、加分项、用户自写的检查项各有一个固定编号，前缀分别是 `m`、`b`、`c`，后面的数字取 `next_no`，取完 `next_no` 加 1。编号在条目新建时分配（生成清单、用户手动加一条都算），之后不变；条目删掉后编号也不再给别的条目用。发给 AI（5.3）、AI 回的结论（8.2）、存下来的结论（`analysis_json`、`user_overrides_json`）都用这个编号，所以清单增删条目后，已有商品的结论不会对错条目。字典里的检查项直接用字典编号。
- `musts` 是规格以外的硬条件，只能是用户说过的（`source = user`）。保存时程序检查：`source = ai` 的必须满足一律移到 `bonuses`（编号前缀改成 `b`，取新编号）。AI 补充的条件只能进加分项或风险（`DECISIONS.md` 第 48 条）。生成清单时，AI 标成 `source = user` 的必须满足，程序还要在需求原文里核对一遍（8.6）。
- `risks`：字典里的检查项 `id` 是字典编号（5.7）；用户自己写的检查项 `id` 是 `c` 开头的固定编号，文字在 `custom` 里。`attitude` 是 `reject` 不能接受 / `mind` 介意 / `ignore` 无所谓。检查项 = 品类模板带出的 + AI 额外挑的 + 用户自己加的：模板带出的默认态度来自字典；生成时 AI 可以改态度、加项，不能删掉模板项（8.6）。检查项和规格、必须满足一样带 `source` 和 `edited`。用户写的检查项没有预设话术，问卖家时用「关于『xx』，方便说明一下吗？」。
- 用户改了品类（`category`）时，只替换没改过（`edited = false`）的模板项：旧品类模板里有、新品类模板里没有的去掉；新品类模板里有、清单里还没有的按字典默认态度加上；两边都有的不动。`edited = true` 的、旧模板以外的（AI 额外挑的、用户自己写的）都保留。不调 AI。
- `allowed_kinds`：用户的需求本身就是配件、坏机这类时，第二层放行的类型（如 `["broken"]`），见 5.2。
- `edited`：用户手动改过的条目（规格、必须满足、加分项、检查项都算），重新生成时保留（8.6）。界面上 `source = ai` 的条目颜色淡一些，标「AI 补充的」。
- 关键词任务的 `profile_json` 为 `{}`。

**task_channels**：`task_id`、`channel_id`，主键是两者组合，两边都 `ON DELETE CASCADE`。新建任务时默认勾上所有已启用的推送渠道。任务里没勾任何渠道就不推送。

### 2.7 商品、价格与卖家

**listings**

| 字段 | 类型 | 说明 |
|---|---|---|
| item_id | TEXT PK | 闲鱼商品 ID，去重只用它 |
| title | TEXT NOT NULL | |
| price | INTEGER | 分，当前价格 |
| price_text / original_price_text | TEXT | 闲鱼原文 |
| thumbnail_url | TEXT | 搜索结果里的主图 |
| image_urls_json | TEXT | 详情里的全部图片 |
| desktop_url | TEXT NOT NULL | 网页版链接。手机端链接由商品 ID 现算，不存（见 13.1） |
| area | TEXT | |
| seller_id | TEXT | 打开详情后才知道 |
| seller_nick | TEXT | |
| published_at | INTEGER | |
| want_count / browse_count | INTEGER | |
| tags_json | TEXT | 如「包邮」「验货宝」 |
| description | TEXT | 打开详情后才有 |
| seller_registration_days | INTEGER | 详情里卖家的注册天数 |
| zhima_level | TEXT | |
| first_seen_at / last_seen_at | INTEGER NOT NULL | 任何任务搜到它都更新 `last_seen_at` |
| detail_fetched_at | INTEGER | |
| is_favorite | INTEGER NOT NULL DEFAULT 0 | 收藏 |
| favorited_at | INTEGER | |
| is_dismissed | INTEGER NOT NULL DEFAULT 0 | 不感兴趣 |
| last_drop_amount | INTEGER | 最近一次降价的幅度（分），涨价时清空 |
| last_drop_at | INTEGER | |
| sale_status | TEXT | `on_sale` / `sold` / `offline`，空 = 不知道。只在打开详情时顺便更新，不为它额外访问闲鱼（详情接口里的字段路径待用真实页面确认，`XIANYU.md` 第十一节）。不是 `on_sale` 时卡片和详情打「已卖出」「已下架」标签 |

索引：`(last_seen_at)`、`(seller_id)`、`(is_favorite)`。

**price_points**：`id`、`item_id REFERENCES listings ON DELETE CASCADE`、`price INTEGER NOT NULL`、`observed_at INTEGER NOT NULL`。只在价格变化（或还没有价格点）时追加。索引：`(item_id, observed_at)`。

**task_sightings**：每个任务每天搜到每个商品时的最后价格，市场行情用。

| 字段 | 类型 | 说明 |
|---|---|---|
| task_id | INTEGER NOT NULL REFERENCES tasks ON DELETE CASCADE | |
| item_id | TEXT NOT NULL | |
| day | TEXT NOT NULL | `general.timezone` 时区下的日期，`YYYY-MM-DD` |
| price | INTEGER | 当天最后一次看到的价格 |
| last_seen_at | INTEGER NOT NULL | |

主键 `(task_id, day, item_id)`。同一天再搜到就覆盖价格和时间。

**task_item_specs**：每个任务眼里每件商品是什么、什么配置，比价用（`DECISIONS.md` 第 49、50 条）。不随发现删除。

| 字段 | 类型 | 说明 |
|---|---|---|
| task_id | INTEGER NOT NULL REFERENCES tasks ON DELETE CASCADE | |
| item_id | TEXT NOT NULL | |
| kind | TEXT | 第二层识别的类型：`sale` / `wanted` / `accessory` / `recycle` / `rental` / `repair` / `box` / `multi` / `broken` / `other`。关键词任务只有规则认出的 `wanted`、`recycle`、`rental`，其余为空 |
| specs_json | TEXT | `[{"name": "存储", "value": "256G"}]`，按任务清单的规格名；读不出来的值为 `null` |
| brand_new | INTEGER | 1 全新或未拆封 / 0 二手 / 空 不知道 |
| config_key | TEXT | 规格值规整（NFKC、去首尾空白）后按清单顺序用 ` / ` 连起来；任何一项为空就为空（规格不明） |
| excluded | INTEGER NOT NULL DEFAULT 0 | 1 = 不按这个配置算合理价（12.1）：第二层判为不符合需求的（`verdict = unmatched`：标题明确违反必须满足或说明有不能接受的问题），或第三层某条必须满足有文字证据地不满足。规格超出勾选范围的不算，仍是 0（相邻配置的差价要用它们） |
| source | TEXT NOT NULL | `title`（第二层）/ `detail`（第三层）/ `user`（用户手动改的） |
| updated_at | INTEGER NOT NULL | |

主键 `(task_id, item_id)`。`source = user` 的不会被第二层、第三层覆盖。清单的规格名改了以后，`config_key` 按新规格名从 `specs_json` 重算，读不到的为空，直到重新分析。

**seller_profiles**：卖家主页的缓存。

| 字段 | 说明 |
|---|---|
| seller_id TEXT PK | |
| nick、avatar_url、introduction | |
| seller_credit、buyer_credit | 闲鱼的信用文字原文，只用来显示 |
| seller_credit_level、buyer_credit_level | 信用等级字段（`attributes.level`，`XIANYU.md` 9.4），判断「信用极好」用（5.8） |
| item_count、rate_count、on_sale_count、sold_count | `item_count` 取 `tabs.item.number`，它是在售数、已售数还是总数待确认（`XIANYU.md` 9.4）；`on_sale_count`、`sold_count` 只数最近 20 条商品，最多各 20 |
| seller_good_rate、buyer_good_rate | 好评率，保留一位小数，没有样本时为空 |
| recent_items_json | 最多 20 条：标题、价格、主图、在售或已售 |
| recent_rates_json | 最近 10 条评价摘要：身份（作为卖家 / 买家）、好中差评、内容前 60 字 |
| fetched_at INTEGER NOT NULL | 在 `crawl.seller_cache_hours` 以内就直接用 |

**blocked_sellers**：`seller_id TEXT PK`、`nick`、`created_at`。永不自动清理。

### 2.8 见过与发现

**seen_items**：`task_id REFERENCES tasks ON DELETE CASCADE`、`item_id TEXT`、`first_seen_at`，主键 `(task_id, item_id)`。**不参与任何清理**，只随任务删除。删除结果、清理过期发现都不碰它。

**findings**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| task_id | INTEGER NOT NULL REFERENCES tasks ON DELETE CASCADE | |
| item_id | TEXT NOT NULL REFERENCES listings | |
| search_generation | INTEGER NOT NULL | 创建时任务的搜索代次 |
| found_at | INTEGER NOT NULL | |
| is_baseline | INTEGER NOT NULL | |
| decision | TEXT NOT NULL | `pending` / `scored`（AI 任务算完分）/ `matched`（关键词任务命中）/ `rejected` / `failed` / `skipped` |
| reject_kind | TEXT | `rejected` 时：`filter`（第一层的价格、包邮、发布时间、地区）/ `excluded`（排除词）/ `junk`（不是在卖这件东西）/ `unmatched`（不符合需求）/ `keyword_miss`（关键词任务没命中） |
| stage | TEXT NOT NULL | 下一步做什么：`title`（等第二层）/ `detail`（等打开详情和第三层）/ `done`。`pending` 和 `failed` 都保留原来的一步：第二层失败的留 `title`，第三层（含打开详情）失败的留 `detail`，下次重试从这一步接着做。只有得出最终结论（`scored`、`matched`、`rejected`）或 `skipped` 时才设为 `done` |
| decided_layer | INTEGER | 在第几层得出结论：1 / 2 / 3；跳过和失败为空 |
| reason | TEXT | 原因代码加参数（2.1）：不符合、不建议、跳过、失败或等待的原因；算完分的是分数的主要依据 |
| analysis_json | TEXT | 第三层提取的事实，已经过证据核对（8.4） |
| user_overrides_json | TEXT | 用户手动改的风险结论，如 `{"risks": {"id_lock": "ok"}}`（5.4） |
| score | INTEGER | 0–100；无法比价、没算分时为空 |
| grade | TEXT | `steal` 捡漏 / `good` 划算 / `fair` 价格合理 / `pricey` 偏贵或有顾虑 / `avoid` 不建议；无法比价且不是不建议时为空 |
| score_breakdown_json | TEXT | 分数构成：基础分和依据、每项加减、AI 判断和理由、封顶（5.4） |
| fair_price / fair_sample / fair_basis | INTEGER / INTEGER / TEXT | 算分时用的合理价、样本数、比价依据（如「同配置 · 近 14 天」「放宽到 30 天 · 去掉网络」） |
| scored_price | INTEGER | 第一次算分时的价格，判断「降价后升档」用（第 10 节） |
| scored_at | INTEGER | 最近一次算分的时间 |
| summary | TEXT | AI 的一句话结论 |
| seller_type | TEXT | `personal` / `dealer_suspect` / `dealer` / `unknown`（5.8） |
| title_screen_reason | TEXT | 第二层的一句话说明 |
| matched_keywords_json | TEXT | 关键词任务命中的词 |
| analysis_model / analysis_provider_id | TEXT / INTEGER | 得出结论的模型和服务商，成功返回后才写 |
| analyzed_at | INTEGER | |
| attempts | INTEGER NOT NULL DEFAULT 0 | 处理过几次：第二层、第三层每处理一次加 1；记为「等待分析」的那一次不加（8.5）。重新分析时清零（5.5） |
| retryable | INTEGER NOT NULL DEFAULT 0 | 失败时能不能下次重试 |
| reanalyze_requested_at | INTEGER | 用户点「重新分析」的时间；有值的在处理集合里排最前面（4.8），处理完（得出结论、失败或等待分析）清空（5.5） |
| is_read | INTEGER NOT NULL | 基线发现创建时就是已读，其余是未读 |
| notify_kind | TEXT | 推送判定的结果（13.2）：`recommended` 按推荐发 / `new` 按「新商品」发 / 空 = 不发或还没判定 |
| notify_skip_reason | TEXT | 判定为不发时写原因代码（13.7）；发送一直失败、到了重发上限时也写（13.4） |
| notified_at | INTEGER | 至少一个渠道发送成功的时间 |
| notify_failures | INTEGER NOT NULL DEFAULT 0 | 所有渠道都发送失败的次数，到了重发上限就不再发（13.4） |
| recommended_price | INTEGER | 推送判定时达到推送条件的价格（13.2），降价通知的基准；降价后升档时也写（第 10 节） |
| last_drop_notified_price | INTEGER | 上一次发降价通知时的价格 |
| drop_notified_at | INTEGER | |

- 唯一约束：`(task_id, item_id)`。
- 索引：`(task_id, found_at)`、`(task_id, decision)`、`(task_id, score)`、`(is_read)`、`(item_id)`。
- 「分析失败（已放弃）」不另存字段：`decision = failed` 且 `retryable = 0`。
- 「等待分析」也不另存字段：`decision = pending` 且 `reason` 的代码是 `await_ai`（8.5）。
- 推送只判定一次：发现第一次得出结论后判定（4.10、13.2），之后 `notify_kind` 和 `notify_skip_reason` 不再改，只有两种例外：发送一直失败时补写 `notify_skip_reason`（13.4）；改了搜索条件时，旧代次里还没发出去的不再发（4.6）。

### 2.9 AI 服务商

**ai_providers**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| uuid | TEXT NOT NULL UNIQUE | 插入前生成，加密的附加数据用（2.2） |
| name | TEXT NOT NULL | |
| preset | TEXT | `openai` / `deepseek` / `modelscope` / `siliconflow` / `openrouter` / `aliyun` / `anthropic` / `ollama` / `custom`，只用于预填地址 |
| base_url | TEXT NOT NULL | 如 `https://api.openai.com/v1`，去掉末尾的 `/` |
| api_key_enc | BLOB | 本地 Ollama 可以不填 |
| vision_model | TEXT NOT NULL | 第三层用，必须支持图片 |
| text_model | TEXT | 第二层、生成需求清单、行情解读用；为空就用 `vision_model` |
| enabled | INTEGER NOT NULL DEFAULT 1 | |
| sort_index | INTEGER NOT NULL | 越小越先用 |
| error_state | TEXT | 出了需要用户处理的错（Key 无效、余额不足、模型不存在、模型不支持图片）时写原因代码加参数（2.1），有值就不参与自动使用 |
| error_at | INTEGER | |

用户保存或试分析成功时清空 `error_state`。`error_state` 是「余额不足」或「当天额度用完」时，`general.timezone` 的第二天 0 点后自动用文字模型发一次最小请求（`Reply with OK`），返回 2xx 就清空。这次请求只看是不是 2xx：不解析回复，不走 8.3 的截断处理，所以不会因为回复被截断把服务商标成「思考太长」。其他原因（Key 无效、模型不存在、不支持图片、思考太长）要用户处理。

**ai_compat**：每个「地址 + 模型」的兼容标记，见 8.3。

| 字段 | 说明 |
|---|---|
| provider_id INTEGER REFERENCES ai_providers ON DELETE CASCADE | |
| model TEXT | |
| json_mode TEXT NOT NULL DEFAULT 'schema' | `schema` / `object` / `text` |
| send_temperature INTEGER NOT NULL DEFAULT 1 | |
| max_tokens_param TEXT NOT NULL DEFAULT 'max_tokens' | `max_tokens` / `max_completion_tokens` |
| max_tokens_floor INTEGER | 回复被截断后调大的输出上限（8.3），为空时按 8.1 表里的值；有值时各用途取两者中较大的 |
| updated_at | |

主键 `(provider_id, model)`。服务商的地址或模型改了，删掉它的兼容标记重新试。

**ai_calls**：每次请求一条，不存请求和回复的内容。字段：`id`、`at`、`provider_id`、`model`、`purpose`（`title_screen` / `analysis` / `profile` / `market` / `test` / `probe`）、`task_id`、`ok`、`http_status`、`error_kind`、`json_mode`、`input_tokens`、`output_tokens`、`reasoning_tokens`（返回的 `usage.completion_tokens_details.reasoning_tokens`，没有为空）、`finish_reason`、`duration_ms`。索引 `(at)`。

### 2.10 推送渠道

**channels**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| uuid | TEXT NOT NULL UNIQUE | 插入前生成，加密的附加数据用（2.2） |
| type | TEXT NOT NULL | `bark` / `telegram` / `ntfy` / `gotify` / `wecom` / `dingtalk` / `feishu` / `pushplus` / `serverchan` / `webhook` / `email` |
| name | TEXT NOT NULL | 同一种渠道配多个时用来区分，如「Telegram · 家人群」 |
| enabled | INTEGER NOT NULL DEFAULT 1 | |
| config_json | TEXT NOT NULL | 不敏感的配置（见 13.6 各渠道的配置项） |
| secret_enc | BLOB | 敏感配置，加密后的 JSON |
| last_test_at / last_test_ok / last_error | | `last_error` 是原因代码加参数（2.1） |

`UNIQUE (type, name)`。

**deliveries**：每次实际发送一条。字段：`id`、`at`、`channel_id`、`task_id`、`kind`（`recommended` / `all_new` / `price_drop` / `summary` / `system` / `test`）、`item_count`、`ok`、`error`（原因代码加参数，2.1；带渠道名，不含地址、Token 或响应正文）。索引 `(at)`、`(channel_id, at)`。

**delivery_items**：一次发送里包含哪些发现。字段：`delivery_id REFERENCES deliveries ON DELETE CASCADE`、`finding_id INTEGER`，主键 `(delivery_id, finding_id)`，索引 `(finding_id)`。合并消息里的每个发现都写一行。用来在发现上显示推送状态（13.7），随 `deliveries` 一起清理。

### 2.11 运行记录与系统事件

**run_records**

| 字段 | 说明 |
|---|---|
| id INTEGER PK | |
| task_id INTEGER REFERENCES tasks ON DELETE SET NULL | |
| task_name TEXT NOT NULL | 冗余保存，任务删了也能显示 |
| trigger TEXT NOT NULL | `auto` / `manual` |
| account_id、account_name、proxy_name | 这次用的账号和代理 |
| started_at、ended_at | |
| outcome TEXT NOT NULL | `success` / `failed` / `skipped` |
| failure_kind TEXT | `risk_verify` / `risk_deny` / `risk_env` / `login` / `network` / `page` / `timeout` / `quota` / `config` / `cancelled` / `restart` |
| message TEXT | 原因代码加参数（2.1）。成功写「扫描 N 页 · 分析 N 件」或「没有新商品」，失败和跳过写原因 |
| repeat_count INTEGER NOT NULL DEFAULT 1 | 同一个任务因同一个原因连续跳过时，只保留一条记录，这里累计次数，`ended_at` 更新为最近一次（4.1） |
| page_count、scanned_count、new_count | 翻了几页、搜到几件、其中几件是新的 |
| layer1_rejected、layer2_count、layer2_rejected、detail_count、seller_page_count | 分层的数量 |
| analyzed_count、recommended_count、notified_count | |
| warnings_json | 卖家资料缺失等不影响结果的提示，同一次运行里去重 |

索引：`(started_at)`、`(task_id, started_at)`。

**run_steps**：逐步日志。字段：`run_id REFERENCES run_records ON DELETE CASCADE`、`seq`、`at`、`status`（`ok` / `warn` / `fail`）、`message`、`detail`（耗时或失败原因）。主键 `(run_id, seq)`。一次运行最多记 100 步，超出后只记失败的步骤。

**system_events**：需要推送和在概览「异常」卡里显示的事件，也用来防止同一件事重复推送。

| 字段 | 说明 |
|---|---|
| id INTEGER PK | |
| kind TEXT | `account_verify` / `account_login` / `account_deny` / `ai_error` / `ai_budget` / `task_failure_pause` / `xianyu_layout` / `login_failures` |
| subject_id INTEGER | 账号、服务商或任务的 id；`ai_budget`、`xianyu_layout`、`login_failures` 为 0 |
| message TEXT | 原因代码加参数（2.1） |
| created_at、resolved_at、notified_at | |

同一个 `(kind, subject_id)` 有未解决的事件时，不再新建、不再推送。解决的时机：账号恢复可用、服务商下一次请求成功或被用户保存、任务暂停结束、第二天 0 点或用户调高每日上限（`ai_budget`）。

另外两种：

- `xianyu_layout`「闲鱼页面结构可能变了」（`DECISIONS.md` 第 42 条）：找不到关键元素（`XIANYU.md` 3.6 节选择器总表里的元素）、搜索接口成功但响应里没有商品列表字段，或者同一个任务连续 3 次运行第 1 页解析出 0 个商品而接口返回成功、并且这个任务以前搜到过商品（按这个任务最近 3 条运行记录判断；从来没搜到过的冷门关键词不算）时新建。之后任何一次运行正常解析出商品时解决。
- `login_failures`：登录 Kakatta 的失败次数达到全局上限时新建（2.3）。下一次登录成功时解决。

### 2.12 市场行情

**market_insights**：`id`、`task_id REFERENCES tasks ON DELETE CASCADE`、`generated_at`、`stats_json`（生成时用的统计）、`text`（AI 行情解读）、`provider_id`、`model`。索引 `(task_id, generated_at)`。结果页显示最新的一条。

---

## 3. 设置项

### 3.1 全部设置

「推荐范围」以外的值允许保存，但界面要明显提示，并写明风险（`DECISIONS.md` 第 17 条）。只拦明显无效的值：0、负数、下限大于上限、不存在的选项。

区间类的值存成 `{"min": 10, "max": 15}`。

**抓取与限制**（理由和风险照 `CRAWL-POLICY.md` 第六节显示在界面上）

| 键 | 默认值 | 推荐范围 |
|---|---|---|
| `crawl.task_interval_minutes` | 30（新建任务的默认间隔） | 30–120 |
| `crawl.interval_jitter_percent` | 30（实际间隔随机延长 0–30%） | ≥ 0 |
| `crawl.account_min_gap_seconds` | 180 | ≥ 120 |
| `crawl.account_min_gap_jitter_seconds` | 60 | ≥ 0 |
| `crawl.account_hourly_visits` | 30 | ≤ 40 |
| `crawl.account_daily_search_visits` | 200 | ≤ 200 |
| `crawl.account_daily_detail_visits` | 100（详情页加卖家主页） | ≤ 150 |
| `crawl.max_pages` | 1（新建任务的默认翻页数） | 1–3 |
| `crawl.page_wait_seconds` | 10–15 | ≥ 10 |
| `crawl.max_details_per_run` | 5（详情页和卖家主页合计，见 4.9） | 3–10 |
| `crawl.detail_pre_wait_seconds` | 3–6 | ≥ 2 |
| `crawl.detail_stay_seconds` | 10–20 | ≥ 8 |
| `crawl.action_wait_seconds` | 1–3 | ≥ 1 |
| `crawl.home_stay_seconds` | 2–5 | ≥ 2 |
| `crawl.typing_interval_ms` | 50–130 | — |
| `crawl.seller_cache_hours` | 24 | ≥ 12 |
| `crawl.quiet_hours` | `{"enabled": true, "start": "01:00", "end": "07:30"}` | 按自己的作息设 |
| `crawl.global_pages` | 1（全局同时打开的页面数；同一账号永远只开一个页面，这是行为不是数值，不做成配置，`DECISIONS.md` 第 41 条） | 1–2（取决于内存） |
| `crawl.verify_hold_minutes` | 30（运行中出现验证后，保留这个 Chrome 等用户处理的时长，见 7.2） | 10–60 |
| `crawl.verify_cooldown_base_minutes` | 120 | 见 `CRAWL-POLICY.md` 第四节 |
| `crawl.verify_cooldown_max_minutes` | 1440 | |
| `crawl.verify_cooldown_reset_hours` | 24 | |
| `crawl.deny_pause_hours` | 24 | ≥ 24 |
| `crawl.failure_threshold` | 3 | — |
| `crawl.failure_pause_minutes` | 30 | — |
| `crawl.run_timeout_minutes` | 15 | — |

**AI**

| 键 | 默认值 | 说明 |
|---|---|---|
| `ai.max_items_per_run` | 10 | 每次运行最多处理几个商品（第三层和关键词判断合计；第二层批量整理不算），推荐 5–20 |
| `ai.image_count` | 4 | 第三层每个商品发几张图，1–4 |
| `ai.max_concurrent_requests` | 2 | 整个服务端同时进行的 AI 请求数 |
| `ai.request_timeout_seconds` | 90 | |
| `ai.proxy_id` | 空 | AI 请求专用的代理，只影响 AI，不影响闲鱼和推送 |
| `ai.daily_token_limit` | 0 | 每天最多用多少 token（输入加输出，所有服务商合计），0 = 不限，见 8.8 |

**通知**

| 键 | 默认值 | 说明 |
|---|---|---|
| `notify.link_target` | `mobile` | 推送里的商品链接打开手机 App（`mobile`）还是网页版（`desktop`） |
| `notify.max_per_run` | 5 | 同一个任务一次运行最多发几条，是总数：超出时单独发 4 条，加 1 条合并消息（13.3） |
| `notify.system_channel_ids` | `[]` | 系统事件推到哪些渠道，空列表 = 所有启用的渠道 |

**通用、数据与缓存**

| 键 | 默认值 | 说明 |
|---|---|---|
| `general.language` | `system` | `system` / `zh-CN` / `en`。只影响 Kakatta 的界面和推送文字；Chrome 的 `--lang` 固定 `zh-CN`，不跟它走（闲鱼是中文网站，中文 Chrome 最常见） |
| `general.appearance` | `system` | `system` / `light` / `dark` |
| `general.timezone` | 环境变量 `TZ`，没有就用 `Asia/Shanghai` | 启动 Chrome 时把 Chrome 进程的 `TZ` 环境变量设成这个值 |
| `general.public_url` | 空 | 用户从外面访问 Kakatta 的地址，推送里「在 Kakatta 查看」的链接用它，空就不放这个链接 |
| `general.update_check` | true | 每天检查一次 GitHub Releases 有没有新版本，有就在网页上提示（`DECISIONS.md` 第 43 条） |
| `general.onboarding_done` | false | |
| `security.trusted_proxies` | `[]` | 可信的反向代理（IP 或网段）列表。只信任列表里的代理传来的 `X-Forwarded-*` 头，用来取真实 IP（2.3，`DECISIONS.md` 第 40 条）；空 = 一律用连接的来源 IP。也可以用环境变量 `KAKATTA_TRUSTED_PROXIES`（逗号分隔）指定，指定了就以它为准，界面上只读 |
| `retention.findings_days` | 30 | 7 / 30 / 90 / 0（0 = 永久） |
| `retention.run_steps_days` | 14 | 逐步日志，日志页只显示这么多天 |
| `retention.stats_days` | 31 | 运行记录、AI 调用、推送记录、行情明细，供「近 30 天」统计 |
| `retention.seller_profile_days` | 30 | |
| `retention.orphan_listing_days` | 30 | 没有发现引用、没收藏的商品多久没再出现就删 |
| `cache.image_max_mb` | 500 | 缩略图缓存上限 |
| `cache.browser_cache_mb` | 256 | 每个账号浏览器的磁盘缓存上限，用 Chrome 的 `--disk-cache-size` 参数 |
| `cleanup.interval_hours` | 24 | 启动时清理一次，之后按这个间隔 |

### 3.2 任务单独的频率设置

`tasks.crawl_overrides_json` 里可以覆盖这些键：`interval_jitter_percent`、`page_wait_seconds`、`max_details_per_run`、`detail_pre_wait_seconds`、`detail_stay_seconds`、`action_wait_seconds`、`home_stay_seconds`、`typing_interval_ms`、`run_timeout_minutes`。没写的跟随全局设置。

按账号算的限制（最小间隔、每小时和每天的额度、冷却）不能按任务覆盖，因为它们保护的是账号。

---

## 4. 一次运行

任务 T 用账号 A。下面每一步出现风控信号（`CRAWL-POLICY.md` 第四节），都**立即结束抓取和判断**，错误一路传到最外层，任何一层都不能吞掉；然后照常做 4.10 的第 1、2 步（重算、发通知），再按第 7 节处理账号。

### 4.1 前置检查

前置检查不满足时都不打开浏览器，**不计入失败**。按「能不能靠等时间解决」分两种：

**靠时间能等到的**：调度器静默等待，不写运行记录，到时间自动开始（6.1、6.2）：

1. 任务启用、失败保护的暂停已过（这两条手动运行不看）。
2. 不在 `crawl.quiet_hours` 里。
3. 距离这个账号上次运行结束，已经过了 `next_gap_seconds`。
4. 账号不在冷却或拒绝访问的暂停期里（`cooling` 冷却期内唯一一次手动运行除外，见 7.2）。
5. 访问额度够：账号的每小时额度和每天的搜索额度，都不少于这个任务筛选完成前必需的访问次数（见下）。
6. AI 任务：今天的 AI 用量没到 `ai.daily_token_limit`（8.8）。
7. 有空闲的页面名额：全局不超过 `crawl.global_pages`；同一账号永远只开一个页面、同时只跑一个任务（`DECISIONS.md` 第 41 条），扫码登录和过验证的会话也算（7.2）。

**筛选完成前必需的访问次数**按这个任务的条件算（计数口径见第 0 节）：首页 1 + 搜索 1 + 发布时间 1 + 开着的个人闲置、包邮各 1 + 地区（设置了时：打开弹层和点省、市、区里任务实际要点的几级，确认不产生请求之前都算，再加提交 1）+ 价格（设置了时 1）。筛选没做完额度就用完，整次运行的结果都要丢掉（4.3 第 6 步），所以不够就不开始。这只是入口检查，真正的限制仍在每次访问前检查（7.3）。

**需要用户处理的**：写一条「已跳过」运行记录并写明原因，`next_run_at` 推后一个运行间隔（按 6.1 的公式重算）：

1. 任务没有账号（「没有可用的闲鱼账号」），或账号是 `signed_out`、`expired`、`needs_verification`。
2. AI 任务没有可用的 AI 服务商（候选为空，或都有 `error_state`，8.5）。
3. AI 任务的需求清单为空。

同一个任务因同一个原因（原因代码相同）连续跳过时，只保留一条记录：不新建，把上一条的 `repeat_count` 加 1、`ended_at` 改成这一次的时间（2.11）。中间有一次真正开始的运行，或原因变了，就重新记一条。

### 4.2 取补处理队列

在创建任何新记录之前，先取本任务的补处理队列 Q：

- `decision = pending`，且 `search_generation` 等于任务当前的代次；
- `decision = failed`、`retryable = 1`、`attempts < 3`，且代次相同。

两类合在一起按 `found_at` 从旧到新排，按发现 id 去重。

取 Q 之前，先把本任务积压太久的发现清掉：`decision = pending`、从 `found_at` 起已经超过 24 小时还没分析的，改为 `skipped`，`stage = done`，原因「积压太久没分析」，结果里看得到（在「未推荐」筛选里）。用户点了重新分析的，从 `reanalyze_requested_at` 起算 24 小时。「等待分析」的（`reason` 代码是 `await_ai`，8.5）不清：AI 恢复后照样补上（`PRODUCT.md` 第 4 节），只是排在新发现后面（4.8）。理由：捡漏的商品一天后多半已经卖掉，积压的旧商品不能挡住新商品。

### 4.3 搜索

1. 启动这个账号的 Chrome（带固定代理）。打开首页之前，先在本地看一下 `unb` Cookie 在不在（只看有没有这个名字的 Cookie，不读它的值，`XIANYU.md` 8.3）。`unb` 是不是登录 Cookie 在实验里确认之前，没有它只在运行记录里记一条警告，不把账号标为失效；登录是否失效以打开首页后是否跳到登录页为准。
2. 打开首页，停留 `crawl.home_stay_seconds`，随机滚动一次。
3. 在搜索框里逐字输入关键词（`crawl.typing_interval_ms`），回车。
4. 按任务条件依次点筛选：发布时间（每次都点，一律选「最新」；「N 天内」由本地补筛，5.1）、个人闲置、包邮、地区、价格。每次点击之间等 `crawl.action_wait_seconds`。每一步都要等到一条新的搜索接口响应，只用最后一条作为第 1 页数据。
   - 没等到响应时怎么等、能不能重做，以 `XIANYU.md` 5.5 节为准：可重复执行的步骤（发布时间、地区、价格）可以重做一次；开关类步骤（个人闲置、包邮，再点一次会取消）**绝不重复点击**。仍然失败就记为页面失败。
   - 任何一步筛选失败，整次运行都按页面失败结束，不带着没筛完的结果继续。「个人闲置」没法在本地补判，尤其不能带着没筛选过的结果继续。
5. 读第 1 页；需要时翻页，最多 `tasks.max_pages` 页，页与页之间等 `crawl.page_wait_seconds`。某一页 0 个商品就停止翻页。第 2 页以后翻页失败（重试后仍失败，`XIANYU.md` 5.6）：停止翻页，保留已经读到的页，在运行记录里记一条警告，这次运行仍算成功。
6. 每次访问前申请额度（7.3）：
   - 筛选还没全部完成时额度用完：整次运行记为「已跳过」，已拿到的结果全部丢弃，因为它们没经过完整筛选。
   - 筛选完成后、翻页途中用完：停止翻页，用已经拿到的页继续。

页面操作的具体选择器和接口见 `XIANYU.md`。

### 4.4 记录商品、价格和「见过」

对搜到的全部商品（经过页面筛选的所有页）：

1. 写入或更新 `listings`，更新 `last_seen_at`。
2. 价格和这个商品最近一个价格点不同（或还没有价格点）时，追加 `price_points`；降价时写 `last_drop_amount` 和 `last_drop_at`，涨价时清空（第 10 节）。
3. 写 `task_sightings`（今天的价格）。
4. 找出新商品：本任务 `seen_items` 里没有的商品 ID。是否见过只看 `seen_items`，不看发现，因为发现会按保留期清理。
5. 把全部新商品写进 `seen_items`，包括下面第一层没通过的。

### 4.5 创建发现

每个新商品创建一条发现。先依次检查：

1. 商品被标记过不感兴趣 → `skipped`，原因「不感兴趣」。
2. 商品的 `seller_id` 已知（之前打开过详情）且卖家被屏蔽 → `skipped`，原因「已屏蔽卖家」。
3. 第一层（5.1）不通过 → `rejected`，`decided_layer = 1`，`reject_kind` 按 5.1 填，原因写哪一条没通过。
4. 其余的是候选。

然后分两种情况：

- **基线运行**（`tasks.baseline_pending = 1`）：所有发现 `is_baseline = 1`、已读。开启了 `analyze_on_first_run` 时，候选里发布时间最新的 10 个为 `pending`，其余为 `skipped`，原因「基线」；关闭时候选全部 `skipped`。
- **普通运行**：候选全部为 `pending`、未读。

`pending` 的发现，AI 任务 `stage = title`，关键词任务 `stage = detail`。

### 4.6 改了搜索条件

保存任务时，关键词、价格区间、个人闲置、包邮、发布时间、地区任何一项变了：

- `search_generation` 加 1，`baseline_pending` 设为 1：下一次运行只记录现有商品、不分析、不通知（开了首次分析时仍然分析最新的 10 个，但不通知）。表单在保存按钮上方提示「修改搜索条件后，下次运行会重新记录现有商品，不会提醒」。
- 旧代次里还是 `pending` 和可重试 `failed` 的发现改为 `skipped`，`stage = done`，原因「搜索条件已修改」，不再分析、不再通知。旧代次里已经得出结论、但还没判定推送或判定了还没发出去的（`notified_at` 为空、`notify_skip_reason` 为空），`notify_kind` 清空，`notify_skip_reason` 写「搜索条件已修改」。历史发现和收藏都保留。
- 这个任务正在运行时：直接取消这次运行（`cancelled`，4.11），不做 4.10 的第 1、2 步（重算、发通知）。这次运行已经建的发现属于旧代次，按上一条处理；已经写进 `seen_items` 的保留，价格点照常保留。之后各次运行的 4.10 只给当前代次的发现发通知。

改名称、间隔、判断规则、通知设置不重置基线。改了间隔时，`next_run_at` 重算为「保存时间 + 新间隔」。

### 4.7 第二层：批量整理标题

只对 AI 任务做（5.2）。不打开任何闲鱼页面，可以在浏览器停在搜索页时进行。处理的商品：

- 本次运行的全部新商品（包括基线运行的），除了已经 `skipped` 的（不感兴趣、屏蔽卖家）和命中排除词的。第一层因价格、包邮、发布时间、地区没通过的也处理，只为比价抽规格，不改变它们的结论。
- 补处理队列 Q 里 `stage = title` 的发现。

结果写进 `task_item_specs`；`stage = title` 的发现按 5.2 得出结论，每处理一个 `attempts` 加 1：

- 通过 → `stage = detail`，记 `title_screen_reason`。
- 不通过 → `rejected`，`decided_layer = 2`，`stage = done`，`reject_kind` 为 `junk` 或 `unmatched`。
- 所有服务商都出错 → 按 8.5 记为 `failed`（`stage` 保持 `title`，下次从第二层重试）或「等待分析」（这一次的 `attempts` 不加）。不打开详情。只为比价处理的商品出错就不再补，不影响结论。

### 4.8 处理集合

第二层做完以后，把 `stage = detail` 的发现排成处理集合 P，按发现 id 去重，顺序是：

1. 用户点了重新分析的（`reanalyze_requested_at` 有值，5.5），按点的时间先后；
2. 本次新建的非基线发现，按发布时间从新到旧；
3. Q 里的非基线发现，按 Q 的顺序；
4. 基线留下的（`is_baseline = 1`，包括本次基线运行新建的），按发布时间从新到旧。

总数不超过 `ai.max_items_per_run`，超出的保持 `pending`，下次进入 Q。理由：捡漏商品一天后多半已经卖掉，新发现比积压的旧发现更值得先看；积压超过 24 小时的按 4.2 改为跳过。

### 4.9 打开详情和判断

对 P 里的发现按 P 的顺序逐个处理。本次运行打开的详情页和卖家主页合计不超过 `max_details_per_run`（任务覆盖值或全局值；卖家主页里切到「信用及评价」不另占名额，第 0 节）。每处理一个，`attempts` 加 1（记为「等待分析」的那一次不加，见第 6 步）。得出最终结论的设 `stage = done`；失败的 `stage` 保持 `detail`。

0. 商品的 `detail_fetched_at` 在 24 小时以内（之前打开过但没判断完，比如上次在等待分析）：直接用已有的详情，跳过第 1、2、7 步，不占详情上限。
1. 打开详情页**之前**，申请一次详情额度。申请不到，或本次的详情上限已用完：P 里剩下的都保持 `pending`，结束这一步，本次运行仍算成功。
2. 等 `crawl.detail_pre_wait_seconds`，打开详情页，读详情。解析失败或加载失败只影响这一件：记为 `failed`、`retryable = 1`（第 3 次失败后为 0），继续下一件。风控、登录失效、取消不在这里处理，照常往外抛。
3. 拿到 `seller_id` 后**再检查一次屏蔽列表**，命中就 `skipped`：不打开卖家主页、不判断、不通知。
4. 用标题加描述再查一次排除词（第 9 节），命中就 `rejected`，`decided_layer = 1`，`reject_kind = excluded`，原因「描述命中排除词：xx」。
5. 关键词任务：按第 9 节判断，`decided_layer = 1`。
6. AI 任务：
   - 卖家资料在缓存里且没过期 → 直接用。
   - 否则先申请一次详情额度并占用一个详情上限名额，再打开卖家主页。申请不到，或卖家主页的普通错误（不是风控、不是登录失效）：卖家资料留空继续判断，在运行记录的提示里写一句，同一次运行里去重。
   - 卖家主页里点「信用及评价」之前，再申请一次详情额度（不占详情上限名额，第 0 节）。申请不到就不点，评价留空，卖家资料的其他部分照常用。
   - 第三层提取事实（5.3），再按 5.4 算分。所有服务商都出错时按 8.5 记为 `failed` 或「等待分析」；记为等待分析的，这一次的 `attempts` 不加。判断可以和下一件的抓取同时进行，同时最多 `ai.max_concurrent_requests` 个；浏览器仍然一次只开一个页面。
7. 看完详情后停留 `crawl.detail_stay_seconds`。

算完分后是否达到推送档位、什么时候写 `recommended_price`，见 13.2。

### 4.10 通知、记录和结束

第 1、2 步不管这次运行成功、失败、超时还是因风控中断都执行，已经保存的结果照常判定和发送；只有因为改了搜索条件被取消的（4.6）不执行。

1. AI 任务先按 12.1 更新本任务各配置的合理价，再把本任务保留期内 `scored` 的发现按 5.4 重算分（只用已存的事实，不调 AI，不访问闲鱼）。然后对这次运行里第一次得出结论的发现做**推送判定**（13.2），每个发现一生只判定一次：达到推送条件的写 `notify_kind` 和 `recommended_price`，没达到的写 `notify_skip_reason`。
2. 按第 13 节发通知：本任务当前代次（4.6）里 `notify_kind` 有值、`notified_at` 为空、`notify_skip_reason` 为空的发现，发之前再确认一次商品没被标为不感兴趣、卖家没被屏蔽；加上本次检测到的降价和降价后升档（第 10 节）。全部渠道都失败的保持 `notified_at` 为空，下次运行再发，即使那时没再搜到这个商品；重发有上限（13.4）。
3. 写 `run_records`，更新任务的 `last_run_at`、`last_success_at`、`next_run_at`、`consecutive_failures`、`last_run_outcome`。基线运行成功后 `baseline_pending = 0`。
4. 更新账号的 `last_run_ended_at`，抽好下一次的 `next_gap_seconds`。关闭这个账号的 Chrome；因为需要人机验证而结束的，按 7.2 保留 Chrome 等用户处理。
5. 成功时按 12.2 判断要不要生成 AI 行情解读，在后台进行，不占用调度。

整次运行（含判断）超过 `run_timeout_minutes` 就超时：停止抓取和判断，照常做第 1、2 步，本次记为失败（`timeout`）。

### 4.11 运行结果怎么算

| 情况 | 结果 | 计入失败保护 |
|---|---|---|
| 正常完成（包括详情额度用完、处理不完留到下次、第 2 页以后翻页失败） | 成功 | — |
| 前置检查里需要用户处理的（4.1）、筛选未完成时额度用完 | 跳过 | 否 |
| 需要人机验证、直接拒绝 | 失败（`risk_verify` / `risk_deny`） | 否，按第 7 节处理账号 |
| 页面出现「非法访问」「请使用正常浏览器访问」（环境被识别） | 失败（`risk_env`） | 否，按直接拒绝处理账号（7.2） |
| 登录失效 | 跳过（`login`） | 否 |
| 用户取消、改了搜索条件被取消（4.6）、服务关闭 | 失败（`cancelled` / `restart`） | 否 |
| 网络或代理连不上、页面结构变化、筛选失败、响应超时、导航失败 | 失败（`network` / `page`） | 是 |
| 整次运行超时 | 失败（`timeout`） | 是 |

服务启动时，上次没结束的运行记录一律改为失败（`restart`）。

---

## 5. 分层判断和打分

核心思路（`DECISIONS.md` 第 48–55 条）：AI 只读懂商品、提取事实、给出证据；分数由程序按规则算，再加上 AI 有上下限的综合判断。规则算出来的分数稳定、能解释、能排序；硬门槛由规则把守，AI 不能突破。

| 层 | 适用 | 输入 | 输出 | 花费 |
|---|---|---|---|---|
| 第一层：硬条件和排除词 | 所有任务 | 搜索结果里的信息 | 通过，或没通过的原因 | 免费 |
| 第二层：批量整理标题 | AI 任务，固定开启；关键词任务只用规则（第 9 节） | 需求清单、最多 20 件商品的搜索信息 | 每件的类型、规格、是否明显不符合 | 便宜 |
| 第三层：看图和描述、提取事实 | AI 任务 | 需求清单、合理价、详情、卖家资料、商品图 | 规格、每条需求和检查项的结论及证据、卖家评价信号、AI 综合判断 | 贵 |
| 算分 | AI 任务 | 第三层的事实、合理价（12.1）、卖家画像（5.8）、用户手动改的结论 | 值得分和档位 | 免费 |

### 5.1 第一层

输入是搜索结果里已有的字段：标题、价格、地区、卖家昵称、主图、发布时间、想要人数、标签。

逐条检查，第一条没通过的写进原因：

1. **价格**：设置了价格区间、商品有价格且不在区间里 → 「价格不在 x–y 元之间」。没有价格的不按价格排除。
2. **包邮**：要求包邮、标签里没有「包邮」 → 「不包邮」。
3. **发布时间**：设置了发布时间范围、商品有发布时间且早于「现在减 N×24 小时」 → 「发布超过 N 天」。发布时间解析不出来的不排除。
4. **地区**：设置了地区、商品的地区文字不为空时复核，地区文字为空时不排除。比较前，任务里的省名、市名和商品的地区文字都去掉末尾的「省」「市」「自治区」「特别行政区」：
   - 省是「全国」且市是「全国」，或者省是「海外」：不做本地复核。
   - 市是「江浙沪」「珠三角」「京津冀」「东三省」之一（地区数据里它们是「全国」下面的市级选项，`XIANYU.md` 5.5）：商品地区文字含这一组里任一省名就算在范围内。江浙沪 = 江苏、浙江、上海；珠三角 = 广东；京津冀 = 北京、天津、河北；东三省 = 辽宁、吉林、黑龙江。
   - 市名以「全」开头（如「全广东」，选了省没选具体的市）：只比省名，商品地区文字含省名就算在范围内。
   - 其他（选了具体的市）：商品地区文字含省名或市名就算在范围内。
   - 商品地区文字只有市名、不含省名时，先用同一份地区数据（`XIANYU.md` 5.5）由市名反查省名再比。地区文字的真实格式（省加市、只有市，还是拼音）待用真实页面确认（`XIANYU.md` 第十一节）。

   不在范围内 → 「不在所选地区」。
5. **排除词**：标题命中任一排除词 → 「命中排除词：xx」。
6. **匹配词**（只对关键词任务，且填了匹配词）：标题没有命中任何匹配词 → 「标题没有命中匹配词」。

`reject_kind`：第 1–4 条为 `filter`，第 5 条为 `excluded`，第 6 条为 `keyword_miss`。

第 1、2、4 条是对页面筛选的二次确认：页面上的筛选有时没生效，本地再查一遍。发布时间在页面上只点「最新」（4.3），「N 天内」只靠第 3 条在本地筛。「个人闲置」在本地判断不了，只靠页面筛选。本地补筛以本节为准（`XIANYU.md` 5.7 引用这里）；被第 1–4 条排除的商品照样进第二层抽规格，用于比价（4.7）。

### 5.2 第二层：批量整理标题

- 用服务商的 `text_model`，提示词是 8.6 的「标题整理模板」。一次最多 20 件，超过就分批。一批 20 个标题约 2000 token；基线运行 30–90 件是 2–5 次请求。
- 用户消息：

  ````
  ## 需求清单
  ```json
  <清单 JSON>
  ```

  ## 商品（搜索列表）
  ```json
  <商品数组 JSON>
  ```
  ````

  清单 JSON：`specs[{name, options, values}]`、`musts[]`（文字）、`reject_checks[]`（态度为「不能接受」的检查项名称）、`allowed_kinds`。商品数组每件：`no`（本批序号，从 1 开始）、`title`、`price`（元）、`tags`。键名英文、按键名排序、中文不转义。
- 输出见 8.2：每件给出 `kind`、`specs`、`brand_new`、`verdict`（`pass` / `unmatched`）和 `reason`。
- 程序按顺序得出结论，结果写进 `task_item_specs`（`source = title`）：
  1. `kind` 不是 `sale`、也不在清单的 `allowed_kinds` 里 → 垃圾，`reject_kind = junk`，原因写类型名：求购帖、配件或周边、回收广告、租赁、维修服务、空盒或包装、多款同链接、坏机或配件机、其他。
  2. 某项规格的值在这项的 `options` 里、但不在 `values` 里（`values` 为空表示都行）→ `unmatched`，原因如「型号是 iPad mini 5，不在你选的范围里」。值不在 `options` 里的当作看不出，记 `null`。
  3. `verdict = unmatched`（标题明确违反某条必须满足，或标题就能看出某个「不能接受」的检查项，如「有锁」「监管机」「卡贴」）→ `unmatched`，原因用 `reason`；`task_item_specs.excluded = 1`，不算进合理价（12.1）。
  4. 其余通过。
- 原则是「拿不准就放行」：标题看不出规格的记「规格不明」照样放行，打开详情后用描述和图片补全或纠正。第二层只用来省掉明显不对的商品的详情访问和多模态费用，宁可多放过，也不能错杀。
- 回复里缺了某件、或某件格式不对：这件按通过处理，规格不明，写一条日志。
- 垃圾和不符合的：不分析、不推送、不进行情；结果页默认隐藏，在「未推荐」筛选里能看到；照常记入「见过」，不会再变成新商品。

### 5.3 第三层：看图和描述、提取事实

- 用服务商的 `vision_model`，提示词是 8.6 的「商品分析模板」。
- 消息结构：一条 `system` 消息放提示词；一条 `user` 消息，`content` 是数组，先放图片，每张是 `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,..."}}`，最后放 `{"type":"text","text": ...}`。没有图片时 `content` 直接用字符串。
- 用户消息文字：

  ````
  ## 需求清单
  ```json
  <清单 JSON>
  ```

  ## 商品信息
  ```json
  <商品 JSON>
  ```
  ````

  没有图片时在末尾加一行：`补充说明：本次没有提供商品图片，请只根据文字信息判断，不要推测图片内容。`
- 清单 JSON：`specs[{name, options, values}]`；`musts[{id, text}]`、`bonuses[{id, text}]`（`id` 是清单里的固定编号，如 `m1`、`b2`，2.6）；`checks[{key, name, hint}]`：任务的检查项（含「无所谓」的，用来显示），`key` 是字典编号或用户自写检查项的固定编号（如 `c3`），`hint` 是字典里的判断说明（5.7）。卖家检查项（`seller_*`）不放进去：它们由程序按 5.8 算，AI 对评价的判断另外用 `seller_signals` 给出。`fair_price`：这件所在配置的合理价、样本数和依据（12.1），无法比价时为 `null`。
- 商品 JSON（键名英文、按键名排序、中文不转义）：`title`、`price`、`original_price`、`description`、`published_at`、`area`、`tags`、`want_count`、`browse_count`、`url`，以及 `seller{nickname,registration,introduction,zhima_level,seller_credit,buyer_credit,item_count,on_sale_count,sold_count,rate_count,seller_good_rate,buyer_good_rate,recent_rates[],recent_items[]}`。`registration` 写成「来闲鱼 X 年 Y 个月」。没有的字段省略。
- 图片：取详情图片的前 `ai.image_count` 张，同时最多下载 3 张，不带 Cookie，走这个任务所用账号的代理（2.1）。最长边缩到 1024 像素，转成质量 80 的 JPEG，再编成 base64。下载或转换失败的图片直接跳过（包括 Go 解不了的格式）。不用搜索结果的缩略图代替。
- AI 输出的是事实，不是分数（8.2）：
  - `specs`：从标题、描述和图片读出的规格，`brand_new`。
  - `musts`、`bonuses`：每条 `met` / `unmet` / `unknown` 和证据。
  - `checks`：每个检查项 `ok` 没问题 / `risk` 有风险 / `suspect` 疑似 / `unknown` 没提到，附证据和证据来源（`text` 原文引用 / `image` 第几张图看到什么）。
  - `seller_signals`：从卖家评价和简介里读出的 `desc_mismatch` 描述不符、`after_sale` 售后推诿、`slow_ship` 发货慢、`partial_refund` 作为买家被评到手刀或恶意退款、`dealer_words` 简介里的商家用语，各附原文。
  - `price_note`：价格明显低于合理价时，卖家有没有给出可信的原因（如写明急出原因），附原文；没有就为 `null`。
  - `adjust`：−15 到 +8 的综合判断和一句理由（5.4 第 5 条）。
  - `summary`：一句话结论。
- 解析和证据核对见 8.4。核对后存进 `analysis_json`，规格写进 `task_item_specs`（`source = detail`，用户改过的不覆盖），然后按 5.4 算分。

### 5.4 算分

只用已存的事实，不调 AI、不访问闲鱼，所以随时可以重算。每次运行结束（4.10）、用户改需求清单、改某件商品的规格或检查项结论时，都重算受影响的发现。

**1. 每个检查项的最终结论**：用户手动改过的（`user_overrides_json`）为准；否则用 AI 的结论（已经过 8.4 的证据核对）。只靠看图得出的 `risk` 叫「看图判断」，标黄，不当成有文字证据的风险。

- 「只看有没有」的检查项（字典里 `presence_only` 为是的，5.7）：AI 给的 `unknown` 当作 `ok`。卖家没提到就当作没问题：不封顶、不进「联系前问这几句」，界面也不显示「没提到」。
- 卖家检查项（`seller_*`）的结论由 5.8 算出，不来自 AI。程序按卖家主页数字算出的 `risk`（疑似商家、新号）算有证据，和有文字证据的风险一样扣分和判断；差评和到手刀来自通过原文核对的评价（8.4），也算有文字证据。没有卖家资料时，依赖卖家主页的卖家检查项不参与扣分和封顶，新号照常判断（5.8）。

**2. 不符合需求**（不打分，`rejected`，`reject_kind = unmatched`，默认隐藏）：

- 某项规格读出的值在 `options` 里、但不在 `values` 里；
- 某条必须满足 `unmet`，且有文字证据。只靠看图的 `unmet` 按 `unknown` 处理。这种情况同时写 `task_item_specs.excluded = 1`，不算进合理价（12.1）；只是规格超出勾选范围的不写。

**3. 不建议**（`grade = avoid`，分数最高 39，原因写清楚，如「有 ID 锁（你设为不能接受）」）：

- 态度为「不能接受」的检查项是 `risk`，且有文字证据；
- 骗子组合：卖家是新号（5.8）+ 低得反常（下面第 4 条）+ `off_platform` 或 `deposit` 是 `risk` 或 `suspect`。

**4. 基础分**：d =（合理价 − 价格）÷ 合理价，按段线性插值：

| 比同配置合理价 | 基础分 |
|---|---|
| 贵 30% 以上（d ≤ −0.30） | 30 |
| 贵 10–30%（−0.30 < d < −0.10） | 30 → 49 |
| 上下 10% 以内（−0.10 ≤ d ≤ 0.10） | 50 → 69 |
| 便宜 10–20%（0.10 < d ≤ 0.20） | 70 → 84 |
| 便宜 20–35%（0.20 < d ≤ 0.35） | 85 → 94 |
| 便宜 35% 以上（d > 0.35） | 84，标「低得反常，先确认」 |

例：合理价 1,300、价格 1,000，d = 0.23，基础分 = 85 + (0.23 − 0.20) ÷ 0.15 × 9 ≈ 86.8。合理价按这件的配置取（12.1），所以不同配置的商品可以直接按分数比。

**5. 加减分**：

- 每条加分项 `met` +2，最多 +4。
- 卖家是个人、卖家信用极好、最近 10 条评价没有差评：+2。
- 态度为「介意」的检查项：`risk` 且有文字证据 −5（程序算出的卖家检查项 `risk` 算有证据，见第 1 条）；`suspect` 或看图判断 −2。
- 态度为「不能接受」的检查项：`suspect` 或看图判断 −2（同时封顶，见第 7 条）。
- 态度为「无所谓」的只显示，不影响分数。
- 没有卖家资料时，卖家部分不加不减，依赖卖家主页的卖家检查项也不扣分（新号照常，5.8）。
- **AI 综合判断**：`adjust` 限在 −15 到 +8 之间，超出的截到边界；没写理由的按 0。AI 看完全部信息后可以在这个范围内调整，字典里没有的问题也写在这里。往下给的空间比往上大：帮用户挑出问题比把分数抬高更重要。

**6. 合计**：基础分 + 加减分 + AI 综合判断，四舍五入，限在 0–100。

**7. 封顶**（在 AI 综合判断之后执行，AI 不能突破）：

- 态度为「不能接受」的检查项是 `unknown`、`suspect` 或看图判断：最高 89，提示「先问清楚」。「只看有没有」的项没提到时不封顶（第 1 条）；没有卖家资料时，依赖卖家主页的卖家检查项不封顶（5.8）。
- 低得反常：最高 84；`price_note` 给出了通过证据核对的原因时放宽到 94。
- 不建议：最高 39。

**8. 档位**：

| 分数 | 档位 | 颜色 |
|---|---|---|
| 90 以上 | 捡漏 `steal` | 绿色，加重 |
| 80–89 | 划算 `good` | 绿色 |
| 65–79 | 价格合理 `fair` | 中性色 |
| 50–64 | 偏贵或有顾虑 `pricey` | 黄色 |
| 50 以下 | 不建议 `avoid` | 红色 |

不设「100 分无脑买」。

**9. 无法比价**（这件的配置算不出合理价，12.1；商品价格为空时也按无法比价处理）：没有基础分，`score` 为空，显示「无法比价」；命中不建议的照样是 `avoid`，否则 `grade` 为空。推送见 13.2。

**10. 分数构成** `score_breakdown_json`：`base`（基础分、d、合理价、样本数、依据）、`items[]`（每项加减：名称、分值、来源是规则还是用户改的）、`ai`（调整值和理由）、`caps[]`（触发的封顶和原因）、`gates[]`（不建议的原因）。详情页照它写出分数怎么来的，规则部分和 AI 判断分开，如「基础 87（比合理价低 23%）· 电池 88% +2 · 个人信用极好 +2 · AI 判断 +3：带原装充电器和发票 · ID 锁没提到，最高 89」。

**改需求清单以后**：改规格勾选、加分项、检查项态度、预算，马上重算，不花 AI 费用。原来第三层判为不符合的，放宽勾选后会回到算分；放宽勾选不会让第二层已经判为不符合的商品回来，界面提示「有 N 个商品之前因规格不符被排除，重新分析才能纳入」。新增一条必须满足、加分项或检查项时，已有商品没有这一条的结论，按 `unknown` 算，并提示「已有 N 个商品要重新分析才能判断这一条」，由用户决定要不要重新分析。

**关键词任务**没有分数和档位，命中显示「关键词命中」。第一、二层得出的结论也没有分数，显示原因。重新分析失败时，旧的分数不再显示，按「分析失败」显示。

### 5.5 重新分析

结果页的任意发现都可以「重新分析」（包括关键词判断、待处理和跳过的）。重新分析不单独开一次运行：

- 用任务当前的条件重判第一层（5.1）。没通过就直接得出结论（`rejected`，`decided_layer = 1`），到此为止。
- 通过的放回补处理队列：`decision = pending`，`attempts = 0`，`retryable = 0`，`search_generation` 改成任务当前的代次，`reanalyze_requested_at` 写现在；AI 任务 `stage = title`（从第二层开始重走，用当前的需求清单），关键词任务 `stage = detail`。之后由下一次正常运行处理：在处理集合里排最前面（4.8），仍受 `ai.max_items_per_run` 和 `max_details_per_run` 限制，处理不完的留到再下一次。
- 商品的 `detail_fetched_at` 在 24 小时以内：不用等运行，直接用已有的详情重判（AI 任务第二层、第三层照常调 AI），不打开闲鱼、不占额度。
- 界面提示「会在下次运行时分析」，旁边给「立即运行」按钮（6.4）。
- 用户手动改过的规格和检查项结论保留。
- 重新分析不发「发现推荐」和「新商品」通知：这个发现还没做过推送判定的，判定时达到推送条件只写 `recommended_price`，`notify_skip_reason` 写「重新分析的结果不发推荐通知」（13.7）；已经判定过的不再判定（13.2）。之后的降价和降价后升档照常通知（第 10 节）。

### 5.6 长期没有推荐的提示

AI 任务最近 30 个第三层得出结论的发现（不含基线）里没有一个达到「划算」时，任务卡片和结果页顶部显示提示：

- 从这 30 个发现的结论里数原因：不符合需求的那一条、不建议的原因、触发的封顶、−5 的扣分项、基础分低于 70（「价格不比合理价低」），以及无法比价（「同配置样本不够，无法比价」）。按原因计数，取次数最多的 3 项。
- 文案：「最近 30 个商品都没到『划算』。最常见的原因：价格不比合理价低（18 件）、ID 锁没提到（9 件）、不符合『国行』（5 件）。」后面是「编辑需求清单」按钮。
- 只读已有数据，不调 AI、不访问闲鱼。改了需求清单、或出现「划算」及以上的商品后提示消失。用户点「知道了」后写 `tasks.no_good_hint_dismissed_at`，提示隐藏；之后有新的第三层结论（`analyzed_at` 晚于这个时间）、而最近 30 个仍然没有「划算」时再显示。

### 5.7 风险字典和品类模板

程序内置一份固定的风险字典（`DECISIONS.md` 第 51 条），AI 只能对任务清单里的检查项给结论；用户可以在任务里加自己写的检查项。字典随程序发布，界面上不能改，改动要发新版本。

态度：`reject` 不能接受 / `mind` 介意 / `ignore` 无所谓。

| 编号 | 名称 | 类 | 默认态度 | 只看有没有 | 判断说明（发给 AI 的 `hint`） | 问卖家的话 |
|---|---|---|---|---|---|---|
| fake | 假货高仿 | 真伪和来源 | reject |  | 高仿、复刻、A 货、仿品，或图文和正品明显不符 | 方便提供购买凭证或拍一下序列号吗？ |
| refurbished | 翻新 | 真伪和来源 | mind |  | 翻新机、外壳或屏幕翻新 | 这台有没有翻新过？ |
| official_replacement | 官换机 | 真伪和来源 | mind |  | 官换机、官方换新机、售后换新 | 是官换机吗？方便拍一下设置里的型号号码吗？ |
| resource_device | 资源机 | 真伪和来源 | mind |  | 资源机、海外有锁机解锁、美版有锁 | 是国行还是资源机？ |
| expanded | 扩容 | 真伪和来源 | reject |  | 扩容、改容量 | 存储有没有扩容过？ |
| assembled | 拼装 | 真伪和来源 | reject |  | 组装机、拼装机、配件拼凑 | 是整机原装还是组装的？ |
| unknown_source | 来路不明 | 真伪和来源 | mind |  | 无票无盒且说不清来源、公司发的、抵债来的 | 这台是怎么来的，有购买记录吗？ |
| id_lock | ID 锁 | 锁和账号 | reject |  | Apple ID、小米账号、华为账号没退出，或有激活锁 | 方便拍一下设置里账号已退出的截图吗？ |
| mdm | 监管机 | 锁和账号 | reject |  | 监管机、企业机、设备管理描述文件 | 是监管机或企业设备吗？设置里有没有设备管理？ |
| carrier_lock | 网络锁 | 锁和账号 | reject |  | 卡贴机、有锁机、只能用某家运营商 | 是无锁机吗？插任何卡都能用吗？ |
| account_bound | 账号绑定 | 锁和账号 | mind |  | 游戏账号、会员、序列号绑在卖家账号上，转手不能换绑 | 账号或授权能换绑到我名下吗？ |
| console_banned | 游戏机被封 | 锁和账号 | reject |  | 主机或账号被封、不能联网 | 主机能正常联网吗？有没有被封过？ |
| repaired | 维修或拆修 | 状态 | mind |  | 修过、拆过、换过主板或任何内部零件 | 有没有拆修过？ |
| water_damage | 进水 | 状态 | reject |  | 进水、受潮、进水标变色 | 有没有进过水？ |
| screen_replaced | 换屏 | 状态 | mind |  | 换过屏幕，原装屏或非原装屏 | 屏幕是原装的吗？ |
| battery_replaced | 换电池 | 状态 | mind |  | 换过电池 | 电池换过吗？现在健康度多少？ |
| cosmetic_damage | 磕碰划痕 | 状态 | mind |  | 磕碰、掉漆、明显划痕、裂痕 | 外观有没有磕碰？方便拍一下四个角吗？ |
| malfunction | 功能故障 | 状态 | reject |  | 有功能坏了：不开机、触摸失灵、摄像头、面容、指纹、按键、接口等 | 所有功能都正常吗？ |
| missing_parts | 缺配件 | 状态 | ignore |  | 缺原装充电器、线、盒子、说明书等 | 配件都有哪些？ |
| stains | 脏污 | 状态 | mind |  | 污渍、发黄、霉点 | 有没有污渍？方便拍一下细节吗？ |
| pilling | 起球 | 状态 | mind |  | 起球、磨损、勾丝 | 有没有起球或磨损？ |
| odor | 异味 | 状态 | mind |  | 烟味、霉味、宠物味、香水味 | 有没有异味？家里有没有人抽烟或养宠物？ |
| washed | 洗过 | 状态 | ignore |  | 洗过、干洗过 | 洗过吗？ |
| mold | 发霉 | 状态 | mind |  | 发霉、镜头起雾、霉斑 | 镜头有没有霉、雾、灰？ |
| mining | 矿卡 | 状态 | mind |  | 挖过矿、矿卡、长时间满载 | 显卡是自己玩游戏用的吗？挖过矿吗？ |
| notes_marks | 笔记划线 | 状态 | ignore |  | 书里有笔记、划线、签名 | 书里有笔记或划线吗？ |
| size_mismatch | 尺码不符 | 状态 | mind |  | 尺码和需求不符，或标的尺码和实际偏大偏小 | 实际尺码是多少？平铺量一下行吗？ |
| photo_mismatch | 图文不一致 | 描述可信度 | mind |  | 图里的东西和标题、描述对不上 | 图片是这台实物吗？ |
| stock_photo | 网图 | 描述可信度 | mind |  | 用官网图、网图、没有实物图 | 方便拍几张实物图吗？ |
| vague | 关键信息含糊 | 描述可信度 | mind |  | 描述很短、关键状态避而不谈、前后矛盾 | 方便详细说一下成色和使用情况吗？ |
| off_platform | 站外交易 | 交易方式 | reject | 是 | 要求加微信、QQ，或站外付款 | （不建议联系） |
| deposit | 先付定金 | 交易方式 | reject | 是 | 要求先付定金、押金、诚意金 | （不建议联系） |
| pickup_only | 只能自提 | 交易方式 | mind |  | 只能自提、只能面交 | 可以走闲鱼发货吗？ |
| no_return | 不退不换 | 交易方式 | ignore |  | 写明不退不换、售出不退 | 收到后和描述不符可以退吗？ |
| no_inspection | 拒绝验货宝 | 交易方式 | mind |  | 拒绝验货宝或官方验货 | 可以走验货宝吗？ |
| seller_dealer | 疑似商家 | 卖家 | mind |  | 由程序判断（5.8） | 是自己用的吗？用了多久？ |
| seller_new | 新号 | 卖家 | mind |  | 由程序判断（5.8） | — |
| seller_bad_reviews | 差评里有描述不符或售后差 | 卖家 | mind |  | 由程序判断（5.8） | — |
| seller_partial_refund | 被评到手刀 | 卖家 | mind |  | 由程序判断（5.8） | — |

**只看有没有**（`presence_only`）：标「是」的项，卖家正常交易时根本不会提，所以只看有没有出现，卖家没提到就当作没问题（5.4 第 1 条）：不封顶、不进「联系前问这几句」，界面上也不显示「没提到」。其他检查项的封顶规则不变。

**品类模板**：任务的 `category` 决定生成清单时带出哪些检查项（模板项），带出后用户可以增删。生成清单时 AI 可以改模板项的态度、额外挑字典里的其他项，不能删掉模板项（8.6）。每个品类都带「描述可信度」「交易方式」「卖家」三类的全部项，再加：

| 品类 | `category` | 额外带出 |
|---|---|---|
| 手机平板 | `phone_tablet` | fake、refurbished、official_replacement、resource_device、expanded、id_lock、mdm、carrier_lock、repaired、water_damage、screen_replaced、battery_replaced、cosmetic_damage、malfunction、missing_parts |
| 电脑 | `computer` | fake、refurbished、official_replacement、assembled、id_lock、mdm、repaired、water_damage、screen_replaced、battery_replaced、cosmetic_damage、malfunction、mining、missing_parts |
| 相机镜头 | `camera` | fake、refurbished、resource_device、repaired、water_damage、mold、cosmetic_damage、malfunction、missing_parts |
| 游戏机 | `console` | fake、refurbished、account_bound、console_banned、repaired、cosmetic_damage、malfunction、missing_parts |
| 耳机音箱和配件 | `audio_accessory` | fake、refurbished、repaired、battery_replaced、cosmetic_damage、malfunction、missing_parts |
| 家电 | `appliance` | refurbished、repaired、water_damage、cosmetic_damage、malfunction、missing_parts、stains |
| 服装鞋包 | `apparel` | fake、stains、pilling、odor、washed、size_mismatch、cosmetic_damage |
| 奢侈品 | `luxury` | fake、unknown_source、stains、cosmetic_damage、missing_parts、repaired |
| 图书 | `book` | stains、notes_marks、missing_parts |
| 其他 | `general` | fake、repaired、cosmetic_damage、malfunction、missing_parts |

品类由生成清单时 AI 判断（8.6），用户可以在表单里改，改了以后只替换没改过的模板项（2.6）；没配 AI 时用户自己选，默认 `general`。

**联系前问这几句**：结论为 `unknown` 或 `suspect`、态度不是「无所谓」的检查项（「只看有没有」的项没提到时不算，没有卖家资料时依赖卖家主页的卖家检查项不算），按态度（不能接受在前）列出字典里的话；用户自己写的检查项用「关于『xx』，方便说明一下吗？」。最多 5 句，一键复制，由用户自己去问。程序不向闲鱼发任何消息。

### 5.8 卖家画像

数据来自卖家主页缓存（`seller_profiles`，现有设计本来就会打开，`crawl.seller_cache_hours` 内复用，占详情额度）和详情里的注册天数、芝麻信用。

**程序直接算**（卖家检查项不发给 AI，5.3；AI 只从评价和简介里读出 `seller_signals`）：

- **新号**：注册不到 30 天 → `seller_new` 为 `risk`，否则为 `ok`。注册天数取不到或为 0 时当作不知道：`seller_new` 为 `unknown`，不判新号，不参与扣分和封顶，也不算进「骗子组合」。
- **信誉**：卖家信用文字、好评率，以及最近 10 条评价里作为卖家收到的差评数。展示时注明「只看了最近 10 条」。
- **卖家类型** `seller_type`：
  - `dealer_suspect` 疑似商家：最近 20 件里在售的、标题含任务关键词的有 3 件以上；或 AI 读出了 `dealer_words`。
  - `dealer` 职业卖家：**待确认，确认之前不判**。原来的条件是「已售 100 件以上，并且有上面任一疑似商家的迹象」，但现在只读卖家主页第一页的商品，已售数最多 20，这个条件永远达不到。卖家主页的总数字段（`tabs.item.number` 等，`XIANYU.md` 9.4）在实验里确认含义之前，最多只判到疑似商家。
  - `personal` 个人：有卖家资料，且不属于上面两种。
  - `unknown`：没有卖家资料。
  - 疑似商家（以后加上职业卖家）→ `seller_dealer` 为 `risk`；个人为 `ok`。
- **差评和到手刀**：AI 从评价文字读出、并通过原文核对的 `desc_mismatch`、`after_sale`、`slow_ship` → `seller_bad_reviews` 为 `risk`，没有就为 `ok`；`partial_refund` → `seller_partial_refund` 为 `risk`，没有就为 `ok`。

程序算出的 `risk` 算有证据（疑似商家、新号来自卖家主页和详情里的数字，差评和到手刀来自核对过的原文），态度为「介意」时 −5，「不能接受」时直接不建议（5.4）。

**信用不一票否决**：信用好的只加分（5.4 第 5 条）。只有用户在必须满足里明确写了（如「卖家信用极好」），才按硬条件判断（#438）。「信用极好」按信用等级字段（`seller_credit_level`，即 `attributes.level`）判断，信用文字只用来显示；等级的哪个值对应「极好」**待确认**，在实验里看（`XIANYU.md` 第 9.4 节）。

**骗子组合**：新号 + 低得反常 + 要求站外交易或先付定金，直接不建议（5.4 第 3 条）。

**没有卖家资料**（额度用完、主页打不开）：依赖卖家主页的检查项（疑似商家、差评、到手刀）都是 `unknown`，一律不参与扣分和封顶、不进「问这几句」；新号来自详情里的注册天数，照常判断。卖家部分不加不减，显示「没有卖家资料，请自己看一下卖家主页」。

**展示**：卡片一行，如「个人 · 来闲鱼 4 年 · 卖家信用极好 · 近 10 条评价无差评」；详情页列出依据（在售和已售数、同类在售几件、差评原文）和危险信号。

---

## 6. 调度

### 6.1 什么时候跑

任务每次运行结束后算下一次时间：

```
next_run_at = 结束时间 + interval_minutes × (1 + 随机[0, interval_jitter_percent%])
```

只加不减。结果落在 `crawl.quiet_hours` 里时，推到不运行时段的结束时间，再加随机 0–5 分钟。新建任务的 `next_run_at` 是创建时间。

一个任务真正能开始的时间，取下面各项里最晚的：

- `next_run_at`（手动运行不看这一项）；
- `failure_paused_until`（手动运行不看这一项）；
- 账号的 `last_run_ended_at + next_gap_seconds`；
- 账号冷却或拒绝访问的结束时间；
- 不运行时段的结束时间；
- 额度恢复的时间：每小时额度按滚动 60 分钟算，恢复时间 = 窗口里最早一次访问 + 60 分钟；每天额度在 `general.timezone` 的 0 点恢复。额度要够这个任务筛选完成前必需的访问次数（4.1）；
- AI 任务的当天用量到了 `ai.daily_token_limit` 时，`general.timezone` 的第二天 0 点（8.8）。

这些都是「靠时间能等到的」，调度器静默等待，不写运行记录（4.1）。额度只按 `quota_events` 计数，改时区、调系统时间、重启都不能凭空恢复额度。

### 6.2 调度器

- 一个常驻的调度循环：找出所有到了时间、通过前置检查的任务，按真正能开始的时间从早到晚排，手动请求排在最前面。前置检查里需要用户处理的，按 4.1 记一条跳过并把 `next_run_at` 推后一个间隔。
- 全局同时打开的页面数不超过 `crawl.global_pages`；同一个账号永远只开一个页面、同时只跑一个任务（`DECISIONS.md` 第 41 条）。下面这些也算一次运行，占页面名额，和这个账号上的任务互斥：扫码登录的会话、过验证的会话、运行中出现验证后保留着等用户处理的 Chrome（7.2）。前面的任务因为账号被占着而不能跑时，后面别的账号的任务可以先跑，一个任务的长暂停不拖住其他任务。
- 没有到期的任务时，睡到最早能开始的时间；任务增删改、手动运行、账号状态变化、验证完成时提前唤醒。
- 算睡多久时，把冷却结束、失败暂停结束、额度恢复的时间都算进去，保证一结束就恢复。
- 服务重启后，过期的任务按能开始的时间依次运行，同一账号之间照样遵守最小间隔，不会集中一起跑。

### 6.3 失败保护

- 计入失败的见 4.11。同一任务连续失败 `crawl.failure_threshold` 次，`failure_paused_until = 现在 + failure_pause_minutes`，推送「[任务名] 连续失败 N 次，已暂停 M 分钟」（M 按 `crawl.failure_pause_minutes` 的实际值填）。到时间自动恢复。
- 运行成功后计数清零。跳过和不计入的失败不改变计数。
- 普通失败和风控分开算：风控按账号处理（第 7 节），不进失败计数。

### 6.4 手动「立即运行」

- 排到队首，不等 `next_run_at`。另一个任务正在用这个账号或页面名额时，排队等它结束。
- 可以运行被用户停用或失败暂停的任务，但不改它的启用开关；手动运行成功后失败计数清零、失败暂停解除。
- 仍然遵守：账号状态、同账号最小间隔、不运行时段、每小时和每天的额度、冷却期内只能手动一次。被这些挡住时**不排队**：立刻告诉用户原因和预计能运行的时间（账号失效、需要验证这类要用户处理的，只说要做什么，不编时间），并记一条「已跳过」日志。
- 排队后、开始前，如果账号状态变了、额度用完了，把请求移出队列并记跳过，避免几小时后突然执行。
- 手动运行不消耗也不改变自动计划；运行结束后照常重算 `next_run_at`。

---

## 7. 闲鱼账号、代理与风控

### 7.1 账号分配

- 新建任务时，用户可以指定账号（`account_pinned = 1`），也可以自动分配：在 `active` 的账号里选当前分到任务最少的，一样多时选创建最早的。分配结果存进任务，之后每次运行都用它，不随机换。
- 没有任何可用账号时，任务照常保存，`account_id` 为空，运行时记跳过「没有可用的闲鱼账号」；有账号登录后自动分配。
- 账号**登录失效**时：自动分配的任务，改分到另一个 `active` 账号（同样按任务最少的选），并立刻排一次运行（仍遵守新账号的全部限制）；用户指定账号的任务不换，等这个账号重新登录。
- 遇到人机验证、直接拒绝**不换账号**：同一环境里换号重试没用（上游 #136）。
- 账号被**删除**时：用它的任务 `account_id` 变为空（`ON DELETE SET NULL`），`account_pinned` 改回 0，按自动分配重新选账号；没有可用账号就按上面「没有任何可用账号」处理。

### 7.2 账号状态

| 情况 | 账号状态 | 这个账号上的任务 | 推送 |
|---|---|---|---|
| 新加的账号还没扫码 | `signed_out` | 不运行 | — |
| 扫码登录成功 | `active` | 正常运行 | — |
| 需要人机验证 | `needs_verification`，记下 `verify_url` | 全部暂停 | 「[账号名] 闲鱼需要验证」 |
| 用户完成验证 | `cooling`，见下 | 冷却期内不自动运行，可以手动运行一次 | — |
| 直接拒绝（主框架网址或接口返回的网址含 `action=deny`） | `denied`，`cooldown_until = 现在 + deny_pause_hours` | 全部暂停，不能手动运行 | 「[账号名] 闲鱼拒绝访问，暂停 N 小时」（N 按 `crawl.deny_pause_hours` 的实际值填） |
| 页面出现「非法访问」「请使用正常浏览器访问」（环境被识别，运行记为 `risk_env`） | 按直接拒绝处理：`denied`，暂停 `deny_pause_hours` | 全部暂停，不能手动运行 | 同直接拒绝，正文另外提示检查 WebGL、沙盒和代理设置（13.5） |
| 冷却或拒绝期结束 | `active` | 恢复 | — |
| 登录失效 | `expired` | 按 7.1 处理 | 「[账号名] 闲鱼登录已失效」 |
| 用户退出登录 | `signed_out` | 同登录失效 | — |

**运行中出现验证**：

- 运行按 4.10 结束（第 1、2 步照做），但**不关这个 Chrome**：保留它等用户在远程画面里处理，最多 `crawl.verify_hold_minutes`（默认 30 分钟：给用户时间看到推送、打开网页处理，又不长期占着页面名额）。保留期间它占一个全局页面名额（6.2）。保留期内用户打开了远程画面，就从那时起按下面「过验证的会话」算 10 分钟。到时间没人处理就关掉。
- Chrome 已经关掉后，用户点「去验证」：用这个账号的同一个配置目录重开 Chrome，打开 `verify_url`（当时触发验证的网址），计一次访问：详情页和卖家主页的网址算详情类，其余算搜索类。页面上看不到验证框时（比如验证已经过期），在远程画面旁提示用户自己刷新一下页面。

**扫码登录和过验证的会话**都当成一次运行处理：同一账号上和任务互斥，占一个全局页面名额（6.2）；10 分钟内没完成就关掉浏览器；访问额度用完时也允许开始（这是用户主动操作），其中的访问照样计数。

**验证后的冷却**（逐次加倍）：

1. 触发验证时（账号转为 `needs_verification`）先算等级：账号还在冷却期内（`cooling`），或者距 `last_cooldown_end_at` 不到 `verify_cooldown_reset_hours` 小时，`verify_level` 加 1；否则（或第一次）`verify_level = 1`。还在冷却期内就被新的验证打断时，同时把 `last_cooldown_end_at` 写为现在。
2. 用户在远程画面里完成验证，点「我已完成验证」。服务端在**同一个浏览器**里重新检查一次风控信号，不另外打开页面。仍有验证就保持 `needs_verification`；发现登录失效就转为 `expired`。
3. 检查通过后算冷却时长 = `min(verify_cooldown_base_minutes × 2^(verify_level − 1), verify_cooldown_max_minutes)`。默认就是 2 小时、4 小时、8 小时……最长 24 小时。
4. `state = cooling`，`cooldown_until = 现在 + 冷却时长`，`cooldown_manual_used = 0`，清空 `verify_url`，关闭浏览器。冷却结束时写 `last_cooldown_end_at`。
5. 冷却期内用户手动运行一次后，`cooldown_manual_used = 1`，重启也不能重新获得这次机会。

账号状态变化都要推送实时事件（SSE），并唤醒调度器。

### 7.3 访问额度

- 每次访问闲鱼**之前**先申请：检查这个账号滚动 60 分钟内的访问数小于 `account_hourly_visits`，今天同类访问数小于对应的每日上限（`search` 对 `account_daily_search_visits`，`detail` 对 `account_daily_detail_visits`），都满足才写一条 `quota_events`，然后访问。先检查再计数。
- 访问失败也算一次，重试也算一次。
- 系统状态页显示每个账号今天和这一小时已用的额度。

### 7.4 代理

- 账号绑定的代理在这个账号的 Chrome 启动时生效，一直不变。没绑定就直连。带用户名密码的代理（HTTP 和 SOCKS5）Chrome 不能直接用，由服务端给这个账号起一个只监听 127.0.0.1 的本地转发代理，做法见 `CRAWL-POLICY.md` 第二节「代理」。
- 代理连不上算普通失败（`network`），计入失败保护，不换代理、不换账号。
- 「测试代理」只连代理服务器的 host:port（5 秒超时），不经过代理访问任何网站，不访问闲鱼。
- AI 专用代理（`ai.proxy_id`）只用于 AI 请求。地址写错时 AI 请求直接报错，不悄悄直连。

---

## 8. AI

### 8.1 请求

- 接口：`POST {base_url}/chat/completions`，请求头 `Authorization: Bearer <key>`（没填 Key 时不发这个头）、`Content-Type: application/json`。超时 `ai.request_timeout_seconds`。
- 所有用途共用一个信号量，同时最多 `ai.max_concurrent_requests` 个请求。兼容重试持有原来的名额。
- 回复文本取 `choices[0].message.content`；它是数组时把每段的 `text` 拼起来；为空时改取 `choices[0].message.reasoning_content`（参考上游 `src/services/ai_response_parser.py:28-42`）。再去掉闭合的 `<think>…</think>`。
- 每次请求写一条 `ai_calls`，token 数取返回的 `usage`，同时记下 `choices[0].finish_reason`。
- 实际发的 max_tokens 取下表的值和 `ai_compat.max_tokens_floor` 中较大的。

各用途的参数：

| 用途 | 模型 | temperature | max_tokens | 结构化输出 |
|---|---|---|---|---|
| 第二层 `title_screen` | text_model | 0.1 | 3000（一批 20 件） | 有，见 8.2 |
| 第三层 `analysis` | vision_model | 0.1，解析重试时 0.05 | 2000 | 有 |
| 生成需求清单 `profile` | text_model | 0.3 | 2000 | 有 |
| 行情解读 `market` | text_model | 不发 | 400 | 不发 |
| 试分析 `test` | 两步，见 8.7 | 同第二层、第三层 | 同第二层、第三层 | 同第二层、第三层 |
| 第二天自动再试 `probe`（2.9） | text_model | 不发 | 5 | 不发 |

### 8.2 结构化输出逐级降级

每个「服务商 + 模型」从 `ai_compat.json_mode` 记录的那一级开始，默认 `schema`：

1. `schema`：`"response_format": {"type": "json_schema", "json_schema": {"name": ..., "strict": true, "schema": ...}}`。
2. `object`：`"response_format": {"type": "json_object"}`。
3. `text`：不发 `response_format`，靠提示词要求 JSON，再宽松解析。

返回 400 或 422，并且返回内容提到 `response_format`、`json_schema`、`json_object` 或 `text.format`（上游 `src/services/ai_request_compat.py:13-19`、`78-91` 的判断条件），就降一级、立刻重发，并把新等级写进 `ai_compat`，以后直接从这一级开始。每次降级写一条运行日志和服务日志，写明从哪一级降到哪一级、服务商和模型。

**第二层的 JSON Schema**（`name: "title_screen"`）：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["items"],
  "properties": {
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["no", "kind", "specs", "brand_new", "verdict", "reason"],
        "properties": {
          "no": {"type": "integer"},
          "kind": {"type": "string", "enum": ["sale", "wanted", "accessory", "recycle", "rental", "repair", "box", "multi", "broken", "other"]},
          "specs": {"$ref": "#/$defs/specs"},
          "brand_new": {"type": ["boolean", "null"]},
          "verdict": {"type": "string", "enum": ["pass", "unmatched"]},
          "reason": {"type": "string"}
        }
      }
    }
  },
  "$defs": {
    "specs": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "value"],
        "properties": {"name": {"type": "string"}, "value": {"type": ["string", "null"]}}
      }
    }
  }
}
```

**第三层的 JSON Schema**（`name: "listing_facts"`）：严格模式要求所有字段都在 `required` 里，可以没有的写成可以为 `null`。

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["specs", "brand_new", "musts", "bonuses", "checks", "seller_signals", "price_note", "adjust", "summary"],
  "properties": {
    "specs": {"$ref": "#/$defs/specs"},
    "brand_new": {"type": ["boolean", "null"]},
    "musts": {"type": "array", "items": {"$ref": "#/$defs/req"}},
    "bonuses": {"type": "array", "items": {"$ref": "#/$defs/req"}},
    "checks": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["key", "status", "source", "evidence"],
        "properties": {
          "key": {"type": "string"},
          "status": {"type": "string", "enum": ["ok", "risk", "suspect", "unknown"]},
          "source": {"type": ["string", "null"], "enum": ["text", "image", null]},
          "evidence": {"type": ["string", "null"]}
        }
      }
    },
    "seller_signals": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["key", "evidence"],
        "properties": {
          "key": {"type": "string", "enum": ["desc_mismatch", "after_sale", "slow_ship", "partial_refund", "dealer_words"]},
          "evidence": {"type": "string"}
        }
      }
    },
    "price_note": {"type": ["string", "null"]},
    "adjust": {
      "type": "object",
      "additionalProperties": false,
      "required": ["value", "reason"],
      "properties": {"value": {"type": "integer"}, "reason": {"type": "string"}}
    },
    "summary": {"type": "string"}
  },
  "$defs": {
    "specs": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "value"],
        "properties": {"name": {"type": "string"}, "value": {"type": ["string", "null"]}}
      }
    },
    "req": {
      "type": "object",
      "additionalProperties": false,
      "required": ["id", "status", "source", "evidence"],
      "properties": {
        "id": {"type": "string"},
        "status": {"type": "string", "enum": ["met", "unmet", "unknown"]},
        "source": {"type": ["string", "null"], "enum": ["text", "image", null]},
        "evidence": {"type": ["string", "null"]}
      }
    }
  }
}
```

规格用 `[{name, value}]` 数组，不用以规格名为键的对象：严格模式不允许事先不知道的键名。必须满足和加分项按清单里的固定编号 `id`（如 `m1`、`b2`，2.6）回答，检查项按 `key`（字典编号或自写检查项的编号）回答。

**生成需求清单的 JSON Schema**（`name: "task_profile"`）：

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["summary", "category", "specs", "musts", "bonuses", "risks", "custom_checks", "allowed_kinds", "budget_min", "budget_max", "keyword", "exclude_words"],
  "properties": {
    "summary": {"type": "string"},
    "category": {"type": "string", "enum": ["phone_tablet", "computer", "camera", "console", "audio_accessory", "appliance", "apparel", "luxury", "book", "general"]},
    "specs": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "options", "values", "source"],
        "properties": {
          "name": {"type": "string"},
          "options": {"type": "array", "items": {"type": "string"}},
          "values": {"type": "array", "items": {"type": "string"}},
          "source": {"type": "string", "enum": ["user", "ai"]}
        }
      }
    },
    "musts": {"$ref": "#/$defs/items"},
    "bonuses": {"$ref": "#/$defs/items"},
    "risks": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "attitude", "source"],
        "properties": {
          "id": {"type": "string"},
          "attitude": {"type": "string", "enum": ["reject", "mind", "ignore"]},
          "source": {"type": "string", "enum": ["user", "ai"]}
        }
      }
    },
    "custom_checks": {"$ref": "#/$defs/items"},
    "allowed_kinds": {"type": "array", "items": {"type": "string"}},
    "budget_min": {"type": ["number", "null"]},
    "budget_max": {"type": ["number", "null"]},
    "keyword": {"type": ["string", "null"]},
    "exclude_words": {"type": "array", "items": {"type": "string"}}
  },
  "$defs": {
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["text", "source"],
        "properties": {"text": {"type": "string"}, "source": {"type": "string", "enum": ["user", "ai"]}}
      }
    }
  }
}
```

`musts` 也带 `source`：提示词要求只放用户说过的，但程序不完全相信 AI 标的 `user`，会在需求原文里再核对一遍（8.6）。编号（`m1`、`b2`、`c3`）不让 AI 给，由程序在合并时分配（2.6）。`custom_checks` 是用户提到、字典里没有的检查项，默认态度「介意」。

### 8.3 其他兼容处理

和结构化输出一样，命中时调整参数、立刻重发，不计入解析重试，调整结果写进 `ai_compat`：

- 返回内容提到 `temperature`，并带有 `not supported`、`unsupported`、`invalid` 或「参数错误」→ 以后不发 `temperature`（上游 `src/services/ai_request_compat.py:160-168`）。
- 返回 400，内容同时提到 `max_tokens` 和 `max_completion_tokens` → 以后改用 `max_completion_tokens`。
- **回复被截断**：`finish_reason = "length"`（思考模型常见：推理也算在输出上限里，上游 #499 里 DeepSeek 4000 个输出 token 有 3849 个花在思考上）。不当成解析失败，也不换服务商：
  1. `max_tokens_floor` 为空或小于 8000 → 设为 8000，立刻重发。
  2. 已经是 8000 还截断 → 这个服务商写 `error_state`「回复被截断：这个模型思考太长，不适合」，新建 `ai_error` 事件并推送，换下一个服务商。
  生成需求清单和 AI 行情解读（12.2）同样适用；生成需求清单「没闭合的 `<think>`」的报错（8.6）保留，作为没有 `finish_reason` 的服务商的兜底。第二天自动再试（`probe`，2.9）不适用：它只看返回是不是 2xx，不会因为截断把服务商标成「思考太长」。

不做 Responses API 回退：Kakatta 只支持 Chat Completions 接口，设置页写明这一点。

### 8.4 解析结果和核对证据

1. 去掉 ```` ```json ```` 包裹；整段解析不了时，从第一个 `{` 开始找第一个能完整解析的 JSON 对象（上游 `src/services/ai_response_parser.py:47-53`、`100-118`）。
2. **第三层必须满足**：`checks` 是数组，每项有 `key` 和合法的 `status`；`adjust` 有数字 `value`（整数、小数都接受，四舍五入取整）；`summary` 是字符串。不满足就算解析失败。其余字段缺失、为 `null` 或格式不对时只丢掉那一项：`specs` 丢掉当作都看不出，`musts`、`bonuses`、`checks` 缺的条目按 `unknown`，`seller_signals` 丢掉当作没有，`price_note` 丢掉当作没有。
3. **第二层必须满足**：`items` 是数组。单件缺字段或 `kind` 不认识时只丢掉那一件，按 5.2 当作通过。
4. **只认清单里有的**：`checks` 里不认识的 `key`（包括 `seller_*`，卖家检查项不发给 AI，5.3）、`musts` 和 `bonuses` 里清单没有的 `id`、`specs` 里清单没有的规格名，都丢掉并写一条日志。
5. **证据核对**（防编造）：`source = text` 的证据，以及 `seller_signals`、`price_note`，都必须能在原文里找到。原文：商品的标题加描述；卖家信号用评价文字和卖家简介。核对方法：两边都做 NFKC 规整、去掉全部空白和常见标点（`，。、！？；：,.!?;:"'“”‘’「」（）()`）后，证据是原文的子串。找不到时：
   - `risk` → `suspect`，`unmet` → `unknown`，`met`、`ok` → `unknown`；`suspect` 仍是 `suspect`，证据栏标「原文没找到」；
   - 卖家信号和 `price_note` 直接丢掉；
   - 在详情页显示「AI 引用的原文没找到」，写一条日志。
   AI 有时会把原文概括后再引用，所以证据是原文的一部分即可，太短（规整后不到 2 个字）的不算证据。
6. `source = image` 的证据不核对，在 `risk` 上标「看图判断」（5.4 第 1 条）。

### 8.5 出错重试和换服务商

一次判断（第二层或第三层）按下面的规则用服务商：

1. 候选：`enabled = 1`、`error_state` 为空的服务商，按 `sort_index` 从小到大。
2. 对当前服务商：
   - 兼容调整（8.2、8.3）后重发，不算重试。
   - 返回成功但解析失败：同一个服务商再试一次（第三层用 temperature 0.05）。还是失败就换下一个。第二层一批里所有发现共用这次判断的结果：整批失败时，批里每个 `stage = title` 的发现都按下面第 3、4 条处理。
   - 401、403、402、余额不足、404 模型不存在、模型不支持图片：写 `error_state`，新建 `ai_error` 系统事件并推送，换下一个。返回内容按下面的关键词识别（不区分大小写）：
     - 余额不足：不是 429，且含 `insufficient`、`balance`、`quota`、`余额`、`欠费` 之一（402 不看内容，直接算余额不足）；
     - 模型不支持图片：400，且含 `image`、`vision`、`image_url`、`multimodal`、`图片`、`图像` 之一。
   - 429 且返回内容提到 `quota`、`额度`、`exceeded today`、`insufficient`、`balance`：当天额度用完，写 `error_state`「当天额度用完」（第二天自动再试，2.9），新建 `ai_error` 事件并推送，换下一个。
   - 其他 429、5xx、网络错误、超时：换下一个，不在同一个服务商上立刻重试。
   - 今天的用量到了 `ai.daily_token_limit`（8.8）：不再发请求，按「没有可用的服务商」处理。
3. 失败全是服务商这边不能用（候选为空；或每个服务商的失败都写了 `error_state`；或到了每日上限）：发现保持 `pending`、`stage` 不变，`reason` 的代码是 `await_ai`，参数说明是「没有可用的 AI 服务商」还是「今天的 AI 用量已到上限」，`attempts` 不加。之后服务商恢复（试分析成功、用户保存、第二天自动再试成功）或第二天用量清零，下一次运行从补处理队列里接着处理。
4. 其他情况全部失败：发现记为 `failed`，`stage` 保持不变（第二层是 `title`，第三层是 `detail`，2.8），`reason` 写最后一个错误的说明（遮掉 Key）。只要有一个服务商的失败属于临时错误（429、5xx、网络、超时），`retryable = 1`；否则为 0。`attempts` 达到 3 后不再自动重试，界面显示「分析失败（已放弃）」，仍可手动重新分析。
5. 有任何一次请求成功，就解决这个服务商的 `ai_error` 事件。

生成需求清单、行情解读、试分析不换服务商：生成清单和行情解读用第一个可用的服务商，试分析只测被点的那个。

### 8.6 提示词原文

四份模板都可以在 Prompt 页编辑和还原（2.4）。每个 AI 任务的需求清单在任务表单里改，不在 Prompt 页。保存后下一次运行生效。

保存前检查：商品分析模板必须带着带引号的键名 `"checks"`、`"adjust"`、`"summary"`；标题整理模板必须带着 `"items"`、`"kind"`、`"verdict"`；需求清单模板必须带着 `"specs"`、`"risks"`。这些带引号的键名都原样出现在三份内置模板结尾的 JSON 输出格式示例里，所以内置模板稍改几个字照样能保存；缺了就不能保存，并说明缺哪个。分数的算法不在提示词里，改提示词不能改变打分规则。

**商品分析模板**（第三层的 system 消息）：

```
你是一名谨慎的二手交易顾问，帮用户核查闲鱼上的一件商品。你会收到用户的需求清单（JSON）、商品信息（JSON）以及若干商品图片。
你的工作是读懂商品、提取事实、给出证据，不需要打分：分数由程序根据你提取的事实计算。

规则：
1. specs：按需求清单里的规格名，从标题、描述和图片读出这件商品的值。和某个 options 意思相同时，照抄那个 options 的写法（例如「256GB」写成清单里的「256G」）。看不出来就填 null，不要猜。brand_new 表示是否全新或未拆封，看不出来填 null。
2. musts、bonuses：对清单里的每一条，按它的编号 id 给出 met（满足）、unmet（不满足）或 unknown（没提到、看不出来）。
3. checks：对清单里的每个检查项，按 key 给出结论：
   - ok：有明确证据说明没有这个问题，例如描述写「全原无拆修」；
   - risk：有直接证据说明存在这个问题；
   - suspect：只有间接迹象；
   - unknown：卖家没提到，图里也看不出来。
   每个检查项的 hint 说明了它指什么。
4. 证据：met、unmet、ok、risk、suspect 都要给证据。source 为 text 时，evidence 必须原样摘抄标题或描述里的一小段文字，不要改写；source 为 image 时，evidence 写「第 N 张图：看到了什么」。unknown 的 source 和 evidence 填 null。
5. 看图要谨慎：反光、贴膜、灰尘容易被误认成划痕或胶痕，拿不准就写 suspect。
6. seller_signals：只从卖家的评价文字和简介里找，evidence 原样摘抄：
   - desc_mismatch：作为卖家被评描述不符；
   - after_sale：作为卖家被评售后推诿；
   - slow_ship：作为卖家被评发货慢；
   - partial_refund：作为买家被评收货后压价、到手刀、恶意退款；
   - dealer_words：简介里有「工作室」「同行勿扰」「量大从优」这类商家用语。
   没有就给空数组。卖家信用、注册时长、在售数量由程序计算，你不用判断。
7. price_note：如果价格明显低于清单里的 fair_price，卖家有没有在描述里给出可信的原因（例如写明急出的原因），有就原样摘抄，没有填 null。
8. adjust：看完全部信息后的综合判断，-15 到 8 之间的整数，reason 用一句话写明依据。用来表达清单没覆盖到的情况，例如「+5：配件齐全、有购买发票」「-8：描述前后矛盾，先说自用后说帮朋友卖」。没有特别的就给 0。往下调可以大胆一些，往上调要谨慎。
9. summary：一句话结论，不超过 60 个字，说清这件商品最值得注意的地方。

只输出一个 JSON 对象，不要输出任何其他文字，格式如下：
{
  "specs": [{"name": "规格名", "value": "值或 null"}],
  "brand_new": false,
  "musts": [{"id": "m1", "status": "met 或 unmet 或 unknown", "source": "text 或 image 或 null", "evidence": "原文摘抄或 null"}],
  "bonuses": [{"id": "b2", "status": "met 或 unmet 或 unknown", "source": "text 或 image 或 null", "evidence": "原文摘抄或 null"}],
  "checks": [{"key": "检查项 key", "status": "ok 或 risk 或 suspect 或 unknown", "source": "text 或 image 或 null", "evidence": "原文摘抄或 null"}],
  "seller_signals": [{"key": "desc_mismatch", "evidence": "评价原文摘抄"}],
  "price_note": null,
  "adjust": {"value": 0, "reason": "一句话依据"},
  "summary": "一句话结论"
}
```

**标题整理模板**（第二层的 system 消息）：

```
你是一名二手交易顾问，帮用户整理闲鱼的搜索结果。你会收到用户的需求清单（JSON）和一批商品（JSON 数组）：只有标题、价格和标签，没有描述和图片。
对每件商品，按它的序号 no 做三件事：

1. kind：判断这条是什么。
   sale 正常出售这件东西；wanted 求购；accessory 配件或周边（壳、膜、充电器、包装以外的附属品）；recycle 回收或收购广告；rental 出租或租赁；repair 维修服务；box 空盒或包装；multi 一个链接挂多个款式或配置，价格只对应其中一款；broken 坏机、配件机、拆机件；other 其他和需求无关的。
   拿不准时判为 sale。
2. specs：按需求清单里的规格名，从标题读出这件商品的值。和某个 options 意思相同时照抄那个 options 的写法。标题看不出来就填 null，不要猜。brand_new 表示标题是否说明全新或未拆封，看不出来填 null。
3. verdict：只有标题已经明确违反 musts 中的某一条，或标题直接说明存在 reject_checks 中的某个问题（例如「有锁」「监管机」「卡贴」），才判为 unmatched，否则判为 pass。标题没提到的一律当作还不知道。不要因为价格偏低、标题简短或信息少就判为 unmatched。规格是否在用户选的范围里由程序判断，你不用管。

reason 用一句话说明判断依据，不超过 20 个字。
每件商品都要输出，不要漏掉。只输出一个 JSON 对象，不要输出任何其他文字，格式如下：
{"items": [{"no": 1, "kind": "sale", "specs": [{"name": "规格名", "value": "值或 null"}], "brand_new": null, "verdict": "pass", "reason": "一句话依据"}]}
```

**需求清单模板**（生成需求清单的 system 消息）：

```
你是一名二手交易顾问。用户会用大白话描述想在闲鱼上买什么，请把它整理成一份需求清单，之后程序会用它逐个核查商品、比价和打分。
你会收到用户的描述、可选的检查项字典（JSON），以及用户已经手动改过、必须原样保留的条目。

要求：
1. category：判断商品属于哪个品类。
2. specs：影响价格的关键规格，例如型号、存储、尺寸、版本，按对价格的影响从大到小排，最多 4 项。options 列出这项常见的取值，values 是用户可以接受的值：用户说「都行」「都可以」就都放进去，没说就留空数组表示都行。
3. musts：只放用户明确说过的硬性要求，并且不是规格。用户没说过的条件绝对不能放进 musts。
4. bonuses：让商品更值得买的特征，例如「电池 85% 以上」「在保」。
5. risks：从检查项字典里挑出和这类商品相关的项，attitude 用字典里的默认值；用户明确说不能接受的改成 reject，说无所谓的改成 ignore。custom_checks 放用户提到、但字典里没有的检查项。
6. source：用户说过的写 user，你补充的写 ai。
7. allowed_kinds：只有用户本来就想要配件、坏机、空盒这类东西时才填，否则给空数组。
8. budget_min、budget_max：用户说的大概预算，单位是元；「两三千左右」写成 2000 和 3000；没说填 null。
9. keyword：建议的闲鱼搜索词，简短、常用的叫法。exclude_words：建议的排除词，例如「壳 膜 求购 回收」拆成单个词。
10. summary：一句大白话总结，以「会帮你找：」开头，不超过 60 个字。
11. 已经被用户改过的条目必须原样保留，不要删除或改写。

只输出一个 JSON 对象，不要输出任何其他文字，格式如下：
{
  "summary": "会帮你找：一句话总结",
  "category": "品类，例如 phone_tablet",
  "specs": [{"name": "规格名", "options": ["常见取值"], "values": ["可以接受的值"], "source": "user 或 ai"}],
  "musts": [{"text": "用户说过的硬性要求", "source": "user"}],
  "bonuses": [{"text": "加分项", "source": "user 或 ai"}],
  "risks": [{"id": "检查项字典里的 id", "attitude": "reject 或 mind 或 ignore", "source": "user 或 ai"}],
  "custom_checks": [{"text": "字典里没有的检查项", "source": "user 或 ai"}],
  "allowed_kinds": [],
  "budget_min": null,
  "budget_max": null,
  "keyword": "建议的搜索词",
  "exclude_words": ["排除词"]
}
```

用户消息：

````
我想买：<keyword>
我的要求：<requirement>

## 检查项字典
```json
<字典里的 id、名称、类、默认态度>
```

## 必须保留的条目
```json
<当前清单里 edited = true 的条目，没有时为 []>
```
````

返回后去掉闭合的 `<think>…</think>`；还有没闭合的 `<think>`，或去掉后没有正文，就报「生成被思考过程截断，请换一个模型或重试」。界面可以取消生成，15 秒后显示「仍在生成…」。

生成结果的处理：

- **核对必须满足的来源**：AI 照常给每条 `musts` 标 `source`。程序再核一遍：每条 `source = user` 的必须满足，按 8.4 的方法规整（NFKC、去掉全部空白和常见标点）后，必须是需求原文（`requirement`）或任务搜索关键词（同样规整）的子串；找不到的改成 `source = ai`，按 2.6 移到加分项。AI 自己标 `ai` 的同样移到加分项。
- `risks` 里字典没有的 `id` 丢掉；`custom_checks` 转成自写检查项 `{"custom": 文字, "attitude": "mind", "source": ...}`。
- **检查项 = 品类模板带出的 + AI 额外挑的**：先按 `category` 的品类模板（5.7）带出模板项，默认态度来自字典；AI 的 `risks` 里有同一个 `id` 的，用 AI 给的态度和 `source`；AI 没提到的模板项照样保留（`source = ai`，态度用字典默认值），AI 不能删掉模板项；AI 挑的模板以外的项加在后面。
- **和当前清单合并**：`edited = true` 的条目保留原样；其余按新结果替换。所以改了需求文字重新生成，只会更新用户没动过的条目。新旧条目这样对应：规格按 `name`；检查项按字典 `id`，自写检查项按文字（规整后相同）；必须满足和加分项按文字（规整后相同）。对上的条目沿用原来的编号（2.6），新出现的条目分配新编号，没对上的旧条目（且 `edited = false`）删掉，它的编号不再使用。
- 搜索词、排除词、预算不直接改表单，在清单下方显示成建议，点一下才填进去。预算填进去时，表单的价格上限同时建议填成预算上限 × 1.2（取整到 10 元），避免卡掉贵一点但很值的商品。
- 编辑任务时改了「我的要求」，提示「需求已修改，是否重新生成清单？没改过的条目会更新，你改过的保留」，选项是「重新生成」「保持现在的清单」。
- 没配 AI 时不显示「生成」，清单由用户自己加条目、选品类。

**行情解读模板**（12.2）：

```
你是一名二手市场分析师。你会收到某个闲鱼搜索任务近 14 天按配置分组的价格统计（JSON）。
请用简体中文写一段不超过 120 字的行情解读：说明各配置的价格走势、最集中的价格带、配置之间差价是否合理，以及什么价位的商品值得优先联系。
只输出这段话，不要标题、列表、Markdown 或寒暄。
```

### 8.7 试分析

代替「测试连接」。上游的测试只让模型回一个 OK，真实分析带结构化输出、更长的输出和多张大图，所以 20 多条 issue 是「测试成功、实际全失败」（#200、#252、#287、#499）。

- **样例**：程序自带一件虚构的样例商品（标题、价格、描述、卖家资料），一份固定的需求清单，一张正常尺寸的商品照片（最长边 1024 像素的 JPEG，打包进程序）。
- **两步**，都按真实用途的参数发（8.1 的表、8.2 和 8.3 的兼容处理照常进行，结果写进 `ai_compat`，所以试分析之后第一次真实运行就不用再摸索）：
  1. `text_model` 按第二层发一次（标题整理模板，样例商品加 4 个虚构标题凑成一批）。`text_model` 为空时用 `vision_model`。
  2. `vision_model` 按第三层发一次（商品分析模板，带那张图）。
- **通过**：两步都返回 2xx、没有被截断、按 8.4 能解析出合法结果。
- **结果逐项显示**：
  - 文字模型：通过 / 失败及原因。
  - 看图：通过 / 「这个模型不能看图，只能用来看标题」。
  - 结构化输出：用的是 `schema`、`object` 还是 `text`。
  - 思考模型：`reasoning_tokens` 大于 0、有 `reasoning_content` 或回复里有 `<think>` 时显示「是」，并写这次思考用了多少 token。
  - 两步各用了多少 token、耗时多久。
- **失败时说人话**，原始返回放在可以展开的「详细信息」里（HTTP 状态码和返回内容的前 200 字，先遮掉 Key）：

  | 情况 | 显示 |
  |---|---|
  | 401、403 | API Key 不对或没有权限 |
  | 402、返回内容说余额不足（关键词见 8.5） | 余额不足 |
  | 404 | 地址或模型名不对：地址一般以 `/v1` 结尾，模型名要和服务商控制台里的一致 |
  | 400 且提到图片（关键词见 8.5） | 这个模型不能看图，只能用来看标题（填在「文字模型」里） |
  | 截断后调到 8000 还截断 | 这个模型思考太长，不适合 |
  | 解析失败 | 模型没按要求输出 JSON，建议换一个模型 |
  | 连接被拒绝，且地址的主机是 `localhost`、`127.0.0.1` 或 `::1` | 容器里的 localhost 指容器自己。本机的 Ollama 等服务请填宿主机的局域网 IP，或 `host.docker.internal` |
  | 其他网络错误、超时 | 连不上这个地址，检查地址和 AI 专用代理 |

- 成功时清空 `error_state`。试分析的请求记为 `purpose = test`，算进当天用量，但到了每日上限也允许试分析。
- **地址自动修正**：保存服务商时，`base_url` 去掉末尾的 `/`，再去掉末尾的 `/chat/completions`（上游 #214：用户把完整接口地址填进来），并在表单上提示「已去掉结尾的 /chat/completions」。

### 8.8 用量和每日上限

- **用量**：当天用量 = `general.timezone` 今天 0 点以来 `ai_calls` 的 `input_tokens + output_tokens` 之和，所有服务商和用途合计；没返回 `usage` 的请求按 0 算，并在 AI 设置页注明「部分服务商不返回用量，实际可能更多」。
- **显示**：AI 设置页显示今天、近 7 天、近 30 天的用量，按任务和用途（整理标题、看图分析、生成清单、行情解读、试分析）分开；概览的 AI 小卡显示合计（第 16 节）。
- **每日上限** `ai.daily_token_limit`（0 = 不限）：
  - 发每个 AI 请求之前检查，到了就不发（试分析除外）。请求已经发出去的照常完成，所以实际用量可能略超上限。
  - 到了上限：新建 `ai_budget` 系统事件并推送「今天的 AI 用量已到上限 N，AI 任务暂停到明天 0 点；关键词任务照常运行」；AI 任务在前置检查里静默等到第二天 0 点（4.1，靠时间能等到的，不记跳过），已经在跑的运行把剩下的商品记为「等待分析」（8.5）。
  - 第二天 0 点，或用户调高、取消上限时，解决 `ai_budget` 事件，AI 任务照常运行，等待分析的商品从补处理队列接着处理。

---

## 9. 关键词判断

参考上游 `src/keyword_rule_engine.py`，有三处不同（见下面标注）。

- **规整**：NFKC 统一全角半角（上游没有），转小写，连续空白合并成一个空格。
- **规则输入**：按换行、英文逗号、中文逗号拆分，去掉首尾空白，规整后去重。
- **匹配方式**：只由 `[a-z0-9 ]` 组成的词按完整单词匹配，避免 `q1` 命中 `q1r5`（上游 `keyword_rule_engine.py:61-69` 用的是 `(?<![a-z0-9])词(?![a-z0-9])`，Go 的正则不支持这种写法）：先找子串，再检查这一处前后各一个字符都不是 `[a-z0-9]`（在文本开头或结尾也算），每一处出现都检查，有一处满足就算命中。其他词按子串匹配。
- **匹配的文本**：第一层只有标题；打开详情后是标题加描述。不包括卖家的其他商品和评价，避免误命中（上游会把卖家信息也算进去，`keyword_rule_engine.py:37-46`）。
- **结论**（打开详情后的最终判断）：
  1. 命中任一排除词 → `rejected`，`reject_kind = excluded`，原因「命中排除词：xx」。排除词优先于匹配词。
  2. 否则命中任一匹配词 → `matched`，原因「命中 N 个关键词：a、b」。
  3. 没填匹配词、也没被排除 → `matched`，原因「没有排除词命中」（上游这种情况判为不推荐）。
- **最明显的垃圾**（第二层的规则版，不调 AI，`DECISIONS.md` 第 49 条）：在第一层之后、打开详情之前，按规整后的标题判断：以「求购」「收购」开头，或以「收」加一个量词（一、个、台、部、只、套、件、副、张）开头（如「收一台」「收个」），或含「高价收」→ `wanted`；含「回收」→ `recycle`；含「出租」或「租赁」→ `rental`。只看开头的「收」会把「收音机」「收纳柜」都当成求购，所以要带量词。命中的 `rejected`，`decided_layer = 2`，`reject_kind = junk`，不打开详情，`task_item_specs.kind` 记下类型，不算进行情。
- 结果卡片和详情显示命中了哪些词（`matched_keywords_json`）。

---

## 10. 价格历史与降价

参考上游 `src/services/price_history_service.py` 的价格解析（`parse_price_value`），但不照搬它按关键词存快照的方式。

- **追加价格点**：每次搜到商品，有价格并且和最近一个价格点不同（或还没有价格点）时追加。比较的是最近的价格点，不是可能已被别的任务更新过的商品快照。
- **降价标签**：新价格低于上一个价格点时，`last_drop_amount = 上一个价格 − 新价格`，商品打「降价」标签并显示降了多少。之后价格不变时保留标签；涨价时清空。
- **降价通知**（唯一规则）：同时满足下面几条才通知：
  - 发现的 `recommended_price` 不为空，即曾经达到推送条件（13.2，包括基线发现）；
  - 任务开了 `notify_price_drop`，推送档位不是 `none`；
  - 商品没被标为不感兴趣，卖家没被屏蔽；
  - 发现当前不是不建议（`grade = avoid`），也不是被拒绝（`decision = rejected`，比如重新分析后不符合需求）；
  - 当前价格低于基准价。基准价是 `last_drop_notified_price`，为空时取 `recommended_price`。

  发送成功后把 `last_drop_notified_price` 更新为当前价格，记 `drop_notified_at`。不要求这一轮一定新写了降价点：别的任务可能已经记下了同样的现价。

  例子：推荐时 ¥100 → ¥90 通知 → ¥110 不通知 → ¥95 不通知 → ¥85 通知。另一个例子：推荐时 ¥100 → ¥120 → ¥110 不通知（110 不低于基准 100）。

- **降价后升档**（只对 AI 任务）：`recommended_price` 为空、`decision = scored` 的发现，当前价格低于 `scored_price`（第一次算分时的价格），4.10 重算后达到推送条件（13.2）→ 按推荐推送一次，标题写「降价后 + 档位」（13.1），写 `recommended_price` 为当前价格。不要求这次运行新写了降价点：别的任务可能已经记下了同样的现价。之后再降价按上面的降价通知走。推送档位是 `none`、或任务没开 `notify_price_drop` 时不发，但照样写 `recommended_price`。基线发现也适用：降价是基线之后的新变化。
- **降价金额**：通知标题里的金额 = 基准价 − 当前价格。

---

## 11. 不感兴趣、屏蔽卖家、收藏、删除结果

- **不感兴趣**：标在商品上（`listings.is_dismissed`），对所有任务生效。之后不再发任何通知（包括降价），仍然记录价格；别的任务第一次搜到它时直接 `skipped`，不分析。从列表里隐藏，可以在「已隐藏」里撤销。
- **屏蔽卖家**：写进 `blocked_sellers`。之后所有任务都跳过这个卖家的商品：已知卖家 ID 的新商品在第一层之前就跳过；全新商品要打开详情拿到卖家 ID 后才能判断，命中就立刻停止，不打开卖家主页、不判断、不通知。已有的发现从列表隐藏。可以在设置里取消屏蔽。
- **收藏**：只在 Kakatta 里收藏（`listings.is_favorite`），不碰闲鱼的收藏。收藏的商品和它的发现不受保留期清理。删除任务会删掉任务的发现，收藏的商品本身保留在「收藏」里。
- **删除结果**：
  - 删除全部：删除这个任务的全部发现，收藏商品的发现保留；`seen_items` 不动，老商品不会再变成新商品。没被引用、没收藏的商品之后由清理删掉。
  - 单个删除：删掉这一条发现。商品被收藏的话收藏仍在。
- **批量操作**：结果列表可以多选（当前筛选结果「全选」最多 200 条），批量删除、重新分析、标为不感兴趣。
  - 批量重新分析按 5.5 逐个放回补处理队列，由之后的正常运行处理，不单独开运行。其中需要重新打开详情的（`detail_fetched_at` 超过 24 小时），确认框里写明「其中 N 个需要重新打开详情，会占用详情额度，在之后的运行里处理，可能要分几次运行才处理完」。
  - 结果页的「分析失败」筛选下有「重新分析全部失败的」，等于全选后批量重新分析。
- **新加的排除词补判已有结果**：保存任务时排除词有新增，统计这个任务现有的发现里，商品标题（打开过详情的加上描述）命中新增排除词、没收藏、结论不是 `rejected` 的有几条。有的话在保存后提示「已有 N 条结果命中新的排除词，要不要一起隐藏？」，选项「隐藏」「保留」：
  - 隐藏：这些发现改为 `rejected`，`decided_layer = 1`，`reject_kind = excluded`，原因「命中排除词：xx（后来添加）」，从默认列表里隐藏，可以在「未推荐」筛选里看到。记录保留，`seen_items` 不动，不会被当成新商品重新分析；已经发过的通知不撤回。
  - 保留：什么都不改，只对之后的新商品生效。
- **已读**：打开详情就标为已读，可以手动标为未读。基线发现创建时就是已读。

---

## 12. 市场行情

### 12.1 合理价和统计口径

按配置分组比价（`DECISIONS.md` 第 50 条）。只读数据库，不调 AI、不访问闲鱼；每次运行结束（4.10）和第三层分析前（要发给 AI，5.3）各算一次，同一次运行里复用。

**样本**：本任务 `task_sightings` 近 14 天的记录，连上 `task_item_specs` 拿到配置。去掉：

- 第二层认成垃圾的（`kind` 不是 `sale`，也不在清单的 `allowed_kinds` 里）；
- 标题命中任务**当前**排除词的（按 `listings.title` 现算，改了排除词立刻生效，上游 #479）；
- 标为不感兴趣的；
- 规格不明的（`config_key` 为空）；
- `task_item_specs.excluded = 1` 的（第二层判为不符合需求，或第三层有文字证据地不满足某条必须满足，2.7）。

第一层因价格、包邮等没通过的照样算进来：它们是同配置的真实挂牌价。规格超出勾选范围的也照样算（相邻配置的差价要用）。

**合理价**：对每个「配置 + 全新或二手」组（`brand_new = 1` 是全新组，其余是二手组）：

1. 每件商品只算一次，取它在窗口内最近一天的价格。
2. 同一卖家在同一组里挂了多件只算一件，取最低价（免得商家一次挂十几件同款把中位数拉向他的定价）。还不知道卖家的商品各算各的。
3. 样本不少于 4 个时去掉离群价格：低于 P25 − 1.5×(P75 − P25) 或高于 P75 + 1.5×(P75 − P25)。分位数用线性插值。
4. 剩下不少于 5 个：合理价 = 中位数，依据「同配置 · 近 14 天」。
5. 不够时逐级放宽：
   1. 窗口放宽到 30 天，依据「同配置 · 近 30 天」；
   2. 还不够就去掉清单里最后一项规格（最次要的），在 30 天窗口里按剩下的规格分组，依据「放宽到 30 天 · 不分网络」（写出去掉的规格名）；清单只有一项规格时没有这一步；
   3. 还不够：无法比价，显示「无法比价（同配置只有 N 个）」。
6. 不拿预算顶替合理价：预算是用户愿意出多少，不是市场价。

页面上写清依据，如「合理价 ¥1,900（同配置在售 23 个，近 14 天，挂牌价，实际成交通常更低）」，点开是这些商品的列表（详情页的「同配置对比表」，按价格排）。

**各配置行情一览**（结果页顶部）：清单勾选范围内、有样本的每个组一行：

| 列 | 算法 |
|---|---|
| 配置 | 如「iPad mini 6 / 256G」，全新组后面加「全新」 |
| 合理价 | 上面的结果和依据；无法比价时写样本数 |
| 价格带 | 当前窗口样本的 P25–P75 |
| 现在最便宜 | 最近一次运行里还搜得到、不是垃圾的最低价那件，可以点开 |
| 较 14 天前 | 窗口内最早有样本那天和最近有样本那天的中位数相比，两天都不少于 3 个样本时才显示 |

**相邻配置差价**：详情页对只差一项规格的相邻配置给一句「升级划算度」：「比 256G 的合理价只多 ¥100，平时要多 ¥600」。前者是这件的价格减去低一档配置的合理价，后者是两个配置合理价之差；任一个无法比价时不显示。低一档 = 这项规格在 `options` 里排在前面一个的值。

**其他统计**：

| 指标 | 算法 |
|---|---|
| 每日走势 | 每个配置每天的中位数，样本按上面的规则去重去离群 |
| 今日新增、今日推荐 | 今天创建的发现数、其中推荐的数（推荐的口径见第 1 节） |
| 个人卖家占比 | 近 14 天有 `seller_type` 的发现里 `personal` 的比例 |
| 快照时间 | 最近一次 sighting 的时间 |

**关键词任务**没有需求清单，不分配置：全部样本（去掉规则认出的求购、回收、租赁和上面另外三种）算一组，只显示价格带、中位数和每日走势，不用于打分。

概览「任务表现」里的近 7 天走势用样本最多的那一组的最近 7 个自然日的中位数，没有样本的日子不拿更早的数据凑。

### 12.2 AI 行情解读

一次运行**成功结束**后，同时满足下面几条就在后台生成一次：

- 有可用的 AI 服务商；
- 今天的样本数不少于 10；
- 距离这个任务上次生成至少 6 小时。

请求：text_model，system 是行情解读模板，user 是 12.1 各配置行情一览和每日走势的 JSON，不带图片。回复被截断时按 8.3 处理（调大输出上限重发，还截断就标「思考太长」）。成功写进 `market_insights`；失败只写日志，旧的解读保留。没配置 AI 时结果页不显示这一块。

---

## 13. 通知

### 13.1 通知内容

| 字段 | 内容 |
|---|---|
| title | 见下面的表 |
| item_title | 商品标题前 40 字 |
| body | 见下面的表 |
| desktop_url | 网页版链接 |
| mobile_url | `https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=` 加上 URL 编码后的 `{"id":<商品 ID>}`（上游 `src/utils.py:105-114`） |
| link | 按 `notify.link_target` 选 `mobile_url` 或 `desktop_url`；选中的为空时用另一个 |
| image_url | 商品主图 |
| kakatta_url | `general.public_url` 不为空时，指向 Kakatta 里这条发现的地址 |
| urgent | 捡漏为真，其余为假（13.6 按渠道换成紧急级别） |

| 类型 | title | body |
|---|---|---|
| AI 任务的推荐 | `[任务名] 捡漏 91 · mini 6 256G ¥1,000` | `比同配置低 23% · 个人信用极好 · 1 项待确认（ID 锁）`，换行后是 AI 的一句话结论 |
| AI 任务、无法比价 | `[任务名] 无法比价 · mini 6 256G ¥1,000` | `同配置只有 3 个，没法比价 · ……`，同上 |
| 降价后升档（第 10 节） | `[任务名] 降价后划算 84 · mini 6 256G ¥1,150` | `从 ¥1,300 降到 ¥1,150 · 比同配置低 15% · ……`，同上 |
| 关键词任务的推荐 | `[任务名] 发现推荐 ¥价格` | 关键词命中说明 |
| `all_new` 下的其他新商品（`notify_kind = new`） | `[任务名] 新商品 ¥价格` | 档位和分数（有的话）· 一句话原因；不符合需求的开头标「不符合需求」 |
| 降价 | `[任务名] 降价 ¥xx` | `¥原价 → ¥现价` · 档位和分数（有的话） |

- 标题里的规格是 `config_key` 去掉分隔符的简写，规格不明时省略；超过渠道的标题长度时先截规格。
- body 的第一行按顺序拼：比合理价低或高多少；卖家一句（个人信用极好 / 疑似商家 / 新号 / 没有卖家资料）；待确认项的数量和前两项名称；超出预算时不会推送，所以不写预算。

价格显示成 `¥6,850`，整数不显示小数。

### 13.2 发哪些

**AI 任务**按推送档位（`notify_policy`）：

| 档位 | 推送 | 门槛分 T |
|---|---|---|
| `steal` 只推捡漏 | 分数 ≥ 90 | 90 |
| `good` 划算及以上（默认） | 分数 ≥ 80，以及无法比价的 | 80 |
| `fair` 价格合理及以上 | 分数 ≥ 65，以及无法比价的 | 65 |
| `all_new` 所有新商品 | 达到 80 的按推荐发；其余判断完的新发现按「新商品」发，但下面这些不发：第一层没通过的（价格、包邮、发布时间、地区、排除词，都是用户自己定的条件）、第二层认成垃圾的、不建议、超出预算、不感兴趣、屏蔽卖家、分析失败。不符合需求的照发，标「不符合需求」 | 80 |
| `none` 不推送 | 不发，降价也不发 | 80 |

一个发现**达到推送条件**，要同时满足：

- `decision = scored`，分数 ≥ T；或分数为空、`grade` 为空（无法比价、没有不建议），且档位是 `good`、`fair` 或 `all_new`；
- 不是不建议（不能接受的风险有文字证据就是不建议，5.4 第 3 条）；
- 设置了预算上限时，价格不超过 `budget_max`；超出的记为「超出预算」不推送，结果里照常显示并标「超出预算」；
- 商品没被标为不感兴趣，卖家没被屏蔽。

**推送只判定一次**：一个发现第一次得出结论后，在那次运行 4.10 第 1 步重算之后判定一次（重新分析的见 5.5）：

- 达到推送条件 → `notify_kind = recommended`，写 `recommended_price` 为当前价格；
- 没达到、但 `all_new` 下要按「新商品」发 → `notify_kind = new`；
- 都不是 → 写 `notify_skip_reason`（13.7）。基线发现、档位是 `none`、任务没勾渠道的，达到推送条件时仍写 `recommended_price`，只是 `notify_kind` 为空、写 `notify_skip_reason`。

之后行情变化让分数怎么升降，都不再判定，也不清空 `recommended_price`；降价和降价后升档按第 10 节另算。用户改需求清单、改规格、改检查项结论引起的重算也不再判定。

**关键词任务**：`recommended` 通知命中的（`decision = matched`，命中时写 `recommended_price`）；`all_new` 命中的按推荐发，其余判断完的新发现（主要是没命中匹配词的）按「新商品」发，但第一层没通过的（价格、包邮、发布时间、地区、排除词，都是用户自己定的条件）和规则认出的求购、回收、租赁（第 9 节）不发；`none` 不通知，降价也不通知。

**两种任务都一样**：

- 基线发现永远不发「发现推荐」和「新商品」通知，即使它们因为额度不足留到之后的运行才分析；但基线发现达到推送条件后会写 `recommended_price`，之后的降价和降价后升档照常通知。
- 「分析失败」和 `skipped` 不通知，也不做推送判定（它们没有得出结论）。
- 捡漏按紧急发送（13.6）。

### 13.3 合并发送

- 同一个任务一次运行最多发 `notify.max_per_run` 条（默认 5），这是总数，含合并的那一条：超出时单独发 4 条，再加 1 条合并消息。
- 排序：推荐和降价后升档在前，AI 任务按分数从高到低（无法比价的排在有分数的后面），关键词任务按发布时间从新到旧；然后是降价，按降幅从大到小；最后是 `all_new` 的其他新商品。
- 超出时发前 `max_per_run − 1` 条，再加一条合并消息，合计不超过上限：标题 `[任务名] 还有 N 个推荐`（被合并的里面有降价或其他新商品时写「还有 N 条提醒」），正文列出被合并商品的档位、分数和标题（每个一行，最多 10 行），链接用 `kakatta_url` 指向这个任务的结果页，没设置 `public_url` 时用被合并的第一个商品的链接。合并消息发送成功后，被合并的发现都记为已通知。被合并的里面有捡漏时，合并消息也按紧急发送。

### 13.4 发送规则

- 发到任务勾选的渠道（`task_channels`）里启用的那些。
- 渠道之间同时发，同一渠道内按顺序发。企业微信和钉钉每个机器人每分钟最多 20 条，所以同一渠道两条之间至少隔 3 秒；飞书至少隔 1 秒。这个间隔按渠道全局控制：几次运行（几个任务）同时往同一个渠道发，也排在同一个队列里，不能突破。
- 任一渠道发送成功就算已通知，写 `notified_at`；其他渠道失败的不再补发。只有全部渠道都失败时才保持未通知、下次运行再发，并把 `notify_failures` 加 1。重发有上限：全部失败已经 3 次，或者离第一次全部失败（`delivery_items` 里这个发现最早的一次发送）已经超过 24 小时，就不再发，写 `notify_skip_reason`「发送失败」。每次发送写一条 `deliveries`，消息里包含的每个发现写一条 `delivery_items`（合并消息包含被合并的全部发现）。
- 失败只记日志，不影响其他渠道和运行结果。错误信息只写固定格式、带渠道名的说明，不写含 Token 的地址、请求头或响应正文。
- 先保存已经成功的送达标记，再处理取消，避免部分成功后重复推送。
- 「发送测试」用固定的示例消息，标题「Kakatta 测试通知」，带一张示例商品图地址和一个闲鱼首页链接。

### 13.5 系统事件推送

服务端在后台运行，没人看着，所以这些事要推送出去。发到 `notify.system_channel_ids`（空 = 所有启用的渠道），同一件事没解决之前只推一次（2.11 `system_events`）。

| 事件 | 标题 | 正文 |
|---|---|---|
| 需要人机验证 | `[账号名] 闲鱼需要验证` | `这个账号上的 N 个任务已暂停。请打开 Kakatta，在远程画面里完成验证。`（浏览器还保留着时加一句「浏览器会保留到 HH:mm」，7.2） |
| 直接拒绝访问 | `[账号名] 闲鱼拒绝访问` | `这个账号暂停 N 小时，到 HH:mm 自动恢复。`（N 按 `crawl.deny_pause_hours` 的实际值填）。因为页面出现「非法访问」「请使用正常浏览器访问」而拒绝的（7.2），再加一句：`闲鱼可能认出了浏览器环境，请检查 WebGL、Chrome 沙盒和代理设置。` |
| 登录失效 | `[账号名] 闲鱼登录已失效` | `请打开 Kakatta 重新扫码登录。`（自动分配的任务已换到别的账号时加一句「N 个任务已换到 xx」） |
| AI 接口出错 | `AI 接口出错：服务商名` | 错误说明，如「API Key 无效」「余额不足」「当天额度用完」「这个模型思考太长」 |
| AI 用量到上限 | `今天的 AI 用量已到上限` | `已用 N token，AI 任务暂停到明天 0 点；关键词任务照常运行。` |
| 任务连续失败暂停 | `[任务名] 连续失败 N 次` | `已暂停 M 分钟，最近一次的原因：xx`（M 按 `crawl.failure_pause_minutes` 的实际值填） |
| 闲鱼页面结构可能变了（`xianyu_layout`，2.11） | `闲鱼页面结构可能变了` | `最近的运行读不出商品（原因：xx）。可能是闲鱼改版了，不是你的配置问题；请留意 Kakatta 的更新。` |
| 登录失败次数过多（`login_failures`，2.3） | `Kakatta 登录失败次数过多` | `登录失败已达到上限 N 次，最近一次来自 IP xx。如果不是你本人，请改一个更强的密码并开启两步验证。` |

系统事件没有商品图，链接是 `kakatta_url`（账号页、AI 设置页或任务的日志），没设置 `public_url` 时不带链接。

### 13.6 各渠道的请求格式

下文「正文」指 `item_title + 换行 + body`。超时除注明的外都是 10 秒。

**紧急级别**：`urgent` 为真的消息（捡漏，13.1）和系统事件（13.5，都要用户去处理）按下面各渠道写的紧急级别发，其余按普通级别发，避免每条都打扰用户（`DECISIONS.md` 第 55 条）。没写级别的渠道两者一样。

**Bark**（照上游 `notification_clients/bark_client.py`；官方文档：<https://bark.day.app/#/tutorial>）

- 配置：推送地址，如 `https://api.day.app/<key>`（加密保存）。
- 请求：`POST` 到推送地址，`Content-Type: application/json; charset=utf-8`：

  ```json
  {"title": "<title>", "body": "<正文>", "url": "<link>", "group": "Kakatta", "icon": "<image_url>", "level": "active"}
  ```

  没有图片时不带 `icon`。紧急时 `level` 为 `timeSensitive`（上游一律用它）。
- 成功：2xx，并且返回里有 `code` 字段时 `code == 200`。

**Telegram**（参考上游 `notification_clients/telegram_client.py`；官方文档：<https://core.telegram.org/bots/api#sendmessage>、<https://core.telegram.org/bots/api#sendphoto>）

- 配置：Bot Token（加密）、Chat ID、API 地址（默认 `https://api.telegram.org`，可以填自建的转发地址）。
- 内容（HTML）：

  ```
  <b>{title}</b>
  <b>{item_title}</b>
  {body}
  <a href="{link}">在闲鱼打开</a>
  ```

  所有插入的文字都要做 HTML 转义（`&`、`<`、`>`，链接里再转义 `"`）。上游没有转义，标题里有 `<` 时会发送失败。
- 有图片：`POST {api}/bot{token}/sendPhoto`，`{"chat_id", "photo": "<image_url>", "caption": "<内容>", "parse_mode": "HTML"}`。`item_title` 和其他渠道一样是商品标题前 40 字（13.1）。caption 最多 1024 字，按 UTF-16 长度保守计算，超出时只截短商品标题，标签、价格、理由和链接保留完整。
- 没有图片：`POST {api}/bot{token}/sendMessage`，`{"chat_id", "text": "<内容>", "parse_mode": "HTML"}`，最多 4096 字。
- 成功：返回的 `ok == true`。

**ntfy**（参考上游 `notification_clients/ntfy_client.py`；官方文档：<https://docs.ntfy.sh/publish/>）

- 配置：Topic 地址（如 `https://ntfy.sh/<topic>`，加密）、访问令牌（可选，加密）。
- 请求：`POST` 到 Topic 地址，请求体是纯文本正文（UTF-8），请求头：
  - `Title: <title>`：含中文时按 RFC 2047 编成 `=?UTF-8?B?…?=`，每段不超过 75 字符、不拆开一个字。官方文档说明 ntfy 支持这种编码；上游直接把 UTF-8 字节放进请求头，有的 HTTP 库会把它变成问号。
  - `Priority: default`，紧急时 `urgent`（上游一律 `urgent`）、`Tags: bell`、`Click: <link>`；有图片时 `Attach: <image_url>`；填了访问令牌时 `Authorization: Bearer <token>`。
- 成功：2xx。

**Gotify**（参考上游 `notification_clients/gotify_client.py`；官方文档：<https://gotify.net/docs/pushmsg>、<https://gotify.net/docs/msgextras>）

- 配置：服务器地址、App Token（加密）。
- 请求：`POST {url}/message`，请求头 `X-Gotify-Key: <token>`，JSON：

  ```json
  {
    "title": "<title>",
    "message": "<正文>\n<link>",
    "priority": 5,
    "extras": {"client::notification": {"click": {"url": "<link>"}, "bigImageUrl": "<image_url>"}}
  }
  ```

  没有图片时不带 `bigImageUrl`。紧急时 `priority` 为 8。和上游不同：Token 放请求头，不放 `?token=`，避免出现在访问日志和代理记录里；请求体用 JSON，不用表单。
- 成功：2xx。

**企业微信群机器人**（上游 `notification_clients/wecom_bot_client.py` 用 markdown；官方文档：<https://developer.work.weixin.qq.com/document/path/91770>）

- 配置：Webhook 地址（加密）。
- 请求：`POST` 到 Webhook 地址，用图文消息，能带商品图：

  ```json
  {"msgtype": "news", "news": {"articles": [{"title": "<title> · <item_title>", "description": "<body>", "url": "<link>", "picurl": "<image_url>"}]}}
  ```

  `title` 最多 128 字节、`description` 最多 512 字节，超出按 UTF-8 字符边界截断；没有图片时不带 `picurl`。系统事件没有链接时用 `{"msgtype": "text", "text": {"content": "<title>\n<正文>"}}`（最多 2048 字节）。
- 成功：`errcode == 0`。每个机器人每分钟最多 20 条。

**钉钉群自定义机器人**（上游没有；官方文档：<https://open.dingtalk.com/document/orgapp/custom-bot-to-send-group-chat-messages>、加签：<https://open.dingtalk.com/document/robots/customize-robot-security-settings>）

- 配置：Webhook 地址（`https://oapi.dingtalk.com/robot/send?access_token=...`，加密）、加签密钥（`SEC` 开头，可选，加密）。机器人安全设置选「自定义关键词」时，用户要把关键词设成「Kakatta」，我们的标题里不一定有这个词，所以每条消息正文末尾固定加一行 `来自 Kakatta`。
- 加签：`timestamp` 为当前毫秒时间戳；`sign = URL编码(Base64(HmacSHA256(key = 密钥, 内容 = timestamp + "\n" + 密钥)))`；在 Webhook 地址后加 `&timestamp=...&sign=...`。时间戳和钉钉服务器相差不能超过 1 小时。
- 请求：`POST`，`Content-Type: application/json`：

  ```json
  {"msgtype": "markdown", "markdown": {"title": "<title>", "text": "#### <title>\n\n**<item_title>**\n\n<body>\n\n![](<image_url>)\n\n[在闲鱼打开](<link>)\n\n来自 Kakatta"}}
  ```

  没有图片时去掉图片那一行，没有链接时去掉链接那一行。插入的文字去掉换行符。
- 成功：`errcode == 0`。每个机器人每分钟最多 20 条，超出会被限流 10 分钟。
- 注意：钉钉 2023 年起逐步限制新建自定义机器人，已有的机器人仍可使用；设置页说明这一点。

**飞书群自定义机器人**（上游没有；官方文档：<https://open.feishu.cn/document/client-docs/bot-v3/add-custom-bot>）

- 配置：Webhook 地址（`https://open.feishu.cn/open-apis/bot/v2/hook/...`，加密）、签名密钥（可选，加密）。
- 签名：`timestamp` 为当前秒级时间戳（字符串）；`sign = Base64(HmacSHA256(key = timestamp + "\n" + 密钥, 内容 = 空字符串))`。`timestamp` 和 `sign` 放在请求体的最外层。和当前时间相差不能超过 1 小时。
- 请求：`POST`，JSON 富文本消息：

  ```json
  {
    "timestamp": "1599360473",
    "sign": "<sign>",
    "msg_type": "post",
    "content": {"post": {"zh_cn": {
      "title": "<title>",
      "content": [
        [{"tag": "text", "text": "<item_title>"}],
        [{"tag": "text", "text": "<body>"}],
        [{"tag": "a", "text": "在闲鱼打开", "href": "<link>"}]
      ]
    }}}
  }
  ```

  没配签名密钥时不带 `timestamp` 和 `sign`。飞书的图片要先用应用凭证上传换成 `image_key`，自定义机器人做不到，所以不附图。请求体不超过 20KB。
- 成功：返回的 `code == 0`。每个机器人每分钟最多 100 条、每秒最多 5 条。

**PushPlus**（参考上游 `notification_clients/pushplus_client.py`；官方文档：<https://www.pushplus.plus/doc/guide/api.html>）

- 配置：Token（加密）、群组编码 topic（可选）。
- 请求：`POST https://www.pushplus.plus/send`，JSON：

  ```json
  {"token": "<token>", "title": "<title>", "content": "<HTML 内容>", "template": "html", "topic": "<topic>"}
  ```

  没填 topic 时不带这个字段。内容是 HTML：`<b>item_title</b><br>body<br><img src="image_url" style="max-width:100%"><br><a href="link">在闲鱼打开</a>`，所有插入的文字做 HTML 转义，换行用 `<br>`。上游用 `txt` 模板（因为 html 模板会吃掉换行），我们用 `html` 是为了带商品图，换行显式写成 `<br>`。
- 成功：2xx 且返回的 `code == 200`。PushPlus 在 Token 无效或额度用完时 HTTP 仍返回 200，失败写在 `code` 里（上游 `pushplus_client.py:60-63`）。`code == 200` 只表示收到了，实际送达是异步的。

**Server 酱**（上游没有；参考官方 SDK <https://github.com/easychen/serverchan-sdk-golang> 的 `serverchan.go`，同时支持 Server 酱 Turbo 和 Server 酱³）

- 配置：SendKey（加密）。
- 地址按 SendKey 选：以 `sctp` 开头时，用正则 `^sctp(\d+)t` 取出数字 N，地址是 `https://N.push.ft07.com/send/<sendkey>.send`（Server 酱³），取不出数字就在保存时报「SendKey 格式不对」；其他的是 `https://sctapi.ftqq.com/<sendkey>.send`（Turbo）。
- 请求：`POST`，`Content-Type: application/json;charset=utf-8`：

  ```json
  {"title": "<title>", "desp": "<Markdown 内容>", "short": "<item_title>"}
  ```

  `title` 超过 32 个字时截短（Turbo 的标题上限）。`desp` 是 Markdown：`**item_title**`、空行、`body`、空行、`![](image_url)`（图片地址是 https 时才放）、空行、`[在闲鱼打开](link)`；插入的文字里的 Markdown 符号转义。系统事件不带 `short` 和图片。
- 成功：2xx 且返回的 `code == 0`。

**Webhook**（照上游 `notification_clients/webhook_client.py`）

- 配置：地址（加密）、方法 `GET` / `POST`、请求头（JSON 对象，加密）、查询参数（JSON 对象）、内容类型 `JSON` / `FORM`、请求体模板（JSON）。
- 占位符写成 `{{name}}`，也认上游的 `${name}`（`webhook_client.py:145-146`），方便从上游迁移：

  | 占位符 | 内容 |
  |---|---|
  | `title` | 通知标题 |
  | `content` | 商品标题、`¥价格 · 理由`、链接，各一行 |
  | `body` | 通知正文（13.1 的 body） |
  | `price` | 价格文字 |
  | `reason` | 理由：`body` 的第一行 |
  | `score` / `grade` | 分数、档位（如 `91`、`捡漏`）；没有时为空字符串 |
  | `config` | 配置，如 `iPad mini 6 / 256G`；规格不明或关键词任务为空 |
  | `fair_price` | 合理价文字，如 `¥1,300`；无法比价时为空 |
  | `desktop_link` / `mobile_link` | 网页版和手机端链接（和上游含义相同，不跟随 `link_target`） |
  | `link` | 按 `link_target` 选好的链接 |
  | `kakatta_url` | Kakatta 里这条发现的地址（13.1），没设置 `public_url` 时为空 |
  | `urgent` | 是否紧急，`true` 或 `false` |
  | `item_id` | 闲鱼商品 ID |
  | `item_title` / `image` / `task` / `kind` | 商品标题、主图、任务名、消息类型 |

- 替换在**解析后的 JSON 结构**里进行：只替换字符串值，再整体编码，所以值里的引号、换行不会弄坏 JSON。查询参数替换后做 URL 编码，和地址里原有的参数合并。
- 方法是 `POST` 而没填请求体模板时，用下面这份默认模板（内容类型按 `JSON`）：

  ```json
  {"title": "{{title}}", "body": "{{body}}", "item_title": "{{item_title}}", "item_id": "{{item_id}}", "price": "{{price}}", "score": "{{score}}", "grade": "{{grade}}", "config": "{{config}}", "fair_price": "{{fair_price}}", "reason": "{{reason}}", "link": "{{link}}", "desktop_link": "{{desktop_link}}", "mobile_link": "{{mobile_link}}", "kakatta_url": "{{kakatta_url}}", "image": "{{image}}", "task": "{{task}}", "kind": "{{kind}}", "urgent": "{{urgent}}"}
  ```

- `GET` 只发地址；`POST` + `JSON` 发 JSON 请求体，没写 `Content-Type` 时加 `application/json; charset=utf-8`；`POST` + `FORM` 要求模板是 JSON 对象，按表单编码，没写时加 `application/x-www-form-urlencoded`。
- 超时 15 秒，成功：2xx。

**邮件（SMTP）**（上游没有；标准：[RFC 5321](https://www.rfc-editor.org/rfc/rfc5321)、[RFC 5322](https://www.rfc-editor.org/rfc/rfc5322)、[RFC 2047](https://www.rfc-editor.org/rfc/rfc2047)、[RFC 3207](https://www.rfc-editor.org/rfc/rfc3207)、[RFC 8314](https://www.rfc-editor.org/rfc/rfc8314)；Go 标准库 [`net/smtp`](https://pkg.go.dev/net/smtp)）

- 配置：SMTP 服务器、端口、加密方式（`ssl` 直接 TLS，通常 465 / `starttls`，通常 587 / `none`）、用户名、密码或授权码（加密）、发件人地址、收件人（可以多个，逗号分隔）。
- 认证用 PLAIN，只在 TLS 连接上发送；选了 `none` 时不认证（只给局域网里的转发服务用）。
- 邮件：`Subject` 是通知标题，按 RFC 2047 编码；`From`、`To`、`Date`、`Message-ID` 齐全；`Content-Type: multipart/alternative`，一份纯文本（正文和链接各一行），一份 HTML（标题加粗、正文、宽 300 像素的商品图、「在闲鱼打开」链接，插入的文字做 HTML 转义），都用 UTF-8。
- 超时 30 秒。成功：服务器接受了 `DATA`。

### 13.7 推送状态

用户最常问的是「为什么没收到」，所以每条发现都要能看出推没推、为什么。

- **推送判定时（13.2）确定不发**，写 `findings.notify_skip_reason`，取第一条符合的：
  1. 基线发现：「首次运行只记录，不提醒」。
  2. 任务的推送档位是 `none`：「这个任务设置了不推送」。
  3. 商品标为不感兴趣，或卖家被屏蔽：「已标为不感兴趣」或「卖家已屏蔽」。
  4. 第一层没通过：「没通过你设的条件：价格不在 x–y 元之间」（写 5.1 的原因）。两种任务的 `all_new` 都不推（13.2）。
  5. 第二层认成垃圾：「求购帖不推送」（写类型名；`all_new` 也不推，关键词任务规则认出的求购、回收、租赁同样）。
  6. 不建议：「不建议：有 ID 锁（你设为不能接受）」。
  7. 超出预算：「超出预算：¥3,400，预算上限 ¥3,000」。
  8. 不符合需求、档位不是 `all_new`：「不符合需求：型号是 iPad mini 5，不在你选的范围里」（写结论的原因）。
  9. AI 任务低于推送档位：「价格合理 72，任务只推划算及以上」；无法比价而档位是只推捡漏：「无法比价，任务只推捡漏」。
  10. 关键词任务的推送档位是 `recommended` 而没命中：「没命中，任务只通知命中的商品」。
  11. 任务没有勾选启用的渠道：「这个任务没有选推送渠道」。
  12. 重新分析才得出结论：「重新分析的结果不发推荐通知」（5.5）。
  分析失败、跳过、还在等待的没有得出结论，不做判定也不写，界面按结论显示。
- **判定之后补写的**（2.8）：全部渠道都发送失败、到了重发上限（13.4）：「发送失败：所有渠道都没发出去，不再重发」；改了搜索条件，旧代次里还没发出去的（4.6）：「搜索条件已修改」。
- **卡片和详情页显示**：
  - 有 `delivery_items`：每个渠道最近一次的结果，例如「已推送 · Bark、Telegram · 14:32」；合并发送的写「合并在一条『还有 N 个推荐』里」。有渠道成功、有渠道失败时，失败的写「Telegram 发送失败：返回 401」，不再重发；全部渠道都失败时写各渠道的失败原因，并说明「下次运行会再发」（到了重发上限就改成「没有推送：发送失败」）。
  - 没有 `delivery_items`、有 `notify_skip_reason`：「没有推送：原因」。
  - `notify_kind` 有值、`notified_at` 和 `notify_skip_reason` 都为空：「等待推送」（本次运行还没到发送那一步，或上次全部失败）。
  - 降价通知同样显示在详情页的价格走势旁。
- **渠道的发送记录**：通知设置页每个渠道显示最近 20 条 `deliveries`：时间、类型、任务、成功或失败原因；失败的可以点开看是哪几个发现。
- 「发送测试」成功只说明渠道能用，测试结果旁固定写一句「真实提醒只在商品符合任务的通知设置时发送」（上游 #437、#451 的作者回复反复解释的就是这一点）。

---

## 14. 数据保留与清理

启动时清理一次，之后每 `cleanup.interval_hours` 一次。

| 数据 | 规则 |
|---|---|
| 发现 | 超过 `retention.findings_days` 天的删除（0 = 永久）；商品被收藏的发现不删 |
| 商品 | 没有发现引用、没收藏、`task_sightings` 里也没有它的记录、`last_seen_at` 超过 `retention.orphan_listing_days` 天的删除，价格点一并删除。还有行情记录的商品不删：行情要用它的标题现算排除词（12.1） |
| 行情明细 `task_sightings` | 超过 `retention.stats_days` 天的删除 |
| 规格 `task_item_specs` | 这个任务既没有这件商品的发现、`task_sightings` 里也没有它的记录时删除；`source = user` 的同样删除（商品已经不在保留期里） |
| 行情解读 `market_insights` | 超过 `retention.stats_days` 天的删除，每个任务至少保留最新一条 |
| 运行记录、AI 调用、推送记录 | 超过 `retention.stats_days` 天的删除 |
| 逐步日志 `run_steps` | 超过 `retention.run_steps_days` 天的删除 |
| 卖家缓存 | `fetched_at` 超过 `retention.seller_profile_days` 天的删除 |
| 额度记录 `quota_events` | 超过 48 小时的删除 |
| 系统事件 | 已解决且超过 `retention.stats_days` 天的删除 |
| 会话和令牌 | 过期或吊销超过 30 天的删除 |
| 缩略图缓存 | 超过 `cache.image_max_mb` 时按最近使用时间从旧到新删；单张超过上限的不落盘 |
| **永不自动清理** | `seen_items`（只随任务删除）、屏蔽卖家、收藏、任务、账号、设置、提示词、渠道、服务商 |

保留期设得比统计范围短时（比如保留 7 天看近 30 天），概览只能统计还在的数据，界面写明这一点。

**清除全部数据**（设置 → 通用）：删除任务、发现、商品、价格、见过、行情、日志、统计、卖家缓存、屏蔽卖家和图片缓存。保留登录密码和令牌、闲鱼账号和它们的登录状态、AI 服务商、推送渠道、代理、设置和提示词；另外保留额度记录 `quota_events`、AI 调用记录 `ai_calls`，以及账号的冷却状态（`state`、`cooldown_until`、`verify_level`、`last_cooldown_end_at` 等），否则访问额度、当天的 AI 用量和风控冷却会凭空恢复。二次确认，要求输入「清除」。

---

## 15. 导入导出与备份

### 15.1 任务配置导入导出

导出文件（JSON，UTF-8）：

```json
{
  "format": "kakatta.tasks",
  "version": 1,
  "exported_at": "2026-11-10T12:00:00+08:00",
  "tasks": [
    {
      "name": "MacBook Pro M3",
      "keyword": "MacBook Pro M3",
      "enabled": true,
      "min_price": 5000,
      "max_price": 7000,
      "personal_only": true,
      "free_shipping_only": false,
      "publish_window": "day7",
      "region": {"province": "浙江", "city": "杭州", "district": null},
      "max_pages": 1,
      "interval_minutes": 30,
      "crawl_overrides": {},
      "judge_mode": "ai",
      "requirement": "M3 芯片，16G 或 24G 内存，电池健康 90% 以上，不要维修过的，预算五六千",
      "category": "computer",
      "profile": {"summary": "会帮你找：……", "specs": [], "musts": [], "bonuses": [], "risks": [], "allowed_kinds": []},
      "budget_min": 5000,
      "budget_max": 6000,
      "match_keywords": [],
      "exclude_keywords": ["回收", "维修机"],
      "analyze_on_first_run": true,
      "notify_policy": "good",
      "notify_price_drop": true,
      "channels": [{"type": "telegram", "name": "Telegram · 家人群"}]
    }
  ]
}
```

- 价格和预算的单位是元（可以有小数）。`profile` 就是 2.6 的 `profile_json`，原样导出。导出时分页读完全部任务，不漏。
- 不导出：闲鱼账号（换了机器账号不同）、运行状态、搜索代次、发现。

导入规则：

1. `format` 不是 `kakatta.tasks` → 整份拒绝。`version` 比当前支持的高 → 整份拒绝，避免读错新字段。
2. 每个任务单独解码和校验，一个任务出错不影响其他任务。
3. 每个字段都可以省略，按新建表单的默认值补齐。
4. 逐个**跳过**（写明原因）：关键词为空或超过 50 字；最低价高于最高价或为负数；发布时间不是认识的值；地区在地区数据里选不到；运行间隔不是正整数；字段类型不对。
5. 自动**修正**（写明改了什么）：名称为空用关键词，超过 30 字截断；翻页数小于 1 的改成 1（超过 3 照样导入，按第 6 条提示）；推送档位不认识、或和判断方式不合法时（如 AI 任务写了 `recommended`、关键词任务写了 `good`）按任务类型取默认（AI 任务 `good`，关键词任务 `recommended`）；需求清单里不认识的检查项编号、规格名为空的规格、不认识的态度和 `allowed_kinds` 去掉，`source = ai` 的必须满足移到加分项（2.6），缺固定编号或编号重复的条目重新分配编号、`next_no` 补到比已用的最大编号大（2.6）；预算下限高于上限时两个都去掉；品类不认识时用 `general`；`crawl_overrides` 里不认识的键去掉、明显无效的值（0、负数、下限大于上限）去掉；渠道按「类型 + 名称」匹配本机已有的渠道，匹配不上的去掉（写了空列表就是不推送；没写这个字段就和新建表单一样，勾上所有已启用的渠道，2.6）；AI 任务的需求清单一项规格和必须满足都没有时改成关键词判断。
6. 运行间隔、等待时间这类数值超出推荐范围时照样导入，在结果里提示，和表单的处理一样（`DECISIONS.md` 第 17 条）。
7. 本机没有可用 AI 服务商时，AI 任务照样导入，运行时记跳过「没有配置 AI」，结果里提示。
8. 导入的任务和表单新建的一样：自动分配账号、`baseline_pending = 1`、立即进入调度。
9. 导入完成后提示：导入了几个、修正了哪些、跳过了哪几个和原因。中途保存出错时，已经存下的任务照常运行，提示里写明导入了几个。

### 15.2 导出 CSV

- 导出当前任务、当前筛选下的全部发现，分批读取，不受界面分页影响。
- 列：标题、价格、配置、合理价、分数、档位、结论、主要风险、理由、卖家、地区、发现时间、链接。配置、合理价、分数、档位在关键词任务和没算分的发现里为空；主要风险是有风险和待确认的检查项名称，用「、」连起来；结论写「推荐」「不符合需求」「求购帖」这类中文。价格写成不带货币符号的数字；链接用网页版地址。
- UTF-8 带 BOM（方便 Excel 打开），按 RFC 4180 转义，CRLF 换行。默认文件名「任务名-日期.csv」。

### 15.3 导出运行日志

导出当前筛选下的全部运行记录和逐步日志，UTF-8 文本，每行一条，字段用制表符分隔。

### 15.4 全量备份和恢复

用户换机器、重装时用。

- **包含**：数据库里的全部业务数据（任务、发现、商品、价格、见过、行情、日志、统计、卖家缓存、屏蔽卖家、收藏、设置、提示词、AI 服务商、推送渠道、代理、闲鱼账号的名称和代理绑定、登录 Kakatta 的用户名、密码哈希和两步验证设置），以及加密主密钥（否则 Key 和 Token 解不开）。
- **不包含**：闲鱼登录状态（各账号的浏览器配置目录）、会话和令牌、额度记录、图片缓存。
- **格式**：一个 zip 文件，里面两个文件：
  - `manifest.json`：`{"format": "kakatta.backup", "version": 1, "app_version": "...", "schema_version": N, "created_at": "...", "kdf": {"name": "argon2id", "salt": "...", "time": 3, "memory_kib": 65536, "threads": 1}}`；
  - `payload.bin`：用备份密码经 Argon2id 派生的密钥加密的 tar，里面是用 `VACUUM INTO` 生成的数据库快照（已删掉不包含的表的内容）和当前的主密钥。
- **分块加密**：tar 按固定大小（1 MiB）切块，每块单独用 AES-256-GCM 加密，各用一个随机 nonce；附加数据是块号和「是不是最后一块」，块被调换、删掉或文件被截短都能发现。导出和恢复都边读边处理，不把整个文件读进内存（2GB 内存的 NAS 也要能跑，`DECISIONS.md` 第 15 条）。
- 导出时用户必须设一个备份密码，因为文件里有能解开所有 Key 的主密钥。
- **恢复**：上传文件并输入备份密码 → 检查格式和版本（`schema_version` 比当前高就拒绝，低就恢复后照常迁移）→ 二次确认「会覆盖这台服务端上的全部数据，所有闲鱼账号要重新扫码登录」→ 停止调度和所有运行 → 替换数据库和主密钥 → 删掉整个 `browser/` 目录（备份不含闲鱼登录状态，旧的配置目录和新数据库里的账号对不上），所有闲鱼账号改为 `signed_out`（要重新扫码），所有会话和令牌失效（用备份里的密码重新登录）→ 重启调度。失败时保留原来的数据不动。
- **主密钥用环境变量或文件指定时**（`KAKATTA_SECRET_KEY` 或 `KAKATTA_SECRET_KEY_FILE`，2.2）：这时不读 `secret.key`，替换它没用。备份里的主密钥和当前的不同时，用备份里的主密钥解开所有加密字段，再用当前主密钥重新加密一遍（附加数据照 2.2），全部成功才算恢复完成；一样时直接用。

---

## 16. 统计口径

监控概览可以切换今天 / 近 7 天 / 近 30 天。

- **比较**：今天对比昨天 0 点到昨天此刻；近 7 天对比再往前 7 天；近 30 天超出保存期，不显示比较。前一段少于 50 时写差值，否则写百分比。
- **统计卡**：扫描商品（`run_records.scanned_count` 之和）、新上架（`new_count` 之和）、推荐（推荐发现数，口径见第 1 节：AI 任务是捡漏或划算，或者无法比价且不是不建议，同时没超预算、没被标为不感兴趣、卖家没被屏蔽；关键词任务是命中；按 AI 和关键词拆分）、降价提醒（推送记录里成功发出的降价通知条数：`deliveries.kind = price_drop` 且 `ok = 1`，同一条降价同时发到几个渠道只算一条；不按 `drop_notified_at` 算，它只记最近一次）。
- **检查活动**：按小时（今天）或按天的新上架和已见过数量。「今天」按实际小时数分桶，夏令时那天是 23 或 25 根柱子。
- **任务状态**：监控中（启用、在排队或等下次）、检查中、出错（最近一次失败或失败暂停）、已暂停（停用；或任务没有可用账号；或账号冷却中、需要验证、已失效、被拒绝）。附检查成功率、平均每轮耗时、下次检查时间。
- **任务表现**：每个任务的扫描数、推荐数、推荐率、当前最低价、近 7 天价格走势（每天的中位数，12.1）。推荐率 = 推荐数 ÷ 新发现数：分母是这段时间里这个任务新建的发现数，分子是其中的推荐数，两边都不含基线发现。
- **AI 档位分布**：捡漏、划算、价格合理、偏贵或有顾虑、不建议、无法比价、不符合需求、垃圾各多少件；有分数的发现按商品去重（同一商品被几个任务分析过只算最近一次），重新分析失败的不算。
- **四张小卡**：闲鱼账号（N 个中 M 个可用）、AI 调用（次数、成功率、平均耗时、token）、推送（条数、成功率）、异常（未解决的系统事件，风控和网络两类失败次数）。
- **运行日志概览**：过去 24 小时的成功率（不算跳过）、运行次数、失败次数、平均耗时；每小时一根柱子，分成功、跳过、失败。

---

## 17. 和 Mekke 的差异

| 方面 | Mekke | Kakatta | 理由 |
|---|---|---|---|
| 频率和额度 | 写死，不能调低 | 全部是配置项，默认值见 `CRAWL-POLICY.md` 第六节，超出推荐范围时提示但允许保存 | `DECISIONS.md` 第 17 条 |
| 额度按什么算 | 全部账号合计；每天搜索 200、详情加卖家 150 | 按每个闲鱼账号算；加了每小时 30 次；详情加卖家默认每天 100 | `CRAWL-POLICY.md` 第六节 |
| 访问计数 | 每次访问或点击都算，包括只打开菜单、聚焦输入框 | 只算会让闲鱼产生请求的动作，以 `XIANYU.md` 5.8 节为准，待确认的照算（第 0 节） | 菜单展开、聚焦输入框都算的话，默认的每小时 30 次连一个 30 分钟间隔的任务都跑不满 |
| 运行间隔 | 固定几档，最短 10 分钟，随机延长 0–40% | 任意分钟数，默认 30，随机延长 0–30% | `CRAWL-POLICY.md` 第六节 |
| 两次运行的间隔 | 任意两个任务之间至少 90 秒 | 同一账号两次运行之间至少 3 分钟加随机 0–60 秒；不同账号之间不限 | 同上 |
| 并发 | 全应用一个页面，所有任务串行 | 全局同时打开的页面数可配（默认 1），同一账号同时只开一个 | 多账号；`DECISIONS.md` 第 16 条 |
| 打开详情 | 每个新商品都打开详情再判断，每次最多 10 个（可调到 20） | 先用搜索结果本地筛选，只给通过前两层的打开，每次默认最多 5 个（详情页和卖家主页合计） | `CRAWL-POLICY.md` 第一、三节 |
| 判断 | 一次多模态判断，给 0–100 匹配度 | 三层：硬条件和排除词 → 文字模型批量整理标题、清掉垃圾 → 多模态提取事实；分数由程序按同配置合理价、风险和卖家算，AI 只能在 −15 到 +8 之间调整 | `DECISIONS.md` 第 48–55 条 |
| 需求 | AI 生成一段分析标准文字 | AI 整理成可编辑的清单：多选规格、必须满足、加分项、带三档态度的风险检查项、大概预算 | `DECISIONS.md` 第 48 条 |
| 推送 | 达到推荐门槛就推 | 按档位推送（默认划算及以上），超出预算不推，捡漏按紧急发送 | `DECISIONS.md` 第 55 条 |
| 排除词 | 只在关键词任务里，作用于标题加描述 | 所有任务都有，第一层查标题，打开详情后再查描述 | `PRODUCT.md` 第 4 节 |
| 关键词任务的匹配词 | 在标题加描述里找 | 第一层要求标题命中，打开详情后结论按标题加描述 | 只给可能匹配的商品打开详情，省详情额度 |
| 遇到人机验证 | 暂停全部任务，冷却固定 30 分钟 | 只暂停这个账号，冷却 2 小时起逐次加倍，最长 24 小时 | `CRAWL-POLICY.md` 第四节 |
| 直接拒绝（deny） | 和验证码一样处理 | 账号暂停 24 小时，不能手动运行 | 同上 |
| 多账号 | 可选账号轮换：按任务固定或每次随机；登录失效、限流时换号重试 | 每个任务固定一个账号，不轮换；只有登录失效时把自动分配的任务换到另一个账号，本次不重试 | `PRODUCT.md` 第 2 节，上游 #136 |
| 代理 | 代理池，按任务固定或每次随机，失败换下一个 | 每个账号固定一个代理或不用，不轮换，连不上算普通失败 | `PRODUCT.md` 第 2 节，上游 #492 |
| 普通失败 | 断网、代理全部失败不计入失败保护 | 网络和代理失败也计入（连续 3 次暂停 30 分钟） | `CRAWL-POLICY.md` 第四节 |
| AI 服务商 | 可以存多个，只用最上面的，出错不换 | 按顺序用，出错自动换下一个；Key 无效等需要用户处理的错误会让这个服务商停用并推送 | `PRODUCT.md` 第 4 节 |
| 结构化输出 | 只用 `json_object`，不支持时去掉 | JSON Schema → json_object → 纯文本宽松解析，逐级降级并写日志 | `DECISIONS.md` 第 20 条 |
| 模型 | 每个服务商一个模型 | 每个服务商一个多模态模型，可再配一个便宜的文字模型 | 第二层要用便宜模型 |
| 密钥保存 | macOS 钥匙串 | AES-256-GCM 加密存在数据库里，主密钥是数据目录里的文件或环境变量 | 服务端跑在 Docker 和 NAS 上，没有钥匙串 |
| 推送渠道 | 系统通知加 6 种，每种只能配一个 | 11 种，同一种可以配多个，不同任务推到不同渠道；加了系统事件推送 | `PRODUCT.md` 第 7 节 |
| 系统通知 | macOS 通知中心，带操作按钮 | 服务端没有；客户端以后再定 | `PRODUCT.md` 第 12 节 |
| 运行时段 | 「运行时段」，时段外不运行 | 「不运行的时段」，默认 01:00–07:30 | `CRAWL-POLICY.md` 第六节 |
| 睡眠和唤醒 | Mac 睡眠时取消运行，唤醒后等 60 秒 | 服务端常驻，没有睡眠；重启时把未结束的运行记为失败 | 部署方式不同 |
| 不感兴趣 | 不再通知 | 不再通知，别的任务搜到时直接跳过、不分析 | 省 AI 费用和详情额度 |
| 市场行情统计 | 从发现、商品和价格点反推每天的价格 | 单独一张 `task_sightings` 表记每个任务每天看到的价格 | 价格不变的商品也能每天算一个样本，计算简单 |
| 删除结果 | 只能删全部 | 可以删全部，也可以单个删 | `PRODUCT.md` 第 6 节 |
| 备份 | 没有 | 加密的全量备份和恢复，不含闲鱼登录状态 | `PRODUCT.md` 第 11 节 |
| 登录 Kakatta | 不需要 | 一个用户名加密码，可选两步验证；网页会话和 API 令牌（脚本、MCP 用）可以查看和吊销 | `DECISIONS.md` 第 13、19 条 |
