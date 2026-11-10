package notify

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type emailServerOptions struct {
	SSL              bool
	STARTTLS         bool
	BrokenTLS        bool
	RejectRecipient  int
	RejectAuth       bool
	RejectData       bool
	QuitFailure      bool
	HoldQuit         bool
	DisconnectOnMail bool
	OnData           func()
	OnQuit           func()
}

type emailTranscript struct {
	Commands []string
	Message  []byte
	TLS      bool
}

func emailServer(t *testing.T, options emailServerOptions) (ChannelConfig, *tls.Config, <-chan emailTranscript) {
	t.Helper()
	certificate, trust := emailTestCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan emailTranscript, 1)
	go func() {
		transcript := emailTranscript{}
		defer func() { done <- transcript; close(done) }()
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		accepted := conn
		t.Cleanup(func() { _ = accepted.Close() })
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if options.SSL {
			secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
			if secure.Handshake() != nil {
				return
			}
			conn = secure
			transcript.TLS = true
		}
		text := textproto.NewConn(conn)
		if text.PrintfLine("220 local SMTP") != nil {
			return
		}
		recipientCount := 0
		for {
			line, err := text.ReadLine()
			if err != nil {
				return
			}
			transcript.Commands = append(transcript.Commands, line)
			command, _, _ := strings.Cut(line, " ")
			switch command {
			case "EHLO":
				if text.PrintfLine("250-local SMTP") != nil {
					return
				}
				if options.STARTTLS && !transcript.TLS {
					if text.PrintfLine("250-STARTTLS") != nil {
						return
					}
				}
				if text.PrintfLine("250 AUTH PLAIN") != nil {
					return
				}
			case "HELO":
				if text.PrintfLine("250 local SMTP") != nil {
					return
				}
			case "STARTTLS":
				if !options.STARTTLS {
					_ = text.PrintfLine("502 unsupported")
					continue
				}
				if text.PrintfLine("220 go ahead") != nil {
					return
				}
				if options.BrokenTLS {
					_, _ = conn.Write([]byte("not a TLS handshake\r\n"))
					return
				}
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
				if secure.Handshake() != nil {
					return
				}
				conn = secure
				text = textproto.NewConn(secure)
				transcript.TLS = true
			case "AUTH":
				if options.RejectAuth {
					if text.PrintfLine("535 SECRET-SMTP-DETAIL rejected") != nil {
						return
					}
				} else {
					if text.PrintfLine("235 authenticated") != nil {
						return
					}
				}
			case "MAIL":
				if options.DisconnectOnMail {
					return
				}
				if text.PrintfLine("250 sender accepted") != nil {
					return
				}
			case "RCPT":
				recipientCount++
				if recipientCount == options.RejectRecipient {
					if text.PrintfLine("550 SECRET-SMTP-DETAIL recipient rejected") != nil {
						return
					}
				} else {
					if text.PrintfLine("250 recipient accepted") != nil {
						return
					}
				}
			case "DATA":
				if options.OnData != nil {
					options.OnData()
				}
				if text.PrintfLine("354 send data") != nil {
					return
				}
				transcript.Message, err = io.ReadAll(text.DotReader())
				if err != nil {
					return
				}
				if options.RejectData {
					if text.PrintfLine("554 SECRET-SMTP-DETAIL DATA rejected") != nil {
						return
					}
				} else {
					if text.PrintfLine("250 queued") != nil {
						return
					}
				}
			case "QUIT":
				if options.OnQuit != nil {
					options.OnQuit()
				}
				if options.HoldQuit {
					_, _ = io.Copy(io.Discard, conn)
					return
				}
				if options.QuitFailure {
					_ = text.PrintfLine("500 SECRET-SMTP-DETAIL quit rejected")
				} else {
					_ = text.PrintfLine("221 bye")
				}
				return
			default:
				_ = text.PrintfLine("500 unsupported")
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	encryption := "none"
	if options.SSL {
		encryption = "ssl"
	} else if options.STARTTLS {
		encryption = "starttls"
	}
	config, _ := json.Marshal(emailConfig{Host: host, Port: port, Encryption: encryption, Username: "mail-user", From: `寄件人 <from@example.test>`, To: `"收件人, 一" <one@example.test>, two@example.test`})
	secrets, _ := json.Marshal(emailSecrets{Password: "SECRET-SMTP-PASSWORD"})
	return ChannelConfig{Type: Email, Config: config, Secrets: secrets}, trust, done
}

var emailCertificateOnce sync.Once
var emailCertificate tls.Certificate
var emailTrust *tls.Config
var emailCertificateError error

func emailTestCertificate(t *testing.T) (tls.Certificate, *tls.Config) {
	t.Helper()
	emailCertificateOnce.Do(func() {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			emailCertificateError = err
			return
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			emailCertificateError = err
			return
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			emailCertificateError = err
			return
		}
		certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		emailCertificate, err = tls.X509KeyPair(certificatePEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		if err != nil {
			emailCertificateError = err
			return
		}
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(certificatePEM)
		emailTrust = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	})
	if emailCertificateError != nil {
		t.Fatal(emailCertificateError)
	}
	return emailCertificate, emailTrust.Clone()
}

func emailWaitTranscript(t *testing.T, done <-chan emailTranscript) emailTranscript {
	t.Helper()
	select {
	case value := <-done:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP fake did not stop")
		return emailTranscript{}
	}
}

func TestEmailEncryptionAndMIME(t *testing.T) {
	for _, test := range []struct {
		name    string
		options emailServerOptions
		auth    bool
	}{{"none", emailServerOptions{}, false}, {"ssl", emailServerOptions{SSL: true}, true}, {"starttls", emailServerOptions{STARTTLS: true}, true}} {
		t.Run(test.name, func(t *testing.T) {
			config, trust, done := emailServer(t, test.options)
			now := time.Date(2026, 11, 10, 12, 34, 56, 0, time.FixedZone("UTC+8", 8*3600))
			message := Message{Title: `通知 <标签> "引号" 😀`, ItemTitle: "商品 & 测试", Body: "第一行\n第二行 ¥1,000", Link: `https://example.test/?x="&y=1`, ImageURL: `https://example.test/image?a="&b=2`}
			result := New(Dependencies{TLSConfig: trust, Now: func() time.Time { return now }}).Send(context.Background(), config, message)
			if !result.OK {
				t.Fatalf("result = %+v", result)
			}
			transcript := emailWaitTranscript(t, done)
			authCommands := 0
			for _, command := range transcript.Commands {
				if strings.HasPrefix(command, "AUTH ") {
					authCommands++
					if !transcript.TLS {
						t.Fatal("AUTH on plaintext connection")
					}
					encoded := strings.TrimPrefix(command, "AUTH PLAIN ")
					decoded, err := base64.StdEncoding.DecodeString(encoded)
					if err != nil || string(decoded) != "\x00mail-user\x00SECRET-SMTP-PASSWORD" {
						t.Fatalf("invalid PLAIN payload")
					}
				}
			}
			if (authCommands > 0) != test.auth {
				t.Fatalf("AUTH count = %d", authCommands)
			}
			parsed, err := mail.ReadMessage(bytes.NewReader(transcript.Message))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
			if err != nil || subject != message.Title {
				t.Fatalf("subject = %q, %v", subject, err)
			}
			for _, key := range []string{"From", "To", "Date", "Message-ID", "MIME-Version"} {
				if parsed.Header.Get(key) == "" {
					t.Errorf("missing %s", key)
				}
			}
			date, err := mail.ParseDate(parsed.Header.Get("Date"))
			if err != nil || !date.Equal(now) {
				t.Fatalf("date = %s, %v", date, err)
			}
			kind, parameters, err := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
			if err != nil || kind != "multipart/alternative" {
				t.Fatalf("content type = %q, %v", kind, err)
			}
			reader := multipart.NewReader(parsed.Body, parameters["boundary"])
			plain, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			text, err := io.ReadAll(plain)
			if err != nil {
				t.Fatal(err)
			}
			if string(text) != withLink(plainBody(message), message.Link) {
				t.Fatalf("plain text = %q", text)
			}
			htmlPart, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			htmlBody, err := io.ReadAll(htmlPart)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(htmlBody), `width="300"`) || !strings.Contains(string(htmlBody), "&lt;标签&gt;") || !strings.Contains(string(htmlBody), "&amp;") || strings.Contains(string(htmlBody), `<标签>`) {
				t.Fatalf("HTML = %s", htmlBody)
			}
			if _, err = reader.NextPart(); err != io.EOF {
				t.Fatalf("extra MIME part: %v", err)
			}
		})
	}
}

func TestEmailFailuresAndAcceptedData(t *testing.T) {
	for _, test := range []struct {
		name    string
		options emailServerOptions
		ok      bool
		code    string
	}{{"recipient", emailServerOptions{RejectRecipient: 2}, false, "smtp_rejected"}, {"auth", emailServerOptions{SSL: true, RejectAuth: true}, false, "smtp_rejected"}, {"data", emailServerOptions{RejectData: true}, false, "smtp_rejected"}, {"disconnect", emailServerOptions{DisconnectOnMail: true}, false, "transport"}, {"quit", emailServerOptions{QuitFailure: true}, true, ""}, {"broken_starttls", emailServerOptions{STARTTLS: true, BrokenTLS: true}, false, "transport"}} {
		t.Run(test.name, func(t *testing.T) {
			config, trust, done := emailServer(t, test.options)
			result := New(Dependencies{TLSConfig: trust}).Send(context.Background(), config, Message{Title: "测试", Body: "正文"})
			if result.OK != test.ok || result.ErrorCode != test.code {
				t.Fatalf("result = %+v", result)
			}
			if strings.Contains(fmt.Sprint(result), "SECRET") {
				t.Fatalf("secret in result")
			}
			transcript := emailWaitTranscript(t, done)
			if test.name == "recipient" && len(transcript.Message) > 0 {
				t.Fatal("DATA sent after a recipient rejection")
			}
			if test.name == "broken_starttls" {
				for _, command := range transcript.Commands {
					if strings.HasPrefix(command, "AUTH ") || strings.HasPrefix(command, "MAIL ") {
						t.Fatalf("continued after TLS failure")
					}
				}
			}
		})
	}
}

func TestEmailStartTLSRequired(t *testing.T) {
	config, _, done := emailServer(t, emailServerOptions{})
	var settings emailConfig
	_ = json.Unmarshal(config.Config, &settings)
	settings.Encryption = "starttls"
	config.Config, _ = json.Marshal(settings)
	result := New(Dependencies{}).Send(context.Background(), config, Message{Title: "测试"})
	if result.OK || result.ErrorCode != "smtp_rejected" {
		t.Fatalf("result = %+v", result)
	}
	transcript := emailWaitTranscript(t, done)
	for _, command := range transcript.Commands {
		if strings.HasPrefix(command, "MAIL ") || strings.HasPrefix(command, "AUTH ") {
			t.Fatal("continued without STARTTLS")
		}
	}
}

func TestEmailUntrustedCertificateStopsAuthentication(t *testing.T) {
	config, _, done := emailServer(t, emailServerOptions{SSL: true})
	result := New(Dependencies{}).Send(context.Background(), config, Message{Title: "测试"})
	if result.OK || result.ErrorCode != "transport" {
		t.Fatalf("result = %+v", result)
	}
	transcript := emailWaitTranscript(t, done)
	if len(transcript.Commands) != 0 {
		t.Fatal("SMTP commands sent after certificate failure")
	}
}

func TestEmailCancellation(t *testing.T) {
	for _, afterAccepted := range []bool{false, true} {
		t.Run(strconv.FormatBool(afterAccepted), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			options := emailServerOptions{}
			if afterAccepted {
				options.OnQuit = cancel
			} else {
				options.OnData = cancel
			}
			config, _, done := emailServer(t, options)
			result := New(Dependencies{}).Send(ctx, config, Message{Title: "测试", Body: "正文"})
			if afterAccepted {
				if !result.OK {
					t.Fatalf("accepted DATA lost: %+v", result)
				}
			} else if result.OK || result.ErrorCode != "cancelled" {
				t.Fatalf("result = %+v", result)
			}
			emailWaitTranscript(t, done)
		})
	}
}

