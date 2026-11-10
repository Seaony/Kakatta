package browser

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
)

func TestWaitNavigationRequiresMatchingMainFrameLoader(t *testing.T) {
	s := &Session{ctx: context.Background(), navigation: newNavigationState()}
	s.navigation.event(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "main", LoaderID: "old", URL: "http://local/search?q=old"}})
	s.navigation.event(&page.EventLifecycleEvent{FrameID: "main", LoaderID: "old", Name: "DOMContentLoaded"})
	mark := s.NavigationMark()
	s.navigation.event(&page.EventFrameNavigated{Frame: &cdp.Frame{ID: "main", LoaderID: "new", URL: "http://local/search?q=new"}})
	s.navigation.event(&page.EventLifecycleEvent{FrameID: "child", LoaderID: "new", Name: "DOMContentLoaded"})
	s.navigation.event(&page.EventLifecycleEvent{FrameID: "main", LoaderID: "old", Name: "DOMContentLoaded"})
	if _, err := s.WaitNavigation(context.Background(), mark, func(string) bool { return true }, time.Millisecond); err == nil {
		t.Fatal("把旧页面或子框架的完成事件当成了新主页面")
	}
	s.navigation.event(&page.EventLifecycleEvent{FrameID: "main", LoaderID: "new", Name: "DOMContentLoaded"})
	url, err := s.WaitNavigation(context.Background(), mark, func(string) bool { return true }, time.Second)
	if err != nil || url != "http://local/search?q=new" {
		t.Fatalf("没等到这次导航：%s %v", url, err)
	}
}
