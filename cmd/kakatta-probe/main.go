// kakatta-probe 是动工前用真实闲鱼账号做实验的命令行程序，规格见 docs/EXPERIMENT.md。
// 它不调用 AI、不发推送、不写数据库，只把每一步写进本地的 JSONL 日志。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/pacer"
	"github.com/Seaony/Kakatta/internal/quota"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

const usage = `用法：
  kakatta-probe login [参数]   打开浏览器，自己扫码登录闲鱼
  kakatta-probe run -keyword 关键词 [参数]   按间隔循环搜索，出现任何风控立即退出
  kakatta-probe check [参数]   只打开首页，看登录还在不在

每个子命令加 -h 查看参数。
退出码：0 正常结束，1 出错，2 触发风控，3 登录失效。`

// 退出码
const (
	exitOK    = 0
	exitError = 1
	exitRisk  = 2
	exitLogin = 3
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(exitError)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var code int
	switch os.Args[1] {
	case "login":
		code = cmdLogin(ctx, os.Args[2:])
	case "run":
		code = cmdRun(ctx, os.Args[2:])
	case "check":
		code = cmdCheck(ctx, os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		code = exitError
	}
	os.Exit(code)
}

// common 是三个子命令都有的参数和准备工作。
type common struct {
	site        web.Site
	dir         string
	chrome      string
	proxy       string
	searchInput string
	limits      quota.Limits
	pace        web.Pace
	keyDelay    pacer.Range

	j     *journal
	state *state
}

func (c *common) register(fs *flag.FlagSet) {
	c.site = web.Goofish()
	home, _ := os.UserHomeDir()
	fs.StringVar(&c.dir, "dir", filepath.Join(home, ".kakatta-probe"), "数据目录：浏览器配置、日志和状态文件都放在这里")
	fs.StringVar(&c.chrome, "chrome", "", "Chrome 或 Edge 的路径，留空自动查找")
	fs.StringVar(&c.proxy, "proxy", "", "代理地址，留空直连")
	fs.StringVar(&c.searchInput, "search-input", "", "首页搜索框的 CSS 选择器，留空自动找页面顶部最宽的输入框")
	// 默认值见 CRAWL-POLICY 第六节。
	fs.IntVar(&c.limits.Hourly, "hourly", 30, "每小时访问上限（滚动 60 分钟）")
	fs.IntVar(&c.limits.DailySearch, "daily-search", 200, "每天搜索类访问上限")
	fs.IntVar(&c.limits.DailyDetail, "daily-detail", 100, "每天详情类访问上限")
	c.pace = web.Pace{
		HomeStay:  pacer.Between(2*time.Second, 5*time.Second),
		Action:    pacer.Between(time.Second, 3*time.Second),
		PageWait:  pacer.Between(10*time.Second, 15*time.Second),
		PreDetail: pacer.Between(3*time.Second, 6*time.Second),
		Detail:    pacer.Between(10*time.Second, 20*time.Second),
	}
	c.keyDelay = pacer.Between(50*time.Millisecond, 130*time.Millisecond)
	fs.Var((*rangeFlag)(&c.pace.HomeStay), "home-stay", "首页停留")
	fs.Var((*rangeFlag)(&c.pace.Action), "action-wait", "普通操作之间等待")
	fs.Var((*rangeFlag)(&c.pace.PageWait), "page-wait", "页与页之间等待")
	fs.Var((*rangeFlag)(&c.pace.PreDetail), "pre-detail", "打开详情前等待")
	fs.Var((*rangeFlag)(&c.pace.Detail), "detail-stay", "看完详情后停留")
	fs.Var((*rangeFlag)(&c.keyDelay), "key-delay", "搜索框输入每个字的间隔")
}

// prepare 建目录、打开日志、读状态文件。
func (c *common) prepare() error {
	if err := os.MkdirAll(filepath.Join(c.dir, "profile"), 0o700); err != nil {
		return err
	}
	j, err := openJournal(filepath.Join(c.dir, "probe.jsonl"))
	if err != nil {
		return err
	}
	c.j = j
	s, err := loadState(filepath.Join(c.dir, "state.json"), c.limits)
	if err != nil {
		return err
	}
	s.log = j.log
	c.state = s
	return nil
}

func (c *common) profileDir() string { return filepath.Join(c.dir, "profile") }

// open 启动浏览器并创建 Runner。log 是这次要用的记录函数。
func (c *common) open(ctx context.Context, log func(string, map[string]any)) (*browser.Session, *web.Runner, error) {
	s, err := browser.Start(ctx, browser.Options{ExecPath: c.chrome, ProfileDir: c.profileDir(), Proxy: c.proxy})
	if err != nil {
		return nil, nil, err
	}
	r := web.NewRunner(s, pacer.New(nil), c.keyDelay, c.site, c.pace, c.state, log)
	r.SearchInput = c.searchInput
	return s, r, nil
}

// exitFor 把错误换成退出码，并写一条结束记录。
func (c *common) exitFor(err error, log func(string, map[string]any)) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, web.ErrLoginExpired):
		log("登录失效，程序结束", map[string]any{"原因": err.Error()})
		return exitLogin
	case web.IsStop(err):
		var re *web.RiskError
		f := map[string]any{"原因": err.Error()}
		if errors.As(err, &re) {
			f["步骤"], f["信号"] = re.Where, re.Signal
		}
		log("触发风控，程序结束", f)
		return exitRisk
	case errors.Is(err, context.Canceled):
		log("手动停止", nil)
		return exitOK
	}
	log("出错，程序结束", map[string]any{"原因": err.Error()})
	return exitError
}

// rangeFlag 让命令行用「2s-5s」这样的写法设置随机等待区间。
type rangeFlag pacer.Range

func (r *rangeFlag) String() string {
	if r == nil {
		return ""
	}
	return fmt.Sprintf("%s-%s", r.Min, r.Max)
}

func (r *rangeFlag) Set(v string) error {
	a, b, ok := strings.Cut(v, "-")
	if !ok {
		return fmt.Errorf("要写成「最小-最大」，比如 2s-5s")
	}
	lo, err := time.ParseDuration(a)
	if err != nil {
		return err
	}
	hi, err := time.ParseDuration(b)
	if err != nil {
		return err
	}
	if lo <= 0 || hi < lo {
		return fmt.Errorf("区间不对：%s", v)
	}
	*r = rangeFlag(pacer.Between(lo, hi))
	return nil
}
