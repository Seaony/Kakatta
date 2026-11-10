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
- **随机等待**：在区间里均匀取值，结果永远不低于区间下限（已实现在 `internal/pacer`）。
- **一个「访问」**：程序做的、会让闲鱼产生页面加载或数据接口请求的一个动作。每个动作产生什么请求，以 `XIANYU.md` 5.8 节的表为准：
  - 算：打开首页（包括登录时程序打开的首页）、在搜索框提交关键词、发布时间选项、个人闲置、包邮、地区的提交、价格提交、翻页和翻页重试、打开详情页、打开卖家主页、卖家主页里切到「信用及评价」、登录后打开自己的主页。表里写着「待用真实页面确认」的动作（比如地区弹层里点省、市），确认之前也算。
  - 不算：表里写着「一般没有请求」的动作（打开「新发布」菜单、关广告），以及聚焦输入框。
  - 用户自己在远程画面里的操作（扫码、过验证）不是程序做的，不算。

  额度（7.3 节）按访问计数。首页、搜索、筛选、翻页算搜索类；详情页、卖家主页、自己的主页算详情类。

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
| 推荐 | AI 任务里匹配度达到任务的推荐门槛，或关键词任务里命中规则。 |
| 分析标准 | AI 任务里，由用户的大白话需求生成、用户可以修改的一段文字，AI 照它判断。 |
| 分层判断 | 第一层硬条件和排除词（免费，所有任务）→ 第二层文字模型看标题（便宜，AI 任务可选）→ 第三层多模态模型看图和描述（贵）。前一层通过才进下一层，详情页只给通过前两层的商品打开。 |
| 基线 | 任务第一次运行（或改了搜索条件后的第一次运行）看到的商品，只记录、不通知。 |
| 补处理队列 | 上次没处理完（额度不够、超出本次上限）或可以重试的失败发现，下次运行优先处理。 |
| 运行 | 任务的一次执行：搜索 → 找出新商品 → 分层判断 → 通知 → 记日志。 |
| 访问额度 | 每个闲鱼账号每小时、每天能访问闲鱼的次数上限。 |
| 风控冷却 | 某个账号遇到人机验证后，用户完成验证，账号还要暂停一段时间才自动运行，时长逐次加倍。 |
| 推送渠道 | 一个配置好的推送目标，比如某个 Telegram 机器人。同一种渠道可以配多个，不同任务可以推到不同的渠道。 |
| AI 服务商 | 一个 OpenAI 兼容接口的配置（地址、Key、模型）。可以配多个，按顺序使用，当前这个出错时换下一个。 |
| 市场行情 | 某个任务近 14 天搜到的商品的价格统计，以及可选的 AI 行情解读。 |

---

## 2. 数据模型（SQLite）

### 2.1 通用规则

- 驱动用 `modernc.org/sqlite`。连接打开后执行 `PRAGMA journal_mode=WAL`、`PRAGMA foreign_keys=ON`、`PRAGMA busy_timeout=5000`。
- 数据库文件放在数据目录 `<data>/kakatta.db`。同一目录下还有：`secret.key`（加密主密钥）、`browser/<账号 id>/`（每个账号的浏览器配置目录）、`cache/images/`（缩略图缓存）。
- 主键一律 `INTEGER PRIMARY KEY`。布尔值存 `0/1`。列表、对象存 JSON 文本，列名以 `_json` 结尾。
- 表结构用编号的迁移脚本升级，当前版本记在 `schema_migrations`。

### 2.2 密钥加密

API Key、推送 Token、Webhook 请求头、代理地址（可能带密码）、邮箱密码都加密保存，列名以 `_enc` 结尾，类型 `BLOB`。

- 主密钥：第一次启动时生成 32 字节随机数，写进 `<data>/secret.key`，文件权限 0600。也可以用环境变量 `KAKATTA_SECRET_KEY`（base64）指定，或者用 `KAKATTA_SECRET_KEY_FILE` 指向一个文件（配合 Docker secrets），指定了就不读 `secret.key`。主密钥丢了，所有加密的值都要重新填。
- 能防什么、不能防什么（文档如实写）：主密钥和数据在同一个目录时，加密只挡得住「只有数据库文件泄露」，挡不住整个数据目录被拿走（`DECISIONS.md` 第 40 条）。
- 算法：AES-256-GCM。存储格式是 `12 字节随机 nonce + 密文`。附加数据（AAD）用 `表名:列名:行 id`，防止把一行的密文挪到另一行用。
- 接口永远不返回明文。界面上只显示「已设置」，用户要改就整个重填。日志、运行记录、错误信息里都不能出现明文，报错时先把当前 Key 遮掉再截取。

### 2.3 登录 Kakatta

**admin**（只有一行）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | 固定为 1，`CHECK (id = 1)` |
| username | TEXT NOT NULL | |
| password_hash | TEXT NOT NULL | Argon2id，PHC 字符串格式（含参数和盐） |
| created_at / password_changed_at | INTEGER NOT NULL | |

- 还没有这一行时，只有填对容器日志里的一次性初始化令牌才能创建（30 分钟有效，过期重启容器再拿新的）。令牌只存在内存里，不写数据库（`DECISIONS.md` 第 40 条）。
- 登录失败按 IP 递增延迟，另有全局失败次数上限；真实 IP 只从可信代理的请求头里读（`DECISIONS.md` 第 40 条）。

**auth_tokens**：网页端的 Cookie 会话和桌面端、手机端的令牌都在这里。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| kind | TEXT NOT NULL | `web_session` / `client_token` |
| token_hash | BLOB NOT NULL UNIQUE | 令牌的 SHA-256，明文只在发放时返回一次 |
| name | TEXT NOT NULL | 设备名，网页会话取浏览器的 User-Agent 摘要 |
| platform | TEXT | `web` / `macos` / `windows` / `linux` / `ios` / `android` |
| push_key_enc | BLOB | 原来留给手机端推送用；客户端先不考虑（`DECISIONS.md` 第 36 条），1.0 为空 |
| created_at / last_used_at | INTEGER | |
| expires_at | INTEGER | 网页会话 30 天不用就过期；客户端令牌为空，直到吊销 |
| revoked_at | INTEGER | |

- 改密码时吊销除当前会话以外的全部会话和令牌。容器里的「重置密码」命令吊销全部。
- 索引：`(kind, revoked_at)`。

### 2.4 设置与提示词

**settings**：`key TEXT PK`、`value_json TEXT NOT NULL`、`updated_at INTEGER`。没有这一行就用第 3 节的默认值。

**prompts**：`key TEXT PK`（`analysis` / `title_screen` / `criteria` / `market`）、`content TEXT NOT NULL`、`updated_at`。没有这一行就用第 8 节的内置原文。「还原」就是删掉这一行；保存的内容和内置原文一样时也直接删掉，系统状态里就不会误报「已自定义」。

### 2.5 闲鱼账号与代理

**proxies**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| name | TEXT NOT NULL UNIQUE | |
| url_enc | BLOB NOT NULL | `http://[用户名:密码@]host:port` 或 `socks5://[用户名:密码@]host:port` |
| created_at | INTEGER | |

被账号或 `ai.proxy_id` 使用的代理不能删，先解除绑定。

**accounts**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | 浏览器配置目录 `browser/<id>/` |
| name | TEXT NOT NULL | 只用于区分 |
| proxy_id | INTEGER REFERENCES proxies | 为空就是不用代理 |
| nickname / avatar_url | TEXT | 登录后读到的闲鱼昵称和头像 |
| state | TEXT NOT NULL | `signed_out` 未登录 / `active` 可用 / `cooling` 冷却中 / `needs_verification` 需要验证 / `denied` 被拒绝访问 / `expired` 已失效 |
| state_reason | TEXT | 给用户看的说明 |
| state_changed_at | INTEGER | |
| cooldown_until | INTEGER | `cooling` 和 `denied` 时有值 |
| verify_level | INTEGER NOT NULL DEFAULT 0 | 连续第几次人机验证，用来算加倍冷却 |
| last_cooldown_end_at | INTEGER | 上一次验证冷却的结束时间 |
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
| kind | TEXT NOT NULL | `search`（首页、搜索、筛选、翻页）/ `detail`（详情页、卖家主页、自己的主页） |
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
| account_pinned | INTEGER NOT NULL | 0 | 1 = 用户指定了账号，0 = 自动分配的 |
| min_price / max_price | INTEGER | 空 | 分；都有值时 `min <= max`，都不能为负 |
| personal_only | INTEGER NOT NULL | 1 | 只看个人闲置 |
| free_shipping_only | INTEGER NOT NULL | 0 | 只看包邮 |
| publish_window | TEXT NOT NULL | `any` | `any` / `day1` / `day3` / `day7` / `day14`。不管选哪个，搜索结果都按最新发布排序 |
| region_province / region_city / region_district | TEXT | 空 | 必须能在随程序发布的地区数据里选到 |
| max_pages | INTEGER NOT NULL | `crawl.max_pages` | 1–3 |
| interval_minutes | INTEGER NOT NULL | `crawl.task_interval_minutes` | ≥ 1 |
| crawl_overrides_json | TEXT NOT NULL | `{}` | 本任务单独的频率设置，见 3.2 |
| judge_mode | TEXT NOT NULL | 配了 AI 时 `ai`，否则 `keyword` | |
| requirement | TEXT NOT NULL | `""` | AI 任务的大白话需求，至少 10 个字 |
| criteria | TEXT NOT NULL | `""` | AI 任务的分析标准，不能为空 |
| title_screening | INTEGER NOT NULL | 1 | AI 任务是否启用第二层（文字模型看标题） |
| recommend_threshold | INTEGER NOT NULL | 70 | 推荐门槛：50 / 60 / 70 / 80 / 90 |
| match_keywords_json | TEXT NOT NULL | `[]` | 关键词任务的匹配词 |
| exclude_keywords_json | TEXT NOT NULL | `[]` | 排除词，所有任务都可以填 |
| analyze_on_first_run | INTEGER NOT NULL | 1 | 首次运行也分析最新的 10 个（不通知） |
| notify_policy | TEXT NOT NULL | `recommended` | `recommended` 只通知推荐 / `all_new` 所有新商品 / `none` 不通知 |
| notify_price_drop | INTEGER NOT NULL | 1 | 推荐过的商品降价时通知 |
| search_generation | INTEGER NOT NULL | 1 | 搜索条件每改一次加 1，见 4.6 |
| baseline_pending | INTEGER NOT NULL | 1 | 1 = 下一次成功运行是基线运行 |
| sort_index | INTEGER NOT NULL | | |
| created_at / updated_at | INTEGER | | |
| last_run_at / last_success_at / next_run_at | INTEGER | | |
| last_run_outcome | TEXT | | `success` / `failed` / `skipped` |
| last_error | TEXT | | |
| consecutive_failures | INTEGER NOT NULL | 0 | |
| failure_paused_until | INTEGER | | 失败保护 |

