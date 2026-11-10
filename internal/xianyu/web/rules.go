package web

import (
	"regexp"
	"time"

	"github.com/Seaony/Kakatta/internal/xianyu"
)

type protocolRules struct {
	xianyu.WebRules
	regionSubmit *regexp.Regexp
}

var rules = func() protocolRules {
	r := xianyu.WebProtocol()
	return protocolRules{WebRules: r, regionSubmit: regexp.MustCompile(r.Text.RegionSubmitPattern)}
}()

func (r protocolRules) timeout(key string) time.Duration {
	return time.Duration(r.Timeouts[key]) * time.Millisecond
}
