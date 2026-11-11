package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/limits"
	"github.com/Seaony/Kakatta/internal/locales"
)

var findingExportHeader = []string{"标题", "价格", "配置", "合理价", "分数", "档位", "结论", "主要风险", "理由", "卖家", "地区", "发现时间", "链接"}

func (s *Service) ExportFindingsCSV(ctx context.Context, taskID int64, query FindingQuery, dst io.Writer) (ExportInfo, error) {
	if taskID <= 0 || taskID > limits.MaxSafeInteger || dst == nil || query.TaskID != nil && *query.TaskID != taskID {
		return ExportInfo{}, queryError()
	}
	query.TaskID, query.ListQuery = &taskID, ListQuery{}
	query, err := normalizeFindingQuery(query)
	if err != nil {
		return ExportInfo{}, err
	}
	var info ExportInfo
	err = s.exportSnapshot(ctx, func(ctx context.Context, tx *sql.Tx) error {
		task, err := loadTaskTx(ctx, tx, taskID)
		if err != nil {
			return err
		}
		location, now, err := s.exportTimeTx(ctx, tx)
		if err != nil {
			return err
		}
		info = ExportInfo{Filename: exportFilename(task.Name, now.Format("2006-01-02"), ".csv"), MediaType: "text/csv; charset=utf-8"}
		writer := exportWriter{ctx, dst}
		if _, err := writer.Write([]byte{0xef, 0xbb, 0xbf}); err != nil {
			return err
		}
		var buffer bytes.Buffer
		writeRow := func(fields []string) error { return writeExportCSVRow(writer, &buffer, fields) }
		if err := writeRow(findingExportHeader); err != nil {
			return err
		}
		var anchor *findingCursor
		for {
			where, args := findingFilter(query)
			if anchor != nil {
				condition, values := findingAfter(*anchor, query.Sort, true)
				where += " AND " + condition
				args = append(args, values...)
			}
			rows, err := tx.QueryContext(ctx, `SELECT f.id,f.found_at,`+findingScoreSQL+`,CASE WHEN `+findingRecommendedSQL+` THEN 1 ELSE 0 END`+findingFromSQL+`WHERE `+where+` ORDER BY `+findingOrder(query.Sort, false)+` LIMIT ?`, append(args, exportBatchSize)...)
			if err != nil {
				return err
			}
			type rowRef struct {
				findingCursor
				recommended bool
			}
			batch := []rowRef{}
			for rows.Next() {
				var item rowRef
				if err := rows.Scan(&item.ID, &item.FoundAt, &item.Score, &item.recommended); err != nil {
					rows.Close()
					return err
				}
				batch = append(batch, item)
			}
			err = errors.Join(rows.Err(), rows.Close())
			if err != nil {
				return err
			}
			if len(batch) == 0 {
				return nil
			}
			for _, item := range batch {
				view, err := s.getFindingViewTx(ctx, tx, item.ID)
				if err != nil {
					return err
				}
				fields, err := findingExportFields(view, item.recommended, location)
				if err != nil {
					return err
				}
				if err := writeRow(fields); err != nil {
					return err
				}
				info.Rows++
			}
			last := batch[len(batch)-1].findingCursor
			anchor = &last
		}
	})
	if err != nil {
		return ExportInfo{}, storageFailure(err)
	}
	return info, nil
}

func writeExportCSVRow(dst io.Writer, buffer *bytes.Buffer, fields []string) error {
	if err := validExportFields(fields); err != nil {
		return err
	}
	buffer.Reset()
	writer := csv.NewWriter(buffer)
	if err := writer.Write(fields); err != nil {
		return err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}
	// 只替换记录尾，保住字段内的孤立回车和原换行。
	line := buffer.Bytes()
	if _, err := dst.Write(line[:len(line)-1]); err != nil {
		return err
	}
	_, err := io.WriteString(dst, "\r\n")
	return err
}

func exportCSVText(value string) string {
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if strings.ContainsRune("=+-@＝＋－＠", r) {
			return "'" + value
		}
		break
	}
	return value
}

func exportMoney(value *int64) string {
	if value == nil {
		return ""
	}
	n := big.NewInt(*value)
	negative := n.Sign() < 0
	n.Abs(n)
	whole, fraction := new(big.Int), new(big.Int)
	whole.QuoRem(n, big.NewInt(100), fraction)
	prefix := ""
	if negative {
		prefix = "-"
	}
	return prefix + whole.String() + "." + strconv.FormatInt(fraction.Int64()+100, 10)[1:]
}

