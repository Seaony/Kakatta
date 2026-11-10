# 模块接口约定

这是实现分工的约定，产品规则仍以 `DOMAIN.md` 和有效的 `DECISIONS.md` 为准。接口调整时先核对调用方，同一次修改一起更新，不能各维护一套同义结构。依赖文件由集成方统一修改；模块只用本地假服务测试，不访问真实闲鱼、通知供应商或收费模型。

## 数据与消息

- 业务时间用 UTC Unix 毫秒，金额用整数分。接口字段用小写蛇形命名，未取得的值保留 `null`。
- 可翻译的原因统一是 `{"code":"...","args":{}}`。空参数也输出对象，不写成已翻译的句子；模型和卖家原话不翻译。
- 唯一字典目录是 `internal/locales/`，文件为 `zh-CN.json`、`en.json`，键为平铺的点分字符串，占位符为 `{name}`。网页直接导入同一份文件。语言标识固定为 `zh-CN`、`en`，设置另有 `system`。
- 有数量变化时使用 `_one`、`_other` 两个键，两语言保留同一键集；中文两种可以同文案。服务端和网页使用同一 `count` 参数选择形式，不把参数里的花括号再次解释成模板。

```go
package locales

type Language string
type Message struct {
    Code string         `json:"code"`
    Args map[string]any `json:"args"`
}
func Select(preference, systemLocale string) Language
func Format(language Language, message Message) (string, error)
func JSON(language Language) ([]byte, error)
```

未知代码和缺参数是错误，不显示残缺模板。字典及公开返回的字节、映射都要独立复制，不能改坏下一次读取。

## 安全计算与存储

`internal/security` 不读环境、文件、数据库，也不管理 HTTP。启动层选择密钥来源；存储层完成会话、吊销和验证码消费事务。

```go
func HashPassword(ctx context.Context, password string) (string, error)
func VerifyPassword(ctx context.Context, password, phc string) (bool, error)
func NewMasterKey() []byte
func DecodeMasterKey(encoded string) ([]byte, error)
func NewCipher(masterKey []byte) (*Cipher, error)
func (c *Cipher) Seal(plaintext []byte, table, column, rowKey string) ([]byte, error)
func (c *Cipher) Open(blob []byte, table, column, rowKey string) ([]byte, error)
func NewToken() (plaintext string, hash [32]byte)
func HashToken(plaintext string) [32]byte
func TokenMatches(plaintext string, expectedHash [32]byte) bool
func NewTOTPSecret() string
func TOTPURI(secret, accountLabel string) (string, error)
func VerifyTOTP(secret, code string, now time.Time, lastAcceptedStep int64) (step int64, ok bool, err error)
```

- 密码为固定 Argon2id PHC，64 MiB、3 轮、4 路、16 字节盐、32 字节结果。Hash/Verify 共用一个进程内计算名额，排队可取消；运行中取消不能提前释放名额。
- 主密钥为 32 字节。默认文件及指定文件保存原始字节，环境变量使用标准 Base64。显式来源错误不回退；已有数据库但默认密钥缺失时拒绝自动补造。
- AES-GCM 自带随机 nonce，保持 `12 字节 nonce + 密文与 tag` 格式。AAD 严格为 `表:列:不可变键`，字段非空且不含冒号。错误不回显输入。
- TOTP 为 SHA1、6 位、30 秒、前后一步。匹配步必须大于已使用步，存储层在创建会话的同一事务中条件更新 `admin.totp_last_step`；失败不发 Cookie。
- 初始化与第二步挑战只在内存，分别 30 分钟和 5 分钟；网页登录按 30 天闲置和 90 天绝对期限处理，API 令牌直到吊销。各用途分别生成随机值，不复用令牌。

## 设置

`internal/settings` 只做定义、合并和校验。全部 50 项和 9 项任务覆盖来自 `DOMAIN.md` 第 3 节，不自创配置项。

