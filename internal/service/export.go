package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
)

const exportBatchSize = 200

func (s *Service) exportSnapshot(ctx context.Context, read func(context.Context, *sql.Tx) error) error {
	release, err := s.beginMutation()
	if err != nil {
		return err
	}
	defer release()
	if s.readSnapshot == nil || s.settings == nil {
		return errors.New("导出缺少只读快照或设置存储")
	}
	work, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(s.ctx, func() { cancel(context.Cause(s.ctx)) })
	defer stop()
	defer cancel(nil)
	if s.ctx.Err() != nil {
		cancel(context.Cause(s.ctx))
	}
	err = s.readSnapshot(work, func(tx *sql.Tx) error {
		if err := work.Err(); err != nil {
			return err
		}
		return read(work, tx)
	})
	if work.Err() != nil {
		err = errors.Join(err, work.Err(), context.Cause(work))
	}
	return err
}

func (s *Service) exportTimeTx(ctx context.Context, tx *sql.Tx) (*time.Location, time.Time, error) {
	zone, err := s.marketTimezoneTx(ctx, tx)
	if err != nil {
		return nil, time.Time{}, err
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return nil, time.Time{}, err
	}
	now, err := readObservedNowTx(ctx, tx, s.now())
	return location, now.In(location), err
}

type exportWriter struct {
	ctx context.Context
	dst io.Writer
}

func (w exportWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.ctx.Err()
	}
	return n, err
}

func exportMessage(language locales.Language, message *locales.Message) (string, error) {
	if message == nil {
		return "", nil
	}
	if message.Code == "" {
		return "", errors.New("导出消息缺少代码")
	}
	text, err := locales.Format(language, *message)
	if err == nil {
		return text, nil
	}
	// 字典换过后，旧代码和旧参数仍要随记录导出。
	raw, err := json.Marshal(message)
	return string(raw), err
}

func exportLabel(code string) (string, error) {
	return locales.Format(locales.ZhCN, locales.Message{Code: code})
}

func exportOptional[T ~string | ~int64](value *T) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(*value)
}

func exportTimestamp(value *int64, location *time.Location) string {
	if value == nil {
		return ""
	}
	return time.UnixMilli(*value).In(location).Format("2006-01-02T15:04:05.000Z07:00")
}

func exportFilename(name, date, extension string) string {
	name = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\\:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name))
	if name == "" || name == "." || name == ".." {
		name = "kakatta"
	}
	return name + "-" + date + extension
}

func validExportFields(fields []string) error {
	for _, field := range fields {
		if !utf8.ValidString(field) {
			return errors.New("导出记录不是有效的文字")
		}
	}
	return nil
}
