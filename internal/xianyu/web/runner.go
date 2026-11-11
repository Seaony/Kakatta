package web

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
)

// Site 是闲鱼网页和接口的地址。测试时换成本地的假闲鱼。
type Site struct {
	Home    string // 首页，比如 https://www.goofish.com/
	APIHost string // 接口所在的主机，比如 h5api.m.goofish.com
}

// Goofish 是真实闲鱼的地址（config.py:22-23）。
func Goofish() Site {
	return Site{Home: "https://www.goofish.com/", APIHost: "h5api.m.goofish.com"}
}

// goofishHome 是搜索结果里商品链接的前缀（xianyu.WebLink）。
const goofishHome = "https://www.goofish.com/"

// itemURL 检查商品链接确实是闲鱼的商品页，再换到当前站点上（真实闲鱼不变，测试时换成本地假闲鱼）。
// 从接口数据里解析出来的链接不能把浏览器带到别的网站。
func (s Site) itemURL(webURL string) (string, error) {
	rest, ok := strings.CutPrefix(webURL, goofishHome)
	if !ok || !strings.HasPrefix(rest, "item?") {
		return "", errors.New("不是闲鱼的商品链接")
	}
	return s.Home + rest, nil
}

// 要读的接口（XIANYU.md 2.1 节）。
func (s Site) searchAPI() browser.Match {
	return browser.Match{URL: s.APIHost + rules.API.Search, Method: "POST"}
}

func (s Site) detailAPI() browser.Match {
	return browser.Match{URL: s.APIHost + rules.API.Detail}
}

// mtop 匹配所有闲鱼接口，用来做风控检查。
func (s Site) mtop() browser.Match { return browser.Match{URL: rules.API.Mtop} }

// Pace 是一次运行里各种等待的区间，都来自配置项（CRAWL-POLICY 第六节）。
type Pace struct {
	HomeStay  pacer.Range // 首页停留
	Action    pacer.Range // 普通操作之间等待
	PageWait  pacer.Range // 页与页之间等待
	PreDetail pacer.Range // 打开详情前等待
	Detail    pacer.Range // 看完详情后停留
}

// ErrQuota 表示访问额度用完了。不是风控，这次运行到此为止，已经拿到的结果照常处理。
var ErrQuota = errors.New("访问额度已用完")

// Budget 在每次会让闲鱼产生请求的动作之前申请一次额度；用完时返回包着 ErrQuota 的错误。
type Budget interface {
	Acquire(kind quota.Kind, what string) error
}

// Logger 记录每一步做了什么，给实验和运行日志用。平时不记录正文和 Cookie；解析失败仅交给本机诊断回调。
type Logger func(event string, fields map[string]any)

// Runner 在一个已经启动的浏览器里执行闲鱼上的操作。
type Runner struct {
	S        *browser.Session
	H        *browser.Human
	P        *pacer.Pacer
	Site     Site
	Pace     Pace
	Budget   Budget
	Log      Logger
	Diagnose func(context.Context, Diagnostic) error

	since             uint64 // 风控检查从这里开始查，见 checkResponses
	handledRisks      map[uint64]handledRisk
	observationMu     sync.Mutex
	observed          map[string]string
	observedFrameURL  string
	loginBoxSeen      bool
	loginNavMark      uint64
	loginRequestMark  uint64
	searchNickHints   int
	detailCreditHints int
	ownCountHints     int

	// SearchInput 为空时自动找；固定属性选择器仍待真实确认（XIANYU.md 3.6 节）。
	SearchInput string
}

// NewRunner 创建 Runner，并让浏览器开始记录所有闲鱼接口。
func NewRunner(s *browser.Session, p *pacer.Pacer, keyDelay pacer.Range, site Site, pace Pace, budget Budget, log Logger) *Runner {
	s.Capture.Watch("/h5/mtop.")
	if log == nil {
		log = func(string, map[string]any) {}
	}
	return &Runner{S: s, H: browser.NewHuman(s, p, keyDelay), P: p, Site: site, Pace: pace, Budget: budget, Log: log, since: s.Capture.Mark()}
}

// acquire 在访问前检查接口和当前页面，再申请额度。
func (r *Runner) acquire(ctx context.Context, kind quota.Kind, what string) error {
	if err := r.check(ctx, what+"之前"); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if r.Budget == nil {
		return nil
	}
	return r.Budget.Acquire(kind, what)
}

// wait 按区间随机等待，并记下这一步实际等了多久。
func (r *Runner) wait(ctx context.Context, what string, rg pacer.Range) error {
	start := time.Now()
	err := r.watch(ctx, what, func(waitCtx context.Context) error { return r.P.Wait(waitCtx, rg) })
	r.Log("等待", map[string]any{"步骤": what, "毫秒": time.Since(start).Milliseconds()})
	return err
}

// 等待期间接口仍可能返回风控，检查和后续页面操作在同一个调用里串行执行。
func (r *Runner) watch(ctx context.Context, where string, wait func(context.Context) error) error {
	return r.watchChecked(ctx, func(checkCtx context.Context) error { return r.check(checkCtx, where) }, wait)
}

// Guard 在耗时处理期间继续检查风控，停止后等处理真正结束。
func (r *Runner) Guard(ctx context.Context, where string, work func(context.Context) error) error {
	return r.watch(ctx, where, work)
}

func (r *Runner) watchChecked(ctx context.Context, check func(context.Context) error, wait func(context.Context) error) error {
	if err := check(ctx); err != nil {
		return err
	}
	waitCtx, cancel := context.WithCancel(ctx)
	completed := make(chan error, 1)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		completed <- wait(waitCtx)
	}()
	defer func() {
		cancel()
		<-workerDone
	}()
	for {
		changed := r.S.Capture.Changes()
		if err := check(ctx); err != nil {
			return err
		}
		select {
		case err := <-completed:
			if checkErr := check(ctx); checkErr != nil {
				return checkErr
			}
			return err
		case <-changed:
		case <-ctx.Done():
			return contextError(ctx)
		}
	}
}

func contextError(ctx context.Context) error {
	err := ctx.Err()
	if cause := context.Cause(ctx); err != nil && !errors.Is(err, cause) {
		return errors.Join(err, cause)
	}
	return err
}

func (r *Runner) navigate(ctx context.Context, target, where string, timeout time.Duration) error {
	return r.watch(ctx, where, func(navCtx context.Context) error {
		_, err := r.S.NavigateContext(navCtx, target, timeout)
		return err
	})
}
