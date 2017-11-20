package cmd

import (
	"istio.io/istio/pilot/platform/kube"
	"istio.io/istio/pilot/proxy/envoy"
	"istio.io/istio/pilot/platform/kube/admit"
	"istio.io/istio/pilot/platform"
)

type serviceRegistryArgs interface {
	Id() platform.ServiceRegistry
}

type consulArgs struct {
	config    string
	serverURL string
}

func (a *consulArgs) Id() platform.ServiceRegistry {
	return platform.ConsulRegistry
}

type eurekaArgs struct {
	serverURL string
}

func (a *eurekaArgs) Id() platform.ServiceRegistry {
	return platform.EurekaRegistry
}

// TODO: k8s registry

type args struct {
	kubeconfig string
	meshconfig string

	// namespace for the controller (typically istio installation namespace)
	namespace string

	// ingress sync mode is set to off by default
	controllerOptions kube.ControllerOptions
	discoveryOptions  envoy.DiscoveryServiceOptions

	//registries    []string
	//consul        consulArgs
	//eureka        eurekaArgs
	registries []serviceRegistryArgs
	admissionArgs admit.ControllerOptions
}

discoveryCmd.PersistentFlags().StringSliceVar(&flags.registries, "registries",
[]string{string(platform.KubernetesRegistry)},
fmt.Sprintf("Comma separated list of platform service registries to read from (choose one or more from {%s, %s, %s})",
platform.KubernetesRegistry, platform.ConsulRegistry, platform.EurekaRegistry))
discoveryCmd.PersistentFlags().StringVar(&flags.kubeconfig, "kubeconfig", "",
"Use a Kubernetes configuration file instead of in-cluster configuration")
discoveryCmd.PersistentFlags().StringVar(&flags.meshconfig, "meshConfig", "/etc/istio/config/mesh",
fmt.Sprintf("File name for Istio mesh configuration"))
discoveryCmd.PersistentFlags().StringVarP(&flags.namespace, "namespace", "n", "",
"Select a namespace where the controller resides. If not set, uses ${POD_NAMESPACE} environment variable")
discoveryCmd.PersistentFlags().StringVarP(&flags.controllerOptions.WatchedNamespace, "appNamespace",
"a", metav1.NamespaceAll,
"Restrict the applications namespace the controller manages; if not set, controller watches all namespaces")
discoveryCmd.PersistentFlags().DurationVar(&flags.controllerOptions.ResyncPeriod, "resync", 60*time.Second,
"Controller resync interval")
discoveryCmd.PersistentFlags().StringVar(&flags.controllerOptions.DomainSuffix, "domain", "cluster.local",
"DNS domain suffix")

discoveryCmd.PersistentFlags().IntVar(&flags.discoveryOptions.Port, "port", 8080,
"Discovery service port")
discoveryCmd.PersistentFlags().BoolVar(&flags.discoveryOptions.EnableProfiling, "profile", true,
"Enable profiling via web interface host:port/debug/pprof")
discoveryCmd.PersistentFlags().BoolVar(&flags.discoveryOptions.EnableCaching, "discovery_cache", true,
"Enable caching discovery service responses")

discoveryCmd.PersistentFlags().StringVar(&flags.consul.config, "consulconfig", "",
"Consul Config file for discovery")
discoveryCmd.PersistentFlags().StringVar(&flags.consul.serverURL, "consulserverURL", "",
"URL for the Consul server")
discoveryCmd.PersistentFlags().StringVar(&flags.eureka.serverURL, "eurekaserverURL", "",
"URL for the Eureka server")

discoveryCmd.PersistentFlags().StringVar(&flags.admissionArgs.ExternalAdmissionWebhookName,
"admission-webhook-name", "pilot-webhook.istio.io", "Webhook name for Pilot admission controller")
discoveryCmd.PersistentFlags().StringVar(&flags.admissionArgs.ServiceName,
"admission-service", "istio-pilot-external",
"Service name the admission controller uses during registration")
discoveryCmd.PersistentFlags().IntVar(&flags.admissionArgs.Port, "admission-service-port", 443,
"HTTPS port of the admission service. Must be 443 if service has more than one port ")
discoveryCmd.PersistentFlags().StringVar(&flags.admissionArgs.SecretName, "admission-secret", "pilot-webhook",
"Name of k8s secret for pilot webhook certs")
discoveryCmd.PersistentFlags().DurationVar(&flags.admissionArgs.RegistrationDelay,
"admission-registration-delay", 5*time.Second,
"Time to delay webhook registration after starting webhook server")

type serverArgs struct {
	kubeconfig string
	meshconfig string

	// namespace for the controller (typically istio installation namespace)
	namespace string

	// ingress sync mode is set to off by default
	controllerOptions kube.ControllerOptions
	discoveryOptions  envoy.DiscoveryServiceOptions

	registries    []string
	consul        consulArgs
	eureka        eurekaArgs
	admissionArgs admit.ControllerOptions
}

func (sa *serverArgs) String() string {
	var b bytes.Buffer
	s := *sa
	b.WriteString(fmt.Sprint("maxMessageSize: ", s.maxMessageSize, "\n"))
	b.WriteString(fmt.Sprint("maxConcurrentStreams: ", s.maxConcurrentStreams, "\n"))
	b.WriteString(fmt.Sprint("apiWorkerPoolSize: ", s.apiWorkerPoolSize, "\n"))
	b.WriteString(fmt.Sprint("adapterWorkerPoolSize: ", s.adapterWorkerPoolSize, "\n"))
	b.WriteString(fmt.Sprint("expressionEvalCacheSize: ", s.expressionEvalCacheSize, "\n"))
	b.WriteString(fmt.Sprint("port: ", s.port, "\n"))
	b.WriteString(fmt.Sprint("configAPIPort: ", s.configAPIPort, "\n"))
	b.WriteString(fmt.Sprint("monitoringPort: ", s.monitoringPort, "\n"))
	b.WriteString(fmt.Sprint("singleThreaded: ", s.singleThreaded, "\n"))
	b.WriteString(fmt.Sprint("compressedPayload: ", s.compressedPayload, "\n"))
	b.WriteString(fmt.Sprint("traceOutput: ", s.traceOutput, "\n"))
	b.WriteString(fmt.Sprint("serverCertFile: ", s.serverCertFile, "\n"))
	b.WriteString(fmt.Sprint("serverKeyFile: ", s.serverKeyFile, "\n"))
	b.WriteString(fmt.Sprint("clientCertFiles: ", s.clientCertFiles, "\n"))
	b.WriteString(fmt.Sprint("configStoreURL: ", s.configStoreURL, "\n"))
	b.WriteString(fmt.Sprint("configStore2URL: ", s.configStore2URL, "\n"))
	b.WriteString(fmt.Sprint("configDefaultNamespace: ", s.configDefaultNamespace, "\n"))
	b.WriteString(fmt.Sprint("configFetchIntervalSec: ", s.configFetchIntervalSec, "\n"))
	b.WriteString(fmt.Sprint("configIdentityAttribute: ", s.configIdentityAttribute, "\n"))
	b.WriteString(fmt.Sprint("configIdentityAttributeDomain: ", s.configIdentityAttributeDomain, "\n"))
	b.WriteString(fmt.Sprint("stringTablePurgeLimit: ", s.stringTablePurgeLimit, "\n"))
	return b.String()
}
