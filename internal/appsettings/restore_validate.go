package appsettings

import (
	"context"
	"database/sql"

	"github.com/Seaony/Kakatta/internal/settings"
)

// ValidateStoredTx 校验候选设置和引用，不读取或推进额度时钟，事务仍由调用方管理。
func ValidateStoredTx(ctx context.Context, tx *sql.Tx, env settings.Environment) (settings.Snapshot, error) {
	stored, err := readValues(ctx, tx)
	if err != nil {
		return settings.Snapshot{}, err
	}
	snapshot, err := settings.Resolve(stored, env)
	if err != nil {
		return settings.Snapshot{}, err
	}
	if err := validateReferences(ctx, tx, snapshot.Values); err != nil {
		return settings.Snapshot{}, err
	}
	return snapshot, nil
}
