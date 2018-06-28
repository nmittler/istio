//  Copyright 2018 Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package pilot

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net"
	url2 "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	xdsapi "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	xdsapi_core "github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	"github.com/gogo/protobuf/jsonpb"
	multierror "github.com/hashicorp/go-multierror"
	"google.golang.org/grpc"

	"istio.io/api/networking/v1alpha3"
	"istio.io/istio/pilot/pkg/model"
	"istio.io/istio/pkg/log"
	"istio.io/istio/pkg/test/local/envoy"
	"istio.io/istio/pkg/test/local/envoy/agent"
	"istio.io/istio/pkg/test/local/envoy/agent/pilot/reserveport"
	"istio.io/istio/pkg/test/local/envoy/discovery"
)

const (
	serviceNodeSeparator = "~"
	serviceCluster       = "local"
	proxyType            = "sidecar"
	localIPAddress       = "127.0.0.1"
	localCIDR            = "127.0.0.1/8"
	maxStreams           = 100000
	listenerType         = "type.googleapis.com/envoy.api.v2.Listener"
	routeType            = "type.googleapis.com/envoy.api.v2.RouteConfiguration"
	clusterType          = "type.googleapis.com/envoy.api.v2.Cluster"
	// TODO(nmittler): Pilot seems to require that the domain have multiple dot-separated parts
	defaultDomain    = "svc.local"
	defaultNamespace = "default"

	// TODO(nmittler): Add listener support for all protocols (not just HTTP).
	envoyYamlTemplateStr = `
{{- $serviceName := .ServiceName -}}
stats_config:
  use_all_default_tags: false
node:
  id: {{ .NodeID }}
  cluster: {{ .Cluster }}
admin:
  access_log_path: "/dev/null"
  address:
    socket_address:
      address: 127.0.0.1
      port_value: {{.AdminPort}}
dynamic_resources:
  lds_config:
    ads: {}
  cds_config:
    ads: {}
  ads_config:
    api_type: GRPC
    refresh_delay: 1s
    cluster_names:
    - xds-grpc
static_resources:
  clusters:
  - name: xds-grpc
    type: STATIC
    connect_timeout: 1s
    lb_policy: ROUND_ROBIN
    http2_protocol_options: {}
    hosts:
    - socket_address:
        address: {{.DiscoveryIPAddress}}
        port_value: {{.DiscoveryPort}}
  {{ range $i, $p := .Ports -}}
  - name: service_{{$serviceName}}_{{$p.ApplicationPort}}
    connect_timeout: 0.25s
    type: STATIC
    lb_policy: ROUND_ROBIN
    hosts:
    - socket_address:
        address: 127.0.0.1
        port_value: {{$p.ApplicationPort}}    
  {{ end -}}
  listeners:
  {{- range $i, $p := .Ports }}
  - address:
      socket_address:
        address: 127.0.0.1
        port_value: {{$p.ProxyPort}}
    use_original_dst: true
    filter_chains:
    - filters:
      - name: envoy.http_connection_manager
        config:
          codec_type: auto
          stat_prefix: ingress_http
          route_config:
            name: service_{{$serviceName}}_{{$p.ProxyPort}}_to_{{$p.ApplicationPort}}
            virtual_hosts:
            - name: service_{{$serviceName}}
              domains:
              - "*"
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: service_{{$serviceName}}_{{$p.ApplicationPort}}
          http_filters:
          - name: envoy.cors
            config: {}
          - name: envoy.fault
            config: {}
          - name: envoy.router
            config: {}
  {{- end -}}
`
)

var (
	// The Template object parsed from the template string
	envoyYamlTemplate              = getEnvoyYamlTemplate()
	inboundListenerNamePattern, _  = regexp.Compile("127.0.0.1_[0-9]+")
	outboundListenerNamePattern, _ = regexp.Compile("0.0.0.0_[0-9]+")
)

func getEnvoyYamlTemplate() *template.Template {
	tmpl := template.New("istio_agent_proxy_config")
	_, err := tmpl.Parse(envoyYamlTemplateStr)
	if err != nil {
		log.Warn("unable to parse proxy bootstrap config")
	}
	return tmpl
}

// Factory is responsible for manufacturing proxy instances which use Pilot for configuration. The returned proxy
// implements the util.URLInterceptor interface.
type Factory struct {
	Namespace        string
	Domain           string
	DiscoveryAddress *net.TCPAddr
	TmpDir           string
}

// NewProxiedApplication is an agent.ApplicationProxyFactory function that creates new proxy instances which use Pilot for configuration
func (f *Factory) NewProxiedApplication(serviceName string, app agent.Application) (agent.ApplicationProxy, agent.StopFunc, error) {
	portMgr, err := reserveport.NewPortManager()
	if err != nil {
		return nil, nil, err
	}

	proxy := &applicationProxy{
		boundPortMap: make(map[uint32]int),
		portMgr:      portMgr,
	}
	stopFunc := proxy.stop
	defer func() {
		if err != nil {
			_ = stopFunc()
		}
	}()

	if err = proxy.start(serviceName, app, f); err != nil {
		return nil, nil, err
	}

	return proxy, stopFunc, nil
}

func (f *Factory) getNamespace() string {
	if f.Namespace != "" {
		return f.Namespace
	}
	return defaultNamespace
}

func (f *Factory) getDomain() string {
	if f.Domain != "" {
		return f.Domain
	}
	return defaultDomain
}

func (f *Factory) getFQD() string {
	return fmt.Sprintf("%s.%s", f.getNamespace(), f.getDomain())
}

func (f *Factory) generateServiceNode(serviceName string) string {
	id := fmt.Sprintf("%s.%s", serviceName, randomBase64String(10))
	return strings.Join([]string{proxyType, localIPAddress, id, f.getFQD()}, serviceNodeSeparator)
}

type applicationProxy struct {
	envoy                     *envoy.Envoy
	adminPort                 int
	ports                     []*agent.MappedPort
	yamlFile                  string
	ownedDir                  string
	serviceEntry              model.Config
	discoveryFilterGrpcServer *grpc.Server
	discoveryFilter           *discovery.Filter
	discoveryFilterAddr       *net.TCPAddr
	boundPortMap              map[uint32]int
	portMgr                   reserveport.PortManager
}

// GetConfig implements the agent.ApplicationProxy interface.
func (p *applicationProxy) GetConfig() model.Config {
	return p.serviceEntry
}

// GetPorts implements the agent.ApplicationProxy interface.
func (p *applicationProxy) GetAdminPort() int {
	return p.adminPort
}

// GetPorts implements the agent.ApplicationProxy interface.
func (p *applicationProxy) GetPorts() []*agent.MappedPort {
	return p.ports
}

// InterceptURL implements the util.URLInterceptor interface.
func (p *applicationProxy) InterceptURL(url string) (string, error) {
	parsedURL, err := url2.Parse(url)
	if err != nil {
		return url, nil
	}

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		return url, nil
	}

	boundPort, ok := p.boundPortMap[uint32(port)]
	if ok {
		parsedURL.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(boundPort))
		url = parsedURL.String()
	}

	return url, nil
}

func (p *applicationProxy) start(serviceName string, app agent.Application, f *Factory) error {
	// Create the service entry for this service.
	p.serviceEntry = model.Config{
		ConfigMeta: model.ConfigMeta{
			Name:      serviceName,
			Namespace: f.getNamespace(),
			Domain:    f.getDomain(),
			Type:      model.ServiceEntry.Type,
		},
		Spec: &v1alpha3.ServiceEntry{
			Hosts: []string{
				fmt.Sprintf("%s.%s", serviceName, f.getFQD()),
			},
			Addresses: []string{
				localCIDR,
			},
			Resolution: v1alpha3.ServiceEntry_STATIC,
			Location:   v1alpha3.ServiceEntry_MESH_INTERNAL,
			Endpoints: []*v1alpha3.ServiceEntry_Endpoint{
				{
					Address: localIPAddress,
				},
			},
		},
	}

	p.discoveryFilter = &discovery.Filter{
		DiscoveryAddr: f.DiscoveryAddress.String(),
		FilterFunc:    p.filterDiscoveryResponse,
	}

	// Start a GRPC server and register the proxy handlers.
	p.discoveryFilterGrpcServer = grpc.NewServer(grpc.MaxConcurrentStreams(uint32(maxStreams)))
	// get the grpc server wired up
	grpc.EnableTracing = true
	p.discoveryFilter.Register(p.discoveryFilterGrpcServer)

	// Dynamically assign a port for the proxy's GRPC server.
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		return err
	}

	// Start the gRPC server for the discovery filter.
	p.discoveryFilterAddr = listener.Addr().(*net.TCPAddr)
	go func() {
		if err = p.discoveryFilterGrpcServer.Serve(listener); err != nil {
			log.Warna(err)
		}
	}()

	// Generate the port mappings between Envoy and the backend service.
	p.adminPort, p.ports, err = p.createPorts(app.GetPorts())
	if err != nil {
		return err
	}

	nodeID := f.generateServiceNode(serviceName)

	// Create the YAML configuration file for Envoy.
	if err = p.createYamlFile(serviceName, nodeID, f); err != nil {
		return err
	}

	// Start Envoy with the configuration
	logPrefix := fmt.Sprintf("[ENVOY-%s]", nodeID)
	p.envoy = &envoy.Envoy{
		YamlFile:       p.yamlFile,
		LogEntryPrefix: logPrefix,
	}
	if err = p.envoy.Start(); err != nil {
		return err
	}

	// Wait for Envoy to become healthy.
	if err = envoy.WaitForHealthCheckLive(p.adminPort); err != nil {
		return err
	}

	// Update the config entry with the ports.
	p.serviceEntry.Spec.(*v1alpha3.ServiceEntry).Ports = p.getConfigPorts()

	fmt.Printf("NM: Service %s ports:\n", serviceName)
	for _, p := range p.ports {
		fmt.Println(strconv.Itoa(p.ProxyPort))
	}
	return err
}

func (p *applicationProxy) stop() (err error) {
	if p.discoveryFilterGrpcServer != nil {
		p.discoveryFilterGrpcServer.Stop()
	}
	if p.envoy != nil {
		err = p.envoy.Stop()
	}
	if p.ownedDir != "" {
		_ = os.RemoveAll(p.ownedDir)
	} else if p.yamlFile != "" {
		_ = os.Remove(p.yamlFile)
	}
	// Free any reserved ports.
	if e := p.portMgr.Close(); e != nil {
		err = multierror.Append(err, e)
	}
	return
}

func (p *applicationProxy) getConfigPorts() []*v1alpha3.Port {
	ports := make([]*v1alpha3.Port, len(p.ports))
	for i, p := range p.ports {
		ports[i] = &v1alpha3.Port{
			Name:     p.Name,
			Protocol: string(p.Protocol),
			Number:   uint32(p.ProxyPort),
		}
	}
	return ports
}

func (p *applicationProxy) createYamlFile(serviceName, nodeID string, f *Factory) error {
	// Create a temporary output directory if not provided.
	outDir := f.TmpDir
	if outDir == "" {
		var err error
		p.ownedDir, err = createTempDir()
		if err != nil {
			return err
		}
	}

	// Create an output file to hold the generated configuration.
	var err error
	p.yamlFile, err = createTempfile(outDir, "istio_agent_proxy_config", ".yaml")
	if err != nil {
		return err
	}

	// Apply the template with the current configuration
	var filled bytes.Buffer
	w := bufio.NewWriter(&filled)
	if err := envoyYamlTemplate.Execute(w, map[string]interface{}{
		"ServiceName":        serviceName,
		"NodeID":             nodeID,
		"Cluster":            serviceCluster,
		"AdminPort":          p.adminPort,
		"Ports":              p.ports,
		"DiscoveryIPAddress": localIPAddress,
		"DiscoveryPort":      p.getDiscoveryPort(),
	}); err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// Write the content of the file.
	configBytes := filled.Bytes()
	fmt.Println("NM: Envoy config: " + string(configBytes))
	if err := ioutil.WriteFile(p.yamlFile, configBytes, 0644); err != nil {
		return err
	}
	return nil
}

func (p *applicationProxy) getDiscoveryPort() int {
	return p.discoveryFilterAddr.Port
	// NOTE: uncomment this code to use use Pilot directly, without a filter.
	//addr, _ := net.ResolveTCPAddr("tcp", p.discoveryFilter.DiscoveryAddr)
	//return addr.Port
}

func (p *applicationProxy) printDiscoveryResponse(resp *xdsapi.DiscoveryResponse) {
	m := jsonpb.Marshaler{
		Indent: "  ",
	}
	str, _ := m.MarshalToString(resp)
	fmt.Printf("NM: Envoy [%s] config from Pilot: %s", p.serviceEntry.Name, str)
}

func (p *applicationProxy) filterDiscoveryResponseDirect(resp *xdsapi.DiscoveryResponse) (*xdsapi.DiscoveryResponse, error) {
	p.printDiscoveryResponse(resp)
	return resp, nil
}

func (p *applicationProxy) filterDiscoveryResponse(resp *xdsapi.DiscoveryResponse) (*xdsapi.DiscoveryResponse, error) {
	newResponse := xdsapi.DiscoveryResponse{
		TypeUrl:     resp.TypeUrl,
		Canary:      resp.Canary,
		VersionInfo: resp.VersionInfo,
		Nonce:       resp.Nonce,
	}

	for _, any := range resp.Resources {
		fmt.Println("NM: resource URL " + any.TypeUrl)
		switch any.TypeUrl {
		case listenerType:
			l := &xdsapi.Listener{}
			if err := l.Unmarshal(any.Value); err != nil {
				return nil, err
			}

			// Filter out unnecessary listeners.
			switch {
			case l.Name == "virtual":
				// Exclude the iptables-mapped listener from the Envoy configuration. It's hard-coded to port 15001,
				// which will likely fail to be bound.
				continue
			case l.Name == "127.0.0.1_3333" || l.Name == "127.0.0.1_9999":
				// Exclude listeners for the management ports.
				continue
			case inboundListenerNamePattern.Match([]byte(l.Name)):
				// This is a dynamic listener generated by Pilot for an inbound port. All inbound ports for the local
				// proxy are built into the static config, so we can safely ignore this listener.
				//
				// In addition, since we're using 127.0.0.1 as the IP address for all services/instances, the external
				// service registry's GetProxyServiceInstances() will mistakenly return instances for all services.
				// This is due to the fact that it uses IP address alone to map the instances. This results in Pilot
				// incorrectly generating inbound listeners for other services. These listeners shouldn't cause any
				// problems, but filtering them out here for correctness and clarity of the Envoy config.
				continue
			case outboundListenerNamePattern.Match([]byte(l.Name)):
				portFromPilot := l.Address.GetSocketAddress().GetPortValue()
				boundPort, ok := p.boundPortMap[portFromPilot]

				// Bind a real port for the outbound listener if we haven't already.
				if !ok {
					var err error
					boundPort, err = p.findFreePort()
					if err != nil {
						return nil, err
					}
					p.boundPortMap[portFromPilot] = boundPort
					fmt.Printf("NM: Service %s generating port %d for pilot port %d\n", p.serviceEntry.Name, boundPort, portFromPilot)
				}

				// Store the bound port in the listener.
				l.Address.GetSocketAddress().PortSpecifier.(*xdsapi_core.SocketAddress_PortValue).PortValue = uint32(boundPort)
				l.DeprecatedV1.BindToPort.Value = true

				// Output this content of the any.
				b, err := l.Marshal()
				if err != nil {
					return nil, err
				}
				any.Value = b
				newResponse.Resources = append(newResponse.Resources, any)
			default:
				newResponse.Resources = append(newResponse.Resources, any)
			}
		case clusterType:
			// Remove any management clusters.
			c := &xdsapi.Cluster{}
			if err := c.Unmarshal(any.Value); err != nil {
				return nil, err
			}
			switch {
			case strings.Contains(c.Name, "mgmtCluster"):
				continue
			default:
				newResponse.Resources = append(newResponse.Resources, any)
			}
		default:
			newResponse.Resources = append(newResponse.Resources, any)
		}
		newResponse.Resources = append(newResponse.Resources, any)
	}

	// Take a second pass to update routes to use updated listener ports.
	for i, any := range newResponse.Resources {
		switch any.TypeUrl {
		case routeType:
			r := &xdsapi.RouteConfiguration{}
			if err := r.Unmarshal(any.Value); err != nil {
				return nil, err
			}

			// Dynamic routes for outbound ports are named with their port.
			port, err := strconv.Atoi(r.Name)
			if err != nil {
				continue
			}

			// Look up the port to see if we have a custom bound port
			boundPort, ok := p.boundPortMap[uint32(port)]
			if !ok {
				continue
			}

			modified := false
			for i, vh := range r.VirtualHosts {
				for domainIndex, domain := range vh.Domains {
					parts := strings.Split(domain, ":")
					if len(parts) == 2 {
						modified = true
						r.VirtualHosts[i].Domains[domainIndex] = fmt.Sprintf("%s:%d", parts[0], boundPort)
					}
				}
			}

			if modified {
				// Update the resource.
				b, err := r.Marshal()
				if err != nil {
					return nil, err
				}
				newResponse.Resources[i].Value = b
			}
		}
	}

	p.printDiscoveryResponse(&newResponse)
	return &newResponse, nil
}

