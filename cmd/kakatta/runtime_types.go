package main

import (
	"context"
	"errors"
	"net/http"
	"sync"

	modelservice "github.com/Seaony/Kakatta/internal/analysis/service"
	"github.com/Seaony/Kakatta/internal/appsettings"
	"github.com/Seaony/Kakatta/internal/auth"
	"github.com/Seaony/Kakatta/internal/backup"
	"github.com/Seaony/Kakatta/internal/notification"
	"github.com/Seaony/Kakatta/internal/server"
	"github.com/Seaony/Kakatta/internal/service"
	"github.com/Seaony/Kakatta/internal/store"
	"github.com/Seaony/Kakatta/internal/update"
)

var errRuntimeGraphNotDrained = errors.New("运行代未排空")

type runtimeGraph struct {
	generation string
	key        [backup.MasterKeyBytes]byte
	keySource  backup.KeySource
	ctx        context.Context
	cancel     context.CancelFunc

	database        *store.Store
	preferences     *appsettings.Store
	authentication  *auth.Service
	models          *modelservice.Service
	notifications   *notification.Service
	core            *service.Service
	handler         *server.Server
	updates         update.API
	updateTransport *http.Transport

	maintenanceMu   sync.Mutex
	maintenanceWake chan struct{}
	maintenanceDone chan struct{}

	drainOnce sync.Once
	drainErr  error
	closeOnce sync.Once
	closeErr  error
}
