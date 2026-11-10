package main

import (
	"context"
	"sync"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
)

type runtimeGraph struct {
	generation string
	key        [backup.MasterKeyBytes]byte
	keySource  backup.KeySource
	ctx        context.Context
	cancel     context.CancelFunc

	database       *store.Store
	preferences    *appsettings.Store
	authentication *auth.Service
	bootstrap      auth.Bootstrap
	models         *modelservice.Service
	notifications  *notification.Service
	core           *service.Service
	handler        *server.Server

	closeOnce sync.Once
	closeErr  error
}
