package service

// Wake 让已提交的设置或依赖变化进入原有调度，不另开轮询。
func (s *Service) Wake() { s.wakeScheduler() }
