package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type emailConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Encryption string `json:"encryption"`
	Username   string `json:"username"`
	From       string `json:"from"`
	To         string `json:"to"`
}

type emailSecrets struct {
	Password string `json:"password"`
}

type emailSettings struct {
	emailConfig
	Password   string
	Sender     *mail.Address
	Recipients []*mail.Address
}

func loadEmail(config ChannelConfig) (emailSettings, error) {
	var result emailSettings
	if err := decodeObject(config.Config, &result.emailConfig, "config"); err != nil {
		return result, err
	}
	var secrets emailSecrets
	if err := decodeObject(config.Secrets, &secrets, "secrets"); err != nil {
		return result, err
	}
	result.Password = secrets.Password
	if result.Host == "" || strings.ContainsAny(result.Host, "\r\n\t /?#@\\") || strings.Contains(result.Host, ":") && net.ParseIP(result.Host) == nil {
		return result, configError("host")
	}
	if result.Port < 1 || result.Port > 65535 {
		return result, configError("port")
	}
	if result.Encryption != "ssl" && result.Encryption != "starttls" && result.Encryption != "none" {
		return result, configError("encryption")
	}
	if strings.ContainsAny(result.Username, "\x00\r\n") {
		return result, configError("username")
	}
	if strings.ContainsRune(result.Password, '\x00') {
		return result, configError("password")
	}
	if strings.ContainsAny(result.From, "\r\n") {
		return result, configError("from")
	}
	if strings.ContainsAny(result.To, "\r\n") {
		return result, configError("to")
	}
	var err error
	result.Sender, err = mail.ParseAddress(result.From)
	if err != nil {
		return result, configError("from")
	}
	result.Recipients, err = mail.ParseAddressList(result.To)
	if err != nil || len(result.Recipients) == 0 {
		return result, configError("to")
	}
	return result, nil
}

func validateEmail(config ChannelConfig) error { _, err := loadEmail(config); return err }

func (s *Sender) sendEmail(ctx context.Context, config ChannelConfig, message Message) Result {
	settings, err := loadEmail(config)
	if err != nil {
		return failure("invalid_config")
	}
	data, err := emailMessage(settings, message, s.now())
	if err != nil {
		return failure("invalid_message")
	}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := s.dial(requestContext, "tcp", net.JoinHostPort(settings.Host, strconv.Itoa(settings.Port)))
	if err != nil {
		return emailFailure(requestContext, err)
	}
	defer conn.Close()
	deadline, _ := requestContext.Deadline()
	if err = conn.SetDeadline(deadline); err != nil {
		return emailFailure(requestContext, err)
	}
	// 标准库的 SMTP 操作没有 context；关闭底层连接使取消立即生效。
	stopCancel := context.AfterFunc(requestContext, func() { _ = conn.Close() })
	defer stopCancel()
	transport := conn
	tlsConfig := s.tlsConfig.Clone()
	tlsConfig.ServerName = settings.Host
	if settings.Encryption == "ssl" {
		secureConn := tls.Client(conn, tlsConfig)
		if err = secureConn.HandshakeContext(requestContext); err != nil {
			return emailFailure(requestContext, err)
		}
		transport = secureConn
	}
	client, err := smtp.NewClient(transport, settings.Host)
	if err != nil {
		return emailFailure(requestContext, err)
	}
	defer client.Close()
	if settings.Encryption == "starttls" {
		if err = client.Hello("localhost"); err != nil {
			return emailFailure(requestContext, err)
		}
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return failure("smtp_rejected")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return emailFailure(requestContext, err)
		}
	}
	if settings.Encryption != "none" && settings.Username != "" {
		if _, secure := client.TLSConnectionState(); !secure {
			return failure("smtp_rejected")
		}
		if err = client.Auth(smtp.PlainAuth("", settings.Username, settings.Password, settings.Host)); err != nil {
			return emailFailure(requestContext, err)
		}
	}
	if err = client.Mail(settings.Sender.Address); err != nil {
		return emailFailure(requestContext, err)
	}
	for _, recipient := range settings.Recipients {
		if err = client.Rcpt(recipient.Address); err != nil {
			return emailFailure(requestContext, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return emailFailure(requestContext, err)
	}
	if _, err = writer.Write(data); err != nil {
		return emailFailure(requestContext, err)
	}
	if err = writer.Close(); err != nil {
		return emailFailure(requestContext, err)
	}
	// DATA 已确认接收，退出失败或此后的取消不能把它改成失败。
	_ = client.Quit()
	return Result{OK: true}
}

func emailFailure(ctx context.Context, err error) Result {
	if ctx.Err() != nil {
		return failureFor(ctx.Err())
	}
	var protocolError *textproto.Error
	if errors.As(err, &protocolError) {
		return failure("smtp_rejected")
	}
	return failureFor(err)
}

func emailMessage(settings emailSettings, message Message, now time.Time) ([]byte, error) {
	if strings.ContainsAny(message.Title, "\r\n") {
		return nil, errors.New("invalid_message")
	}
	var body bytes.Buffer
	parts := multipart.NewWriter(&body)
	for _, part := range []struct{ kind, body string }{
		{"text/plain", withLink(plainBody(message), message.Link)},
		{"text/html", emailHTML(message)},
	} {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", mime.FormatMediaType(part.kind, map[string]string{"charset": "UTF-8"}))
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		writer, err := parts.CreatePart(header)
		if err != nil {
			return nil, err
		}
		encoded := quotedprintable.NewWriter(writer)
		if _, err = encoded.Write([]byte(part.body)); err != nil {
			return nil, err
		}
		if err = encoded.Close(); err != nil {
			return nil, err
		}
	}
	if err := parts.Close(); err != nil {
		return nil, err
	}
	var id [16]byte
	_, _ = rand.Read(id[:])
	recipients := make([]string, len(settings.Recipients))
	for index, recipient := range settings.Recipients {
		recipients[index] = recipient.String()
	}
	var result bytes.Buffer
	fmt.Fprintf(&result, "Subject: %s\r\nFrom: %s\r\nTo: %s\r\nDate: %s\r\nMessage-ID: <%s@kakatta.local>\r\nMIME-Version: 1.0\r\nContent-Type: %s\r\n\r\n", emailSubject(message.Title), settings.Sender.String(), strings.Join(recipients, ",\r\n "), now.Format(time.RFC1123Z), hex.EncodeToString(id[:]), mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": parts.Boundary()}))
	result.Write(body.Bytes())
	return result.Bytes(), nil
}

func emailSubject(value string) string {
	var words []string
	for value != "" {
		chunk := TruncateUTF8(value, 45)
		words = append(words, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(chunk))+"?=")
		value = value[len(chunk):]
	}
	return strings.Join(words, "\r\n ")
}

func emailHTML(message Message) string {
	parts := []string{"<b>" + htmlText(message.Title) + "</b>"}
	if body := plainBody(message); body != "" {
		parts = append(parts, htmlLines(body))
	}
	if message.ImageURL != "" {
		parts = append(parts, `<img src="`+htmlText(message.ImageURL)+`" width="300">`)
	}
	if message.Link != "" {
		parts = append(parts, `<a href="`+htmlText(message.Link)+`">`+htmlText(linkLabel(message))+`</a>`)
	}
	return strings.Join(parts, "<br>")
}