索引：`(enabled, next_run_at)`、`(account_id)`。

**task_channels**：`task_id`、`channel_id`，主键是两者组合，两边都 `ON DELETE CASCADE`。任务里没勾任何渠道就不推送。

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

**seller_profiles**：卖家主页的缓存。

| 字段 | 说明 |
|---|---|
| seller_id TEXT PK | |
| nick、avatar_url、introduction | |
| seller_credit、buyer_credit | 闲鱼的信用文字原文 |
| item_count、rate_count、on_sale_count、sold_count | |
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
| decision | TEXT NOT NULL | `pending` / `recommended` / `rejected` / `failed` / `skipped` |
| stage | TEXT NOT NULL | `pending` 时下一步做什么：`title`（等第二层）/ `detail`（等打开详情）；其他状态为 `done` |
| decided_layer | INTEGER | 在第几层得出结论：1 / 2 / 3；跳过和失败为空 |
| reason | TEXT | 一句话理由，或跳过、失败的原因 |
| match_score | INTEGER | 0–100，只有第三层 AI 判断有 |
| risk_tags_json / criteria_results_json | TEXT | 第三层的风险标签和逐项结果 |
| seller_type | TEXT | `personal` / `dealer` / `unknown` |
| insights_json | TEXT | 第三层的商品参数、卖家类型依据、交易风险、到手刀风险、描述可信度、联系卖家前建议 |
| title_screen_reason | TEXT | 第二层的理由 |
| matched_keywords_json | TEXT | 关键词任务命中的词 |
| analysis_model / analysis_provider_id | TEXT / INTEGER | 得出结论的模型和服务商，成功返回后才写 |
| analyzed_at | INTEGER | |
| attempts | INTEGER NOT NULL DEFAULT 0 | 处理过几次 |
| retryable | INTEGER NOT NULL DEFAULT 0 | 失败时能不能下次重试 |
| is_read | INTEGER NOT NULL | 基线发现创建时就是已读，其余是未读 |
| notified_at | INTEGER | 至少一个渠道发送成功的时间 |
| recommended_price | INTEGER | 判为推荐时的价格 |
| last_drop_notified_price | INTEGER | 上一次发降价通知时的价格 |
| drop_notified_at | INTEGER | |

- 唯一约束：`(task_id, item_id)`。
- 索引：`(task_id, found_at)`、`(task_id, decision)`、`(is_read)`、`(item_id)`。
- 「分析失败（已放弃）」不另存字段：`decision = failed` 且 `retryable = 0`。

### 2.9 AI 服务商

**ai_providers**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| name | TEXT NOT NULL | |
| preset | TEXT | `openai` / `deepseek` / `modelscope` / `siliconflow` / `openrouter` / `aliyun` / `anthropic` / `ollama` / `custom`，只用于预填地址 |
| base_url | TEXT NOT NULL | 如 `https://api.openai.com/v1`，去掉末尾的 `/` |
| api_key_enc | BLOB | 本地 Ollama 可以不填 |
| vision_model | TEXT NOT NULL | 第三层用，必须支持图片 |
| text_model | TEXT | 第二层、生成标准、行情解读用；为空就用 `vision_model` |
| enabled | INTEGER NOT NULL DEFAULT 1 | |
| sort_index | INTEGER NOT NULL | 越小越先用 |
| error_state | TEXT | 出了需要用户处理的错（Key 无效、余额不足、模型不存在、模型不支持图片）时写原因，有值就不参与自动使用 |
| error_at | INTEGER | |

用户保存或测试成功时清空 `error_state`。

**ai_compat**：每个「地址 + 模型」的兼容标记，见 8.3。

| 字段 | 说明 |
|---|---|
| provider_id INTEGER REFERENCES ai_providers ON DELETE CASCADE | |
| model TEXT | |
| json_mode TEXT NOT NULL DEFAULT 'schema' | `schema` / `object` / `text` |
| send_temperature INTEGER NOT NULL DEFAULT 1 | |
| max_tokens_param TEXT NOT NULL DEFAULT 'max_tokens' | `max_tokens` / `max_completion_tokens` |
| updated_at | |

主键 `(provider_id, model)`。服务商的地址或模型改了，删掉它的兼容标记重新试。

**ai_calls**：每次请求一条，不存请求和回复的内容。字段：`id`、`at`、`provider_id`、`model`、`purpose`（`title_screen` / `analysis` / `criteria` / `market` / `test`）、`task_id`、`ok`、`http_status`、`error_kind`、`json_mode`、`input_tokens`、`output_tokens`、`duration_ms`。索引 `(at)`。

### 2.10 推送渠道

**channels**

| 字段 | 类型 | 说明 |
|---|---|---|
| id | INTEGER PK | |
| type | TEXT NOT NULL | `bark` / `telegram` / `ntfy` / `gotify` / `wecom` / `dingtalk` / `feishu` / `pushplus` / `webhook` / `email` |
| name | TEXT NOT NULL | 同一种渠道配多个时用来区分，如「Telegram · 家人群」 |
| enabled | INTEGER NOT NULL DEFAULT 1 | |
| config_json | TEXT NOT NULL | 不敏感的配置（见 13.6 各渠道的配置项） |
| secret_enc | BLOB | 敏感配置，加密后的 JSON |
| last_test_at / last_test_ok / last_error | | |

`UNIQUE (type, name)`。

**deliveries**：每次实际发送一条。字段：`id`、`at`、`channel_id`、`task_id`、`kind`（`recommended` / `all_new` / `price_drop` / `summary` / `system` / `test`）、`item_count`、`ok`、`error`（固定格式、带渠道名的说明，不含地址、Token 或响应正文）。索引 `(at)`。

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
| failure_kind TEXT | `risk_verify` / `risk_deny` / `login` / `network` / `page` / `timeout` / `quota` / `config` / `cancelled` / `restart` |
| message TEXT | 成功写「扫描 N 页 · 分析 N 件」或「没有新商品」，失败和跳过写原因 |
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
| kind TEXT | `account_verify` / `account_login` / `account_deny` / `ai_error` / `task_failure_pause` |
| subject_id INTEGER | 账号、服务商或任务的 id |
| message TEXT | |
| created_at、resolved_at、notified_at | |

同一个 `(kind, subject_id)` 有未解决的事件时，不再新建、不再推送。解决的时机：账号恢复可用、服务商下一次请求成功或被用户保存、任务暂停结束。

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
| `crawl.pages_per_account` | 1 | 1 |
| `crawl.global_pages` | 1 | 1–2（取决于内存） |
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
| `ai.max_items_per_run` | 10 | 每次运行最多处理几个商品（第二、三层和关键词判断合计），推荐 5–20 |
| `ai.image_count` | 4 | 第三层每个商品发几张图，1–4 |
| `ai.max_concurrent_requests` | 2 | 整个服务端同时进行的 AI 请求数 |
| `ai.request_timeout_seconds` | 90 | |
| `ai.proxy_id` | 空 | AI 请求专用的代理，只影响 AI，不影响闲鱼和推送 |

**通知**

| 键 | 默认值 | 说明 |
|---|---|---|
| `notify.link_target` | `mobile` | 推送里的商品链接打开手机 App（`mobile`）还是网页版（`desktop`） |
| `notify.max_per_run` | 5 | 同一个任务一次运行最多发几条，超出的合并 |
| `notify.system_channel_ids` | `[]` | 系统事件推到哪些渠道，空列表 = 所有启用的渠道 |

**通用、数据与缓存**

| 键 | 默认值 | 说明 |
|---|---|---|
| `general.language` | `system` | `system` / `zh-CN` / `en` |
| `general.appearance` | `system` | `system` / `light` / `dark` |
| `general.timezone` | 环境变量 `TZ`，没有就用 `Asia/Shanghai` | |
| `general.public_url` | 空 | 用户从外面访问 Kakatta 的地址，推送里「在 Kakatta 查看」的链接用它，空就不放这个链接 |
| `general.onboarding_done` | false | |
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

