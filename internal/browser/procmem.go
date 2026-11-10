package browser

import (
	"github.com/chromedp/chromedp"
)

// proc 是一个进程的父进程号和常驻内存（字节）。
type proc struct {
	pid, ppid int
	rss       int64
}

// PID 返回浏览器主进程的进程号，拿不到时返回 0。
func (s *Session) PID() int {
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil || c.Browser.Process() == nil {
		return 0
	}
	return c.Browser.Process().Pid
}

// TreeMemory 返回进程 root 和它所有子孙进程的常驻内存之和（字节）。Chrome 的渲染、GPU 等进程都是主进程的子进程。
func TreeMemory(root int) (int64, error) {
	procs, err := listProcs()
	if err != nil {
		return 0, err
	}
	return sumTree(root, procs), nil
}

func sumTree(root int, procs []proc) int64 {
	children := map[int][]proc{}
	var total int64
	for _, p := range procs {
		children[p.ppid] = append(children[p.ppid], p)
		if p.pid == root {
			total += p.rss
		}
	}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, c := range children[pid] {
			if c.pid == root {
				continue
			}
			total += c.rss
			queue = append(queue, c.pid)
		}
	}
	return total
}
