package service

import (
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/Seaony/Kakatta/internal/analysis"
	"github.com/Seaony/Kakatta/internal/locales"
)

func ValidateProfileDraftJSON(raw []byte) error {
	if reason := taskJSONShape(raw, reflect.TypeFor[analysis.Profile]()); reason != nil {
		return invalidTaskField("profile", *reason)
	}
	return nil
}

// 生成前只整理草稿；真正保存任务时才推进编号水位。
func PrepareProfileGenerationTx(ctx context.Context, tx *sql.Tx, taskID *int64, proposed analysis.Profile) (trustedOld, prepared analysis.Profile, warnings []locales.Message, err error) {
	warnings = []locales.Message{}
	if taskID != nil {
		task, err := loadTaskTx(ctx, tx, *taskID)
		if err != nil {
			return trustedOld, prepared, warnings, storageFailure(err)
		}
		watermark, err := loadTaskProfileWatermarkTx(ctx, tx, task)
		if err != nil {
			return trustedOld, prepared, warnings, storageFailure(err)
		}
		trustedOld = task.Profile
		trustedOld.NextNo = watermark
	}
	prepared, err = taskProfileForRevision(trustedOld, proposed)
	if err != nil {
		return trustedOld, analysis.Profile{}, warnings, err
	}
	prepared, warnings, err = analysis.ReviseProfile(trustedOld, prepared)
	if err != nil {
		var invalid *analysis.Error
		if errors.As(err, &invalid) {
			err = invalidTaskField("profile", invalid.Message)
		}
		return trustedOld, analysis.Profile{}, warnings, err
	}
	return trustedOld, prepared, warnings, nil
}