按账号算的限制（最小间隔、每小时和每天的额度、同时打开的页面数、冷却）不能按任务覆盖，因为它们保护的是账号。

---

## 4. 一次运行

任务 T 用账号 A。下面每一步出现风控信号（`CRAWL-POLICY.md` 第四节），都**立即结束整次运行**，错误一路传到最外层，任何一层都不能吞掉；然后按第 7 节处理账号。

### 4.1 前置检查

任一条不满足就不打开浏览器，本次记为「已跳过」并写明原因，**不计入失败**：

1. 任务启用（手动运行除外），失败保护的暂停已过（手动运行除外）。
2. 任务有账号，账号状态是 `active`；或者是 `cooling` 且这是冷却期内唯一一次手动运行（见 7.2）。
3. 不在 `crawl.quiet_hours` 里。
4. 账号的每小时额度和每天的搜索额度都至少还剩 2 次（首页加搜索；这只是入口估算，真正的限制在每次访问前检查）。
5. 距离这个账号上次运行结束，已经过了 `next_gap_seconds`。
6. AI 任务至少有一个可用的 AI 服务商；分析标准不为空。
7. 有空闲的页面名额：全局不超过 `crawl.global_pages`，这个账号不超过 `crawl.pages_per_account`。没有名额时不记跳过，排队等。

### 4.2 取补处理队列

在创建任何新记录之前，先取本任务的补处理队列 Q：

- `decision = pending`，且 `search_generation` 等于任务当前的代次；
- `decision = failed`、`retryable = 1`、`attempts < 3`，且代次相同。

两类合在一起按 `found_at` 从旧到新排，按发现 id 去重。

### 4.3 搜索

1. 启动这个账号的 Chrome（带固定代理），打开首页，停留 `crawl.home_stay_seconds`，随机滚动一次。
2. 在搜索框里逐字输入关键词（`crawl.typing_interval_ms`），回车。
3. 按任务条件依次点筛选：发布时间（每次都点，`any` 时选「最新」）、个人闲置、包邮、地区、价格。每次点击之间等 `crawl.action_wait_seconds`。每一步都要等到一条新的搜索接口响应，只用最后一条作为第 1 页数据。
   - 可重复执行的步骤（发布时间、地区、价格）没等到响应：等 5 秒重做一次，仍然失败就记为页面失败。
   - 开关类步骤（个人闲置、包邮，再点一次会取消）：**绝不重复点击**，再等 10 秒看响应是否晚到，还没有就记为页面失败。
   - 「个人闲置」没法在本地补判，所以它的步骤失败必须让整次运行失败，不能带着没筛选过的结果继续。
4. 读第 1 页；需要时翻页，最多 `tasks.max_pages` 页，页与页之间等 `crawl.page_wait_seconds`。某一页 0 个商品就停止翻页。
5. 每次访问前申请额度（7.3）：
   - 筛选还没全部完成时额度用完：整次运行记为「已跳过」，已拿到的结果全部丢弃，因为它们没经过完整筛选。
   - 筛选完成后、翻页途中用完：停止翻页，用已经拿到的页继续。

页面操作的具体选择器和接口见 `XIANYU.md`。

### 4.4 记录商品、价格和「见过」

对搜到的全部商品（经过页面筛选的所有页）：

1. 写入或更新 `listings`，更新 `last_seen_at`。
2. 价格和这个商品最近一个价格点不同（或还没有价格点）时，追加 `price_points`；降价时写 `last_drop_amount` 和 `last_drop_at`，涨价时清空（第 10 节）。
3. 写 `task_sightings`（今天的价格）。
4. 计算本次的价格参考：有效价格不少于 3 个时，取最低价、中位数、最高价和样本数；少于 3 个时没有价格参考。
5. 找出新商品：本任务 `seen_items` 里没有的商品 ID。是否见过只看 `seen_items`，不看发现，因为发现会按保留期清理。
6. 把全部新商品写进 `seen_items`，包括下面第一层没通过的。

### 4.5 创建发现

每个新商品创建一条发现。先依次检查：

1. 商品被标记过不感兴趣 → `skipped`，原因「不感兴趣」。
2. 商品的 `seller_id` 已知（之前打开过详情）且卖家被屏蔽 → `skipped`，原因「已屏蔽卖家」。
3. 第一层（5.1）不通过 → `rejected`，`decided_layer = 1`，原因写哪一条没通过。
4. 其余的是候选。

然后分两种情况：

- **基线运行**（`tasks.baseline_pending = 1`）：所有发现 `is_baseline = 1`、已读。开启了 `analyze_on_first_run` 时，候选里发布时间最新的 10 个为 `pending`，其余为 `skipped`，原因「基线」；关闭时候选全部 `skipped`。
- **普通运行**：候选全部为 `pending`、未读。

`pending` 的发现，AI 任务开了第二层时 `stage = title`，否则 `stage = detail`。

### 4.6 改了搜索条件

保存任务时，关键词、价格区间、个人闲置、包邮、发布时间、地区任何一项变了：

- `search_generation` 加 1，`baseline_pending` 设为 1：下一次运行只记录现有商品、不分析、不通知（开了首次分析时仍然分析最新的 10 个，但不通知）。表单在保存按钮上方提示「修改搜索条件后，下次运行会重新记录现有商品，不会提醒」。
- 旧代次里还是 `pending` 和可重试 `failed` 的发现改为 `skipped`，原因「搜索条件已修改」，不再分析、不再通知。历史发现和收藏都保留。
- 正在运行的旧代次运行，结束时发现代次变了，就丢弃本次结果，不写发现、不算完成基线（价格点照常记录）。

改名称、间隔、判断规则、通知设置不重置基线。改了间隔时，`next_run_at` 重算为「保存时间 + 新间隔」。

### 4.7 处理集合

处理集合 P = Q 在前，本次新建的 `pending` 发现在后（按发布时间从新到旧），按发现 id 去重，总数不超过 `ai.max_items_per_run`。超出的保持 `pending`，下次进入 Q。

### 4.8 第二层：文字模型看标题

只对 AI 任务、`title_screening = 1`、`stage = title` 的发现做（5.2）。不打开任何闲鱼页面，可以在浏览器停在搜索页时进行，最多 `ai.max_concurrent_requests` 个同时请求。

- 通过 → `stage = detail`，记 `title_screen_reason`。
- 不通过 → `rejected`，`decided_layer = 2`。
- 所有服务商都出错 → `failed`，按 8.5 决定能不能重试。不打开详情。

### 4.9 打开详情和判断

对 P 里 `stage = detail` 的发现，按 Q 在前、新发现在后的顺序逐个处理。本次运行打开的详情页和卖家主页合计不超过 `max_details_per_run`（任务覆盖值或全局值）。每处理一个，`attempts` 加 1。

1. 打开详情页**之前**，申请一次详情额度。申请不到，或本次的详情上限已用完：P 里剩下的都保持 `pending`，结束这一步，本次运行仍算成功。
2. 等 `crawl.detail_pre_wait_seconds`，打开详情页，读详情。解析失败或加载失败只影响这一件：记为 `failed`、`retryable = 1`（第 3 次失败后为 0），继续下一件。风控、登录失效、取消不在这里处理，照常往外抛。
3. 拿到 `seller_id` 后**再检查一次屏蔽列表**，命中就 `skipped`：不打开卖家主页、不判断、不通知。
4. 用标题加描述再查一次排除词（第 9 节），命中就 `rejected`，`decided_layer = 1`，原因「描述命中排除词：xx」。
5. 关键词任务：按第 9 节判断，`decided_layer = 1`。
6. AI 任务：
   - 卖家资料在缓存里且没过期 → 直接用。
   - 否则先申请一次详情额度并占用一个详情上限名额，再打开卖家主页。申请不到，或卖家主页的普通错误（不是风控、不是登录失效）：卖家资料留空继续判断，在运行记录的提示里写一句，同一次运行里去重。
   - 第三层判断（5.3）。判断可以和下一件的抓取同时进行，同时最多 `ai.max_concurrent_requests` 个；浏览器仍然一次只开一个页面。
7. 看完详情后停留 `crawl.detail_stay_seconds`。

判断得出结论后：推荐时写 `recommended_price` 为当前价格。

### 4.10 通知、记录和结束

1. 按第 13 节发通知：本任务已判断完、还没通知过、符合通知策略、不是基线、商品没被标为不感兴趣、卖家没被屏蔽的发现；加上本次检测到的降价。发送失败的保持 `notified_at` 为空，下次运行再发，即使那时没再搜到这个商品。
2. 写 `run_records`，更新任务的 `last_run_at`、`last_success_at`、`next_run_at`、`consecutive_failures`、`last_run_outcome`。基线运行成功后 `baseline_pending = 0`。
3. 更新账号的 `last_run_ended_at`，抽好下一次的 `next_gap_seconds`。关闭这个账号的 Chrome。
4. 成功时按 12.2 判断要不要生成 AI 行情解读，在后台进行，不占用调度。

整次运行（含判断）超过 `run_timeout_minutes` 就超时：停止抓取和判断，**已经保存的结果照常发通知**，本次记为失败（`timeout`）。

### 4.11 运行结果怎么算

