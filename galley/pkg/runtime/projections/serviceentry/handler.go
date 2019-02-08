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
	"context"
	"strconv"
	"time"

	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/types"

	"go.opencensus.io/tag"

	mcp "istio.io/api/mcp/v1alpha1"
	networking "istio.io/api/networking/v1alpha3"
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

	endpoints      map[resource.FullName]resource.Entry
	serviceEntries map[resource.FullName]serviceEntryWrapper
	podsHandler    processing.Handler
	nodesHandler   processing.Handler
	pods           pod.Cache
	nodes          node.Cache

	// The version number for the current State of the object. Every time mcpResources or versions change,
	// the version number also change
	version      int64
	mcpResources map[resource.FullName]*mcp.Resource

	// pendingEvents counts the number of events awaiting publishing.
	pendingEvents int64

	// lastSnapshotTime records the last time a snapshot was published.
	lastSnapshotTime time.Time

	statsCtx context.Context
}

// NewHandler creates a new Handler instance.
func NewHandler(domainSuffix string, listener processing.Listener) *Handler {
	pods, podsHandler := pod.NewCache()
	nodes, nodesHandler := node.NewCache()

	statsCtx, err := tag.New(context.Background(), tag.Insert(monitoring.CollectionTag,
		metadata.IstioNetworkingV1alpha3SyntheticServiceentries.Collection.String()))
	if err != nil {
		log.Scope.Errorf("Error creating monitoring context for counting state: %v", err)
		statsCtx = nil
	}

	return &Handler{
		domainSuffix:   domainSuffix,
		listener:       listener,
		endpoints:      make(map[resource.FullName]resource.Entry),
		serviceEntries: make(map[resource.FullName]serviceEntryWrapper),
		mcpResources:   make(map[resource.FullName]*mcp.Resource),
		podsHandler:    podsHandler,
		nodesHandler:   nodesHandler,
		pods:           pods,
		nodes:          nodes,
		statsCtx:       statsCtx,
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
	version := strconv.FormatInt(p.version, 10)
	b.Set(collection.String(), version, entries)

	return b.Build()
}

func (p *Handler) handleServiceEvent(event resource.Event) {
	service := event.Entry
	fullName := service.ID.FullName

	switch event.Kind {
	case resource.Added, resource.Updated:
		// Get or create the service entry.
		se, ok := p.serviceEntries[fullName]
		if !ok {
			// Get the associated endpoints, if available.
			var endpoints *coreV1.Endpoints
			if endpointsEntry, ok := p.endpoints[fullName]; ok {
				endpoints = endpointsEntry.Item.(*coreV1.Endpoints)
			}

			se = p.newServiceEntry(service, endpoints)
		} else {
			se.Metadata = service.Metadata
			p.convertService(service, &se.ServiceEntry)
		}
		p.serviceEntries[fullName] = se

		// Convert to an MCP resource to be used in the snapshot.
		mcpEntry, ok := p.toMcpResource(fullName, se)
		if !ok {
			return
		}
		p.setMcpEntry(fullName, mcpEntry)

		p.updateVersion()

	case resource.Deleted:
		// Delete the ServiceEntry
		delete(p.serviceEntries, fullName)
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
		// Store the endpoints.
		p.endpoints[fullName] = entry

		// Look up the ServiceEntry associated with the endpoints.
		se, ok := p.serviceEntries[fullName]
		if !ok {
			// Wait until we get a Service before we create the ServiceEntry.
			scope.Debugf("received endpoints before service for: %s", fullName)
			return
		}

		// Update the service entry.
		endpoints := entry.Item.(*coreV1.Endpoints)
		p.convertEndpoints(endpoints, &se.ServiceEntry)
		p.serviceEntries[fullName] = se

		// Convert to an MCP resource to be used in the snapshot.
		mcpEntry, ok := p.toMcpResource(fullName, se)
		if !ok {
			return
		}
		p.setMcpEntry(fullName, mcpEntry)

		p.updateVersion()

	case resource.Deleted:
		// The lifecycle of the ServiceEntry is bound to the service, so only delete the endpoints entry here.
		delete(p.endpoints, fullName)

		// Look up the service associated with the endpoints.
		se, ok := p.serviceEntries[fullName]
		if ok {
			// Update the MCP entry to clear out the endpoints.
			p.convertEndpoints(nil, &se.ServiceEntry)
			p.serviceEntries[fullName] = se

			mcpEntry, ok := p.toMcpResource(fullName, se)
			if !ok {
				return
			}
			p.setMcpEntry(fullName, mcpEntry)

			p.updateVersion()
		}
	default:
		scope.Errorf("unknown event kind: %v", event.Kind)
	}
}

func (p *Handler) setMcpEntry(fullName resource.FullName, mcpEntry *mcp.Resource) {
	p.mcpResources[fullName] = mcpEntry
}

func (p *Handler) deleteMcpResource(fullName resource.FullName) {
	delete(p.mcpResources, fullName)
}

func (p *Handler) updateVersion() {
	p.version++
	monitoring.RecordStateTypeCountWithContext(p.statsCtx, len(p.mcpResources))

	if scope.DebugEnabled() {
		scope.Debugf("in-memory state has changed:\n%v\n", p)
	}
	p.pendingEvents++
	p.notifyChanged()
}

func (p *Handler) notifyChanged() {
	p.listener.CollectionChanged(collection)
}

func (p *Handler) versionString() string {
	return strconv.FormatInt(p.version, 10)
}

func (p *Handler) newServiceEntry(serviceResource resource.Entry, endpoints *coreV1.Endpoints) serviceEntryWrapper {
	se := serviceEntryWrapper{
		Metadata: serviceResource.Metadata,
	}
	p.convertService(serviceResource, &se.ServiceEntry)
	p.convertEndpoints(endpoints, &se.ServiceEntry)
	return se
}

func (p *Handler) convertService(service resource.Entry, out *networking.ServiceEntry) {
	convert.Service(service.Item.(*coreV1.ServiceSpec), service.Metadata, service.ID.FullName, p.domainSuffix, out)
}

func (p *Handler) convertEndpoints(endpoints *coreV1.Endpoints, out *networking.ServiceEntry) {
	convert.Endpoints(endpoints, p.pods, p.nodes, out)
}

func (p *Handler) toMcpResource(fullName resource.FullName, se serviceEntryWrapper) (*mcp.Resource, bool) {

	// Serialize the proto.
	serialized, err := proto.Marshal(&se.ServiceEntry)
	if err != nil {
		scope.Errorf("error serializing proto from source e: %v:", se.ServiceEntry)
		return nil, false
	}

	// Wrap it in an Any
	body := &types.Any{
		TypeUrl: metadata.IstioNetworkingV1alpha3SyntheticServiceentries.TypeURL.String(),
		Value:   serialized,
	}

	// Create the metadata for the MCP resource.
	mcpMeta := &mcp.Metadata{
		Name:    fullName.String(),
		Version: p.versionString(),
		Labels:  se.Metadata.Labels,
	}

	prevResource := p.mcpResources[fullName]
	if prevResource != nil {
		// Re-use the creation timestamp, since it won't change.
		mcpMeta.CreateTime = prevResource.Metadata.CreateTime

		// Re-use the annotations if nothing changed.
		if convertedAnnotationsEqual(prevResource.Metadata.Annotations, se.Metadata.Annotations) {
			mcpMeta.Annotations = prevResource.Metadata.Annotations
		} else {
			mcpMeta.Annotations = convert.Annotations(se.Metadata.Annotations)
		}

	} else {
		// Convert the creation timestamp.
		var err error
		mcpMeta.CreateTime, err = types.TimestampProto(se.Metadata.CreateTime)
		if err != nil {
			scope.Errorf("error parsing resource create_time for event metadata (%v): %v", se.Metadata, err)
			return nil, false
		}

		// Create the annotations.
		mcpMeta.Annotations = convert.Annotations(se.Metadata.Annotations)
	}

	entry := &mcp.Resource{
		Metadata: mcpMeta,
		Body:     body,
	}
	p.mcpResources[fullName] = entry

	return entry, true
}

func convertedAnnotationsEqual(prev, new map[string]string) bool {
	if len(prev) != len(new)+1 {
		return false
	}
	for k, v1 := range new {
		v2, ok := prev[k]
		if !ok || v1 != v2 {
			return false
		}
	}
	return true
}

type serviceEntryWrapper struct {
	networking.ServiceEntry
	resource.Metadata
}
