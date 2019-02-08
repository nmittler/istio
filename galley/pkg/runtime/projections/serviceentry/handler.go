// Copyright 2019 Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package serviceentry

import (
	"fmt"
	"sync"
	"time"

	"github.com/gogo/protobuf/types"

	mcp "istio.io/api/mcp/v1alpha1"
	"istio.io/istio/galley/pkg/metadata"
	"istio.io/istio/galley/pkg/runtime/log"
	"istio.io/istio/galley/pkg/runtime/monitoring"
	"istio.io/istio/galley/pkg/runtime/processing"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/convert"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/node"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/pod"
	"istio.io/istio/galley/pkg/runtime/resource"
	"istio.io/istio/pkg/mcp/snapshot"

	coreV1 "k8s.io/api/core/v1"
)

var (
	scope      = log.Scope
	collection = metadata.IstioNetworkingV1alpha3SyntheticServiceentries.Collection

	// Schema for types required to generate synthetic ServiceEntry projections.
	Schema *resource.Schema
)

func init() {
	b := resource.NewSchemaBuilder()
	b.RegisterInfo(metadata.K8sCoreV1Pods)
	b.RegisterInfo(metadata.K8sCoreV1Nodes)
	b.RegisterInfo(metadata.K8sCoreV1Services)
	b.RegisterInfo(metadata.K8sCoreV1Endpoints)
	Schema = b.Build()
}

var _ processing.Handler = &Handler{}

// Handler is a processing.Handler that generates snapshots containing synthetic ServiceEntry projections.
type Handler struct {
	domainSuffix string

	listener processing.Listener

	services     map[resource.FullName]resource.Entry
	endpoints    map[resource.FullName]resource.Entry
	podsHandler  processing.Handler
	nodesHandler processing.Handler
	pods         pod.Cache
	nodes        node.Cache

	// The version number for the current State of the object. Every time mcpResources or versions change,
	// the version number also change
	version      int64
	mcpLock      sync.Mutex
	mcpResources map[resource.FullName]*mcp.Resource

	// pendingEvents counts the number of events awaiting publishing.
	pendingEvents int64

	// lastSnapshotTime records the last time a snapshot was published.
	lastSnapshotTime time.Time
}

// NewHandler creates a new Handler instance.
func NewHandler(domainSuffix string, listener processing.Listener) *Handler {
	pods, podsHandler := pod.NewCache()
	nodes, nodesHandler := node.NewCache()

	return &Handler{
		domainSuffix: domainSuffix,
		listener:     listener,
		services:     make(map[resource.FullName]resource.Entry),
		endpoints:    make(map[resource.FullName]resource.Entry),
		mcpResources: make(map[resource.FullName]*mcp.Resource),
		podsHandler:  podsHandler,
		nodesHandler: nodesHandler,
		pods:         pods,
		nodes:        nodes,
	}
}

// Handle incoming events and generate synthetic ServiceEntry projections.
func (p *Handler) Handle(event resource.Event) {
	switch event.Entry.ID.Collection {
	case metadata.K8sCoreV1Endpoints.Collection:
		// Update the projections
		p.handleEndpointsEvent(event)
	case metadata.K8sCoreV1Services.Collection:
		// Update the projections
		p.handleServiceEvent(event)
	case metadata.K8sCoreV1Nodes.Collection:
		// Just add the node to the cache.
		p.nodesHandler.Handle(event)
	case metadata.K8sCoreV1Pods.Collection:
		// Just add the pod to the cache.
		p.podsHandler.Handle(event)
	default:
		scope.Warnf("received event with unexpected collection: %v", event.Entry.ID.Collection)
	}
}

// Builds the snapshot of the current resources.
func (p *Handler) BuildSnapshot() snapshot.Snapshot {
	p.mcpLock.Lock()
	defer p.mcpLock.Unlock()

	now := time.Now()
	monitoring.RecordProcessorSnapshotPublished(p.pendingEvents, now.Sub(p.lastSnapshotTime))
	p.lastSnapshotTime = now
	p.pendingEvents = 0

	b := snapshot.NewInMemoryBuilder()

	// Copy the entries.
	entries := make([]*mcp.Resource, 0, len(p.mcpResources))
	for _, r := range p.mcpResources {
		entries = append(entries, r)
	}

	// Create the collection resources on the Snapshot.
	version := fmt.Sprintf("%d", p.version)
	b.Set(collection.String(), version, entries)

	return b.Build()
}