```go
type Values map[string]json.RawMessage
type Environment struct {
    TZ string
    TrustedProxies *string
}
type Source string // default / stored / environment
type Range struct { Min, Max float64 }
type QuietHours struct { Enabled bool; Start, End string }
type Issue struct { Key string; Reason locales.Message }
type ValidationError struct { Issues []Issue }
type Snapshot struct {
    Values Values
    Sources map[string]Source
    Warnings []Issue
}
func Definitions(env Environment) []Definition
func Resolve(stored Values, env Environment) (Snapshot, error)
func Apply(current Snapshot, patch Values) (Snapshot, error)
func ResolveTask(global Snapshot, intervalMinutes int64, overrides json.RawMessage) (Values, []Issue, error)
```

公开字段统一加蛇形 JSON 标签。`Definition` 提供键、组、类型、默认值、单位、可选值、推荐范围、标签、理由、风险和任务覆盖标记；推荐范围以外只警告，不改用户输入。保存整个 patch 原子校验，失败不返回部分结果。

- `TZ` 只决定默认时区，不锁定设置；明确存在的 `KAKATTA_TRUSTED_PROXIES` 覆盖存储值并只读，空串表示空列表。
- 整数、单位换算、组合抖动和区间均检查溢出；时间严格为 `HH:mm`，不靠非规范字符串比较。仅明确允许的项目可为零。
- `ai.image_count` 只要求正整数，推荐 1–4；不能因超过推荐值拒绝或截回 4，不新增自动减图策略。
- 保存时区时，存储层在同一事务里写旧日窗口、第 64 条最高观察时间和新值；启动时有效默认时区变化同样处理。保存失败不发布新快照。
- 代理和渠道引用在存储事务内核对，纯包不能假装已检查数据库存在性。

## HTTP 和网页类型

### 业务服务和事务

`internal/service` 保存任务、商品、发现、账号、运行、行情和清理的唯一公开类型，HTTP 不复制一套同义对象。同包按职责拆文件，用具名事务函数组合，不引入通用仓储层。依赖只向下，业务包不导入 HTTP 包；保存成功后才发布变化事件、取消旧代运行和唤醒调度。

```go
func (s *appsettings.Store) SnapshotTx(ctx context.Context, tx *sql.Tx) (settings.Snapshot, error)
func (s eventstore.Store) RaiseTx(ctx context.Context, tx *sql.Tx, kind string, subjectID int64, message locales.Message) (bool, error)
func (s eventstore.Store) ResolveTx(ctx context.Context, tx *sql.Tx, kind string, subjectID int64) (bool, error)
func config.AcquireServerLock(dataDir string) (io.Closer, error)
```

这些事务入口不提交、不再申请连接；事务打开者唯一负责提交。`SnapshotTx` 复用现有设置读取与校验，不观察或保存时钟，额度动作仍在同一事务显式 `ObserveClock`、记账和 `SaveClock`。事件和账号状态同事务保存，回滚时一并回滚。正式服务在打开数据库和恢复状态前取得数据目录独占锁，持有到资源和数据库全部关闭；维护命令不拿服务锁，保留既定的跨进程凭据恢复能力。

- 时间为 UTC 毫秒，ID 为正 `int64`，金额为整数分；行情的分位数、中位数和变化额保留 `float64` 分，走势百分比为比率。未知值为 `null`，数组为 `[]`。
- 写入保留字段是否出现：创建缺省用领域默认，更新缺省保留，只有可空字段接受 `null`；服务唯一解码和校验，不在 HTTP 再做一套默认。
- 所有列表使用 `Page<T>{items,next_cursor,total}`，默认 50、最大 200，游标绑定筛选和排序。全量导出使用完整读快照，不受分页上限截断。
- 搜索代次每次写入都检查，晚到结果只更新仍存在的对象，不用 UPSERT 复活已删除数据；非搜索规则提交时重读当前值。
- 资源的排队、启动、保留和关闭由一个所有者管理。关闭完成才释放名额、结算账号间隔并删除恢复标记；排队不预造运行记录、不消耗人工会话十分钟。
- 门禁无法通过等待恢复时不编造恢复时间；所需入口访问数大于配置上限时，自动等待配置变化，手动记跳过且预计时间为空。

