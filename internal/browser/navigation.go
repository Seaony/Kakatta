package browser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

type navigation struct {
	seq    uint64
	frame  cdp.FrameID
	loader cdp.LoaderID
	url    string
	ready  bool
}

type navigationState struct {
	sync.Mutex
	seq     uint64
	entries []navigation
	changed chan struct{}
}

func newNavigationState() *navigationState {
	return &navigationState{changed: make(chan struct{})}
}

func (n *navigationState) event(ev any) {
	n.Lock()
	defer n.Unlock()
	switch e := ev.(type) {
	case *page.EventFrameNavigated:
		if e.Frame == nil || e.Frame.ParentID != "" {
			return
		}
		n.seq++
		n.entries = append(n.entries, navigation{seq: n.seq, frame: e.Frame.ID, loader: e.Frame.LoaderID, url: e.Frame.URL + e.Frame.URLFragment})

	case *page.EventLifecycleEvent:
		if e.Name != "DOMContentLoaded" {
			return
		}
		for i := range n.entries {
			if n.entries[i].frame == e.FrameID && n.entries[i].loader == e.LoaderID {
				n.entries[i].ready = true
			}
		}
	case *page.EventNavigatedWithinDocument:
		if len(n.entries) == 0 || n.entries[len(n.entries)-1].frame != e.FrameID {
			return
		}
		latest := n.entries[len(n.entries)-1]
		n.seq++
		latest.seq, latest.url = n.seq, e.URL
		n.entries = append(n.entries, latest)
	default:
		return
	}
	if len(n.entries) > maxKept {
		copy(n.entries, n.entries[1:])
		n.entries = n.entries[:maxKept]
	}
	close(n.changed)
	n.changed = make(chan struct{})
}

// NavigationMark 在点击或回车之前记下主框架的导航位置。
func (s *Session) NavigationMark() uint64 {
	s.navigation.Lock()
	defer s.navigation.Unlock()
	return s.navigation.seq
}

// WaitNavigation 只等操作后主框架对应的 DOMContentLoaded，不把 iframe 或旧事件当作结果。
func (s *Session) WaitNavigation(ctx context.Context, mark uint64, accept func(string) bool, timeout time.Duration) (string, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.navigation.Lock()
		for i := len(s.navigation.entries) - 1; i >= 0; i-- {
			e := s.navigation.entries[i]
			if e.seq > mark && e.ready && accept(e.url) {
				s.navigation.Unlock()
				return e.url, nil
			}
		}
		wake := s.navigation.changed
		s.navigation.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return "", fmt.Errorf("等待页面跳转超时")
		case <-ctx.Done():
			return "", ctx.Err()
		case <-s.ctx.Done():
			return "", s.ctx.Err()
		}
	}
}
