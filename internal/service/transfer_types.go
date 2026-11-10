package service

import (
	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/settings"
)

type TaskChannelRef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type TaskImportItem struct {
	Index       int64            `json:"index"`
	Status      string           `json:"status"`
	TaskID      *int64           `json:"task_id"`
	Corrections []settings.Issue `json:"corrections"`
	Warnings    []settings.Issue `json:"warnings"`
	Reason      *locales.Message `json:"reason"`
	Fields      []settings.Issue `json:"fields"`
}

type TaskImportReport struct {
	Total       int64            `json:"total"`
	Imported    int64            `json:"imported"`
	Skipped     int64            `json:"skipped"`
	Failed      int64            `json:"failed"`
	Unprocessed int64            `json:"unprocessed"`
	Complete    bool             `json:"complete"`
	Failure     *locales.Message `json:"failure"`
	Items       []TaskImportItem `json:"items"`
}

type ExportInfo struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Rows      int64  `json:"rows"`
}
