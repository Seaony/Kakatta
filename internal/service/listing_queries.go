package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

type listingCursor struct {
	FavoritedAt *int64 `json:"favorited_at"`
	LastSeenAt  int64  `json:"last_seen_at"`
	ItemID      string `json:"item_id"`
}

func (s *Service) GetListing(ctx context.Context, itemID string) (ListingView, error) {
	if strings.TrimSpace(itemID) == "" || !utf8.ValidString(itemID) {
		return ListingView{}, queryError()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ListingView{}, storageFailure(err)
	}
	defer tx.Rollback()
	view, err := getListingViewTx(ctx, tx, itemID)
	if err != nil {
		return ListingView{}, storageFailure(err)
	}
	if err := tx.Commit(); err != nil {
		return ListingView{}, storageFailure(err)
	}
	return view, nil
}

func (s *Service) ListListings(ctx context.Context, q ListingQuery) (Page[ListingView], error) {
	page := Page[ListingView]{Items: []ListingView{}}
	var err error
	q.ListQuery, err = normalizeListQuery(q.ListQuery)
	if err != nil {
		return page, err
	}
	if !utf8.ValidString(q.Q) {
		return page, queryError()
	}
	q.Q = strings.TrimSpace(q.Q)
	binding := q
	binding.ListQuery = ListQuery{}
	var anchor listingCursor
	if err := decodeCursor(q.Cursor, "listings", binding, &anchor); err != nil {
		return page, err
	}
	if q.Cursor != "" && strings.TrimSpace(anchor.ItemID) == "" {
		return page, queryError()
	}
	where, args := []string{"1=1"}, []any{}
	if q.Favorite != nil {
		where, args = append(where, "l.is_favorite=?"), append(args, *q.Favorite)
	}
	if q.Dismissed != nil {
		where, args = append(where, "l.is_dismissed=?"), append(args, *q.Dismissed)
	}
	if q.Q != "" {
		where = append(where, `(instr(lower(l.title),lower(?))>0 OR instr(lower(COALESCE(l.description,'')),lower(?))>0)`)
		args = append(args, q.Q, q.Q)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, storageFailure(err)
	}
	defer tx.Rollback()
	base := strings.Join(where, " AND ")
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM listings l WHERE "+base, args...).Scan(&page.Total); err != nil {
		return page, storageFailure(err)
	}
	if q.Cursor != "" {
		sub := `(l.last_seen_at<? OR (l.last_seen_at=? AND l.item_id>?))`
		if anchor.FavoritedAt == nil {
			where = append(where, `(l.favorited_at IS NULL AND `+sub+`)`)
		} else {
			where = append(where, `(l.favorited_at IS NULL OR l.favorited_at<? OR (l.favorited_at=? AND `+sub+`))`)
			args = append(args, *anchor.FavoritedAt, *anchor.FavoritedAt)
		}
		args = append(args, anchor.LastSeenAt, anchor.LastSeenAt, anchor.ItemID)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT l.item_id,l.favorited_at,l.last_seen_at FROM listings l WHERE `+
		strings.Join(where, " AND ")+` ORDER BY (l.favorited_at IS NULL),l.favorited_at DESC,l.last_seen_at DESC,l.item_id ASC LIMIT ?`, args...)
	if err != nil {
		return page, storageFailure(err)
	}
	anchors := []listingCursor{}
	for rows.Next() {
		var item listingCursor
		if err := rows.Scan(&item.ItemID, &item.FavoritedAt, &item.LastSeenAt); err != nil {
			rows.Close()
			return page, storageFailure(err)
		}
		anchors = append(anchors, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, storageFailure(err)
	}
	if int64(len(anchors)) > q.Limit {
		anchors = anchors[:int(q.Limit)]
		token, err := encodeCursor("listings", binding, anchors[len(anchors)-1])
		if err != nil {
			return page, err
		}
		page.NextCursor = &token
	}
	for _, item := range anchors {
		view, err := getListingViewTx(ctx, tx, item.ItemID)
		if err != nil {
			return Page[ListingView]{Items: []ListingView{}}, storageFailure(err)
		}
		page.Items = append(page.Items, view)
	}
	if err := tx.Commit(); err != nil {
		return Page[ListingView]{Items: []ListingView{}}, storageFailure(err)
	}
	return page, nil
}

func getListingViewTx(ctx context.Context, tx *sql.Tx, itemID string) (ListingView, error) {
	item, err := loadListingTx(ctx, tx, itemID)
	if err != nil {
		return ListingView{}, err
	}
	view := ListingView{Listing: item, Findings: []FindingView{}, Prices: []PricePoint{}}
	if item.SellerID != nil {
		view.Seller, err = loadSellerTx(ctx, tx, *item.SellerID)
		if err != nil {
			return ListingView{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM findings WHERE item_id=? ORDER BY found_at DESC,id DESC`, itemID)
	if err != nil {
		return ListingView{}, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ListingView{}, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ListingView{}, err
	}
	for _, id := range ids {
		finding, err := getFindingViewTx(ctx, tx, id)
		if err != nil {
			return ListingView{}, err
		}
		view.Findings = append(view.Findings, finding)
	}
	view.Prices, err = pricesTx(ctx, tx, itemID)
	return view, err
}

