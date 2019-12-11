package endpoints

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"log"
	"time"

	"k8s.io/api/discovery/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	addrType     = v1alpha1.AddressTypeIP
	hostname     = "my.fake.service.com"
	portName     = "http"
	portProtocol = corev1.ProtocolTCP
	portValue    = int32(80)
)

type Options struct {
	Client             client.Client
	MaxSlices          int
	Period             time.Duration
	NumModifyPerUpdate int
	NumDeletePerUpdate int
	NumCreatePerUpdate int
}

type Generator struct {
	Options

	slices []*v1alpha1.EndpointSlice
}

func NewGenerator(opts Options) *Generator {
	return &Generator{
		Options: opts,
		slices:  make([]*v1alpha1.EndpointSlice, 0),
	}
}

func (g *Generator) Run(ctx context.Context) {
	ticker := time.NewTicker(g.Period)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := g.doUpdate(ctx); err != nil {
				log.Fatal(err)
			}
		}
	}
}

func (g *Generator) doUpdate(ctx context.Context) error {
	// Delete slices.
	for i := 0; i < len(g.slices) && i < g.NumDeletePerUpdate; i++ {
		ep := g.slices[i]
		if err := g.Client.Delete(ctx, ep); err != nil {
			return err
		}

		g.slices = append([]*v1alpha1.EndpointSlice{}, g.slices[i+1:]...)
	}

	// Create slices.
	for i := 0; i < g.NumCreatePerUpdate && len(g.slices) < g.MaxSlices; i++ {
		ep := newEndpointSlice(100)
		err := g.Client.Create(ctx, ep)
		if err != nil {
			return err
		}
		g.slices = append(g.slices, ep)
	}
	return nil
}

func newEndpointSlice(numEndpoints int) *v1alpha1.EndpointSlice {
	return &v1alpha1.EndpointSlice{
		TypeMeta: metav1.TypeMeta{
			Kind:       "EndpointSlice",
			APIVersion: "v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "FakeService",
			Namespace: "endpointtest",
		},
		AddressType: &addrType,
		Endpoints:   newEndpoints(numEndpoints),
		Ports: []v1alpha1.EndpointPort{
			{
				Name:     &portName,
				Protocol: &portProtocol,
				Port:     &portValue,
			},
			{
				Name:     &portName,
				Protocol: &portProtocol,
				Port:     &portValue,
			},
			{
				Name:     &portName,
				Protocol: &portProtocol,
				Port:     &portValue,
			},
		},
	}
}

func newEndpoints(numEndpoints int) []v1alpha1.Endpoint {
	eps := make([]v1alpha1.Endpoint, 0, numEndpoints)
	for i := 0; i < numEndpoints; i++ {
		eps = append(eps, v1alpha1.Endpoint{
			Addresses: []string{
				"127.0.0.1",
			},
			Conditions: v1alpha1.EndpointConditions{},
			Hostname:   &hostname,
			Topology: map[string]string{
				"key1": "value1",
				"key2": "value2",
				"key3": "value3",
			},
		})
	}
	return eps
}