### 传输与基础网页

唯一接口描述是 `api/openapi.json`，格式为 OpenAPI 3.1。Go 嵌入该文件，网页从仓库文件生成类型，不从运行中的服务抓取描述。业务路径统一以 `/api/v1` 开头；`/healthz`、`/mcp` 和远程画面另有固定边界。

失败响应统一为：

```json
{
  "error": {"code": "auth.invalid_credentials", "args": {}},
  "fields": [],
  "request_id": "..."
}
```

`fields` 的元素为 `{"key":"...","reason":{"code":"...","args":{}}}`。成功响应直接返回描述里的对象，不另包一层 `data`。Cookie 会话和 Bearer 令牌共用同一鉴权；登录、初始化和验证码协议端点按各自凭据校验，其他业务、文档、SSE、WebSocket 和远程资产均鉴权。浏览器写操作统一经过 `http.CrossOriginProtection`，不另造 CSRF 令牌协议。

M2 的边界如下，字段由同一描述约束：

- `/api/v1/setup`：读取初始化状态、以一次性令牌建立管理员；不公开用户名或已存配置。
- `/api/v1/auth/login`、`/api/v1/auth/totp`、`/api/v1/auth/logout`：密码、第二步挑战、退出；`/api/v1/me` 返回当前身份和界面语言、外观。
- `/api/v1/settings`、`/api/v1/settings/definitions`：设置快照、原子保存和元数据。
- `/api/v1/security/tokens`、`/api/v1/security/tokens/{id}`：设备和令牌列表、新建令牌、吊销；已存令牌只给元数据，新建成功才返回一次明文。
- `/api/v1/security/password`、`/api/v1/security/totp/setup`、`/api/v1/security/totp/enable`、`/api/v1/security/totp/disable`：密码与两步验证动作。新绑定密钥只在本次绑定响应中返回，验证码通过才落库。
- `/api/v1/events`：SSE；数据以事件类型和原因代码表达，保活、慢客户端和断线恢复由服务端统一处理。已吊销会话不能继续接收业务数据。
- `/api/v1/openapi.json`：已登录用户读取完整接口描述。所有接口均在文档里显式列出认证要求。

网页基础层跟随这些约定：

- `createApiClient({fetch?})` 使用同源地址和 Cookie，取消信号贯穿请求；HTTP 状态、原因和字段错误只在一个入口归一。
- 单一 QueryClient 和 RouterContext，不隐式重试写操作，也不为聚焦窗口自动增加业务请求。
- 语言使用 `zh-CN`、`en`；i18next 配置平铺键和单层花括号占位符，英文复数与 Go 侧一致。
- 构建目标为 Safari 16.0，使用 Tailwind 3.4；颜色、字号、间距和圆角均由 CSS 变量提供。Radix 行为包在项目组件里，页面只组合组件。
- 生成文件提交，生成检查在临时目录比较；生成器说明用简短中文。产物输出 `web/dist`，Go 嵌入与 SPA fallback 由集成层负责。API、健康检查、文档、MCP、远程画面和不存在的静态资产不能被首页吞掉。
- 基础阶段不编造业务页面或直连外部服务；组件按标签、键盘、焦点、错误与取消行为测试。开发服务器由作者启动，测试服务有明确期限并负责关闭。

## 通知适配边界

`internal/notify` 一次只发送一条已经组装好的消息，不自行读库、排队、合并或重试。发送判定、全局渠道间隔、持久结果和取消后的成功保存由业务服务统一处理。