| 情况 | 结果 | 计入失败保护 |
|---|---|---|
| 正常完成（包括详情额度用完、处理不完留到下次） | 成功 | — |
| 前置检查不满足、筛选未完成时额度用完 | 跳过 | 否 |
| 需要人机验证、直接拒绝 | 失败（`risk_verify` / `risk_deny`） | 否，按第 7 节处理账号 |
| 登录失效 | 跳过（`login`） | 否 |
| 用户取消、服务关闭 | 失败（`cancelled` / `restart`） | 否 |
| 网络或代理连不上、页面结构变化、筛选失败、响应超时、导航失败 | 失败（`network` / `page`） | 是 |
| 整次运行超时 | 失败（`timeout`） | 是 |

服务启动时，上次没结束的运行记录一律改为失败（`restart`）。

---

## 5. 分层判断

| 层 | 适用 | 输入 | 输出 | 通过条件 | 花费 |
|---|---|---|---|---|---|
| 第一层：硬条件和排除词 | 所有任务 | 搜索结果里的信息 | 通过，或没通过的原因 | 全部硬条件满足、标题不含排除词；关键词任务还要求标题命中匹配词（没填匹配词时不要求） | 免费 |
| 第二层：文字模型看标题 | AI 任务，可关闭 | 分析标准和搜索结果里的信息 | `{"pass", "reason"}` | `pass = true` | 便宜 |
| 第三层：多模态看图和描述 | AI 任务 | 分析标准、详情、卖家资料、商品图 | 匹配度、理由、风险、逐项结果等 | `match_score >= recommend_threshold` | 贵 |

### 5.1 第一层

输入是搜索结果里已有的字段：标题、价格、地区、卖家昵称、主图、发布时间、想要人数、标签。

逐条检查，第一条没通过的写进原因：

1. **价格**：设置了价格区间、商品有价格且不在区间里 → 「价格不在 x–y 元之间」。没有价格的不按价格排除。
2. **包邮**：要求包邮、标签里没有「包邮」 → 「不包邮」。
3. **发布时间**：设置了发布时间范围、商品有发布时间且早于范围 → 「发布超过 N 天」。
4. **地区**：设置了地区、商品的地区文字不为空，且不包含任务所选的省名或市名（去掉末尾的「省」「市」「自治区」「特别行政区」后比较） → 「不在所选地区」。地区文字为空时不排除。
5. **排除词**：标题命中任一排除词 → 「命中排除词：xx」。
6. **匹配词**（只对关键词任务，且填了匹配词）：标题没有命中任何匹配词 → 「标题没有命中匹配词」。

前四条是对页面筛选的二次确认：页面上的筛选有时没生效，本地再查一遍。「个人闲置」在本地判断不了，只靠页面筛选。

### 5.2 第二层

- 用服务商的 `text_model`，提示词是 8.6 的「标题初筛模板」。
- 用户消息：

  ````
  ## 分析标准
  <任务的 criteria>

  ## 商品信息（搜索列表）
  ```json
  <商品 JSON>
  ```
  ````

  商品 JSON（键名英文、按键名排序、中文不转义）：`title`、`price`（元，数字）、`area`、`published_at`（ISO 8601）、`tags`、`want_count`、`price_reference{min,median,max,sample_size}`（没有价格参考时省略）。
- 输出：`{"pass": true, "reason": "不超过 30 个字"}`。`pass` 不是布尔值时算解析失败。
- 原则是「拿不准就放行」：只有标题或价格明确违反「必须满足」时才不通过。第二层只用来省掉明显不对的商品的详情访问和多模态费用，宁可多放过，也不能错杀。

### 5.3 第三层

- 用服务商的 `vision_model`，提示词是 8.6 的「通用分析模板」。
- 消息结构：一条 `system` 消息放提示词；一条 `user` 消息，`content` 是数组，先放图片，每张是 `{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,..."}}`，最后放 `{"type":"text","text": ...}`。没有图片时 `content` 直接用字符串。
- 用户消息文字：

  ````
  ## 分析标准
  <任务的 criteria>

  ## 商品信息
  ```json
  <商品 JSON>
  ```
  ````

  没有图片时在末尾加一行：`补充说明：本次没有提供商品图片，请只根据文字信息判断，不要推测图片内容。`
- 商品 JSON（键名英文、按键名排序、中文不转义）：`title`、`price`、`original_price`、`description`、`published_at`、`area`、`tags`、`want_count`、`browse_count`、`url`、`price_reference{min,median,max,sample_size}`，以及 `seller{nickname,registration,zhima_level,seller_credit,buyer_credit,item_count,on_sale_count,sold_count,rate_count,seller_good_rate,buyer_good_rate,recent_rates[],recent_items[]}`。`registration` 写成「来闲鱼 X 年 Y 个月」。没有的字段省略。
- 图片：取详情图片的前 `ai.image_count` 张，同时最多下载 3 张，不带 Cookie，不走闲鱼账号的代理。最长边缩到 1024 像素，转成质量 80 的 JPEG，再编成 base64。下载或转换失败的图片直接跳过（包括 Go 解不了的格式）。不用搜索结果的缩略图代替。
- 输出和解析见 8.4。结论：`match_score >= recommend_threshold` 为 `recommended`，否则 `rejected`。以匹配度为准，`is_recommended` 和匹配度不一致时不看 `is_recommended`。

### 5.4 匹配度分级

| 匹配度 | 显示 |
|---|---|
| ≥ 85 | 强烈推荐（绿色） |
| 推荐门槛到 84 | 可以考虑（黄色） |
| 低于门槛 | 不推荐（红色） |

关键词任务没有匹配度，显示「关键词命中」。第一、二层得出的结论也没有匹配度，显示「标题初筛未通过」或第一层的原因。重新分析失败时，旧的匹配度不再显示，按「分析失败」显示。

### 5.5 重新分析

结果页的任意发现都可以「重新分析」（包括关键词判断、待处理和跳过的）：

- 用任务当前的标准，从第二层（AI 任务开了第二层时）开始重走一遍。
- 需要详情时：商品 `detail_fetched_at` 在 24 小时以内就用已有的详情，不打开闲鱼；否则作为一次手动运行排队（遵守第 6 节的全部限制），只处理这一件。
- 重新分析不发「发现推荐」通知，结论变成推荐后，之后的降价照常通知。

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
- 额度恢复的时间：每小时额度按滚动 60 分钟算，恢复时间 = 窗口里最早一次访问 + 60 分钟；每天额度在 `general.timezone` 的 0 点恢复。

额度只按 `quota_events` 计数，改时区、调系统时间、重启都不能凭空恢复额度。

### 6.2 调度器

- 一个常驻的调度循环：找出所有到了时间、通过前置检查的任务，按真正能开始的时间从早到晚排，手动请求排在最前面。
- 同时运行的任务数不超过 `crawl.global_pages`，同一个账号同时只跑一个任务（`crawl.pages_per_account`）。前面的任务因为账号被占着而不能跑时，后面别的账号的任务可以先跑，一个任务的长暂停不拖住其他任务。
- 没有到期的任务时，睡到最早能开始的时间；任务增删改、手动运行、账号状态变化、验证完成时提前唤醒。
- 算睡多久时，把冷却结束、失败暂停结束、额度恢复的时间都算进去，保证一结束就恢复。
- 服务重启后，过期的任务按能开始的时间依次运行，同一账号之间照样遵守最小间隔，不会集中一起跑。

### 6.3 失败保护

- 计入失败的见 4.11。同一任务连续失败 `crawl.failure_threshold` 次，`failure_paused_until = 现在 + failure_pause_minutes`，推送「[任务名] 连续失败 N 次，已暂停 30 分钟」。到时间自动恢复。
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

### 7.2 账号状态

| 情况 | 账号状态 | 这个账号上的任务 | 推送 |
|---|---|---|---|
| 新加的账号还没扫码 | `signed_out` | 不运行 | — |
| 扫码登录成功 | `active` | 正常运行 | — |
| 需要人机验证 | `needs_verification` | 全部暂停 | 「[账号名] 闲鱼需要验证」 |
| 用户完成验证 | `cooling`，见下 | 冷却期内不自动运行，可以手动运行一次 | — |
| 直接拒绝（`action=deny`） | `denied`，`cooldown_until = 现在 + deny_pause_hours` | 全部暂停，不能手动运行 | 「[账号名] 闲鱼拒绝访问，暂停 24 小时」 |
| 冷却或拒绝期结束 | `active` | 恢复 | — |
| 登录失效 | `expired` | 按 7.1 处理 | 「[账号名] 闲鱼登录已失效」 |
| 用户退出登录 | `signed_out` | 同登录失效 | — |

**验证完成后的冷却**（逐次加倍）：

1. 用户在远程画面里完成验证，点「我已完成验证」。服务端在**同一个浏览器**里重新检查一次风控信号，不另外打开页面。仍有验证就保持 `needs_verification`；发现登录失效就转为 `expired`。
2. 检查通过后算等级：上次冷却结束后 `verify_cooldown_reset_hours` 小时以内又触发了验证，`verify_level` 加 1；否则（或第一次）`verify_level = 1`。
3. 冷却时长 = `min(verify_cooldown_base_minutes × 2^(verify_level − 1), verify_cooldown_max_minutes)`。默认就是 2 小时、4 小时、8 小时……最长 24 小时。
4. `state = cooling`，`cooldown_until = 现在 + 冷却时长`，`cooldown_manual_used = 0`。冷却结束时写 `last_cooldown_end_at`。
5. 冷却期内用户手动运行一次后，`cooldown_manual_used = 1`，重启也不能重新获得这次机会。

账号状态变化都要推送实时事件（SSE），并唤醒调度器。

### 7.3 访问额度

- 每次访问闲鱼**之前**先申请：检查这个账号滚动 60 分钟内的访问数小于 `account_hourly_visits`，今天同类访问数小于对应的每日上限（`search` 对 `account_daily_search_visits`，`detail` 对 `account_daily_detail_visits`），都满足才写一条 `quota_events`，然后访问。先检查再计数。
- 访问失败也算一次，重试也算一次。
- 系统状态页显示每个账号今天和这一小时已用的额度。

### 7.4 代理

- 账号绑定的代理在这个账号的 Chrome 启动时生效，一直不变。没绑定就直连。
- 代理连不上算普通失败（`network`），计入失败保护，不换代理、不换账号。
- 「测试代理」只连代理服务器的 host:port（5 秒超时），不经过代理访问任何网站，不访问闲鱼。
- AI 专用代理（`ai.proxy_id`）只用于 AI 请求。地址写错时 AI 请求直接报错，不悄悄直连。

---

## 8. AI

### 8.1 请求

- 接口：`POST {base_url}/chat/completions`，请求头 `Authorization: Bearer <key>`（没填 Key 时不发这个头）、`Content-Type: application/json`。超时 `ai.request_timeout_seconds`。
- 所有用途共用一个信号量，同时最多 `ai.max_concurrent_requests` 个请求。兼容重试持有原来的名额。
- 回复文本取 `choices[0].message.content`；它是数组时把每段的 `text` 拼起来；为空时改取 `choices[0].message.reasoning_content`（参考上游 `src/services/ai_response_parser.py:28-42`）。再去掉闭合的 `<think>…</think>`。
- 每次请求写一条 `ai_calls`，token 数取返回的 `usage`。

各用途的参数：

| 用途 | 模型 | temperature | max_tokens | 结构化输出 |
|---|---|---|---|---|
| 第二层 `title_screen` | text_model | 0.1 | 200 | 有，见 8.2 |
| 第三层 `analysis` | vision_model | 0.1，解析重试时 0.05 | 2000 | 有 |
| 生成分析标准 `criteria` | text_model | 0.5 | 2000 | 不发 |
| 行情解读 `market` | text_model | 不发 | 400 | 不发 |
| 测试连接 `test` | 两个模型各测一次 | 不发 | 5 | 不发 |

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
  "required": ["pass", "reason"],
  "properties": {
    "pass": {"type": "boolean"},
    "reason": {"type": "string"}
  }
}
```

**第三层的 JSON Schema**（`name: "listing_analysis"`）：严格模式要求所有字段都在 `required` 里，所以可选的字段写成可以为 `null`，解析时 `null` 当作缺失。

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["match_score", "is_recommended", "reason", "risk_tags", "seller_type", "criteria",
               "specs", "seller_note", "trade_risk", "return_risk", "description_trust", "contact_tips"],
  "properties": {
    "match_score": {"type": "integer", "minimum": 0, "maximum": 100},
    "is_recommended": {"type": "boolean"},
    "reason": {"type": "string"},
    "risk_tags": {"type": "array", "items": {"type": "string"}},
    "seller_type": {"type": "string", "enum": ["personal", "dealer", "unknown"]},
    "criteria": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "status", "comment"],
        "properties": {
          "name": {"type": "string"},
          "status": {"type": "string", "enum": ["pass", "fail", "unknown"]},
          "comment": {"type": "string"}
        }
      }
    },
    "specs": {
      "type": ["array", "null"],
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["label", "value"],
        "properties": {"label": {"type": "string"}, "value": {"type": "string"}}
      }
    },
    "seller_note": {"type": ["string", "null"]},
    "trade_risk": {"type": ["string", "null"], "enum": ["low", "medium", "high", null]},
    "return_risk": {"anyOf": [{"$ref": "#/$defs/level"}, {"type": "null"}]},
    "description_trust": {"anyOf": [{"$ref": "#/$defs/level"}, {"type": "null"}]},
    "contact_tips": {"type": ["array", "null"], "items": {"type": "string"}}
  },
  "$defs": {
    "level": {
      "type": "object",
      "additionalProperties": false,
      "required": ["level", "comment"],
      "properties": {
        "level": {"type": "string", "enum": ["low", "medium", "high"]},
        "comment": {"type": "string"}
      }
    }
  }
}
```

### 8.3 其他兼容处理

和结构化输出一样，命中时调整参数、立刻重发，不计入解析重试，调整结果写进 `ai_compat`：

- 返回内容提到 `temperature`，并带有 `not supported`、`unsupported`、`invalid` 或「参数错误」→ 以后不发 `temperature`（上游 `src/services/ai_request_compat.py:160-168`）。
- 返回 400，内容同时提到 `max_tokens` 和 `max_completion_tokens` → 以后改用 `max_completion_tokens`。

不做 Responses API 回退：Kakatta 只支持 Chat Completions 接口，设置页写明这一点。

### 8.4 解析第三层的结果

