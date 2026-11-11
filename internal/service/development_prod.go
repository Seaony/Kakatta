//go:build !kakatta_dev

package service

type developmentState struct{}

func (*Service) automaticSchedulingPaused() bool { return false }