```go
type Type string
type ChannelConfig struct {
    Type Type
    Name string
    Config json.RawMessage
    Secrets json.RawMessage
}
type Message struct {
    Title, ItemTitle, Body string
    Link, DesktopLink, MobileLink, KakattaURL, ImageURL string
    Price, Reason, Score, Grade, Config, FairPrice string
    ItemID, Task, Kind string
    Urgent bool
}
type Result struct {
    OK bool
    ErrorCode string
    HTTPStatus int
    ProviderCode string
}
func New(deps Dependencies) *Sender
func Validate(config ChannelConfig) error
func (s *Sender) Send(ctx context.Context, config ChannelConfig, message Message) Result
```

依赖只注入 HTTP 客户端、本机连接、TLS 配置、时钟和固定服务端点，用于本地假服务测试，不增加用户配置。结果不含地址、Token 或响应正文；适配器按第 13 节的长度和业务状态判断成功。格式化层负责在渠道限制内优先缩短规格，不让适配器猜标题结构或静默丢弃理由、价格和链接。

## 纯分析规则

`internal/analysis` 只接收已经取得的数据，不读库、不联网、不自行取当前时间。类型保持 `DOMAIN.md` 的 JSON：`Profile`、生成清单的独立 `GeneratedProfile`、`Facts`、`ItemSpecs`、`UserOverrides`；金额为整数分，可空字段用指针，模型原话不翻译。以下入口固定，具体输入结构只含对应规则实际使用的数据：

```go
func RiskDictionary() []RiskDefinition
func ResolveRisks(Profile) []ResolvedRisk
func DefaultProfile() Profile
func ParseGeneratedProfile([]byte) (GeneratedProfile, []locales.Message, error)
func MergeGeneratedProfile(Profile, GeneratedProfile, string, string) (Profile, ProfileSuggestions, []locales.Message, error)
func NormalizeProfile(Profile) (Profile, []locales.Message, error)
func ReviseProfile(Profile, Profile) (Profile, []locales.Message, error)
func ParseKeywords(string) []string
func MatchingKeywords(string, []string) []string
func FirstLayer(FirstLayerInput) LayerResult
func KeywordKind(string) Kind
func JudgeKeyword(string, string, []string, []string) LayerResult
func ParseTitleScreen([]byte, int) (TitleScreen, []locales.Message, error)
func ResolveSpecs(Profile, []SpecValue, *bool, SpecSource) ItemSpecs
func JudgeTitle(Profile, TitleItem) TitleResult
func ParseFacts([]byte, Profile, EvidenceOriginals) (Facts, []locales.Message, error)
func JudgeSeller(SellerInput, []SellerSignal, string) SellerResult
func Score(ScoreInput) ScoreResult
func ContactQuestions(Profile, []EffectiveCheck) []ContactQuestion
func CalculateMarket(MarketInput) MarketResult
func AdjacentPrices(Profile, ItemSpecs, int64, []MarketGroup) []AdjacentPrice
func NoGoodReasons([]RecentScoredFinding) []ReasonCount
```

清单合并按生成项的顺序替换同名旧编辑项，再按旧相对顺序追加未命中的旧编辑项；不得重用删除的编号。`breakdown` 固定为 `base/items/ai/caps/gates`，程序原因用 Message，模型调整理由保留原文。普通保存不补回被删的通用项；用户规格保护、重分析与通知由编排处理。关键词、证据、规格分别按文档的三套规则规整，不共用一个含糊的文本匹配器。

普通保存调用 NormalizeProfile，不冒用重新生成入口。持久层先守住旧 next_no 水位和既有条目的固定编号，不能相信请求把水位调小；新增空编号由纯包分配。标题整理结果携带独立 Warnings，未知或重复规格不能悄悄吞掉。

