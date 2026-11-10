package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/Seaony/Kakatta/internal/xianyu"
	"github.com/Seaony/Kakatta/internal/xianyu/web"
)

func sellerPageProfile(sellerID string, page web.SellerPage, fetchedAt int64) *SellerProfile {
	profile := &SellerProfile{SellerID: sellerID, FetchedAt: fetchedAt}
	known := false
	if head := page.Head; head != nil {
		profile.Nick, profile.AvatarURL, profile.Introduction = detailText(head.Nick), detailText(head.Avatar), detailText(head.Intro)
		profile.SellerCredit, profile.BuyerCredit = detailText(head.SellerCredit), detailText(head.BuyerCredit)
		profile.SellerCreditLevel, profile.BuyerCreditLevel = detailText(head.SellerCreditLevel), detailText(head.BuyerCreditLevel)
		known = profile.Nick != nil || profile.AvatarURL != nil || profile.Introduction != nil ||
			profile.SellerCredit != nil || profile.BuyerCredit != nil || profile.SellerCreditLevel != nil || profile.BuyerCreditLevel != nil
		if head.ItemCountKnown {
			profile.ItemCount, known = findingInt64(int64(head.ItemCount)), true
		}
		if head.RateCountKnown {
			profile.RateCount, known = findingInt64(int64(head.RateCount)), true
		}
	}
	if items := page.Items; items != nil && items.ListKnown {
		known = true
		profile.RecentItems = []SellerRecentItem{}
		profile.OnSaleCount, profile.SoldCount = findingInt64(int64(items.OnSale)), findingInt64(int64(items.Sold))
		for _, item := range items.Items {
			recent := SellerRecentItem{Title: item.Title, ThumbnailURL: detailText(item.Image)}
			if item.PriceKnown {
				recent.Price = findingInt64(item.PriceCents)
			}
			switch item.Status {
			case xianyu.ItemOnSale:
				recent.SaleStatus = detailText("on_sale")
			case xianyu.ItemSold:
				recent.SaleStatus = detailText("sold")
			}
			profile.RecentItems = append(profile.RecentItems, recent)
		}
	}
	if ratings := page.Ratings; ratings != nil && ratings.ListKnown {
		known = true
		profile.RecentRates = []SellerRecentRate{}
		profile.SellerGoodCount, profile.SellerRateCount = findingInt64(int64(ratings.AsSeller.Good)), findingInt64(int64(ratings.AsSeller.Total))
		profile.BuyerGoodCount, profile.BuyerRateCount = findingInt64(int64(ratings.AsBuyer.Good)), findingInt64(int64(ratings.AsBuyer.Total))
		if rate, ok := ratings.AsSeller.GoodRate(); ok {
			profile.SellerGoodRate = &rate
		}
		if rate, ok := ratings.AsBuyer.GoodRate(); ok {
			profile.BuyerGoodRate = &rate
		}
		for _, rating := range ratings.Recent {
			recent := SellerRecentRate{Text: detailText(rating.Summary)}
			switch rating.Role {
			case xianyu.RoleSeller:
				recent.Role = detailText("seller")
			case xianyu.RoleBuyer:
				recent.Role = detailText("buyer")
			}
			switch rating.Kind {
			case xianyu.RatingGood:
				recent.Kind = detailText("good")
			case xianyu.RatingNeutral:
				recent.Kind = detailText("neutral")
			case xianyu.RatingBad:
				recent.Kind = detailText("bad")
			}
			profile.RecentRates = append(profile.RecentRates, recent)
		}
	}
	if !known {
		return nil
	}
	return profile
}

func detailText(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func saveSellerTx(ctx context.Context, tx *sql.Tx, work WorkRef, profile SellerProfile) error {
	_, finding, err := validateWorkTx(ctx, tx, work, StageDetail)
	if err != nil {
		return err
	}
	item, err := loadListingTx(ctx, tx, finding.ItemID)
	if err != nil {
		return err
	}
	if item.SellerID == nil || profile.SellerID == "" || *item.SellerID != profile.SellerID || profile.FetchedAt <= 0 {
		return errors.New("卖家缓存与当前商品不一致")
	}
	var items, rates any
	if profile.RecentItems != nil {
		raw, err := json.Marshal(profile.RecentItems)
		if err != nil {
			return err
		}
		items = string(raw)
	}
	if profile.RecentRates != nil {
		raw, err := json.Marshal(profile.RecentRates)
		if err != nil {
			return err
		}
		rates = string(raw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO seller_profiles(seller_id,nick,avatar_url,introduction,
		seller_credit,buyer_credit,seller_credit_level,buyer_credit_level,item_count,rate_count,
		on_sale_count,sold_count,seller_good_rate,buyer_good_rate,recent_items_json,recent_rates_json,
		fetched_at,seller_good_count,seller_rate_count,buyer_good_count,buyer_rate_count)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(seller_id) DO UPDATE SET
		nick=excluded.nick,avatar_url=excluded.avatar_url,introduction=excluded.introduction,
		seller_credit=excluded.seller_credit,buyer_credit=excluded.buyer_credit,
		seller_credit_level=excluded.seller_credit_level,buyer_credit_level=excluded.buyer_credit_level,
		item_count=excluded.item_count,rate_count=excluded.rate_count,on_sale_count=excluded.on_sale_count,
		sold_count=excluded.sold_count,seller_good_rate=excluded.seller_good_rate,buyer_good_rate=excluded.buyer_good_rate,
		recent_items_json=excluded.recent_items_json,recent_rates_json=excluded.recent_rates_json,
		fetched_at=excluded.fetched_at,seller_good_count=excluded.seller_good_count,
		seller_rate_count=excluded.seller_rate_count,buyer_good_count=excluded.buyer_good_count,
		buyer_rate_count=excluded.buyer_rate_count WHERE excluded.fetched_at>=seller_profiles.fetched_at`,
		profile.SellerID, profile.Nick, profile.AvatarURL, profile.Introduction, profile.SellerCredit,
		profile.BuyerCredit, profile.SellerCreditLevel, profile.BuyerCreditLevel, profile.ItemCount,
		profile.RateCount, profile.OnSaleCount, profile.SoldCount, profile.SellerGoodRate,
		profile.BuyerGoodRate, items, rates, profile.FetchedAt, profile.SellerGoodCount,
		profile.SellerRateCount, profile.BuyerGoodCount, profile.BuyerRateCount)
	return err
}
