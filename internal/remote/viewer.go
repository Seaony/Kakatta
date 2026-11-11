package remote

import (
	"html/template"
	"net/http"

	"github.com/Seaony/Kakatta/internal/locales"
)

var viewerPage = template.Must(template.New("index.html").ParseFS(assets, "assets/index.html"))

type viewerCopy struct {
	Lang locales.Language
	Text map[string]string
}

func viewerText(language locales.Language, code string) string {
	text, _ := locales.Format(language, locales.Message{Code: "remote.viewer." + code})
	return text
}

func viewerDocument(w http.ResponseWriter, r *http.Request) {
	language := locales.Select("", r.Header.Get("Accept-Language"))
	var document struct {
		Current viewerCopy
		Copies  []viewerCopy
	}
	for _, copyLanguage := range []locales.Language{locales.ZhCN, locales.En, locales.Ja} {
		copy := viewerCopy{Lang: copyLanguage, Text: map[string]string{}}
		for _, code := range []string{
			"title", "connecting", "connected", "disconnected", "connection_failed", "toolbar", "actual", "fit", "pan", "resume", "text_toggle",
			"initial_help", "text_entry", "text_label", "text_help", "text_send", "text_backspace", "text_enter", "text_close", "screen", "canvas",
			"help_pan", "help_control", "help_verification", "help_position_unavailable", "help_disconnected", "text_sent",
		} {
			copy.Text[code] = viewerText(copyLanguage, code)
		}
		document.Copies = append(document.Copies, copy)
		if copyLanguage == language {
			document.Current = copy
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = viewerPage.Execute(w, document)
}
