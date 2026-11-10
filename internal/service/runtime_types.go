package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"time"

	"github.com/Seaony/Kakatta/internal/analysis/client"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/remote"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

type Dependencies struct {
	DB           *sql.DB
	ReadSnapshot func(context.Context, func(*sql.Tx) error) error
	Settings     *appsettings.Store
	Cipher       *security.Cipher
	DataDir      string
	Resources    ResourceFactory
	Models       Models
	Notifier     Notifier
	ImageCache   ImageCache
	Publish      func(kind string, reason *locales.Message, data json.RawMessage) error
	Now          func() time.Time // 默认 time.Now；记账仍经持久 Clock 保护。
	Pacer        *pacer.Pacer     // 默认真实等待；测试注入同一随机与等待来源。
}

// Open 失败必须关闭部分资源并返回 nil；成功后全部资源所有权移交 Service。
type ResourceFactory interface {
	Open(context.Context, ResourceSpec) (SessionResource, error)
}
type ResourceSpec struct {
	AccountID  int64
	ProfileDir string
	Proxy      *url.URL // 已解密的固定代理，仅内存使用，不可记日志。
	Timezone   string
	CacheMB    int
	Pacer      *pacer.Pacer
	Typing     pacer.Range
	Pace       web.Pace
	Budget     web.Budget
	Log        web.Logger
}

// Runner 与 Chrome 必须属于同一页面；不另建 DOM 操作接口。
// M3 完整假页回归复用现有 Runner；M4 提供正式资源工厂，生产不会接假工厂。
type SessionResource interface {
	Runner() *web.Runner
	Chrome() *browser.Session
	OpenRemote(context.Context) (*remote.Session, error) // 幂等，按需开启。
	Close() error                                        // 幂等，等 remote/Chrome/proxy/display 全部关闭才返回。
}

// 直接兼容既有 client.Client；用途、校验、结果、错误与用量不再包第二套。
// 无模型/日限分别返回 client.ErrNoProvider/ErrDailyLimit，基础设施失败不得轮换。
type Models interface {
	Complete(context.Context, client.Request) (client.Result, error)
}

// FlushRun 在重算之后接收本次首次结论；自行用短 Tx 判定/发送/保存，不接外层 Tx。
// 实现须核当前代次/屏蔽状态、保存成功送达再响应取消；错误不得假写已通知。
// M6 接真实适配；M3 注入可断言调用/取消/失败的 fake，不提供成功空实现。
type Notifier interface {
	FlushRun(ctx context.Context, ref RunRef, firstConclusionIDs []int64) (NotifyResult, error)
	System(ctx context.Context, eventID int64) error
	Quiesce(ctx context.Context) (resume func(), err error)
}
type NotifyResult struct {
	NotifiedCount int64
	Warnings      []locales.Message
}

type ImageCache interface {
	Prune(ctx context.Context, maxBytes int64) (int64, error)
	Clear(ctx context.Context) (int64, error)
}
