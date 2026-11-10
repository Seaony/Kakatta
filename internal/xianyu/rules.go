package xianyu

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

//go:embed rules.json
var bundledRules []byte

// WebRules 是随程序发布的网页规则，不包含可执行脚本。
type WebRules struct {
	Version int `json:"version"`
	API     struct {
		Search      string `json:"search"`
		Detail      string `json:"detail"`
		UserHead    string `json:"user_head"`
		UserItems   string `json:"user_items"`
		UserRatings string `json:"user_ratings"`
		Mtop        string `json:"mtop"`
	} `json:"api"`
	Selectors struct {
		SearchCandidates  string   `json:"search_candidates"`
		SearchReady       string   `json:"search_ready"`
		CloseAd           string   `json:"close_ad"`
		NextPage          string   `json:"next_page"`
		RegionPopover     string   `json:"region_popover"`
		RegionAreaClass   string   `json:"region_area_class"`
		RegionOptionClass string   `json:"region_option_class"`
		RegionSubmitClass string   `json:"region_submit_class"`
		PriceContainer    string   `json:"price_container"`
		PriceInput        string   `json:"price_input"`
		LoginBox          string   `json:"login_box"`
		SellerRatings     string   `json:"seller_ratings"`
		VerifyMasks       []string `json:"verify_masks"`
		BlockedTexts      []string `json:"blocked_texts"`
	} `json:"selectors"`
	Text struct {
		PublishMenu         string `json:"publish_menu"`
		PublishLatest       string `json:"publish_latest"`
		Personal            string `json:"personal"`
		Shipping            string `json:"shipping"`
		Region              string `json:"region"`
		RegionRelocate      string `json:"region_relocate"`
		RegionView          string `json:"region_view"`
		RegionSubmitPattern string `json:"region_submit_pattern"`
	} `json:"text"`
	Timeouts    map[string]int `json:"timeouts_ms"`
	Unconfirmed []string       `json:"unconfirmed"`
}

type protocolRules struct {
	WebRules
	Fields struct {
		Search struct {
			List          []string `json:"list"`
			Main          []string `json:"main"`
			Content       []string `json:"content"`
			Args          []string `json:"args"`
			ID            []string `json:"id"`
			Title         []string `json:"title"`
			Price         []string `json:"price"`
			OriginalPrice []string `json:"original_price"`
			Image         []string `json:"image"`
			Area          []string `json:"area"`
			SellerNick    []string `json:"seller_nick"`
			URL           []string `json:"url"`
			Published     []string `json:"published"`
			Want          []string `json:"want"`
			FreeShipTag   []string `json:"free_ship_tag"`
			Tags          []string `json:"tags"`
			TagText       []string `json:"tag_text"`
			PriceText     []string `json:"price_text"`
		} `json:"search"`
		Detail struct {
			Item         []string   `json:"item"`
			Seller       []string   `json:"seller"`
			Images       []string   `json:"images"`
			ImageURL     []string   `json:"image_url"`
			Want         []string   `json:"want"`
			Browse       []string   `json:"browse"`
			SellerID     []string   `json:"seller_id"`
			RegDays      []string   `json:"reg_days"`
			Zhima        []string   `json:"zhima"`
			Descriptions [][]string `json:"descriptions"`
		} `json:"detail"`
		Seller struct {
			Head struct {
				Module    []string `json:"module"`
				Base      []string `json:"base"`
				Nick      []string `json:"nick"`
				Avatar    []string `json:"avatar"`
				Intro     []string `json:"intro"`
				ItemCount []string `json:"item_count"`
				RateCount []string `json:"rate_count"`
				Tags      []string `json:"tags"`
				TagText   []string `json:"tag_text"`
				TagLevel  []string `json:"tag_level"`
				TagRole   []string `json:"tag_role"`
			} `json:"head"`
			Cards    []string `json:"cards"`
			CardData []string `json:"card_data"`
			Items    struct {
				ID     []string `json:"id"`
				Title  []string `json:"title"`
				Price  []string `json:"price"`
				Image  []string `json:"image"`
				Status []string `json:"status"`
			} `json:"items"`
			Ratings struct {
				ID       []string `json:"id"`
				Feedback []string `json:"feedback"`
				Rater    []string `json:"rater"`
				Created  []string `json:"created"`
				Kind     []string `json:"kind"`
				Tags     []string `json:"tags"`
				TagText  []string `json:"tag_text"`
				Images   []string `json:"images"`
			} `json:"ratings"`
		} `json:"seller"`
	} `json:"fields"`
}

var rules = func() protocolRules {
	r, err := decodeRules(bundledRules)
	if err != nil {
		panic(err)
	}
	return r
}()

// WebProtocol 返回独立副本，网页层不能改坏解析层共用的规则。
func WebProtocol() WebRules {
	out := rules.WebRules
	out.Selectors.VerifyMasks = slices.Clone(out.Selectors.VerifyMasks)
	out.Selectors.BlockedTexts = slices.Clone(out.Selectors.BlockedTexts)
	out.Timeouts = maps.Clone(out.Timeouts)
	out.Unconfirmed = slices.Clone(out.Unconfirmed)
	return out
}

func decodeRules(data []byte) (protocolRules, error) {
	var r protocolRules
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, fmt.Errorf("闲鱼规则格式无效：%w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return r, fmt.Errorf("闲鱼规则后有多余内容")
	}
	if r.Version != 2 {
		return r, fmt.Errorf("不支持的闲鱼规则版本：%d", r.Version)
	}
	for name, section := range map[string]any{"api": r.API, "selectors": r.Selectors, "text": r.Text, "fields": r.Fields, "unconfirmed": r.Unconfirmed} {
		if err := validateRuleStrings(reflect.ValueOf(section), name); err != nil {
			return r, err
		}
	}
	keys := []string{"home", "search_navigation", "search_response", "search_input", "search_ready", "advertisement", "element", "filter_response", "filter_extra", "page_extra", "page_button", "response_quiet", "region_popover", "region_area", "region_option", "price_fallback", "detail", "profile", "profile_response", "seller_items_response", "seller_ratings_response", "login_poll", "element_poll"}
	for _, key := range keys {
		if ms := r.Timeouts[key]; ms <= 0 || int64(ms) > math.MaxInt64/int64(time.Millisecond) {
			return r, fmt.Errorf("闲鱼规则缺少有效等待时间：%s", key)
		}
	}
	if len(r.Timeouts) != len(keys) {
		return r, fmt.Errorf("闲鱼规则包含未知等待时间")
	}
	if _, err := regexp.Compile(r.Text.RegionSubmitPattern); err != nil {
		return r, fmt.Errorf("地区提交按钮规则无效：%w", err)
	}
	return r, nil
}

func validateRuleStrings(value reflect.Value, name string) error {
	switch value.Kind() {
	case reflect.Struct:
		for i := range value.NumField() {
			key := value.Type().Field(i).Tag.Get("json")
			if key == "" {
				key = value.Type().Field(i).Name
			}
			if err := validateRuleStrings(value.Field(i), name+"."+key); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if value.Len() == 0 {
			return fmt.Errorf("闲鱼规则缺少 %s", name)
		}
		for i := range value.Len() {
			if err := validateRuleStrings(value.Index(i), name); err != nil {
				return err
			}
		}
	case reflect.String:
		if strings.TrimSpace(value.String()) == "" {
			return fmt.Errorf("闲鱼规则缺少 %s", name)
		}
	}
	return nil
}
