package web

import (
	"context"
	"errors"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
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

// 要读的接口（XIANYU.md 2.1 节）。
func (s Site) searchAPI() browser.Match {
	return browser.Match{URL: s.APIHost + "/h5/mtop.taobao.idlemtopsearch.pc.search/1.0/", Method: "POST"}
}

func (s Site) detailAPI() browser.Match {
	return browser.Match{URL: s.APIHost + "/h5/mtop.taobao.idle.pc.detail"}
}

// mtop 匹配所有闲鱼接口，用来做风控检查。
func (s Site) mtop() browser.Match { return browser.Match{URL: "/h5/mtop."} }

// Pace 是一次运行里各种等待的区间，都来自配置项（CRAWL-POLICY 第六节）。
type Pace struct {
	HomeStay  pacer.Range // 首页停留
	Action    pacer.Range // 普通操作之间等待
	PageWait  pacer.Range // 页与页之间等待
	PreDetail pacer.Range // 打开详情前等待
	Detail    pacer.Range // 看完详情后停留
}

// VisitKind 是访问额度的种类（DOMAIN.md 第 0 节）。
type VisitKind string

const (
	VisitSearch VisitKind = "search" // 首页、搜索、筛选、翻页
	VisitDetail VisitKind = "detail" // 详情页、卖家主页、自己的主页
)

// ErrQuota 表示访问额度用完了。不是风控，这次运行到此为止，已经拿到的结果照常处理。
var ErrQuota = errors.New("访问额度已用完")

// Budget 在每次会让闲鱼产生请求的动作之前申请一次额度；用完时返回包着 ErrQuota 的错误。
type Budget interface {
	Acquire(kind VisitKind, what string) error
}

// Logger 记录每一步做了什么，给实验和运行日志用。不能记录接口正文和 Cookie。
type Logger func(event string, fields map[string]any)

// Runner 在一个已经启动的浏览器里执行闲鱼上的操作。
type Runner struct {
	S      *browser.Session
	H      *browser.Human
	P      *pacer.Pacer
	Site   Site
	Pace   Pace
	Budget Budget
	Log    Logger

	// SearchInput 是首页搜索框的选择器；为空时自动找（XIANYU.md 3.6 节：还没用真实页面确认）。
	SearchInput string
}

// NewRunner 创建 Runner，并让浏览器开始记录所有闲鱼接口。
func NewRunner(s *browser.Session, p *pacer.Pacer, keyDelay pacer.Range, site Site, pace Pace, budget Budget, log Logger) *Runner {
	s.Capture.Watch("/h5/mtop.")
	if log == nil {
		log = func(string, map[string]any) {}
	}
	return &Runner{S: s, H: browser.NewHuman(s, p, keyDelay), P: p, Site: site, Pace: pace, Budget: budget, Log: log}
}

func (r *Runner) acquire(kind VisitKind, what string) error {
	if r.Budget == nil {
		return nil
	}
	return r.Budget.Acquire(kind, what)
}

func (r *Runner) wait(ctx context.Context, rg pacer.Range) error { return r.P.Wait(ctx, rg) }

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }
