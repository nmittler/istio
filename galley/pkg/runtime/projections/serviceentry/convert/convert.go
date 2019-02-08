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

package convert

import (
	"sort"
	"strings"

	networking "istio.io/api/networking/v1alpha3"
	"istio.io/istio/galley/pkg/runtime/log"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/annotations"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/node"
	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/pod"
	"istio.io/istio/galley/pkg/runtime/resource"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pilot/pkg/serviceregistry/kube"

	coreV1 "k8s.io/api/core/v1"
)

// Annotations augments the annotations for the k8s Service.
func Annotations(serviceAnnotations resource.Annotations) resource.Annotations {
	out := make(resource.Annotations)
	for k, v := range serviceAnnotations {
		out[k] = v
	}

	out[annotations.SyntheticResource] = "true"
	return out
}

// Service converts a k8s Service to a networking.ServiceEntry. The target ServiceEntry is passed as an argument (out) in order to
// enable object reuse.
func Service(spec *coreV1.ServiceSpec, metadata resource.Metadata, fullName resource.FullName, domainSuffix string, out *networking.ServiceEntry) {
	resolution := networking.ServiceEntry_STATIC
	location := networking.ServiceEntry_MESH_INTERNAL

	// Check for an external service
	externalName := ""
	var endpoints []*networking.ServiceEntry_Endpoint
	if spec.Type == coreV1.ServiceTypeExternalName && spec.ExternalName != "" {
		externalName = spec.ExternalName
		resolution = networking.ServiceEntry_DNS
		location = networking.ServiceEntry_MESH_EXTERNAL
		endpoints = convertExternalServiceEndpoints(spec, metadata, out.Endpoints)
	} else {
		endpoints = make([]*networking.ServiceEntry_Endpoint, 0)
	}

	// Check for unspecified Cluster IP
	addr := model.UnspecifiedIP
	if spec.ClusterIP != "" && spec.ClusterIP != coreV1.ClusterIPNone {
		addr = spec.ClusterIP
	}
	if addr == model.UnspecifiedIP && externalName == "" {
		// Headless services should not be load balanced
		resolution = networking.ServiceEntry_NONE
	}
	var addresses []string
	if len(out.Addresses) == 1 {
		addresses = out.Addresses
	} else {
		addresses = make([]string, 1)
	}
	addresses[0] = addr

	host := serviceHostname(fullName, domainSuffix)
	var hosts []string
	if len(out.Hosts) == 1 {
		hosts = out.Hosts
	} else {
		hosts = make([]string, 1)
	}
	hosts[0] = host

	ports := convertServicePorts(spec, out.Ports)

	// Store everything in the ServiceEntry.
	out.Hosts = hosts
	out.Addresses = addresses
	out.Resolution = resolution
	out.Location = location
	out.Ports = ports
	out.Endpoints = endpoints
	out.ExportTo = convertExportTo(metadata.Annotations, out.ExportTo)
}

func convertExportTo(annotations resource.Annotations, out []string) []string {
	var exportTo map[string]struct{}
	if annotations[kube.ServiceExportAnnotation] != "" {
		exportTo = make(map[string]struct{})
		for _, e := range strings.Split(annotations[kube.ServiceExportAnnotation], ",") {
			exportTo[strings.TrimSpace(e)] = struct{}{}
		}
	}
	if exportTo == nil {
		return nil
	}

	return convertStringSet(exportTo, out)
}

// Endpoints converts k8s Endpoints to networking.ServiceEntry_Endpoint resources and extracts the service accounts for the endpoints.
// The target ServiceEntry is passed as an argument (out) in order to enable object reuse.
func Endpoints(endpoints *coreV1.Endpoints, pods pod.Cache, nodes node.Cache, out *networking.ServiceEntry) {
	if endpoints == nil {
		out.Endpoints = nil
		out.SubjectAltNames = nil
		return
	}

	// Store the subject alternate names in a set to avoid duplicates.
	subjectAltNameSet := make(map[string]struct{})

	// Determine the number of endpoints that will result.
	endpointCount := 0
	for _, subset := range endpoints.Subsets {
		endpointCount += len(subset.Addresses)
	}
	if out.Endpoints == nil {
		out.Endpoints = make([]*networking.ServiceEntry_Endpoint, 0, endpointCount)
	}

	// Clip the resulting endpoints by the number of results.
	outEndpointCount := len(out.Endpoints)
	if outEndpointCount > endpointCount {
		out.Endpoints = out.Endpoints[0:endpointCount]
		outEndpointCount = endpointCount
	}

	outIndex := 0

	for _, subset := range endpoints.Subsets {

		// Get the ports for this subset.
		var ports map[string]uint32
		if outIndex < outEndpointCount && endpointSubsetPortsMatch(subset.Ports, out.Endpoints[outIndex].Ports) {
			// Optimization: re-use the existing ports array.
			ports = out.Endpoints[outIndex].Ports
		} else {
			// Create the ports for this subset. They will be re-used for each endpoint in the same subset.
			ports = make(map[string]uint32)
			for _, port := range subset.Ports {
				ports[port.Name] = uint32(port.Port)
			}
		}

		// Convert the endpoints in this subset.
		for _, address := range subset.Addresses {
			locality := ""

			ip := address.IP
			p, hasPod := pods.GetPodByIP(ip)
			if hasPod {
				if p.ServiceAccountName != "" {
					subjectAltNameSet[p.ServiceAccountName] = struct{}{}
				}

				n, hasNode := nodes.GetNodeByName(p.NodeName)
				if hasNode {
					locality = n.Locality
				} else {
					log.Scope.Warnf("unable to get node %q for pod %q", p.NodeName, p.FullName)
				}
			}

			var ep *networking.ServiceEntry_Endpoint
			if outIndex < outEndpointCount {
				// Optimization: Overwrite the existing object to avoid an unnecessary allocation.
				ep = out.Endpoints[outIndex]
				outIndex++
			} else {
				ep = &networking.ServiceEntry_Endpoint{}
				out.Endpoints = append(out.Endpoints, ep)
			}

			// Set the values.
			ep.Labels = endpoints.Labels
			ep.Address = ip
			ep.Ports = ports
			ep.Locality = locality
		}
	}

	out.SubjectAltNames = convertStringSet(subjectAltNameSet, out.SubjectAltNames)
	return
}