func findingExportFields(view FindingView, recommended bool, location *time.Location) ([]string, error) {
	fields := make([]string, len(findingExportHeader))
	fields[0], fields[1] = view.Listing.Title, exportMoney(view.Listing.Price)
	if view.Task.JudgeMode != analysis.ModeKeyword && view.Decision == analysis.DecisionScored &&
		(view.Score != nil || view.Grade != nil && *view.Grade == analysis.GradeAvoid) {
		if view.ItemSpecs != nil {
			if view.ItemSpecs.ConfigKey != nil && *view.ItemSpecs.ConfigKey == "*" {
				var err error
				fields[2], err = exportLabel("results.card.no_config")
				if err != nil {
					return nil, err
				}
			} else {
				specs := []string{}
				for _, spec := range view.ItemSpecs.Specs {
					value := exportOptional(spec.Value)
					if spec.Value == nil {
						var err error
						value, err = exportLabel("results.card.config_unknown")
						if err != nil {
							return nil, err
						}
					}
					specs = append(specs, spec.Name+": "+value)
				}
				fields[2] = strings.Join(specs, "、")
			}
		}
		fields[3], fields[4] = exportMoney(view.FairPrice), exportOptional(view.Score)
		if view.Grade != nil {
			var err error
			fields[5], err = exportLabel("analysis.grade." + string(*view.Grade))
			if err != nil {
				return nil, err
			}
		}
	}
	var err error
	fields[6], err = findingExportConclusion(view, recommended)
	if err != nil {
		return nil, err
	}
	fields[7], err = findingExportRisks(view)
	if err != nil {
		return nil, err
	}
	fields[8], err = exportMessage(locales.ZhCN, view.Reason)
	if err != nil {
		return nil, err
	}
	fields[9], fields[10] = exportOptional(view.Listing.SellerNick), exportOptional(view.Listing.Area)
	fields[11], fields[12] = exportTimestamp(&view.FoundAt, location), view.Listing.DesktopURL
	for i := range fields {
		if i != 1 && i != 3 && i != 4 {
			fields[i] = exportCSVText(fields[i])
		}
	}
	return fields, nil
}

func findingExportConclusion(view FindingView, recommended bool) (string, error) {
	if recommended {
		return exportLabel("logs.metrics.recommended")
	}
	if view.Task.BudgetMax != nil && view.Listing.Price != nil && *view.Listing.Price > *view.Task.BudgetMax &&
		(view.Decision == analysis.DecisionMatched || view.Decision == analysis.DecisionScored) {
		return exportLabel("results.card.over_budget")
	}
	switch view.Decision {
	case DecisionPending:
		return exportLabel("results.filter.pending")
	case DecisionFailed:
		return exportLabel("results.filter.failed")
	case DecisionSkipped:
		return exportLabel("results.filter.skipped")
	case analysis.DecisionMatched:
		return exportLabel("results.filter.unrecommended")
	case analysis.DecisionScored:
		if view.Grade != nil {
			return exportLabel("analysis.grade." + string(*view.Grade))
		}
		return exportLabel("results.card.unpriced")
	case analysis.DecisionRejected:
		if view.RejectKind != nil && *view.RejectKind == analysis.RejectUnmatched {
			return exportLabel("overview.unmatched")
		}
		if view.RejectKind != nil && *view.RejectKind == analysis.RejectJunk && view.Reason != nil {
			return exportMessage(locales.ZhCN, view.Reason)
		}
		return exportLabel("results.filter.rejected")
	default:
		return "", errors.New("发现记录的结论无效")
	}
}

func findingExportRisks(view FindingView) (string, error) {
	checks := map[string]analysis.CheckStatus{}
	for _, check := range view.Checks {
		checks[check.Key] = check.Status
	}
	questions := map[string]bool{}
	for _, question := range view.Questions {
		questions[question.Key] = true
	}
	dictionary := map[string]bool{}
	for _, risk := range analysis.RiskDictionary() {
		dictionary[risk.ID] = true
	}
	names, seen := []string{}, map[string]bool{}
	for _, risk := range analysis.ResolveRisks(view.Task.Profile) {
		status := checks[risk.ID]
		if status != analysis.CheckRisk && status != analysis.CheckSuspect && !questions[risk.ID] {
			continue
		}
		name := risk.Name
		if dictionary[risk.ID] {
			var err error
			name, err = exportLabel("analysis.risk_name." + risk.ID)
			if err != nil {
				return "", err
			}
		}
		key := analysis.RiskNameKey(name)
		if !seen[key] {
			names, seen[key] = append(names, name), true
		}
	}
	if view.Analysis != nil {
		for _, extra := range view.Analysis.ExtraFindings {
			key := analysis.RiskNameKey(extra.Name)
			if !seen[key] {
				names, seen[key] = append(names, extra.Name), true
			}
		}
	}
	return strings.Join(names, "、"), nil
}
