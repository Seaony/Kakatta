package web

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/chromedp/cdproto"
)

func staleNicknameError() error {
	return fmt.Errorf("读昵称可见性：%w", &cdproto.Error{Code: -32000, Message: "Could not find node with given id"})
}

func TestLoginNicknameRetriesOnlyExpiredNodeSnapshot(t *testing.T) {
	reads := 0
	confirmed, facts, err := retryLoginNickname(t.Context(), func() (bool, []map[string]any, error) {
		reads++
		if reads < 3 {
			return false, nil, staleNicknameError()
		}
		return true, []map[string]any{{"visible": true, "has_text": true, "login_entry_text": false}}, nil
	})
	if err != nil || !confirmed || len(facts) != 1 || reads != 3 {
		t.Fatalf("页面替换后应重新取完整昵称快照：确认%v，事实%d，读取%d次，错误%v", confirmed, len(facts), reads, err)
	}
}

func TestLoginNicknameUnstableSnapshotKeepsWaiting(t *testing.T) {
	reads := 0
	confirmed, facts, err := retryLoginNickname(t.Context(), func() (bool, []map[string]any, error) {
		reads++
		return true, []map[string]any{{"visible": true}}, staleNicknameError()
	})
	if err != nil || confirmed || facts != nil || reads != 3 {
		t.Fatalf("读不清昵称应继续等，不能借部分事实放行或记录故障：确认%v，事实%v，读取%d次，错误%v", confirmed, facts, reads, err)
	}
}

func TestLoginNicknameDoesNotRetryOtherFailures(t *testing.T) {
	for _, failure := range []error{
		errors.New("连接已断开"),
		&cdproto.Error{Code: -32000, Message: "Connection closed"},
		&cdproto.Error{Code: -32602, Message: "Could not find node with given id"},
		errors.New("Could not find node with given id"),
		ErrDenied,
	} {
		reads := 0
		confirmed, _, err := retryLoginNickname(t.Context(), func() (bool, []map[string]any, error) {
			reads++
			return false, nil, failure
		})
		if confirmed || !errors.Is(err, failure) || reads != 1 {
			t.Fatalf("其它错误不能被昵称重查吞掉：确认%v，读取%d次，错误%v", confirmed, reads, err)
		}
	}
}

func TestLoginNicknameCancellationWinsOverExpiredSnapshot(t *testing.T) {
	cause := errors.New("用户关闭登录画面")
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	reads := 0
	confirmed, _, err := retryLoginNickname(ctx, func() (bool, []map[string]any, error) {
		reads++
		cancel(cause)
		return false, nil, staleNicknameError()
	})
	if confirmed || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || reads != 1 {
		t.Fatalf("取消不能变成继续等待：确认%v，读取%d次，错误%v", confirmed, reads, err)
	}
}
