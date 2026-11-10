package service

// Wake 通知调度器重新检查运行条件。
func (s *Service) Wake() { s.wakeScheduler() }

// SettingsChanged 通知调度和清理重新读取已保存的设置。
func (s *Service) SettingsChanged() {
	s.wakeScheduler()
	select {
	case s.cleanupWake <- struct{}{}:
	default:
	}
}
