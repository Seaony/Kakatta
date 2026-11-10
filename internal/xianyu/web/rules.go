package web

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"time"
)

//go:embed rules.json
var bundledRules []byte

type protocolRules struct {
	Version int `json:"version"`
	API     struct {
		Search   string `json:"search"`
		Detail   string `json:"detail"`
		UserHead string `json:"user_head"`
		Mtop     string `json:"mtop"`
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
	Timeouts     map[string]int `json:"timeouts_ms"`
	Unconfirmed  []string       `json:"unconfirmed"`
	regionSubmit *regexp.Regexp
}

var rules = func() protocolRules {
	r, err := decodeRules(bundledRules)
	if err != nil {
		panic(err)
	}
	return r
}()

func decodeRules(data []byte) (protocolRules, error) {
	var r protocolRules
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, fmt.Errorf("网页规则格式无效：%w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return r, fmt.Errorf("网页规则后有多余内容")
	}
	if r.Version != 1 {
		return r, fmt.Errorf("不支持的网页规则版本：%d", r.Version)
	}
	for _, section := range []any{r.API, r.Selectors, r.Text} {
		value := reflect.ValueOf(section)
		for i := range value.NumField() {
			if value.Field(i).Len() == 0 {
				return r, fmt.Errorf("网页规则缺少 %s", value.Type().Field(i).Tag.Get("json"))
			}
		}
	}
	for _, key := range []string{"home", "search_navigation", "search_response", "search_input", "search_ready", "advertisement", "element", "filter_response", "filter_extra", "page_extra", "page_button", "response_quiet", "region_popover", "region_area", "region_option", "price_fallback", "detail", "profile", "profile_response", "login_poll", "element_poll"} {
		if r.Timeouts[key] <= 0 {
			return r, fmt.Errorf("网页规则缺少有效等待时间：%s", key)
		}
	}
	var err error
	r.regionSubmit, err = regexp.Compile(r.Text.RegionSubmitPattern)
	if err != nil {
		return r, fmt.Errorf("地区提交按钮规则无效：%w", err)
	}
	return r, nil
}

func (r protocolRules) timeout(key string) time.Duration {
	return time.Duration(r.Timeouts[key]) * time.Millisecond
}
