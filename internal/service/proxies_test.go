package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/settings"
)

func TestProxyURLValidationMatchesForwarder(t *testing.T) {
	for _, address := range []string{
		"http://localhost:8080", "http://user:pass@127.0.0.1:9000/", "socks5://u:p@[::1]:1080",
	} {
		if _, err := parseProxyURL(address); err != nil {
			t.Fatal("合法代理被拒", address, err)
		}
	}
	for _, address := range []string{
		"https://localhost:8080", "socks5h://localhost:1080", "http://localhost", "http://localhost:0", "http://localhost:65536",
		"http://localhost:8080/path", "http://localhost:8080/?token=private", "http://localhost:8080?", "http://localhost:8080#private",
		"http://:8080", "http://127.0.0.1:8080\r\nInjected", "http:opaque", "socks5://" + strings.Repeat("a", 256) + ":p@localhost:1080",
	} {
		_, err := parseProxyURL(address)
		accountTestProblem(t, err, ErrorInvalid, "proxies.invalid_url")
		if strings.Contains(err.Error(), "private") {
			t.Fatal("代理校验错误泄露明文", err)
		}
	}
}

func TestProxyEncryptedWriteOnlyAndPatchPresence(t *testing.T) {
	s := accountTestService(t)
	const address = "socks5://private-user:private-password@127.0.0.1:1080"
	created, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "  代理一  ", "url": address}))
	if err != nil || created.Name != "代理一" || !created.Configured {
		t.Fatal("创建失败", created, err)
	}
	var uuid string
	var encrypted []byte
	if err := s.db.QueryRowContext(t.Context(), `SELECT uuid,url_enc FROM proxies WHERE id=?`, created.ID).Scan(&uuid, &encrypted); err != nil {
		t.Fatal(err)
	}
	if len(uuid) != 36 || uuid[14] != '4' || bytes.Contains(encrypted, []byte("private-password")) {
		t.Fatal("没有用独立v4键加密", uuid)
	}
	tx := accountTestTx(t, s)
	decrypted, err := loadProxyURLTx(t.Context(), tx, s.cipher, created.ID)
	if err != nil || decrypted.String() != address {
		t.Fatal("原uuid无法解密", err)
	}
	tx.Rollback()
	renamed, err := s.UpdateProxy(t.Context(), created.ID, accountTestPatch(map[string]any{"name": "改名"}))
	if err != nil || renamed.Name != "改名" {
		t.Fatal(err)
	}
	var afterRename []byte
	if err := s.db.QueryRowContext(t.Context(), `SELECT url_enc FROM proxies WHERE id=?`, created.ID).Scan(&afterRename); err != nil || !bytes.Equal(afterRename, encrypted) {
		t.Fatal("缺省URL把密文改掉", err)
	}
	_, err = s.UpdateProxy(t.Context(), created.ID, accountTestPatch(map[string]any{"url": nil}))
	accountTestProblem(t, err, ErrorInvalid, "proxies.invalid_url")
	_, err = s.UpdateProxy(t.Context(), created.ID, accountTestPatch(map[string]any{"url": "http://127.0.0.1:9090"}))
	if err != nil {
		t.Fatal(err)
	}
	tx = accountTestTx(t, s)
	decrypted, err = loadProxyURLTx(t.Context(), tx, s.cipher, created.ID)
	if err != nil || decrypted.String() != "http://127.0.0.1:9090" {
		t.Fatal("显式URL没有替换", err)
	}
	tx.Rollback()
	page, err := s.ListProxies(t.Context(), ListQuery{})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(page)
	for _, secret := range []string{"private-password", "127.0.0.1", "url_enc", "uuid", "url"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("公开代理泄露了凭据或内部字段", secret, string(raw))
		}
	}
	var object map[string]json.RawMessage
	item, _ := json.Marshal(created)
	if err := json.Unmarshal(item, &object); err != nil || len(object) != 3 {
		t.Fatal("ProxyView不再是三个脱敏字段", string(item), err)
	}
}

