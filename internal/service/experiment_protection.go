package service

import (
	"time"

	"github.com/Seaony/Kakatta/internal/experimentstate"
)

func (s *Service) experimentBlock(now time.Time) *RuntimeBlocked {
	protection, err := experimentstate.ReadProtection(s.dataDir, now)
	if err != nil {
		return runBlock("run.experiment_records_unavailable", nil)
	}
	if protection == nil {
		return nil
	}
	var until *int64
	if protection.Until != nil {
		at := protection.Until.UnixMilli()
		until = &at
	}
	return runBlock("run.experiment_"+protection.Kind, until)
}
