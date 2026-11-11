package web

import (
	"fmt"

	"github.com/Seaony/Kakatta/internal/browser"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

type handledRisk struct{ header, body uint64 }
type loginSignal struct {
	seq, order uint64
	header     bool
	expired    bool
	signal     string
}

func (r *Runner) handleSignals(signals []loginSignal) {
	if r.handledRisks == nil {
		r.handledRisks = map[uint64]handledRisk{}
	}
	for _, signal := range signals {
		handled := r.handledRisks[signal.seq]
		if signal.header {
			handled.header = max(handled.header, signal.order)
		} else {
			handled.body = max(handled.body, signal.order)
		}
		r.handledRisks[signal.seq] = handled
	}
	r.pruneHandledRisks()
}

func (r *Runner) pruneHandledRisks() {
	kept := map[uint64]bool{}
	for _, response := range r.S.Capture.HeadersAfter(r.since, browser.Match{}) {
		kept[response.Seq] = true
	}
	for _, response := range r.S.Capture.DoneAfter(r.since, browser.Match{}) {
		kept[response.Seq] = true
	}
	for seq := range r.handledRisks {
		if !kept[seq] {
			delete(r.handledRisks, seq)
		}
	}
	for len(r.handledRisks) > 200 {
		var oldest uint64
		for seq := range r.handledRisks {
			if oldest == 0 || seq < oldest {
				oldest = seq
			}
		}
		delete(r.handledRisks, oldest)
	}
}

// 登录时扫完所有响应，较早的验证码不能遮住较晚的直接拒绝。
func (r *Runner) loginSignals() ([]loginSignal, uint64, error) {
	if err := r.S.Capture.Err(); err != nil {
		return nil, 0, err
	}
	var signals []loginSignal
	var success uint64
	var firstError error
	for _, response := range r.S.Capture.HeadersAfter(r.since, browser.Match{}) {
		if !response.MainDocument && !r.isMtopResponse(response) {
			continue
		}
		if response.Headers["bxpunish"] == "1" {
			signals = append(signals, loginSignal{seq: response.Seq, order: response.HeaderOrder, header: true, signal: "响应头 bxpunish"})
		}
	}
	for _, response := range r.S.Capture.DoneAfter(r.since, r.Site.mtop()) {
		if response.MainDocument || !r.isMtopResponse(response) {
			continue
		}
		if response.Err != nil {
			if firstError == nil {
				firstError = fmt.Errorf("登录期间读取接口失败：%w", response.Err)
			}
			continue
		}
		kind, ret, err := xianyu.CheckEnvelope(response.Body)
		if err != nil {
			if firstError == nil {
				firstError = r.ReportFailure(r.S.Context(), "登录期间检查接口", fmt.Errorf("登录期间无法检查接口响应：%w", err), response)
			}
			continue
		}
		if kind == xianyu.RetNeedVerify || kind == xianyu.RetDenied || kind == xianyu.RetSessionExpired {
			r.signal(responseRiskFields("等待登录", response, kind, ret))
		}
		if kind == xianyu.RetDenied {
			return signals, success, r.ReportFailure(r.S.Context(), "等待登录", &RiskError{Kind: ErrDenied, Where: "等待登录", Signal: kind.String()}, nil)
		}
		switch kind {
		case xianyu.RetNeedVerify, xianyu.RetSessionExpired:
			signals = append(signals, loginSignal{seq: response.Seq, order: response.BodyOrder, expired: kind == xianyu.RetSessionExpired, signal: kind.String()})
		case xianyu.RetOK:
			if response.Status >= 200 && response.Status < 300 && response.BodyOrder > success {
				success = response.BodyOrder
			}
		}
	}
	return signals, success, firstError
}
