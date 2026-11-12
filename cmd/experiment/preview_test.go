package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

// 仅用于本地画面验收，七分钟后关闭，页面和浏览器都只使用假数据。
func TestExperimentPreview(t *testing.T) {
	if os.Getenv("KAKATTA_PREVIEW_TEST") != "1" {
		t.Skip("设置 KAKATTA_PREVIEW_TEST=1 才开放临时验收页面")
	}
	s := testSettings()
	s.AccountGapSeconds = 1
	c, err := newController(options{Data: t.TempDir(), Account: 450, Details: 2, Keyword: "相机", Fixture: true}, s)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	job, err := c.reserve(context.Background(), "login")
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.makeResources(job, true)
	if err != nil {
		c.release()
		t.Fatal(err)
	}
	if err := r.chrome.Navigate(c.site.Home, 5*time.Second); err != nil {
		c.release()
		t.Fatal(err)
	}
	if err := c.journal.risk(web.ErrNeedVerify, c.site.Home); err != nil {
		c.release()
		t.Fatal(err)
	}
	c.holdVerification(r, web.ErrNeedVerify)
	c.release()
	l, err := net.Listen("tcp4", "127.0.0.1:8080")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: experimentHandler(c), ReadHeaderTimeout: 10 * time.Second}
	defer server.Close()
	go server.Serve(l)
	fmt.Println("临时页面已就绪：http://127.0.0.1:8080")
	stopFile := os.Getenv("KAKATTA_PREVIEW_STOP")
	deadline := time.NewTimer(7 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case <-tick.C:
			if stopFile != "" {
				if _, err := os.Stat(stopFile); err == nil {
					return
				}
			}
		}
	}
}
