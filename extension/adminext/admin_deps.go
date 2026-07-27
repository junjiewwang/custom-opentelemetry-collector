// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package adminext

import (
	"net/http"

	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/arthastunnelext"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/agentregistry"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/appmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/configmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/instrumentationmanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/notification"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager"
	"go.opentelemetry.io/collector/custom/extension/controlplaneext/taskmanager"
	"go.opentelemetry.io/collector/custom/extension/storageext/blobstore"
)

// adminHandlers holds the dependencies required by the admin API handlers
// (apps/services/instances/tasks/instrumentation/notifications/artifact/arthas/
// retention), decoupling them from the *Extension god-object (P1 DI seam).
//
// All admin handler files (handlers.go, app_retention_handler.go,
// artifact_handler.go, notification_handler.go, instrumentation_handlers.go,
// analysis_callback_handler.go, arthas_handler.go) are methods on this struct.
type adminHandlers struct {
	configMgr         configmanager.ConfigManager
	onDemandConfigMgr configmanager.OnDemandConfigManager
	taskMgr           taskmanager.TaskManager
	agentReg          agentregistry.AgentRegistry
	tokenMgr          appmanager.TokenManager
	serviceMgr        servicemanager.ServiceManager
	instrMgr          instrumentationmanager.InstrumentationManager
	blobStore         blobstore.BlobStore
	notificationStore notification.Store
	artifactNotifier  notification.Notifier
	arthasTunnel      arthastunnelext.ArthasTunnel
	wsTokenMgr        WSTokenManager
	retentionProvider appmanager.AppRetentionProvider
	logger            *zap.Logger
}

func newAdminHandlers(e *Extension) *adminHandlers {
	return &adminHandlers{
		configMgr:         e.configMgr,
		onDemandConfigMgr: e.onDemandConfigMgr,
		taskMgr:           e.taskMgr,
		agentReg:          e.agentReg,
		tokenMgr:          e.tokenMgr,
		serviceMgr:        e.serviceMgr,
		instrMgr:          e.instrMgr,
		blobStore:         e.blobStore,
		notificationStore: e.notificationStore,
		artifactNotifier:  e.artifactNotifier,
		arthasTunnel:      e.arthasTunnel,
		wsTokenMgr:        e.wsTokenMgr,
		retentionProvider: e.retentionProvider,
		logger:            e.logger,
	}
}

func (h *adminHandlers) writeJSON(w http.ResponseWriter, status int, data any) {
	writeJSONResp(h.logger, w, status, data)
}

func (h *adminHandlers) writeError(w http.ResponseWriter, status int, message string) {
	writeErrorResp(h.logger, w, status, message)
}

func (h *adminHandlers) handleError(w http.ResponseWriter, err error) {
	handleErrorResp(h.logger, w, err)
}
