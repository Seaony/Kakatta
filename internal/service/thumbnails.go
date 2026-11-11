package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/analysis/images"
)

func (s *Service) ListingThumbnail(ctx context.Context, itemID string, findingID *int64) ([]byte, error) {
	if strings.TrimSpace(itemID) == "" || !utf8.ValidString(itemID) || findingID != nil && *findingID < 1 {
		return nil, queryError()
	}
	ctx, cancel := context.WithTimeout(ctx, images.RequestTimeout)
	defer cancel()
	finish, err := s.beginMutation()
	if err != nil {
		return nil, err
	}
	defer finish()
	source, err := s.readThumbnailSource(ctx, itemID, findingID)
	if err != nil || source.address == nil {
		return nil, err
	}
	key := "listing-thumbnail:" + source.address.String()
	data, err := s.imageCache.Get(ctx, key)
	if err == nil {
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, storageFailure(err)
	}
	if !source.hasAccount {
		return nil, nil
	}

	transport, err := detailImageTransport(source.proxy)
	if err != nil {
		return nil, storageFailure(err)
	}
	defer transport.CloseIdleConnections()
	preparer, err := images.New(images.Dependencies{HTTP: &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if !isThumbnailTarget(req.URL) {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}})
	if err != nil {
		return nil, storageFailure(err)
	}
	data, err = preparer.DownloadJPEG(ctx, source.address.String())
	if err != nil {
		return nil, nil
	}
	if int64(len(data)) <= source.maxBytes {
		if err := s.imageCache.Put(ctx, key, data, source.maxBytes); err != nil {
			return nil, storageFailure(err)
		}
	}
	return data, nil
}

type thumbnailSource struct {
	address    *url.URL
	proxy      *url.URL
	hasAccount bool
	maxBytes   int64
}

func (s *Service) readThumbnailSource(ctx context.Context, itemID string, findingID *int64) (thumbnailSource, error) {
	var source thumbnailSource
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return source, storageFailure(err)
	}
	defer tx.Rollback()
	var raw sql.NullString
	var finding, account, proxy sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT l.thumbnail_url,f.id,a.id,a.proxy_id FROM listings l
		LEFT JOIN findings f ON f.id=(SELECT id FROM findings WHERE item_id=l.item_id AND (? IS NULL OR id=?)
			ORDER BY found_at DESC,id DESC LIMIT 1)
		LEFT JOIN tasks t ON t.id=f.task_id LEFT JOIN accounts a ON a.id=t.account_id WHERE l.item_id=?`,
		findingID, findingID, itemID).Scan(&raw, &finding, &account, &proxy)
	if errors.Is(err, sql.ErrNoRows) {
		return source, listingNotFound()
	}
	if err != nil {
		return source, storageFailure(err)
	}
	if findingID != nil && !finding.Valid {
		return source, findingMissingError()
	}
	address, err := url.Parse(raw.String)
	if err != nil || !isThumbnailTarget(address) {
		return source, nil
	}
	source.address, source.hasAccount = address, account.Valid
	if proxy.Valid {
		source.proxy, err = loadProxyURLTx(ctx, tx, s.cipher, proxy.Int64)
		if err != nil {
			return source, storageFailure(err)
		}
	}
	if source.hasAccount {
		snapshot, err := s.settings.SnapshotTx(ctx, tx)
		if err != nil {
			return source, storageFailure(err)
		}
		var maxMB int64
		if err := json.Unmarshal(snapshot.Values["cache.image_max_mb"], &maxMB); err != nil || maxMB < 1 || maxMB > math.MaxInt64/(1<<20) {
			return source, storageFailure(errors.New("缩略图缓存上限无效"))
		}
		source.maxBytes = maxMB * (1 << 20)
	}
	return source, storageFailure(tx.Commit())
}

func isThumbnailTarget(target *url.URL) bool {
	if target == nil || target.User != nil || target.Opaque != "" || target.Fragment != "" || !strings.EqualFold(target.Hostname(), "img.alicdn.com") {
		return false
	}
	port := target.Port()
	return target.Scheme == "https" && (port == "" || port == "443") || target.Scheme == "http" && (port == "" || port == "80")
}
