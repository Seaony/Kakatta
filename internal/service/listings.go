package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

func loadListingTx(ctx context.Context, tx *sql.Tx, itemID string) (Listing, error) {
	var item Listing
	var images, tags, sellerDetail sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT item_id,title,desktop_url,first_seen_at,last_seen_at,
		price,price_text,original_price_text,thumbnail_url,area,seller_id,seller_nick,description,
		zhima_level,sale_status,image_urls_json,tags_json,published_at,want_count,browse_count,
		seller_registration_days,detail_fetched_at,is_favorite,is_dismissed,favorited_at,
		last_drop_amount,last_drop_at,seller_detail_json FROM listings WHERE item_id=?`, itemID).Scan(
		&item.ItemID, &item.Title, &item.DesktopURL, &item.FirstSeenAt, &item.LastSeenAt,
		&item.Price, &item.PriceText, &item.OriginalPriceText, &item.ThumbnailURL, &item.Area,
		&item.SellerID, &item.SellerNick, &item.Description, &item.ZhimaLevel, &item.SaleStatus,
		&images, &tags, &item.PublishedAt, &item.WantCount, &item.BrowseCount,
		&item.SellerRegistrationDays, &item.DetailFetchedAt, &item.IsFavorite, &item.IsDismissed,
		&item.FavoritedAt, &item.LastDropAmount, &item.LastDropAt, &sellerDetail)
	if errors.Is(err, sql.ErrNoRows) {
		return Listing{}, listingNotFound()
	}
	if err != nil {
		return Listing{}, err
	}
	item.ImageURLs, item.Tags = []string{}, []string{}
	if images.Valid {
		if err := json.Unmarshal([]byte(images.String), &item.ImageURLs); err != nil {
			return Listing{}, fmt.Errorf("商品图片缓存损坏：%w", err)
		}
	}
	if tags.Valid {
		if err := json.Unmarshal([]byte(tags.String), &item.Tags); err != nil {
			return Listing{}, fmt.Errorf("商品标签缓存损坏：%w", err)
		}
	}
	if sellerDetail.Valid {
		item.SellerDetail, err = decodeSellerDetailMetrics(sellerDetail.String)
		if err != nil {
			return Listing{}, err
		}
	}
	if item.ImageURLs == nil {
		item.ImageURLs = []string{}
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	return item, nil
}

func decodeSellerDetailMetrics(raw string) (*xianyu.SellerDetailMetrics, error) {
	bad := errors.New("详情卖家指标格式无效")
	if !utf8.ValidString(raw) || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return nil, bad
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var metrics xianyu.SellerDetailMetrics
	if decoder.Decode(&metrics) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, bad
	}
	if err := validateSellerDetailMetrics(&metrics); err != nil {
		return nil, err
	}
	return &metrics, nil
}

func validateSellerDetailMetrics(metrics *xianyu.SellerDetailMetrics) error {
	bad := errors.New("详情卖家指标格式无效")
	for _, count := range []*int64{metrics.ItemCount, metrics.HasSoldNumInteger} {
		if count != nil && (*count < 0 || *count > limits.MaxSafeInteger) {
			return bad
		}
	}
	if n := metrics.AvgReply30dLong; n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0) {
		return bad
	}
	for _, value := range []*string{metrics.NewGoodRatioRate, metrics.LastVisitTime, metrics.IdleFishCreditLevel} {
		if value != nil && (!utf8.ValidString(*value) || strings.TrimSpace(*value) == "") {
			return bad
		}
	}
	return nil
}

func listingNotFound() error {
	return &ServiceError{Kind: ErrorNotFound, Reason: locales.Message{Code: "listings.not_found", Args: map[string]any{}}}
}

func setListingFlagsTx(ctx context.Context, tx *sql.Tx, itemID string, isFavorite, isDismissed *bool, nowMS int64) (bool, error) {
	item, err := loadListingTx(ctx, tx, itemID)
	if err != nil {
		return false, err
	}
	changed := false
	if isFavorite != nil && item.IsFavorite != *isFavorite {
		item.IsFavorite, changed = *isFavorite, true
		item.FavoritedAt = nil
		if *isFavorite {
			item.FavoritedAt = &nowMS
		}
	}
	if isDismissed != nil && item.IsDismissed != *isDismissed {
		item.IsDismissed, changed = *isDismissed, true
	}
	if !changed {
		return false, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE listings SET is_favorite=?,favorited_at=?,is_dismissed=? WHERE item_id=?`,
		item.IsFavorite, item.FavoritedAt, item.IsDismissed, itemID)
	return err == nil, err
}

func isSellerBlockedTx(ctx context.Context, tx *sql.Tx, sellerID string) (bool, error) {
	var blocked bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM blocked_sellers WHERE seller_id=?)`, sellerID).Scan(&blocked)
	return blocked, err
}
