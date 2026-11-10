package web

import (
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/xianyu"
)

// SellerPage 保留这次取得的部分资料；nil 不是成功取得的空列表。
type SellerPage struct {
	Head     *xianyu.SellerHead
	Items    *xianyu.SellerItems
	Ratings  *xianyu.SellerRatings
	Opened   bool
	Warnings []locales.Message
}
