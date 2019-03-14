package pilot

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"github.com/gogo/protobuf/jsonpb"
	"github.com/gogo/protobuf/proto"
	"istio.io/istio/pilot/pkg/serviceregistry/kube"
	"net"
	"strings"
	"testing"

	xdsapi "github.com/envoyproxy/go-control-plane/envoy/api/v2"
	"github.com/envoyproxy/go-control-plane/envoy/api/v2/core"
	adsapi "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"

	"google.golang.org/grpc"

	"istio.io/istio/pilot/pkg/bootstrap"
	"istio.io/istio/pilot/pkg/model"
	proxyEnvoy "istio.io/istio/pilot/pkg/proxy/envoy"
	"istio.io/istio/pilot/pkg/serviceregistry"
	"istio.io/istio/pkg/keepalive"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	ns                   = "ns"
	domain               = "cluster.local"
	nodeName             = "node1"
	podName              = "pod1"
	podIP                = "10.40.1.4"
	serviceName          = "example"
	serviceNodeSeparator = "~"
	proxyType            = "sidecar"
)

func TestNativePilot(t *testing.T) {
	kubeClient := fake.NewSimpleClientset()

	if _, err := kubeClient.CoreV1().Namespaces().Create(
		&v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: ns,
			},
		},
	); err != nil {
		t.Fatal(err)
	}

	if _, err := kubeClient.CoreV1().Nodes().Create(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
			Labels: map[string]string{
				"failure-domain.beta.kubernetes.io/region": "region",
				"failure-domain.beta.kubernetes.io/zone":   "zone",
			},
		},
		Spec: v1.NodeSpec{
			PodCIDR: "10.0.0.1/24",
		},
	}); err != nil {
		t.Fatal(err)
	}

	_, _ = kubeClient.CoreV1().Pods(ns).Create(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: ns,
		},
		Spec: v1.PodSpec{
			NodeName: nodeName,
		},
		Status: v1.PodStatus{
			HostIP: "10.128.0.5",
			PodIP:  podIP,
			Phase:  v1.PodRunning,
		},
	})

	if _, err := kubeClient.CoreV1().Services(ns).Create(&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: ns,
			Labels: map[string]string{
				"app": serviceName,
			},
		},
		Spec: v1.ServiceSpec{
			ClusterIP: "10.43.240.10",
			Type:      v1.ServiceTypeClusterIP,
			Selector: map[string]string{
				"app": serviceName,
			},
			Ports: []v1.ServicePort{
				{
					Name:     "http",
					Protocol: v1.ProtocolTCP,
					Port:     80,
				},
				{
					Name:       "https",
					Protocol:   v1.ProtocolTCP,
					Port:       443,
					TargetPort: intstr.FromInt(80),
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := kubeClient.CoreV1().Endpoints(ns).Create(&v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: ns,
			Labels: map[string]string{
				"app": serviceName,
			},
		},
		Subsets: []v1.EndpointSubset{
			{
				Ports: []v1.EndpointPort{
					{
						Name:     "http",
						Port:     80,
						Protocol: v1.ProtocolTCP,
					},
					{
						Name:     "http2",
						Port:     80,
						Protocol: v1.ProtocolTCP,
					},
				},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	p, pilotStopFn := newPilot(kubeClient, t)
	defer pilotStopFn()

	discoveryAddr := p.GRPCListeningAddr.(*net.TCPAddr)
	conn, err := grpc.Dial(discoveryAddr.String(), grpc.WithInsecure())
	if err != nil {
		t.Fatal(err)
	}

	adsClient := adsapi.NewAggregatedDiscoveryServiceClient(conn)
	stream, err := adsClient.StreamAggregatedResources(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	req := &xdsapi.DiscoveryRequest{
		Node: &core.Node{
			Id: nodeID(),
		},
		TypeUrl: "type.googleapis.com/envoy.api.v2.Listener",
	}
	if err := stream.Send(req); err != nil {
		t.Fatal(err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}

	addressMap := make(map[string]struct{})
	for _, any := range resp.Resources {
		if any.TypeUrl != "type.googleapis.com/envoy.api.v2.Listener" {
			t.Fatalf("found non-listener resource: %s", any.TypeUrl)
		}

		l := &xdsapi.Listener{}
		if err := proto.Unmarshal(any.Value, l); err != nil {
			t.Fatal(err)
		}

		sa := (&l.Address).GetSocketAddress()
		if sa != nil {
			key := fmt.Sprintf("%s:%d", sa.Address, sa.GetPortValue())
			if _, found := addressMap[key]; found {
				t.Fatalf("found duplicate address:port=%s", key)
			}
			addressMap[key] = struct{}{}
		}
	}

	m := jsonpb.Marshaler{
		Indent: "  ",
	}
	out, err := m.MarshalToString(resp)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("NM: resp=%v\n", out)
}

func nodeID() string {
	id := fmt.Sprintf("%s.%s", serviceName, randomBase64String(10))
	return strings.Join([]string{proxyType, podIP, id, ns + ".svc." + domain}, serviceNodeSeparator)
}

func randomBase64String(len int) string {
	buff := make([]byte, len)
	_, _ = rand.Read(buff)
	str := base64.URLEncoding.EncodeToString(buff)
	return str[:len]
}

func newPilot(kubeClient kubernetes.Interface, t *testing.T) (*bootstrap.Server, func()) {
	t.Helper()

	mesh := model.DefaultMeshConfig()
	bootstrapArgs := bootstrap.PilotArgs{
		Namespace: ns,
		DiscoveryOptions: proxyEnvoy.DiscoveryServiceOptions{
			HTTPAddr:       ":0",
			MonitoringAddr: ":0",
			GrpcAddr:       ":0",
			SecureGrpcAddr: "",
		},
		MeshConfig: &mesh,
		// Use the config store for service entries as well.
		Service: bootstrap.ServiceArgs{
			Registries: []string{
				string(serviceregistry.KubernetesRegistry),
			},
		},
		Config: bootstrap.ConfigArgs{
			ControllerOptions: kube.ControllerOptions{
				DomainSuffix: domain,
			},
		},
		KeepaliveOptions: keepalive.DefaultOption(),
		ForceStop:        true,
		Plugins:   bootstrap.DefaultPlugins,
		KubeClient:       kubeClient,
	}

	// Create the server for the discovery service.
	server, err := bootstrap.NewServer(bootstrapArgs)
	if err != nil {
		t.Fatal(err)
	}

	// Start the server
	stop := make(chan struct{})
	if err := server.Start(stop); err != nil {
		t.Fatal(err)
	}

	stopFn := func() {
		stop <- struct{}{}
	}
	return server, stopFn
}