func (p *Handler) handleServiceEvent(event resource.Event) {
	entry := event.Entry
	fullName := entry.ID.FullName

	switch event.Kind {
	case resource.Added, resource.Updated:

		// Check to see if the service's version has changed.
		curEntry, ok := p.services[fullName]
		if ok && curEntry.ID.Version == entry.ID.Version {
			scope.Debugf("received service for the current, known version: %v", event)
			return
		}

		// Store the service.
		p.services[fullName] = entry

		// Get the associated endpoints, if available.
		var endpoints *coreV1.Endpoints
		if endpointsEntry, ok := p.endpoints[fullName]; ok {
			endpoints = endpointsEntry.Item.(*coreV1.Endpoints)
		}

		// Convert to an MCP resource to be used in the snapshot.
		mcpEntry, ok := p.toMcpResource(entry, endpoints)
		if !ok {
			return
		}
		p.setMcpEntry(fullName, mcpEntry)

		p.updateVersion()

	case resource.Deleted:
		// Delete the Service and ServiceEntry
		delete(p.services, fullName)
		p.deleteMcpResource(fullName)
		p.updateVersion()
	default:
		scope.Errorf("unknown event kind: %v", event.Kind)
	}
}

func (p *Handler) handleEndpointsEvent(event resource.Event) {
	entry := event.Entry
	fullName := entry.ID.FullName

	switch event.Kind {
	case resource.Added, resource.Updated:

		// Check to see if the version has changed.
		curEntry, ok := p.endpoints[fullName]
		if ok && curEntry.ID.Version == entry.ID.Version {
			scope.Debugf("received endpoints for the current, known version: %v", event)
			return
		}

		p.endpoints[fullName] = entry

		// Look up the service associated with the endpoints.
		svcEntry, ok := p.services[fullName]
		if !ok {
			// Wait until we get a Service before we create the ServiceEntry.
			scope.Debugf("received endpoints before service for: %s", fullName)
			return
		}

		// Convert to an MCP resource to be used in the snapshot.
		mcpEntry, ok := p.toMcpResource(svcEntry, entry.Item.(*coreV1.Endpoints))
		if !ok {
			return
		}
		p.setMcpEntry(fullName, mcpEntry)

		p.updateVersion()

	case resource.Deleted:
		// The lifecycle of the ServiceEntry is bound to the service, so only delete the endpoints entry here.
		delete(p.endpoints, fullName)

		// Look up the service associated with the endpoints.
		svcEntry, ok := p.services[fullName]
		if !ok {
			return
		}

		// Update the MCP entry to clear out the endpoints.
		mcpEntry, ok := p.toMcpResource(svcEntry, nil)
		if !ok {
			return
		}
		p.setMcpEntry(fullName, mcpEntry)

		p.updateVersion()
	default:
		scope.Errorf("unknown event kind: %v", event.Kind)
	}
}

func (p *Handler) setMcpEntry(fullName resource.FullName, mcpEntry *mcp.Resource) {
	p.mcpLock.Lock()
	defer p.mcpLock.Unlock()

	p.mcpResources[fullName] = mcpEntry
}

func (p *Handler) deleteMcpResource(fullName resource.FullName) {
	p.mcpLock.Lock()
	defer p.mcpLock.Unlock()

	delete(p.mcpResources, fullName)
}

func (p *Handler) updateVersion() {
	p.version++
	monitoring.RecordStateTypeCount(collection.String(), len(p.mcpResources))

	scope.Debugf("in-memory state has changed:\n%v\n", p)
	p.pendingEvents++
	p.notifyChanged()
}

func (p *Handler) notifyChanged() {
	p.listener.CollectionChanged(collection)
}

func (p *Handler) versionString() string {
	return fmt.Sprintf("%d", p.version)
}

func (p *Handler) toMcpResource(svcEntry resource.Entry, endpoints *coreV1.Endpoints) (*mcp.Resource, bool) {
	fullName := svcEntry.ID.FullName
	svc := svcEntry.Item.(*coreV1.ServiceSpec)
	svcMeta := svcEntry.Metadata
	se := convert.Service(svc, svcMeta, svcEntry.ID.FullName, p.domainSuffix)

	// Set the endpoints, if available.
	if endpoints != nil {
		se.Endpoints, se.SubjectAltNames = convert.Endpoints(endpoints, p.pods, p.nodes)
	}

	body, err := types.MarshalAny(se)
	if err != nil {
		scope.Errorf("error serializing proto from source e: %v:", se)
		return nil, false
	}

	createTime, err := types.TimestampProto(svcMeta.CreateTime)
	if err != nil {
		scope.Errorf("error parsing resource create_time for event metadata (%v): %v", svcMeta, err)
		return nil, false
	}

	entry := &mcp.Resource{
		Metadata: &mcp.Metadata{
			Name:        fullName.String(),
			CreateTime:  createTime,
			Version:     p.versionString(),
			Labels:      svcMeta.Labels,
			Annotations: convert.Annotations(svcMeta.Annotations),
		},
		Body: body,
	}

	return entry, true
}
