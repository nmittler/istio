package retry

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-multierror"
	"istio.io/istio/pkg/test/envoy"
	"istio.io/istio/pkg/test/util/reserveport"
	"os"
	"strconv"
	"strings"
)

const (
	beTemplateStr = `
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
      port_value: {{ .AdminPort }}
static_resources:
  clusters:
  - name: app
    connect_timeout: 0.25s
    type: STATIC
    lb_policy: ROUND_ROBIN
    hosts:
    - socket_address:
        address: 127.0.0.1
        port_value: {{ .AppPort }}
  listeners:
  - address:
      socket_address:
        address: 127.0.0.1
        port_value: {{ .ServicePort }}
    use_original_dst: true
    filter_chains:
    - filters:
      - name: envoy.http_connection_manager
        config:
          codec_type: auto
          stat_prefix: ingress_http
          route_config:
            name: app
            virtual_hosts:
            - name: app
              domains:
              - "*"
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: app
          http_filters:
          - name: envoy.cors
            config: {}
          - name: envoy.fault
            config: {}
          - name: envoy.router
            config: {}
`
)

var (
	beTemplate = parseEnvoyTemplate(beTemplateStr)
)

type backendConfig struct {
	name    string
	tempDir string
	portMgr reserveport.PortManager
}

type backend struct {
	appServer   *server
	envoyProxy  *envoy.Envoy
	servicePort int
	envoyLogFile *os.File
}

func newBackend(ctx context.Context, cfg backendConfig) (*backend, error) {
	// Start the application server.
	appServer, err := newServer(cfg.name)
	if err != nil {
		return nil, err
	}

	// Reserve ports for the admin and service.
	adminPort, err := cfg.portMgr.ReservePort()
	if err != nil {
		return nil, err
	}
	servicePort, err := cfg.portMgr.ReservePort()
	if err != nil {
		return nil, err
	}

	logFileName, err := createTempfile(cfg.tempDir, fmt.Sprintf("envoylog_%s_", cfg.name), ".txt")
	if err != nil {
		return nil, err
	}
	envoyLogFile, err := os.Create(logFileName)
	if err != nil {
		return nil, err
	}
	fmt.Println("Creating Envoy log file: " + logFileName)

	yamlFile, err := createYamlFile(cfg.tempDir, cfg.name, beTemplate, map[string]string{
		"NodeID":      cfg.name,
		"Cluster":     cluster,
		"AdminPort":   strconv.Itoa(int(adminPort.GetPort())),
		"ServicePort": strconv.Itoa(int(servicePort.GetPort())),
		"AppPort":     strconv.Itoa(appServer.GetPort()),
	})
	if err != nil {
		return nil, err
	}
	fmt.Printf("%s yaml: %s\n", strings.ToUpper(cfg.name), yamlFile)
	fmt.Printf("%s servicePort: %d\n", strings.ToUpper(cfg.name), servicePort.GetPort())

	// Free the ports so they can now be used for Envoy.
	adminPort.Close()
	servicePort.Close()

	// Create amd start Envoy
	envoyProxy := &envoy.Envoy{
		YamlFile:       yamlFile,
		LogEntryPrefix: fmt.Sprintf("[ENVOY-%s]", cfg.name),
		LogLevel:       envoy.LogLevelTrace,
		Stdout:         envoyLogFile,
		Stderr:         envoyLogFile,
	}
	if err := envoyProxy.Start(); err != nil {
		return nil, err
	}

	be := &backend{
		appServer:   appServer,
		envoyProxy:  envoyProxy,
		servicePort: int(servicePort.GetPort()),
		envoyLogFile: envoyLogFile,
	}

	go func() {
		<-ctx.Done()
		fmt.Println("Cancelled BE " + cfg.name)
		_ = be.Close()
	}()

	return be, nil
}

func (b *backend) Close() (err error) {
	if b.envoyProxy != nil {
		err = multierror.Append(err, b.envoyProxy.Stop()).ErrorOrNil()
	}
	if b.appServer != nil {
		err = multierror.Append(err, b.appServer.Close()).ErrorOrNil()
	}
	if b.envoyLogFile != nil {
		b.envoyLogFile.Close()
	}

	return
}
