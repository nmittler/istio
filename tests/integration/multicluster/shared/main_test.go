//  Copyright Istio Authors
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

package shared

import (
	"testing"

	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/istio"
	"istio.io/istio/pkg/test/framework/components/pilot"
	"istio.io/istio/pkg/test/framework/label"
	"istio.io/istio/pkg/test/framework/resource"
	"istio.io/istio/pkg/test/framework/resource/environment"
	"istio.io/istio/tests/integration/multicluster"
)

var (
	ist istio.Instance
	p   pilot.Instance
)

func TestReachability(t *testing.T) {
	multicluster.DoReachability(t, p)
}

func TestMain(m *testing.M) {
	framework.
		NewSuite("multicluster", m).
		Label(label.Multicluster).
		Label(label.SharedControlPlane).
		RequireEnvironment(environment.Kube).
		RequireMinClusters(2).
		SetupOnEnv(environment.Kube, istio.Setup(&ist, nil)).
		Setup(func(ctx resource.Context) (err error) {
			if p, err = pilot.New(ctx, pilot.Config{}); err != nil {
				return err
			}
			return nil
		}).
		Run()
}
