package analysis

import "testing"

func sellerCheck(t *testing.T, result SellerResult, key string) EffectiveCheck {
	t.Helper()
	for _, c := range result.Checks {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("missing check %s", key)
	return EffectiveCheck{}
}

func TestSellerRegistrationBoundariesAndUnavailableProfile(t *testing.T) {
	for _, tc := range []struct {
		name string
		days *int
		want CheckStatus
	}{
		{"unknown", nil, CheckUnknown}, {"zero", ptr(0), CheckUnknown}, {"new", ptr(29), CheckRisk}, {"established", ptr(30), CheckOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := JudgeSeller(SellerInput{RegistrationDays: tc.days, CreditExcellent: ptr(true)}, []SellerSignal{{Key: "dealer_words", Evidence: "工作室"}}, "ipad")
			if got.Type != SellerUnknown || got.BonusEligible {
				t.Fatalf("unexpected profile: %+v", got)
			}
			if c := sellerCheck(t, got, "seller_new"); c.Status != tc.want {
				t.Fatalf("new=%+v want %s", c, tc.want)
			}
			for _, key := range []string{"seller_dealer", "seller_bad_reviews", "seller_partial_refund"} {
				if c := sellerCheck(t, got, key); c.Status != CheckUnknown {
					t.Fatalf("unavailable %s=%+v", key, c)
				}
			}
		})
	}
}

func TestSellerProgramFactsAndCredit(t *testing.T) {
	items := []SellerItem{{Title: "iPad mini", OnSale: true}, {Title: "IPAD mini", OnSale: true}, {Title: "ipad mini", OnSale: true}, {Title: "ipad mini", OnSale: false}}
	for _, tc := range []struct {
		name         string
		in           SellerInput
		signals      []SellerSignal
		kind         SellerType
		bonus        bool
		bad, partial CheckStatus
	}{
		{"three active items", SellerInput{Available: true, RecentItems: items, CreditExcellent: ptr(true)}, nil, SellerDealerSuspect, false, CheckOK, CheckOK},
		{"dealer words", SellerInput{Available: true}, []SellerSignal{{Key: "dealer_words", Evidence: "工作室"}}, SellerDealerSuspect, false, CheckOK, CheckOK},
		{"personal excellent", SellerInput{Available: true, CreditExcellent: ptr(true)}, nil, SellerPersonal, true, CheckOK, CheckOK},
		{"credit not mapped", SellerInput{Available: true}, nil, SellerPersonal, false, CheckOK, CheckOK},
		{"buyer negative does not block", SellerInput{Available: true, CreditExcellent: ptr(true), RecentRates: []SellerRate{{Negative: true}}}, nil, SellerPersonal, true, CheckOK, CheckOK},
		{"seller negative blocks bonus", SellerInput{Available: true, CreditExcellent: ptr(true), RecentRates: []SellerRate{{AsSeller: true, Negative: true}}}, nil, SellerPersonal, false, CheckOK, CheckOK},
		{"verified signals", SellerInput{Available: true}, []SellerSignal{{Key: "slow_ship", Evidence: "十天才寄"}, {Key: "partial_refund", Evidence: "到手刀"}}, SellerPersonal, false, CheckRisk, CheckRisk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := JudgeSeller(tc.in, tc.signals, "ipad mini")
			if got.Type != tc.kind || got.BonusEligible != tc.bonus {
				t.Fatalf("result=%+v", got)
			}
			if sellerCheck(t, got, "seller_bad_reviews").Status != tc.bad || sellerCheck(t, got, "seller_partial_refund").Status != tc.partial {
				t.Fatalf("checks=%+v", got.Checks)
			}
		})
	}
	limited := make([]SellerItem, 23)
	limited[20] = items[0]
	limited[21] = items[1]
	limited[22] = items[2]
	if got := JudgeSeller(SellerInput{Available: true, RecentItems: limited}, nil, "ipad mini"); got.Type != SellerPersonal {
		t.Fatal("used items after first 20")
	}
	rates := make([]SellerRate, 11)
	rates[10] = SellerRate{AsSeller: true, Negative: true}
	if got := JudgeSeller(SellerInput{Available: true, RecentRates: rates, CreditExcellent: ptr(true)}, nil, "ipad mini"); !got.BonusEligible {
		t.Fatal("used ratings after first 10")
	}
}