func convertStringSet(stringSet map[string]struct{}, out []string) []string {
	// Clip the length of the output array to the appropriate length
	stringCount := len(stringSet)
	if out == nil {
		out = make([]string, stringCount)
	}
	outStringCount := len(out)
	if outStringCount > stringCount {
		out = out[0:stringCount]
		outStringCount = stringCount
	}

	// Convert the subject alternate names to a sorted array.
	outIndex := 0
	for k := range stringSet {
		if outIndex < outStringCount {
			out[outIndex] = k
			outIndex++
		} else {
			out = append(out, k)
		}
	}
	sort.Sort(sort.StringSlice(out))
	return out
}

func convertExternalServiceEndpoints(svc *coreV1.ServiceSpec, serviceMeta resource.Metadata,
	in []*networking.ServiceEntry_Endpoint) []*networking.ServiceEntry_Endpoint {
	// Generate a single synthetic endpoint for the external service.

	if len(in) == 1 {
		// Optimization: reuse the existing endpoint...
		endpoint := in[0]

		// Reuse the port map if it's unchanged.
		var ports map[string]uint32
		if externalServicePortsMatch(svc.Ports, endpoint.Ports) {
			// Optimization: ports haven't changed - reuse them.
			ports = endpoint.Ports
		} else {
			// The ports have either changed or are uninitialized - create from scratch.
			ports = make(map[string]uint32)
			for _, port := range svc.Ports {
				ports[port.Name] = uint32(port.Port)
			}
		}

		// Reinitialize the endpoint struct.
		*endpoint = networking.ServiceEntry_Endpoint{
			Address: svc.ExternalName,
			Labels:  serviceMeta.Labels,
			Ports:   ports,
		}

		return in
	}

	// Create endpoint from scratch.
	ports := make(map[string]uint32)
	for _, port := range svc.Ports {
		ports[port.Name] = uint32(port.Port)
	}
	addr := svc.ExternalName

	return []*networking.ServiceEntry_Endpoint{
		{
			Address: addr,
			Ports:   ports,
			Labels:  serviceMeta.Labels,
		},
	}
}

func externalServicePortsMatch(servicePorts []coreV1.ServicePort, in map[string]uint32) bool {
	if in == nil {
		return false
	}

	if len(in) == len(servicePorts) {
		for _, p := range servicePorts {
			v, ok := in[p.Name]
			if !ok || v != uint32(p.Port) {
				return false
			}
		}
	}
	return true
}

func endpointSubsetPortsMatch(subsetPorts []coreV1.EndpointPort, ports map[string]uint32) bool {
	if ports == nil || len(ports) != len(subsetPorts) {
		return false
	}

	for _, port := range subsetPorts {
		v, ok := ports[port.Name]
		if !ok || v != uint32(port.Port) {
			return false
		}
	}
	return true
}

func convertServicePorts(spec *coreV1.ServiceSpec, old []*networking.Port) []*networking.Port {
	// Assume the port array doesn't change - same as the old value.
	out := old
	outIndex := 0
	outLength := len(out)

	// Clip the output array by the input array length.
	inLength := len(spec.Ports)
	if outLength > inLength {
		out = out[0:inLength]
	}

	for _, port := range spec.Ports {
		if outIndex < outLength {
			// Write directly to the existing port without allocating.
			outPort := out[outIndex]
			*outPort = networking.Port{
				Name:     port.Name,
				Number:   uint32(port.Port),
				Protocol: string(kube.ConvertProtocol(port.Name, port.Protocol)),
			}
			outIndex++
		} else {
			out = append(out, &networking.Port{
				Name:     port.Name,
				Number:   uint32(port.Port),
				Protocol: string(kube.ConvertProtocol(port.Name, port.Protocol)),
			})
		}
	}
	return out
}

// serviceHostname produces FQDN for a k8s service
func serviceHostname(fullName resource.FullName, domainSuffix string) string {
	namespace, name := fullName.InterpretAsNamespaceAndName()
	if namespace == "" {
		namespace = coreV1.NamespaceDefault
	}
	return name + "." + namespace + ".svc." + domainSuffix
}
