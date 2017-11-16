// Copyright 2017 Istio Authors
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

package discovery

import (
	proxyconfig "istio.io/api/proxy/v1/config"
	"istio.io/istio/pilot/model"
	"istio.io/istio/pilot/proxy"
	"istio.io/istio/pilot/proxy/envoy"
	"github.com/golang/protobuf/ptypes"
	"time"
	"istio.io/istio/pilot/test/mock"
	"istio.io/istio/pilot/platform"
	"istio.io/istio/pilot/adapter/serviceregistry/aggregate"
	"istio.io/istio/pilot/adapter/config/memory"
)

var (
	defaultDiscoveryOptions = envoy.DiscoveryServiceOptions{
		Port:            8080,
		EnableProfiling: true,
		EnableCaching:   true}
)

type mockController struct{}

func (c *mockController) AppendServiceHandler(f func(*model.Service, model.Event)) error {
	return nil
}

func (c *mockController) AppendInstanceHandler(f func(*model.ServiceInstance, model.Event)) error {
	return nil
}

func (c *mockController) Run(<-chan struct{}) {}

func makeTestController() *aggregate.Controller {
	discovery1 := mock.NewDiscovery(
		map[string]*model.Service{
			mock.HelloService.Hostname:   mock.HelloService,
			mock.ExtHTTPService.Hostname: mock.ExtHTTPService,
		}, 2)

	discovery2 := mock.NewDiscovery(
		map[string]*model.Service{
			mock.WorldService.Hostname:    mock.WorldService,
			mock.ExtHTTPSService.Hostname: mock.ExtHTTPSService,
		}, 2)

	registry1 := aggregate.Registry{
		Name:             platform.ServiceRegistry("mockAdapter1"),
		ServiceDiscovery: discovery1,
		ServiceAccounts:  discovery1,
		Controller:       &mockController{},
	}

	registry2 := aggregate.Registry{
		Name:             platform.ServiceRegistry("mockAdapter2"),
		ServiceDiscovery: discovery2,
		ServiceAccounts:  discovery2,
		Controller:       &mockController{},
	}

	ctls := aggregate.NewController()
	ctls.AddRegistry(registry1)
	ctls.AddRegistry(registry2)

	return ctls
}

func makeTestDiscoveryOption() envoy.DiscoveryServiceOptions {
	return defaultDiscoveryOptions
}

func makeTestMeshConfig() *proxyconfig.MeshConfig {
	mesh := proxy.DefaultMeshConfig()
	mesh.MixerAddress = "istio-mixer.istio-system:9091"
	mesh.RdsRefreshDelay = ptypes.DurationProto(10 * time.Millisecond)
	return &mesh
}

// NewPilot creates a new test instance of the Pilot discovery service using an in-memory config store, mock
// discovery.
func NewPilot() (*envoy.DiscoveryService, error) {
	store := memory.Make(model.IstioConfigTypes)
	configCache := memory.NewController(store)
	mockDiscovery := mock.Discovery
	mockDiscovery.ClearErrors()
	mesh := makeTestMeshConfig()
	controller := makeTestController()

	environment := proxy.Environment{
		ServiceDiscovery: mockDiscovery,
		ServiceAccounts:  mockDiscovery,
		IstioConfigStore: model.MakeIstioStore(configCache),
		Mesh:             mesh}

	return envoy.NewDiscoveryService(
		controller,
		configCache,
		environment,
		makeTestDiscoveryOption())
}
