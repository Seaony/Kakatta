//go:build kakatta_dev

package auth

import "context"

// DevelopmentSession 只供独立的本机假数据入口自动建立网页会话。
func (s *Service) DevelopmentSession(ctx context.Context) (Session, error) {
	a, err := s.admin(ctx)
	if err != nil {
		return Session{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, stored(err)
	}
	defer tx.Rollback()
	session, err := insertSession(ctx, tx, a, "本机假数据开发", s.now().UnixMilli())
	if err != nil {
		return Session{}, err
	}
	if err := tx.Commit(); err != nil {
		return Session{}, stored(err)
	}
	return session, nil
}