1. 去掉 ```` ```json ```` 包裹；整段解析不了时，从第一个 `{` 开始找第一个能完整解析的 JSON 对象（上游 `src/services/ai_response_parser.py:47-53`、`100-118`）。
2. 必须满足：`is_recommended` 是布尔值；`reason` 是非空字符串；`risk_tags` 是字符串数组；`criteria` 是数组，每项有 `name`、`status`（`pass` / `fail` / `unknown`）和 `comment`。不满足就算解析失败。
3. `match_score` 缺失或不是 0–100 的数字（字符串、越界、布尔）时，按 `is_recommended` 推出：`true` → 75，`false` → 30，并写一条日志。小数四舍五入。
4. `seller_type` 缺失或不认识时按 `unknown`。
5. `specs`、`seller_note`、`trade_risk`、`return_risk`、`description_trust`、`contact_tips` 都是可选的：缺失、为 `null` 或格式不对时只丢掉那一项，不算解析失败。等级只认 `low` / `medium` / `high`，参数最多保留 6 项，建议最多保留 3 条。这几项存进 `insights_json`。用户自定义的提示词没要这些字段时，详情页对应部分不显示。

第二层的解析：同样去掉包裹、找第一个对象，`pass` 必须是布尔值。

### 8.5 出错重试和换服务商

一次判断（第二层或第三层）按下面的规则用服务商：

1. 候选：`enabled = 1`、`error_state` 为空的服务商，按 `sort_index` 从小到大。
2. 对当前服务商：
   - 兼容调整（8.2、8.3）后重发，不算重试。
   - 返回成功但解析失败：同一个服务商再试一次（第三层用 temperature 0.05）。还是失败就换下一个。
   - 401、403、402、返回内容说余额不足、404 模型不存在、400 且内容说模型不支持图片：写 `error_state`，新建 `ai_error` 系统事件并推送，换下一个。
   - 429、5xx、网络错误、超时：换下一个，不在同一个服务商上立刻重试。
3. 全部失败：发现记为 `failed`，`reason` 写最后一个错误的说明（遮掉 Key）。只要有一个服务商的失败属于临时错误（429、5xx、网络、超时），`retryable = 1`；否则为 0。`attempts` 达到 3 后不再自动重试，界面显示「分析失败（已放弃）」，仍可手动重新分析。
4. 有任何一次请求成功，就解决这个服务商的 `ai_error` 事件。

生成分析标准、行情解读、测试连接不换服务商：生成标准和行情解读用第一个可用的服务商，测试连接只测被点的那个。

### 8.6 提示词原文

四份模板都可以在 Prompt 页编辑和还原（2.4）。每个 AI 任务的分析标准也在 Prompt 页列出，直接改 `tasks.criteria`。保存后下一次运行生效。

保存前检查：通用分析模板必须带着带引号的键名 `"match_score"`、`"is_recommended"`、`"reason"`、`"risk_tags"`、`"criteria"`；标题初筛模板必须带着 `"pass"`、`"reason"`。缺了就不能保存，并说明缺哪个。

**通用分析模板**（第三层的 system 消息）：

```
你是一名谨慎的二手交易顾问，帮用户判断闲鱼上的一件商品值不值得联系购买。
你会收到用户的「分析标准」、商品信息（JSON）以及若干商品图片。

判断规则：
1. 严格按照分析标准逐条判断。「必须满足」中任何一条不满足，就不推荐。
2. 信息缺失时，把对应检查项标为 unknown，不要猜测或编造。
3. 结合图片核对标题和描述是否一致，例如成色、型号、配件。
4. 特别留意这些风险：疑似商家或回收商（大量同类在售、话术模板化）、翻新或维修、描述含糊、价格明显低于本次搜索的中位数、要求站外交易或先付定金。
5. 卖家信息只作参考，不要因为信息少就直接否定。
6. match_score 是 0 到 100 的整数，表示商品符合分析标准的程度，要和逐项结果一致：必须满足项有 fail 时不超过 49；全部通过且没有危险信号时不低于 85。
7. specs 从标题、描述和图片里提取最多 6 项买家最关心的参数（例如成色、配置、电池、购买时间、保修、配件），看不出来的不要写。
8. seller_note 说明判断卖家类型的依据，例如在售和已售数量、品类是否分散、是否批量出售同款。
9. trade_risk 是这笔交易的整体风险；return_risk 根据卖家的历史评价判断收货后压价、拒绝售后等纠纷的可能；description_trust 判断图文是否一致、描述是否坦诚。信息不足以判断的项省略。
10. contact_tips 写最多 3 条联系卖家前值得确认或注意的事，没有就给空数组。

只输出一个 JSON 对象，不要输出任何其他文字，格式如下：
{
  "match_score": 85,
  "is_recommended": true,
  "reason": "一句话结论，不超过 60 个字",
  "risk_tags": ["不超过 8 个字的风险标签"],
  "seller_type": "personal 或 dealer 或 unknown",
  "criteria": [
    {"name": "检查项名称，不超过 10 个字", "status": "pass 或 fail 或 unknown", "comment": "不超过 40 个字的说明"}
  ],
  "specs": [{"label": "参数名，不超过 6 个字", "value": "不超过 20 个字"}],
  "seller_note": "不超过 50 个字",
  "trade_risk": "low 或 medium 或 high",
  "return_risk": {"level": "low 或 medium 或 high", "comment": "不超过 50 个字"},
  "description_trust": {"level": "low 或 medium 或 high", "comment": "不超过 50 个字"},
  "contact_tips": ["不超过 30 个字"]
}
```

第 9 条「信息不足以判断的项省略」在 JSON Schema 模式下就是填 `null`。

**标题初筛模板**（第二层的 system 消息）：

```
你是一名二手交易顾问，帮用户在闲鱼搜索结果里做第一轮粗筛。
你会收到用户的「分析标准」和一件商品在搜索列表里的信息（JSON）：只有标题、价格、地区、发布时间和标签，没有描述和图片。

判断规则：
1. 只有当标题或价格已经明确违反「必须满足」中的某一条时，才判为不通过。例如要求 NVMe 而标题写着 SATA，要求 16G 内存而标题写着 8G。
2. 标题没有提到的条件，一律当作还不知道，判为通过，留到下一轮看详情和图片时再判断。
3. 不要因为价格偏低、标题简短或信息少就判为不通过。
4. 拿不准时判为通过。

只输出一个 JSON 对象，不要输出任何其他文字，格式如下：
{"pass": true, "reason": "一句话说明，不超过 30 个字"}
```

**生成标准模板**（生成分析标准的 system 消息）：

```
你是一名二手交易顾问。用户会用大白话描述想在闲鱼上买什么，请把它整理成一份「分析标准」，之后会用它逐个判断商品。

要求：
1. 使用简体中文纯文本，不要使用 Markdown 代码块，不要寒暄。
2. 分成三部分，每部分逐条列出，每条一行，以「- 」开头：
   【必须满足】硬性条件，任何一条不满足就不推荐。
   【加分项】让商品更值得买的特征。
   【危险信号】需要警惕的情况，例如疑似商家、翻新、描述含糊、价格异常、要求站外交易。
3. 每一条都要具体，并且能从商品标题、描述、图片或卖家信息中判断出来。
4. 用户没有提到、但这类商品常见的关键检查项（例如电子产品的成色、电池、维修史、配件），也要补充进去。
5. 总长度不超过 800 字。
```

用户消息：`我想买：<keyword>\n我的要求：<requirement>`。返回后去掉闭合的 `<think>…</think>`；还有没闭合的 `<think>`，或去掉后没有正文，就报「生成被思考过程截断，请换一个模型或重试」。界面可以取消生成，15 秒后显示「仍在生成…」。编辑任务时改了「要求」，提示「需求已修改，是否重新生成分析标准？」，选项是「重新生成」「保留当前标准」。

**行情解读模板**（12.2）：

```
你是一名二手市场分析师。你会收到某个闲鱼搜索任务近 14 天的价格统计（JSON）。
请用简体中文写一段不超过 120 字的行情解读：说明价格走势、最集中的价格带，以及什么价位和条件的商品值得优先联系。
只输出这段话，不要标题、列表、Markdown 或寒暄。
```

### 8.7 测试连接

- 文字测试：发 `Reply with OK`，`max_tokens: 5`，返回 2xx 就算成功。
- 图片测试：带一张程序内置的 16×16 PNG 再发一次。返回 400 且内容提到图片时，提示「这个模型不支持图片，不能用于第三层」。
- 失败时显示 HTTP 状态码和返回内容的前 200 字（先遮掉 Key）。成功时清空 `error_state`。

---

## 9. 关键词判断

参考上游 `src/keyword_rule_engine.py`，有三处不同（见下面标注）。

- **规整**：NFKC 统一全角半角（上游没有），转小写，连续空白合并成一个空格。
- **规则输入**：按换行、英文逗号、中文逗号拆分，去掉首尾空白，规整后去重。
- **匹配方式**：只由 `[a-z0-9 ]` 组成的词按完整单词匹配，正则 `(?<![a-z0-9])词(?![a-z0-9])`，避免 `q1` 命中 `q1r5`（上游 `keyword_rule_engine.py:61-69`）；其他词按子串匹配。
- **匹配的文本**：第一层只有标题；打开详情后是标题加描述。不包括卖家的其他商品和评价，避免误命中（上游会把卖家信息也算进去，`keyword_rule_engine.py:37-46`）。
- **结论**（打开详情后的最终判断）：
  1. 命中任一排除词 → `rejected`，原因「命中排除词：xx」。排除词优先于匹配词。
  2. 否则命中任一匹配词 → `recommended`，原因「命中 N 个关键词：a、b」。
  3. 没填匹配词、也没被排除 → `recommended`，原因「没有排除词命中」（上游这种情况判为不推荐）。
- 结果卡片和详情显示命中了哪些词（`matched_keywords_json`）。

---

## 10. 价格历史与降价

参考上游 `src/services/price_history_service.py` 的价格解析（`parse_price_value`），但不照搬它按关键词存快照的方式。

- **追加价格点**：每次搜到商品，有价格并且和最近一个价格点不同（或还没有价格点）时追加。比较的是最近的价格点，不是可能已被别的任务更新过的商品快照。
- **降价标签**：新价格低于上一个价格点时，`last_drop_amount = 上一个价格 − 新价格`，商品打「降价」标签并显示降了多少。之后价格不变时保留标签；涨价时清空。
- **降价通知**（唯一规则）：同时满足下面几条才通知：
  - 发现的 `decision = recommended`（包括基线发现被分析成推荐的）；
  - 任务开了 `notify_price_drop`，通知策略不是 `none`；
  - 商品没被标为不感兴趣，卖家没被屏蔽；
  - 当前价格低于基准价。基准价是 `last_drop_notified_price`，为空时取 `recommended_price`。

  发送成功后把 `last_drop_notified_price` 更新为当前价格，记 `drop_notified_at`。不要求这一轮一定新写了降价点：别的任务可能已经记下了同样的现价。

  例子：推荐时 ¥100 → ¥90 通知 → ¥110 不通知 → ¥95 不通知 → ¥85 通知。另一个例子：推荐时 ¥100 → ¥120 → ¥110 不通知（110 不低于基准 100）。
- **降价金额**：通知标题里的金额 = 基准价 − 当前价格。

---

## 11. 不感兴趣、屏蔽卖家、收藏、删除结果

- **不感兴趣**：标在商品上（`listings.is_dismissed`），对所有任务生效。之后不再发任何通知（包括降价），仍然记录价格；别的任务第一次搜到它时直接 `skipped`，不分析。从列表里隐藏，可以在「已隐藏」里撤销。
- **屏蔽卖家**：写进 `blocked_sellers`。之后所有任务都跳过这个卖家的商品：已知卖家 ID 的新商品在第一层之前就跳过；全新商品要打开详情拿到卖家 ID 后才能判断，命中就立刻停止，不打开卖家主页、不判断、不通知。已有的发现从列表隐藏。可以在设置里取消屏蔽。
- **收藏**：只在 Kakatta 里收藏（`listings.is_favorite`），不碰闲鱼的收藏。收藏的商品和它的发现不受保留期清理。删除任务会删掉任务的发现，收藏的商品本身保留在「收藏」里。
- **删除结果**：
  - 删除全部：删除这个任务的全部发现，收藏商品的发现保留；`seen_items` 不动，老商品不会再变成新商品。没被引用、没收藏的商品之后由清理删掉。
  - 单个删除：删掉这一条发现。商品被收藏的话收藏仍在。
- **已读**：打开详情就标为已读，可以手动标为未读。基线发现创建时就是已读。

---

## 12. 市场行情

### 12.1 统计口径

输入：本任务 `task_sightings` 里近 14 天的记录（每个商品每天一个价格，就是当天最后一次看到的价格），包括没推荐、第一层没通过的。

| 指标 | 算法 |
|---|---|
| 当前均价 | 「当前那一天」样本的均价。当前那一天 = 今天；今天没有样本就取最近有样本的一天 |
| 较 14 天前涨跌 | 和窗口内最早有样本那一天的均价比，写百分比 |
| 中位数 | 当前那一天的样本，附样本数 |
| 主流价格带 | 当前那一天样本的 P25–P75，线性插值 |
| 历史最低、最高 | 14 天内所有样本的最低和最高，附日期；那个商品 7 天没再被任何任务搜到时注明「已售出」 |
| 每日走势 | 每天的均价和中位数 |
| 今日新增、今日推荐 | 今天创建的发现数、其中推荐的数 |
| 个人卖家占比 | 近 14 天有 `seller_type` 的发现里 `personal` 的比例 |
| 快照时间 | 最近一次 sighting 的时间 |

概览「任务表现」里的近 7 天走势用同一套算法的最近 7 个自然日，没有样本的日子不拿更早的数据凑。

### 12.2 AI 行情解读

一次运行**成功结束**后，同时满足下面几条就在后台生成一次：

- 有可用的 AI 服务商；
- 今天的样本数不少于 10；
- 距离这个任务上次生成至少 6 小时。

请求：text_model，system 是行情解读模板，user 是 12.1 的统计 JSON，不带图片。成功写进 `market_insights`；失败只写日志，旧的解读保留。没配置 AI 时结果页不显示这一块。

---

## 13. 通知

### 13.1 通知内容

| 字段 | 内容 |
|---|---|
| title | `[任务名] 发现推荐`；`all_new` 策略下的非推荐为 `[任务名] 新商品`；降价为 `[任务名] 降价 ¥xx` |
| item_title | 商品标题前 40 字 |
| body | `¥价格 · 理由`（理由是 AI 的一句话结论、关键词命中说明或标题初筛理由） |
| desktop_url | 网页版链接 |
| mobile_url | `https://pages.goofish.com/sharexy?loadingVisible=false&bft=item&bfs=idlepc.item&spm=a21ybx.item.0.0&bfp=` 加上 URL 编码后的 `{"id":<商品 ID>}`（上游 `src/utils.py:105-114`） |
| link | 按 `notify.link_target` 选 `mobile_url` 或 `desktop_url`；选中的为空时用另一个 |
| image_url | 商品主图 |
| kakatta_url | `general.public_url` 不为空时，指向 Kakatta 里这条发现的地址 |

价格显示成 `¥6,850`，整数不显示小数。

### 13.2 发哪些

- `recommended`：通知推荐的发现。
- `all_new`：通知所有判断完的新发现（推荐和不推荐，包括第一、二层没通过的）；`skipped` 和 `failed` 不通知。
- `none`：不通知，降价也不通知。
- 基线发现永远不发「发现推荐」和「新商品」通知，即使它们因为额度不足留到之后的运行才分析；但基线发现被分析成推荐后，之后的降价照常通知。
- 「分析失败」不通知。

### 13.3 合并发送

- 同一个任务一次运行最多发 `notify.max_per_run` 条（默认 5）。
- 排序：推荐按匹配度从高到低（关键词任务按发布时间从新到旧），然后是降价按降幅从大到小，最后是 `all_new` 的非推荐。
- 超出时发前 `max_per_run − 1` 条，再加一条合并消息，合计不超过上限：标题 `[任务名] 还有 N 个推荐`（被合并的里面有降价或非推荐时写「还有 N 条提醒」），正文列出被合并商品的标题（每个一行，最多 10 行），链接用 `kakatta_url` 指向这个任务的结果页，没设置 `public_url` 时用被合并的第一个商品的链接。合并消息发送成功后，被合并的发现都记为已通知。

### 13.4 发送规则

- 发到任务勾选的渠道（`task_channels`）里启用的那些。
- 渠道之间同时发，同一渠道内按顺序发。企业微信和钉钉每个机器人每分钟最多 20 条，所以同一渠道两条之间至少隔 3 秒；飞书至少隔 1 秒。
- 任一渠道发送成功就算已通知，写 `notified_at`。全部失败的保持未通知，下次运行再发。每次发送写一条 `deliveries`。
- 失败只记日志，不影响其他渠道和运行结果。错误信息只写固定格式、带渠道名的说明，不写含 Token 的地址、请求头或响应正文。
- 先保存已经成功的送达标记，再处理取消，避免部分成功后重复推送。
- 「发送测试」用固定的示例消息，标题「Kakatta 测试通知」，带一张示例商品图地址和一个闲鱼首页链接。

### 13.5 系统事件推送

服务端在后台运行，没人看着，所以这些事要推送出去。发到 `notify.system_channel_ids`（空 = 所有启用的渠道），同一件事没解决之前只推一次（2.11 `system_events`）。

| 事件 | 标题 | 正文 |
|---|---|---|
| 需要人机验证 | `[账号名] 闲鱼需要验证` | `这个账号上的 N 个任务已暂停。请打开 Kakatta，在远程画面里完成验证。` |
| 直接拒绝访问 | `[账号名] 闲鱼拒绝访问` | `这个账号暂停 24 小时，到 HH:mm 自动恢复。` |
| 登录失效 | `[账号名] 闲鱼登录已失效` | `请打开 Kakatta 重新扫码登录。`（自动分配的任务已换到别的账号时加一句「N 个任务已换到 xx」） |
| AI 接口出错 | `AI 接口出错：服务商名` | 错误说明，如「API Key 无效」「余额不足」 |
| 任务连续失败暂停 | `[任务名] 连续失败 N 次` | `已暂停 30 分钟，最近一次的原因：xx` |

系统事件没有商品图，链接是 `kakatta_url`（账号页、AI 设置页或任务的日志），没设置 `public_url` 时不带链接。

### 13.6 各渠道的请求格式

下文「正文」指 `item_title + 换行 + body`。超时除注明的外都是 10 秒。

**Bark**（照上游 `notification_clients/bark_client.py`；官方文档：<https://bark.day.app/#/tutorial>）

- 配置：推送地址，如 `https://api.day.app/<key>`（加密保存）。
- 请求：`POST` 到推送地址，`Content-Type: application/json; charset=utf-8`：

  ```json
  {"title": "<title>", "body": "<正文>", "url": "<link>", "group": "Kakatta", "icon": "<image_url>", "level": "timeSensitive"}
  ```

  没有图片时不带 `icon`。
- 成功：2xx，并且返回里有 `code` 字段时 `code == 200`。

**Telegram**（参考上游 `notification_clients/telegram_client.py`；官方文档：<https://core.telegram.org/bots/api#sendmessage>、<https://core.telegram.org/bots/api#sendphoto>）

- 配置：Bot Token（加密）、Chat ID、API 地址（默认 `https://api.telegram.org`，可以填自建的转发地址）。
- 内容（HTML）：

  ```
  <b>{title}</b>
  <b>{商品标题前 50 字}</b>
  {body}
  <a href="{link}">在闲鱼打开</a>
  ```

  所有插入的文字都要做 HTML 转义（`&`、`<`、`>`，链接里再转义 `"`）。上游没有转义，标题里有 `<` 时会发送失败。
- 有图片：`POST {api}/bot{token}/sendPhoto`，`{"chat_id", "photo": "<image_url>", "caption": "<内容>", "parse_mode": "HTML"}`。caption 最多 1024 字，按 UTF-16 长度保守计算，超出时只截短商品标题，标签、价格、理由和链接保留完整。
- 没有图片：`POST {api}/bot{token}/sendMessage`，`{"chat_id", "text": "<内容>", "parse_mode": "HTML"}`，最多 4096 字。
- 成功：返回的 `ok == true`。

**ntfy**（参考上游 `notification_clients/ntfy_client.py`；官方文档：<https://docs.ntfy.sh/publish/>）

- 配置：Topic 地址（如 `https://ntfy.sh/<topic>`，加密）、访问令牌（可选，加密）。
- 请求：`POST` 到 Topic 地址，请求体是纯文本正文（UTF-8），请求头：
  - `Title: <title>`：含中文时按 RFC 2047 编成 `=?UTF-8?B?…?=`，每段不超过 75 字符、不拆开一个字。官方文档说明 ntfy 支持这种编码；上游直接把 UTF-8 字节放进请求头，有的 HTTP 库会把它变成问号。
  - `Priority: urgent`（和上游一致）、`Tags: bell`、`Click: <link>`；有图片时 `Attach: <image_url>`；填了访问令牌时 `Authorization: Bearer <token>`。
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

  没有图片时不带 `bigImageUrl`。和上游不同：Token 放请求头，不放 `?token=`，避免出现在访问日志和代理记录里；请求体用 JSON，不用表单。
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

**Webhook**（照上游 `notification_clients/webhook_client.py`）

- 配置：地址（加密）、方法 `GET` / `POST`、请求头（JSON 对象，加密）、查询参数（JSON 对象）、内容类型 `JSON` / `FORM`、请求体模板（JSON）。
- 占位符写成 `{{name}}`，也认上游的 `${name}`（`webhook_client.py:145-146`），方便从上游迁移：

  | 占位符 | 内容 |
  |---|---|
  | `title` | 通知标题 |
  | `content` | 商品标题、`¥价格 · 理由`、链接，各一行 |
  | `price` | 价格文字 |
  | `reason` | 理由 |
  | `desktop_link` / `mobile_link` | 网页版和手机端链接（和上游含义相同，不跟随 `link_target`） |
  | `link` | 按 `link_target` 选好的链接 |
  | `item_title` / `image` / `task` / `kind` | 商品标题、主图、任务名、消息类型 |

- 替换在**解析后的 JSON 结构**里进行：只替换字符串值，再整体编码，所以值里的引号、换行不会弄坏 JSON。查询参数替换后做 URL 编码，和地址里原有的参数合并。
- `GET` 只发地址；`POST` + `JSON` 发 JSON 请求体，没写 `Content-Type` 时加 `application/json; charset=utf-8`；`POST` + `FORM` 要求模板是 JSON 对象，按表单编码，没写时加 `application/x-www-form-urlencoded`。
- 超时 15 秒，成功：2xx。

**邮件（SMTP）**（上游没有；标准：[RFC 5321](https://www.rfc-editor.org/rfc/rfc5321)、[RFC 5322](https://www.rfc-editor.org/rfc/rfc5322)、[RFC 2047](https://www.rfc-editor.org/rfc/rfc2047)、[RFC 3207](https://www.rfc-editor.org/rfc/rfc3207)、[RFC 8314](https://www.rfc-editor.org/rfc/rfc8314)；Go 标准库 [`net/smtp`](https://pkg.go.dev/net/smtp)）

- 配置：SMTP 服务器、端口、加密方式（`ssl` 直接 TLS，通常 465 / `starttls`，通常 587 / `none`）、用户名、密码或授权码（加密）、发件人地址、收件人（可以多个，逗号分隔）。
- 认证用 PLAIN，只在 TLS 连接上发送；选了 `none` 时不认证（只给局域网里的转发服务用）。
- 邮件：`Subject` 是通知标题，按 RFC 2047 编码；`From`、`To`、`Date`、`Message-ID` 齐全；`Content-Type: multipart/alternative`，一份纯文本（正文和链接各一行），一份 HTML（标题加粗、正文、宽 300 像素的商品图、「在闲鱼打开」链接，插入的文字做 HTML 转义），都用 UTF-8。
- 超时 30 秒。成功：服务器接受了 `DATA`。

---

## 14. 数据保留与清理

启动时清理一次，之后每 `cleanup.interval_hours` 一次。

| 数据 | 规则 |
|---|---|
| 发现 | 超过 `retention.findings_days` 天的删除（0 = 永久）；商品被收藏的发现不删 |
| 商品 | 没有发现引用、没收藏、`last_seen_at` 超过 `retention.orphan_listing_days` 天的删除，价格点一并删除 |
| 行情明细 `task_sightings` | 超过 `retention.stats_days` 天的删除 |
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

**清除全部数据**（设置 → 通用）：删除任务、发现、商品、价格、见过、行情、日志、统计、卖家缓存、屏蔽卖家和图片缓存。保留登录密码和令牌、闲鱼账号和它们的登录状态、AI 服务商、推送渠道、代理、设置和提示词。二次确认，要求输入「清除」。

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
      "requirement": "M3 芯片，16G 内存，电池健康 90% 以上，个人卖家，不要维修过的",
      "criteria": "【必须满足】\n- ...",
      "title_screening": true,
      "recommend_threshold": 70,
      "match_keywords": [],
      "exclude_keywords": ["回收", "维修机"],
      "analyze_on_first_run": true,
      "notify_policy": "recommended",
      "notify_price_drop": true,
      "channels": [{"type": "telegram", "name": "Telegram · 家人群"}]
    }
  ]
}
```

- 价格的单位是元（可以有小数）。导出时分页读完全部任务，不漏。
- 不导出：闲鱼账号（换了机器账号不同）、运行状态、搜索代次、发现。

导入规则：

1. `format` 不是 `kakatta.tasks` → 整份拒绝。`version` 比当前支持的高 → 整份拒绝，避免读错新字段。
2. 每个任务单独解码和校验，一个任务出错不影响其他任务。
3. 每个字段都可以省略，按新建表单的默认值补齐。
4. 逐个**跳过**（写明原因）：关键词为空或超过 50 字；最低价高于最高价或为负数；发布时间不是认识的值；地区在地区数据里选不到；运行间隔不是正整数；字段类型不对。
5. 自动**修正**（写明改了什么）：名称为空用关键词，超过 30 字截断；翻页数限制到 1–3；推荐门槛取最近的一档（50 / 60 / 70 / 80 / 90）；`crawl_overrides` 里不认识的键去掉、明显无效的值（0、负数、下限大于上限）去掉；渠道按「类型 + 名称」匹配本机已有的渠道，匹配不上的去掉（写了空列表就是不推送，没写这个字段就用默认：不推送）；AI 任务没有分析标准或要求不到 10 个字时改成关键词判断。
6. 运行间隔、等待时间这类数值超出推荐范围时照样导入，在结果里提示，和表单的处理一样（`DECISIONS.md` 第 17 条）。
7. 本机没有可用 AI 服务商时，AI 任务照样导入，运行时记跳过「没有配置 AI」，结果里提示。
8. 导入的任务和表单新建的一样：自动分配账号、`baseline_pending = 1`、立即进入调度。
9. 导入完成后提示：导入了几个、修正了哪些、跳过了哪几个和原因。中途保存出错时，已经存下的任务照常运行，提示里写明导入了几个。

### 15.2 导出 CSV

- 导出当前任务、当前筛选下的全部发现，分批读取，不受界面分页影响。
- 列：标题、价格、匹配度、结论、理由、卖家、地区、发现时间、链接。价格写成不带货币符号的数字；链接用网页版地址。
- UTF-8 带 BOM（方便 Excel 打开），按 RFC 4180 转义，CRLF 换行。默认文件名「任务名-日期.csv」。

### 15.3 导出运行日志

导出当前筛选下的全部运行记录和逐步日志，UTF-8 文本，每行一条，字段用制表符分隔。

### 15.4 全量备份和恢复

用户换机器、重装时用。

- **包含**：数据库里的全部业务数据（任务、发现、商品、价格、见过、行情、日志、统计、卖家缓存、屏蔽卖家、收藏、设置、提示词、AI 服务商、推送渠道、代理、闲鱼账号的名称和代理绑定、登录 Kakatta 的用户名和密码哈希），以及加密主密钥（否则 Key 和 Token 解不开）。
- **不包含**：闲鱼登录状态（各账号的浏览器配置目录）、会话和令牌、额度记录、图片缓存。
- **格式**：一个 zip 文件，里面两个文件：
  - `manifest.json`：`{"format": "kakatta.backup", "version": 1, "app_version": "...", "schema_version": N, "created_at": "...", "kdf": {"name": "argon2id", "salt": "...", "time": 3, "memory_kib": 65536, "threads": 1}}`；
  - `payload.bin`：用备份密码经 Argon2id 派生的密钥做 AES-256-GCM 加密的 tar，里面是用 `VACUUM INTO` 生成的数据库快照（已删掉不包含的表的内容）和 `secret.key`。
- 导出时用户必须设一个备份密码，因为文件里有能解开所有 Key 的主密钥。
- **恢复**：上传文件并输入备份密码 → 检查格式和版本（`schema_version` 比当前高就拒绝，低就恢复后照常迁移）→ 二次确认「会覆盖这台服务端上的全部数据」→ 停止调度和所有运行 → 替换数据库和主密钥 → 所有闲鱼账号改为 `signed_out`（要重新扫码），所有会话和令牌失效（用备份里的密码重新登录）→ 重启调度。失败时保留原来的数据不动。

---

## 16. 统计口径

监控概览可以切换今天 / 近 7 天 / 近 30 天。

- **比较**：今天对比昨天 0 点到昨天此刻；近 7 天对比再往前 7 天；近 30 天超出保存期，不显示比较。前一段少于 50 时写差值，否则写百分比。
- **统计卡**：扫描商品（`run_records.scanned_count` 之和）、新上架（`new_count` 之和）、推荐（推荐发现数，按 AI 和关键词拆分，有 `analysis_model` 的算 AI）、降价提醒（发出的降价通知数，按 `drop_notified_at`）。
- **检查活动**：按小时（今天）或按天的新上架和已见过数量。「今天」按实际小时数分桶，夏令时那天是 23 或 25 根柱子。
- **任务状态**：监控中（启用、在排队或等下次）、检查中、出错（最近一次失败或失败暂停）、已暂停（停用，或账号需要验证、已失效、被拒绝）。附检查成功率、平均每轮耗时、下次检查时间。
- **任务表现**：每个任务的扫描数、推荐数、推荐率、当前最低价、近 7 天均价走势（12.1）。
- **AI 匹配度分布**：有匹配度的发现按商品去重（同一商品被几个任务分析过只算最近一次），重新分析失败的不算。
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
| 判断 | 一次多模态判断 | 三层：硬条件和排除词 → 文字模型看标题 → 多模态 | `DECISIONS.md` 第 20 条 |
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
| 推送渠道 | 系统通知加 6 种，每种只能配一个 | 10 种，同一种可以配多个，不同任务推到不同渠道；加了系统事件推送 | `PRODUCT.md` 第 7 节 |
| 系统通知 | macOS 通知中心，带操作按钮 | 服务端没有；客户端以后再定 | `PRODUCT.md` 第 12 节 |
| 运行时段 | 「运行时段」，时段外不运行 | 「不运行的时段」，默认 01:00–07:30 | `CRAWL-POLICY.md` 第六节 |
| 睡眠和唤醒 | Mac 睡眠时取消运行，唤醒后等 60 秒 | 服务端常驻，没有睡眠；重启时把未结束的运行记为失败 | 部署方式不同 |
| 不感兴趣 | 不再通知 | 不再通知，别的任务搜到时直接跳过、不分析 | 省 AI 费用和详情额度 |
| 市场行情统计 | 从发现、商品和价格点反推每天的价格 | 单独一张 `task_sightings` 表记每个任务每天看到的价格 | 价格不变的商品也能每天算一个样本，计算简单 |
| 删除结果 | 只能删全部 | 可以删全部，也可以单个删 | `PRODUCT.md` 第 6 节 |
| 备份 | 没有 | 加密的全量备份和恢复，不含闲鱼登录状态 | `PRODUCT.md` 第 11 节 |
| 登录 Kakatta | 不需要 | 一个用户名加密码，网页会话和客户端令牌可以查看和吊销 | `DECISIONS.md` 第 13、19 条 |