func randomBase64String(len int) string {
	buff := make([]byte, len)
	rand.Read(buff)
	str := base64.URLEncoding.EncodeToString(buff)
	return str[:len]
}

func (p *applicationProxy) createPorts(servicePorts model.PortList) (adminPort int, mappedPorts []*agent.MappedPort, err error) {
	adminPort, err = p.findFreePort()
	if err != nil {
		return
	}

	mappedPorts = make([]*agent.MappedPort, len(servicePorts))
	for i, servicePort := range servicePorts {
		var envoyPort int
		envoyPort, err = p.findFreePort()
		if err != nil {
			return
		}

		mappedPorts[i] = &agent.MappedPort{
			Name:            servicePort.Name,
			Protocol:        servicePort.Protocol,
			ApplicationPort: servicePort.Port,
			ProxyPort:       envoyPort,
		}
	}
	return
}

func (p *applicationProxy) findFreePort() (int, error) {
	reservedPort, err := p.portMgr.ReservePort()
	if err != nil {
		return 0, err
	}
	defer reservedPort.Close()

	return int(reservedPort.GetPort()), nil
}

func createTempDir() (string, error) {
	tmpDir, err := ioutil.TempDir(os.TempDir(), "istio_agent_test")
	if err != nil {
		return "", err
	}
	return tmpDir, nil
}

func createTempfile(tmpDir, prefix, suffix string) (string, error) {
	f, err := ioutil.TempFile(tmpDir, prefix)
	if err != nil {
		return "", err
	}
	var tmpName string
	if tmpName, err = filepath.Abs(f.Name()); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Remove(tmpName); err != nil {
		return "", err
	}
	return tmpName + suffix, nil
}