func TestEmailCancellationInterruptsSilentQuitAfterAcceptedData(t *testing.T) {
	quitReached := make(chan struct{})
	config, _, transcript := emailServer(t, emailServerOptions{
		HoldQuit: true,
		OnQuit:   func() { close(quitReached) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan Result, 1)
	go func() {
		result <- New(Dependencies{}).Send(ctx, config, Message{Title: "测试", Body: "正文"})
	}()
	select {
	case <-quitReached:
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP fake did not reach QUIT")
	}
	cancel()
	select {
	case value := <-result:
		if !value.OK || value.ErrorCode != "" {
			t.Fatalf("accepted DATA lost after cancellation: %+v", value)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt unanswered QUIT")
	}
	if got := emailWaitTranscript(t, transcript); len(got.Message) == 0 {
		t.Fatal("SMTP fake did not receive DATA")
	}
}

func TestEmailValidateAllRecipientsBeforeDial(t *testing.T) {
	calls := 0
	sender := New(Dependencies{DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, fmt.Errorf("SECRET-DIAL")
	}})
	base := emailConfig{Host: "localhost", Port: 25, Encryption: "none", From: "from@example.test", To: "one@example.test, two@example.test"}
	for _, test := range []struct {
		name   string
		change func(*emailConfig)
	}{{"recipient", func(c *emailConfig) { c.To = "valid@example.test, invalid" }}, {"from_injection", func(c *emailConfig) { c.From = "from@example.test\r\nBcc: victim@example.test" }}, {"to_injection", func(c *emailConfig) { c.To = "one@example.test\nBcc: victim@example.test" }}, {"port", func(c *emailConfig) { c.Port = 0 }}, {"host_url", func(c *emailConfig) { c.Host = "https://secret.example.test" }}, {"encryption", func(c *emailConfig) { c.Encryption = "optional" }}} {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.change(&config)
			raw, _ := json.Marshal(config)
			channel := ChannelConfig{Type: Email, Config: raw}
			if err := Validate(channel); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if result := sender.Send(context.Background(), channel, Message{Title: "测试"}); result.ErrorCode != "invalid_config" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("dialed %d times for invalid configuration", calls)
	}
}

func TestEmailSystemAndSubjectEncoding(t *testing.T) {
	config, _, done := emailServer(t, emailServerOptions{})
	title := strings.Repeat("中文😀", 80)
	result := New(Dependencies{}).Send(context.Background(), config, Message{Title: title, Body: "请重新登录\n第二行", Kind: "system", Urgent: true})
	if !result.OK {
		t.Fatalf("result = %+v", result)
	}
	transcript := emailWaitTranscript(t, done)
	parsed, err := mail.ReadMessage(bytes.NewReader(transcript.Message))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := new(mime.WordDecoder).DecodeHeader(parsed.Header.Get("Subject"))
	if err != nil || decoded != title {
		t.Fatalf("subject roundtrip failed: %v", err)
	}
	for _, word := range strings.Fields(parsed.Header.Get("Subject")) {
		if len(word) > 75 {
			t.Fatalf("encoded word has %d bytes", len(word))
		}
	}
	_, parameters, _ := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	reader := multipart.NewReader(parsed.Body, parameters["boundary"])
	for index := 0; index < 2; index++ {
		part, err := reader.NextRawPart()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(quotedprintable.NewReader(part))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "<img") || strings.Contains(string(content), "<a href") {
			t.Fatalf("unexpected item content: %s", content)
		}
	}
}

func TestEmailTimeoutAndPreCancelled(t *testing.T) {
	config := ChannelConfig{Type: Email, Config: json.RawMessage(`{"host":"localhost","port":25,"encryption":"none","from":"from@example.test","to":"to@example.test"}`)}
	dialed := 0
	sender := New(Dependencies{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialed++
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := sender.Send(ctx, config, Message{Title: "测试"}); result.ErrorCode != "cancelled" {
		t.Fatalf("result = %+v", result)
	}
	if dialed != 0 {
		t.Fatal("dialed after pre-cancel")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if result := sender.Send(ctx, config, Message{Title: "测试"}); result.ErrorCode != "timeout" {
		t.Fatalf("result = %+v", result)
	}
}
