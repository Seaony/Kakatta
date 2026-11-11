package fake

import (
	"encoding/json"
	"net/http"
	"net/url"
)

type SearchRound struct {
	Page1, Page2 []SearchItem
}

type searchSequence struct {
	rounds []SearchRound
	starts int
}

// SetSearchSequence 让一个搜索词按真实初次搜索推进；筛选和翻页仍用同一轮数据。
func (f *Xianyu) SetSearchSequence(keyword string, rounds []SearchRound) {
	copyRounds := make([]SearchRound, len(rounds))
	for i, round := range rounds {
		copyRounds[i] = SearchRound{Page1: append([]SearchItem(nil), round.Page1...), Page2: append([]SearchItem(nil), round.Page2...)}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sequences == nil {
		f.sequences = make(map[string]*searchSequence)
		f.sequenceSellers = make(map[string]string)
	}
	f.sequences[keyword] = &searchSequence{rounds: copyRounds}
	for _, round := range copyRounds {
		for _, items := range [][]SearchItem{round.Page1, round.Page2} {
			for _, item := range items {
				if item.SellerID != "" {
					f.sequenceSellers[item.ID] = item.SellerID
				}
			}
		}
	}
}

func (f *Xianyu) SearchSequenceStarts(keyword string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if sequence := f.sequences[keyword]; sequence != nil {
		return sequence.starts
	}
	return 0
}

func (f *Xianyu) serveSearchSequence(w http.ResponseWriter, r *http.Request, body []byte) bool {
	query, err := url.ParseQuery(string(body))
	if err != nil {
		return false
	}
	f.mu.Lock()
	sequence := f.sequences[query.Get("q")]
	if sequence == nil {
		f.mu.Unlock()
		return false
	}
	if r.URL.Query().Get("first") == "1" {
		sequence.starts++
	}
	var items []SearchItem
	if len(sequence.rounds) != 0 {
		round := sequence.rounds[min(max(sequence.starts-1, 0), len(sequence.rounds)-1)]
		items = round.Page1
		if r.URL.Query().Get("page") == "2" {
			items = round.Page2
		}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(searchItemsJSON(f.srv.URL, items))
	return true
}

func (f *Xianyu) sequenceSellerLocked(id string) string { return f.sequenceSellers[id] }
