package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func upsertSearchListingTx(ctx context.Context, tx *sql.Tx, observation SearchObservation) (Listing, *PriceChange, error) {
	if strings.TrimSpace(observation.ItemID) == "" || observation.Price != nil && *observation.Price < 0 {
		return Listing{}, nil, errors.New("搜索商品编号或价格无效")
	}
	previous, err := loadListingTx(ctx, tx, observation.ItemID)
	var missing *ServiceError
	if err != nil && (!errors.As(err, &missing) || missing.Kind != ErrorNotFound) {
		return Listing{}, nil, err
	}
	if err == nil && observation.ObservedAt < previous.LastSeenAt {
		return previous, nil, nil
	}
	var tags any
	if observation.Tags != nil {
		raw, err := json.Marshal(observation.Tags)
		if err != nil {
			return Listing{}, nil, err
		}
		tags = string(raw)
	}
	var sellerID *string
	if observation.SellerID != nil {
		value := strings.TrimSpace(*observation.SellerID)
		if value != "" {
			sellerID = &value
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO listings(item_id,title,desktop_url,price,price_text,
		original_price_text,thumbnail_url,area,seller_id,seller_nick,published_at,want_count,tags_json,first_seen_at,last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(item_id) DO UPDATE SET
		title=CASE WHEN excluded.title<>'' THEN excluded.title ELSE listings.title END,
		desktop_url=CASE WHEN excluded.desktop_url<>'' THEN excluded.desktop_url ELSE listings.desktop_url END,
		price=excluded.price,price_text=excluded.price_text,
		original_price_text=COALESCE(excluded.original_price_text,listings.original_price_text),
		thumbnail_url=COALESCE(excluded.thumbnail_url,listings.thumbnail_url),area=COALESCE(excluded.area,listings.area),
		seller_id=COALESCE(excluded.seller_id,listings.seller_id),
		seller_nick=COALESCE(excluded.seller_nick,listings.seller_nick),
		published_at=COALESCE(excluded.published_at,listings.published_at),
		want_count=COALESCE(excluded.want_count,listings.want_count),tags_json=COALESCE(excluded.tags_json,listings.tags_json),
		last_seen_at=excluded.last_seen_at`, observation.ItemID, observation.Title, observation.DesktopURL,
		observation.Price, observation.PriceText, observation.OriginalPriceText, observation.ThumbnailURL,
		observation.Area, sellerID, observation.SellerNick, observation.PublishedAt, observation.WantCount, tags,
		observation.ObservedAt, observation.ObservedAt)
	if err != nil {
		return Listing{}, nil, err
	}
	change, err := appendPricePointTx(ctx, tx, observation.ItemID, observation.Price, observation.ObservedAt)
	if err != nil {
		return Listing{}, nil, err
	}
	item, err := loadListingTx(ctx, tx, observation.ItemID)
	return item, change, err
}

func appendPricePointTx(ctx context.Context, tx *sql.Tx, itemID string, price *int64, observedAt int64) (*PriceChange, error) {
	if price == nil {
		return nil, nil
	}
	var previous int64
	err := tx.QueryRowContext(ctx, `SELECT price FROM price_points WHERE item_id=? ORDER BY observed_at DESC,id DESC LIMIT 1`, itemID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	change := &PriceChange{ItemID: itemID, Current: *price, ObservedAt: observedAt}
	if err == nil {
		if previous == *price {
			return nil, nil
		}
		if previous < 0 {
			return nil, errors.New("商品历史价格无效")
		}
		change.Previous = &previous
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_points(item_id,price,observed_at) VALUES(?,?,?)`, itemID, *price, observedAt); err != nil {
		return nil, err
	}
	if change.Previous != nil {
		var amount, at any
		if previous > *price {
			amount, at = previous-*price, observedAt
		}
		if _, err := tx.ExecContext(ctx, `UPDATE listings SET last_drop_amount=?,last_drop_at=? WHERE item_id=?`, amount, at, itemID); err != nil {
			return nil, err
		}
	}
	return change, nil
}

func recordSightingTx(ctx context.Context, tx *sql.Tx, taskID int64, itemID string, price *int64, observedAt int64, timezone string) error {
	if timezone == "" || timezone == "Local" {
		return errors.New("行情时区无效")
	}
	zone, err := time.LoadLocation(timezone)
	if err != nil {
		return fmt.Errorf("行情时区无效：%w", err)
	}
	day := time.UnixMilli(observedAt).In(zone).Format("2006-01-02")
	_, err = tx.ExecContext(ctx, `INSERT INTO task_sightings(task_id,item_id,day,price,last_seen_at) VALUES(?,?,?,?,?)
		ON CONFLICT(task_id,day,item_id) DO UPDATE SET price=excluded.price,last_seen_at=excluded.last_seen_at
		WHERE excluded.last_seen_at>=task_sightings.last_seen_at`, taskID, itemID, day, price, observedAt)
	return err
}

func saveDetailTx(ctx context.Context, tx *sql.Tx, work WorkRef, observation DetailObservation) error {
	_, finding, err := validateWorkTx(ctx, tx, work, StageDetail)
	if err != nil {
		return err
	}
	if observation.ItemID != finding.ItemID {
		return errors.New("详情商品与当前发现不一致")
	}
	item, err := loadListingTx(ctx, tx, observation.ItemID)
	if err != nil {
		return err
	}
	if item.DetailFetchedAt != nil && observation.FetchedAt < *item.DetailFetchedAt {
		return nil
	}
	var images any
	if observation.ImageURLs != nil {
		raw, err := json.Marshal(observation.ImageURLs)
		if err != nil {
			return err
		}
		images = string(raw)
	}
	var sellerDetail any
	if observation.SellerDetail != nil {
		if err := validateSellerDetailMetrics(observation.SellerDetail); err != nil {
			return err
		}
		raw, err := json.Marshal(observation.SellerDetail)
		if err != nil {
			return err
		}
		sellerDetail = string(raw)
	}
	_, err = tx.ExecContext(ctx, `UPDATE listings SET image_urls_json=COALESCE(?,image_urls_json),
		description=COALESCE(?,description),want_count=COALESCE(?,want_count),browse_count=COALESCE(?,browse_count),
		seller_id=COALESCE(?,seller_id),seller_registration_days=COALESCE(?,seller_registration_days),
		zhima_level=COALESCE(?,zhima_level),sale_status=COALESCE(?,sale_status),seller_detail_json=COALESCE(?,seller_detail_json),detail_fetched_at=? WHERE item_id=?`,
		images, observation.Description, observation.WantCount, observation.BrowseCount,
		observation.SellerID, observation.SellerRegistrationDays, observation.ZhimaLevel,
		observation.SaleStatus, sellerDetail, observation.FetchedAt, observation.ItemID)
	return err
}
