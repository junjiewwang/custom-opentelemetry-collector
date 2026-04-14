package servicemanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/custom/extension/controlplaneext/servicemanager/store"
)

func TestServiceService_ListServicesByApp_SortsByServiceName(t *testing.T) {
	svcMgr := newTestServiceManager()
	ctx := context.Background()

	mustCreateService(t, svcMgr, ctx, "app-a", "z-service")
	mustCreateService(t, svcMgr, ctx, "app-a", "a-service")
	mustCreateService(t, svcMgr, ctx, "app-a", "m-service")

	services, err := svcMgr.ListServicesByApp(ctx, "app-a", ListServicesQuery{})
	require.NoError(t, err)

	assert.Equal(t, []string{"a-service", "m-service", "z-service"}, serviceNames(services))
}

func TestServiceService_ListAllServices_SortsByAppThenServiceName(t *testing.T) {
	svcMgr := newTestServiceManager()
	ctx := context.Background()

	mustCreateService(t, svcMgr, ctx, "app-b", "z-service")
	mustCreateService(t, svcMgr, ctx, "app-a", "z-service")
	mustCreateService(t, svcMgr, ctx, "app-a", "a-service")
	mustCreateService(t, svcMgr, ctx, "app-b", "a-service")

	services, err := svcMgr.ListAllServices(ctx, ListServicesQuery{})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"app-a/a-service",
		"app-a/z-service",
		"app-b/a-service",
		"app-b/z-service",
	}, appServicePairs(services))
}

func newTestServiceManager() *ServiceService {
	logger := zap.NewNop()
	serviceStore := store.NewMemoryServiceStore(logger)
	return NewServiceService(logger, DefaultConfig(), serviceStore)
}

func mustCreateService(t *testing.T, svcMgr *ServiceService, ctx context.Context, appID, serviceName string) {
	t.Helper()
	_, err := svcMgr.CreateService(ctx, &CreateServiceRequest{
		AppID:       appID,
		ServiceName: serviceName,
	})
	require.NoError(t, err)
}

func serviceNames(items []*ServiceInfo) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, item.ServiceName)
	}
	return out
}

func appServicePairs(items []*ServiceInfo) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, item.AppID+"/"+item.ServiceName)
	}
	return out
}
