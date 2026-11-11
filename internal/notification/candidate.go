package notification

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Seaony/Kakatta/internal/locales"
	"github.com/Seaony/Kakatta/internal/security"
	"github.com/Seaony/Kakatta/internal/service"
)

// ValidateCandidate 只读取候选事务，不启动服务，也不根据当前业务重判历史。
func ValidateCandidate(ctx context.Context, tx *sql.Tx, cipher *security.Cipher) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx == nil || cipher == nil {
		return errors.New("通知候选校验缺少事务或密钥")
	}
	if err := validateCandidateChannels(ctx, tx, cipher); err != nil {
		return err
	}
	if err := validateCandidateDeliveries(ctx, tx); err != nil {
		return err
	}
	if err := validateCandidateIntents(ctx, tx); err != nil {
		return err
	}
	if err := validateCandidateSystemEvents(ctx, tx); err != nil {
		return err
	}
	for _, link := range []struct{ table, query string }{
		{"delivery_items", `SELECT delivery_id,finding_id FROM delivery_items`},
		{"delivery_intents", `SELECT delivery_id,intent_id FROM delivery_intents`},
	} {
		if err := candidateRows(ctx, tx, link.table, link.query, func(rows *sql.Rows) error {
			var deliveryID, memberID int64
			if err := rows.Scan(&deliveryID, &memberID); err != nil {
				return candidateError(link.table, deliveryID, "ids")
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func candidateError(table string, id int64, field string) error {
	return fmt.Errorf("通知候选 %s[%d].%s 无效", table, id, field)
}

func candidateRows(ctx context.Context, tx *sql.Tx, table, query string, visit func(*sql.Rows) error) error {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return candidateError(table, 0, "rows")
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := visit(rows); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return candidateError(table, 0, "rows")
	}
	return nil
}

func validateCandidateChannels(ctx context.Context, tx *sql.Tx, cipher *security.Cipher) error {
	return candidateRows(ctx, tx, "channels", `SELECT `+channelColumns+`,secret_enc IS NOT NULL FROM channels ORDER BY id`, func(rows *sql.Rows) error {
		var record channelRecord
		var config string
		var lastError sql.NullString
		var hasSecret bool
		// 历史参数保留 JSON 数字，不能由 scanChannel 的浮点解码截断或溢出。
		if err := rows.Scan(&record.ID, &record.UUID, &record.Type, &record.Name, &record.Enabled, &config, &record.SecretEnc,
			&record.LastTestAt, &record.LastTestOK, &lastError, &hasSecret); err != nil {
			return candidateError("channels", record.ID, "row")
		}
		if hasSecret && len(record.SecretEnc) == 0 {
			return candidateError("channels", record.ID, "secret_enc")
		}
		if record.ID <= 0 {
			return candidateError("channels", record.ID, "id")
		}
		if !utf8.ValidString(record.UUID) || record.UUID == "" || strings.Contains(record.UUID, ":") {
			return candidateError("channels", record.ID, "uuid")
		}
		if !utf8.ValidString(record.Name) || strings.TrimSpace(record.Name) == "" {
			return candidateError("channels", record.ID, "name")
		}
		if channelSecretKeys(record.Type) == nil {
			return candidateError("channels", record.ID, "type")
		}
		record.Config = json.RawMessage(config)
		if _, err := channelObject(record.Config, "config"); err != nil {
			return candidateError("channels", record.ID, "config")
		}
		validated, err := channelConfigWithCipher(record, cipher)
		clear(validated.Secrets)
		if err != nil {
			field := "config"
			if errors.Is(err, security.ErrCiphertext) || errors.Is(err, security.ErrAAD) || errors.Is(err, security.ErrMasterKey) {
				field = "secret_enc"
			}
			return candidateError("channels", record.ID, field)
		}
		if !candidateMessage(lastError, true) {
			return candidateError("channels", record.ID, "last_error")
		}
		return nil
	})
}

func candidateMessage(raw sql.NullString, allowNull bool) bool {
	if !raw.Valid {
		return true
	}
	if !utf8.ValidString(raw.String) {
		return false
	}
	data := bytes.TrimSpace([]byte(raw.String))
	if bytes.Equal(data, []byte("null")) {
		return allowNull
	}
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var message locales.Message
	return decoder.Decode(&message) == nil && message.Code != "" && decoder.Decode(new(any)) == io.EOF
}

func validateCandidateDeliveries(ctx context.Context, tx *sql.Tx) error {
	return candidateRows(ctx, tx, "deliveries", `SELECT id,at,channel_id,task_id,kind,item_count,ok,error,event_id,format_level FROM deliveries`, func(rows *sql.Rows) error {
		var id, formatLevel int64
		var at, channelID, taskID, count, eventID sql.NullInt64
		var kind, failure sql.NullString
		var ok sql.NullBool
		if err := rows.Scan(&id, &at, &channelID, &taskID, &kind, &count, &ok, &failure, &eventID, &formatLevel); err != nil {
			return candidateError("deliveries", id, "row")
		}
		if formatLevel < 0 || formatLevel > 5 {
			return candidateError("deliveries", id, "format_level")
		}
		if kind.Valid {
			switch kind.String {
			case "recommended", "all_new", "price_drop", "summary", "system", "test":
			default:
				return candidateError("deliveries", id, "kind")
			}
		}
		if !candidateMessage(failure, true) {
			return candidateError("deliveries", id, "error")
		}
		return nil
	})
}

func validateCandidateIntents(ctx context.Context, tx *sql.Tx) error {
	return candidateRows(ctx, tx, "notification_intents", `SELECT id,finding_id,task_id,kind,state,created_at,updated_at,price,previous_price,failure_count,first_failed_at,sent_at,reason FROM notification_intents`, func(rows *sql.Rows) error {
		var value intent
		var reason sql.NullString
		if err := rows.Scan(&value.ID, &value.FindingID, &value.TaskID, &value.Kind, &value.State, &value.CreatedAt, &value.UpdatedAt,
			&value.Price, &value.PreviousPrice, &value.FailureCount, &value.FirstFailedAt, &value.SentAt, &reason); err != nil {
			return candidateError("notification_intents", value.ID, "row")
		}
		switch value.Kind {
		case intentRecommended, intentNew, intentPriceDrop, intentPriceUpgrade:
		default:
			return candidateError("notification_intents", value.ID, "kind")
		}
		switch value.State {
		case intentPending, intentSent, intentAbandoned, intentCancelled:
		default:
			return candidateError("notification_intents", value.ID, "state")
		}
		switch {
		case value.Price != nil && *value.Price < 0:
			return candidateError("notification_intents", value.ID, "price")
		case value.PreviousPrice != nil && *value.PreviousPrice < 0:
			return candidateError("notification_intents", value.ID, "previous_price")
		case value.FailureCount < 0:
			return candidateError("notification_intents", value.ID, "failure_count")
		}
		if !candidateMessage(reason, false) {
			return candidateError("notification_intents", value.ID, "reason")
		}
		return nil
	})
}

func validateCandidateSystemEvents(ctx context.Context, tx *sql.Tx) error {
	return candidateRows(ctx, tx, "system_events", `SELECT id,kind,subject_id,message,created_at,resolved_at,notified_at,failure_count,first_failed_at FROM system_events`, func(rows *sql.Rows) error {
		var id, failures int64
		var kind, message sql.NullString
		var subjectID, createdAt, resolvedAt, notifiedAt, firstFailedAt sql.NullInt64
		if err := rows.Scan(&id, &kind, &subjectID, &message, &createdAt, &resolvedAt, &notifiedAt, &failures, &firstFailedAt); err != nil {
			return candidateError("system_events", id, "row")
		}
		if kind.Valid {
			switch service.SystemEventKind(kind.String) {
			case service.EventAccountVerify, service.EventAccountLogin, service.EventAccountDeny, service.EventAIError,
				service.EventAIBudget, service.EventTaskFailurePause, service.EventXianyuLayout, service.EventLoginFailures:
			default:
				return candidateError("system_events", id, "kind")
			}
		}
		if failures < 0 {
			return candidateError("system_events", id, "failure_count")
		}
		if !candidateMessage(message, false) {
			return candidateError("system_events", id, "message")
		}
		return nil
	})
}