type priceCursor struct {
	ObservedAt int64 `json:"observed_at"`
	ID         int64 `json:"id"`
}

func (s *Service) ListPrices(ctx context.Context, itemID string, q ListQuery) (Page[PricePoint], error) {
	page := Page[PricePoint]{Items: []PricePoint{}}
	if strings.TrimSpace(itemID) == "" || !utf8.ValidString(itemID) {
		return page, queryError()
	}
	q, err := normalizeListQuery(q)
	if err != nil {
		return page, err
	}
	var anchor priceCursor
	if err := decodeCursor(q.Cursor, "prices:"+itemID, ListQuery{}, &anchor); err != nil {
		return page, err
	}
	if q.Cursor != "" && anchor.ID <= 0 {
		return page, queryError()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, storageFailure(err)
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM listings WHERE item_id=?)`, itemID).Scan(&exists); err != nil {
		return page, storageFailure(err)
	}
	if !exists {
		return page, listingNotFound()
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM price_points WHERE item_id=?`, itemID).Scan(&page.Total); err != nil {
		return page, storageFailure(err)
	}
	where, args := `item_id=?`, []any{itemID}
	if q.Cursor != "" {
		where += ` AND (observed_at>? OR (observed_at=? AND id>?))`
		args = append(args, anchor.ObservedAt, anchor.ObservedAt, anchor.ID)
	}
	args = append(args, q.Limit+1)
	rows, err := tx.QueryContext(ctx, `SELECT id,item_id,price,observed_at FROM price_points WHERE `+where+` ORDER BY observed_at,id LIMIT ?`, args...)
	if err != nil {
		return page, storageFailure(err)
	}
	page.Items, err = scanPrices(rows)
	if err != nil {
		return page, storageFailure(err)
	}
	if int64(len(page.Items)) > q.Limit {
		page.Items = page.Items[:int(q.Limit)]
		last := page.Items[len(page.Items)-1]
		token, err := encodeCursor("prices:"+itemID, ListQuery{}, priceCursor{ObservedAt: last.ObservedAt, ID: last.ID})
		if err != nil {
			return page, err
		}
		page.NextCursor = &token
	}
	if err := tx.Commit(); err != nil {
		return Page[PricePoint]{Items: []PricePoint{}}, storageFailure(err)
	}
	return page, nil
}

func pricesTx(ctx context.Context, tx *sql.Tx, itemID string) ([]PricePoint, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,item_id,price,observed_at FROM price_points WHERE item_id=? ORDER BY observed_at,id`, itemID)
	if err != nil {
		return nil, err
	}
	return scanPrices(rows)
}

func scanPrices(rows *sql.Rows) ([]PricePoint, error) {
	defer rows.Close()
	points := []PricePoint{}
	for rows.Next() {
		var point PricePoint
		if err := rows.Scan(&point.ID, &point.ItemID, &point.Price, &point.ObservedAt); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func loadSellerTx(ctx context.Context, tx *sql.Tx, sellerID string) (*SellerProfile, error) {
	if sellerID == "" {
		return nil, nil
	}
	var seller SellerProfile
	var items, rates sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT seller_id,nick,avatar_url,introduction,seller_credit,buyer_credit,
		seller_credit_level,buyer_credit_level,item_count,rate_count,on_sale_count,sold_count,
		seller_good_rate,buyer_good_rate,seller_good_count,seller_rate_count,buyer_good_count,buyer_rate_count,
		recent_items_json,recent_rates_json,fetched_at FROM seller_profiles WHERE seller_id=?`, sellerID).Scan(
		&seller.SellerID, &seller.Nick, &seller.AvatarURL, &seller.Introduction, &seller.SellerCredit,
		&seller.BuyerCredit, &seller.SellerCreditLevel, &seller.BuyerCreditLevel, &seller.ItemCount,
		&seller.RateCount, &seller.OnSaleCount, &seller.SoldCount, &seller.SellerGoodRate, &seller.BuyerGoodRate,
		&seller.SellerGoodCount, &seller.SellerRateCount, &seller.BuyerGoodCount, &seller.BuyerRateCount,
		&items, &rates, &seller.FetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if items.Valid {
		if err := json.Unmarshal([]byte(items.String), &seller.RecentItems); err != nil {
			return nil, fmt.Errorf("卖家商品缓存损坏：%w", err)
		}
	}
	if rates.Valid {
		if err := json.Unmarshal([]byte(rates.String), &seller.RecentRates); err != nil {
			return nil, fmt.Errorf("卖家评价缓存损坏：%w", err)
		}
	}
	return &seller, nil
}
