package bootstrap

import (
	"istio.io/istio/pilot/pkg/features"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/config/host"
	"istio.io/istio/pkg/config/schemas"
)

var _ xdsHandler = &defaultHandler{}
var _ xdsHandler = &kubeHandler{}

type xdsHandler interface {
	model.ServiceDiscoveryHandler

	setXDSUpdater(updater model.XDSUpdater)
}

type defaultHandler struct {
	updater model.XDSUpdater
}

func (h *defaultHandler) setXDSUpdater(updater model.XDSUpdater) {
	h.updater = updater
}

func (h *defaultHandler) OnServiceEvent(clusterID string, service *model.Service, event model.Event) {
	// Flush cached discovery responses whenever services configuration change.
	doFullPushForNamespace(h.updater, service.Attributes.Namespace)
}

func (h *defaultHandler) OnEndpointsEvent(clusterID, namespace string, hostname host.Name, service *model.Service,
	endpoints []*model.IstioEndpoint, event model.Event) {
	// TODO: This is an incomplete code. This code path is called for service entries, consul, etc.
	// In all cases, this is simply an instance update and not a config update. So, we need to update
	// EDS in all proxies, and do a full config push for the instance that just changed (add/update only).
	doFullPushForNamespace(h.updater, namespace)
}

// kubeHandler handles events from a k8s service registry and updates the XDS server appropriately.
// TODO(nmittler): We should look into making this the one-and-only handler.
type kubeHandler struct {
	updater model.XDSUpdater
}

func (h *kubeHandler) setXDSUpdater(updater model.XDSUpdater) {
	h.updater = updater
}

func (h *kubeHandler) OnServiceEvent(clusterID string, service *model.Service, event model.Event) {
	// EDS needs to just know when service is deleted.
	h.updater.SvcUpdate(clusterID, service.Attributes.Name, service.Attributes.Namespace, event)

	// Flush cached discovery responses whenever services configuration change.
	doFullPushForNamespace(h.updater, service.Attributes.Namespace)
}

func (h *kubeHandler) OnEndpointsEvent(clusterID, namespace string, hostname host.Name, service *model.Service,
	endpoints []*model.IstioEndpoint, event model.Event) {
	// headless service cluster discovery type is ORIGINAL_DST, we do not need update EDS.
	if features.EnableHeadlessService.Get() && service != nil && service.IsHeadless() {
		doFullPushForNamespace(h.updater, namespace)
	} else {
		_ = h.updater.EDSUpdate(clusterID, string(hostname), namespace, endpoints)
	}
}

func doFullPushForNamespace(updater model.XDSUpdater, namespace string) {
	updater.ConfigUpdate(&model.PushRequest{
		Full:              true,
		NamespacesUpdated: map[string]struct{}{namespace: {}},
		// TODO: extend and set service instance type, so no need to re-init push context
		ConfigTypesUpdated: map[string]struct{}{schemas.ServiceEntry.Type: {}},
	})
}