`Requirement`、`ProfileRisk`、`RequirementFact`、`CheckFact` 增加正整数 `rev`。手动风险覆盖的值为 `RiskOverride{Status CheckStatus, Rev int64}`。`ReviseProfile(old, proposed)` 在规范化后按固定编号比较判断文字，保留或增加服务端版本，新增项为 1；不能使用请求里的版本回拨计数。生成合并沿用同一规则。模型响应格式不加内部版本，由 ParseFacts 按请求清单补齐；评分只采用与当前清单匹配的事实和覆盖，旧对象不被修改。

没有规格维度的任务使用固定配置键 `*`，并非规格不明；「较 14 天前」只用原配置 14 天序列，价格带跟随合理价实际样本窗口。最终合理价落到整数分时四舍五入，每日中位数和分位数保留插值精度。

## 模型请求

`internal/analysis/client` 不依赖分析规则、数据库、抓取或业务 HTTP。`New(Dependencies)` 必须取得 `Repository`、`Accounting`、全应用共享的 `Slots`、专用 `HTTPDoer` 和超时；普通调用 `Complete(ctx, Request)`，试分析 `Test(ctx, providerID, titleRequest, analysisRequest)`，恢复探测 `Probe(ctx, providerID)`。Request 仅传用途、任务编号、提示词、图片、结构化 Schema 和纯 Validate 回调，不开放跳过每日上限的参数。

- Repository 提供 `Providers/Provider/LoadCompat/SaveCompat/ChangeProvider`；ProviderRef 的 `ID/ConfigToken` 防止旧请求写坏新配置。兼容保存原子地只向降级方向合并并返回实际保存值，地址或模型变化后的重置归存储层。
- Accounting 的 `Before(ctx, CallStart) (Ticket,error)` 紧邻每次物理请求，检查每日上限并返回内存票据才发送，不新增持久票据；`After(ctx, Ticket, CallEnd)` 每票据恰好一次，在请求结束后写一条 ai_calls，用有界且不随用户取消的上下文收尾。usage 按每次请求计；reasoning 已含在输出，不再加一次。After 写入失败后停止本进程全部后续模型请求，不把存储失败当供应商错误轮换。进程取得 usage 前崩溃按缺失 usage 处理，不封整天或增加未知用量状态。
- `CallStart` 含服务商配置引用、模型、purpose、TaskID 和实际 JSONMode；`CallEnd` 含可用结果 OK、HTTPStatus、ErrorKind、Usage、FinishReason、Duration。Result 含文本、服务商/模型、JSONMode、Thinking、耗时与全部 Call 元数据，不含密钥或请求正文。
- Slots 的 `Acquire(ctx) (release,error)` 覆盖整个逻辑调用，所有用途共享；兼容重发、解析重试和轮换不新建名额池。每次实际请求重新 Before，只有 test 可越过每日上限。probe 固定输出上限 5。
- 返回可识别的 `ErrNoProvider/ErrDailyLimit/ErrInfrastructure/ErrConfigChanged`；普通失败为带 Last 原因和 Retryable 的 Failure。取消保留原 context 错误；记账、兼容和状态存储失败立即结束，不重发。请求状态与重试严格按第 8 节，不隐含 SDK 重试或 3xx 跳转。

## 集成分工

- 安全计算：`internal/security/`。
- 设置和唯一字典：`internal/settings/`、`internal/locales/`；其他模块新增文案时先交给该目录的维护方合并。
- 数据目录、密钥文件、SQLite 迁移与备份、认证事务、HTTP、事件流、进程入口：集成层。
- 网页基础：`web/`；不修改 Go、字典、部署和接口描述。
- 通知适配：按文件分组，共用同一包接口；HTTP 公共代码和格式化先由集成层固定。
- 分析纯规则和模型请求层分别定义，不互相读库；用量、兼容状态和服务商错误状态经明确的存储接口交接。

各模块交回前跑本模块测试；集成后统一构建、类型检查、全部测试和本地回归。模块产出不能用跳过的集成测试冒充已验证。
