package retry

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-multierror"
	"istio.io/istio/pkg/test/envoy"
	"istio.io/istio/pkg/test/util/reserveport"
	"os"
)

const (
	feName = "fe"

	feTemplateStr = `
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
  - name: backends
    connect_timeout: 0.25s
    type: STATIC
    lb_policy: ROUND_ROBIN
    hosts:
    {{ range $i, $p := .BEPorts -}}
    - socket_address:
        address: 127.0.0.1
        port_value: {{$p}}
    {{ end -}}
    circuit_breakers:
      thresholds:
        # Max concurrent retries for this cluster
        max_retries: 10
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
            name: backends
            virtual_hosts:
            - name: backends
              domains:
              - "*"
              routes:
              - match:
                  prefix: "/"
                route:
                  cluster: backends
# With this, the client never sees 503s.
                  retry_policy:
                    retry_on: connect-failure,refused-stream,unavailable,cancelled,resource-exhausted
                    retriable_status_codes: 503
                    num_retries: 10
                    # per_try_timeout: 0s
                    # retry_host_predicate:
                    # - name: envoy.retry_host_predicates.previous_hosts
                    # host_selection_retry_max_attempts: 3
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
	feTemplate = parseEnvoyTemplate(feTemplateStr)
)

type frontendConfig struct {
	tempDir  string
	portMgr  reserveport.PortManager
	backends []*backend
}

type frontend struct {
	envoyProxy  *envoy.Envoy
	servicePort int
	envoyLogFile *os.File
}

func newFrontend(ctx context.Context, cfg frontendConfig) (*frontend, error) {
	/*logFileName, err := createTempfile(cfg.tempDir, fmt.Sprintf("envoylog_%s_", feName), ".txt")
	if err != nil {
		return nil, err
	}
	envoyLogFile, err := os.Create(logFileName)
	if err != nil {
		return nil, err
	}
	fmt.Println("Creating Envoy log file: " + logFileName)*/

	// Reserve ports for the admin and service.
	adminPort, err := cfg.portMgr.ReservePort()
	if err != nil {
		return nil, err
	}
	servicePort, err := cfg.portMgr.ReservePort()
	if err != nil {
		return nil, err
	}

	bePorts := make([]int, 0, len(cfg.backends))
	for _, be := range cfg.backends {
		bePorts = append(bePorts, be.servicePort)
	}

	args := struct {
		NodeID      string
		Cluster     string
		AdminPort   int
		ServicePort int
		BEPorts     []int
	}{
		NodeID:      feName,
		Cluster:     cluster,
		AdminPort:   int(adminPort.GetPort()),
		ServicePort: int(servicePort.GetPort()),
		BEPorts:     bePorts,
	}

	yamlFile, err := createYamlFile(cfg.tempDir, feName, feTemplate, args)
	if err != nil {
		return nil, err
	}
	fmt.Println("FE yaml:\n" + yamlFile)

	// Free the ports so they can now be used for Envoy.
	_ = adminPort.Close()
	_ = servicePort.Close()

	// Create amd start Envoy
	envoyProxy := &envoy.Envoy{
		YamlFile:       yamlFile,
		LogEntryPrefix: fmt.Sprintf("[ENVOY-%s]", feName),
		//LogLevel:       envoy.LogLevelTrace,
		//Stdout:         envoyLogFile,
		//Stderr:         envoyLogFile,
	}
	if err := envoyProxy.Start(); err != nil {
		return nil, err
	}

	fe := &frontend{
		envoyProxy:  envoyProxy,
		servicePort: int(servicePort.GetPort()),
		//envoyLogFile: envoyLogFile,
	}

	go func() {
		<-ctx.Done()
		fmt.Println("Cancelled FE")
		_ = fe.Close()
	}()

	return fe, nil
}

func (f *frontend) Close() (err error) {
	if f.envoyProxy != nil {
		err = multierror.Append(err, f.envoyProxy.Stop()).ErrorOrNil()
	}
	if f.envoyLogFile != nil {
		_ = f.envoyLogFile.Close()
	}
	return
}