func TestProxyCipherCannotMoveBetweenRows(t *testing.T) {
	s := accountTestService(t)
	first, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "一", "url": "http://private:p@localhost:8001"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "二", "url": "http://private:p@localhost:8002"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE proxies SET url_enc=(SELECT url_enc FROM proxies WHERE id=?) WHERE id=?`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	tx := accountTestTx(t, s)
	if _, err := loadProxyURLTx(t.Context(), tx, s.cipher, second.ID); !errors.Is(err, security.ErrCiphertext) {
		t.Fatal("把其他行密文当成当前代理", err)
	}
}

func TestProxyNameUniquenessAndFailureRollback(t *testing.T) {
	s := accountTestService(t)
	first, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "一", "url": "http://localhost:8001"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "二", "url": "http://localhost:8002"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "一", "url": "http://localhost:8003"}))
	accountTestProblem(t, err, ErrorInvalid, "proxies.name_in_use")
	_, err = s.UpdateProxy(t.Context(), second.ID, accountTestPatch(map[string]any{"name": "一", "url": "http://localhost:8004"}))
	accountTestProblem(t, err, ErrorInvalid, "proxies.name_in_use")
	page, err := s.ListProxies(t.Context(), ListQuery{})
	if err != nil || !reflect.DeepEqual(page.Items, []ProxyView{first, second}) {
		t.Fatal("失败只保存了部分修改", page, err)
	}
	for _, patch := range []map[string]json.RawMessage{
		{}, {"name": json.RawMessage(`null`), "url": json.RawMessage(`"http://localhost:8001"`)},
		{"name": json.RawMessage(`"三"`), "url": json.RawMessage(`"http://localhost:8001"`), "configured": json.RawMessage(`true`)},
	} {
		if _, err := s.CreateProxy(t.Context(), patch); err == nil {
			t.Fatal("无效代理保存成功", patch)
		}
	}
}

func TestProxyCannotDeleteWhileReferencedByAccountOrModels(t *testing.T) {
	s := accountTestService(t)
	proxy, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "一", "url": "http://localhost:8001"}))
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.CreateAccount(t.Context(), accountTestPatch(map[string]any{"name": "账号", "proxy_id": proxy.ID}))
	if err != nil {
		t.Fatal(err)
	}
	accountTestProblem(t, s.DeleteProxy(t.Context(), proxy.ID), ErrorConflict, "proxies.in_use")
	if _, err := s.UpdateAccount(t.Context(), account.ID, accountTestPatch(map[string]any{"proxy_id": nil})); err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(proxy.ID)
	if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.proxy_id": value}); err != nil {
		t.Fatal(err)
	}
	accountTestProblem(t, s.DeleteProxy(t.Context(), proxy.ID), ErrorConflict, "proxies.in_use")
	if _, err := s.settings.Apply(t.Context(), settings.Values{"ai.proxy_id": json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProxy(t.Context(), proxy.ID); err != nil {
		t.Fatal(err)
	}
	accountTestProblem(t, s.DeleteProxy(t.Context(), proxy.ID), ErrorNotFound, "proxies.not_found")
}

func TestProxyConnectionTestSendsNoProtocolOrWebsiteRequest(t *testing.T) {
	s := accountTestService(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	observed := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			observed <- []byte("accept failed")
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		data, _ := io.ReadAll(conn)
		observed <- data
	}()
	proxy, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "本地假代理", "url": "http://private:password@" + listener.Addr().String()}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.TestProxy(t.Context(), proxy.ID)
	if err != nil || !result.OK || result.Reason != nil {
		t.Fatal("TCP连接被假当成网站测试失败", result, err)
	}
	select {
	case data := <-observed:
		if len(data) != 0 {
			t.Fatal("测试代理发送了协议或真实访问", string(data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("测试没有在连接后关闭")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.TestProxy(ctx, proxy.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("代理测试吞掉取消", err)
	}
	listener.Close()
	result, err = s.TestProxy(t.Context(), proxy.ID)
	if err != nil || result.OK || result.Reason == nil || result.Reason.Code != "proxies.test_failed" {
		t.Fatal("连接失败没有固定的去密错误", result, err)
	}
}

func TestProxyListCursorAndMissingCipherFailClosed(t *testing.T) {
	s := accountTestService(t)
	for _, name := range []string{"一", "二"} {
		if _, err := s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": name, "url": "http://localhost:8001"})); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ListProxies(t.Context(), ListQuery{Limit: 1})
	if err != nil || first.NextCursor == nil || first.Total != 2 || len(first.Items) != 1 {
		t.Fatal("代理第一页不完整", first, err)
	}
	second, err := s.ListProxies(t.Context(), ListQuery{Cursor: *first.NextCursor})
	if err != nil || second.Total != 2 || len(second.Items) != 1 || second.Items[0].ID <= first.Items[0].ID {
		t.Fatal("代理后页重复或少记录", second, err)
	}
	s.cipher = nil
	_, err = s.CreateProxy(t.Context(), accountTestPatch(map[string]any{"name": "无密钥", "url": "http://localhost:8001"}))
	accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
	_, err = s.TestProxy(t.Context(), first.Items[0].ID)
	accountTestProblem(t, err, ErrorUnavailable, "service.storage_failure")
}
