// Copyright Istio Authors
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

package multicluster

import (
	"fmt"
	"testing"
	"time"

	"istio.io/istio/pkg/config/protocol"
	"istio.io/istio/pkg/test/echo/common/scheme"
	"istio.io/istio/pkg/test/framework"
	"istio.io/istio/pkg/test/framework/components/echo"
	"istio.io/istio/pkg/test/framework/components/echo/echoboot"
	"istio.io/istio/pkg/test/framework/components/namespace"
	"istio.io/istio/pkg/test/framework/components/pilot"
	"istio.io/istio/pkg/test/framework/label"
	"istio.io/istio/pkg/test/framework/resource"
	"istio.io/istio/pkg/test/util/retry"
)

var (
	retryTimeout = time.Second * 30
	retryDelay   = time.Millisecond * 100
)

// DoReachability tests that services in 2 different clusters can talk to each other.
func DoReachability(t *testing.T, p pilot.Instance) {
	framework.NewTest(t).
		Label(label.Multicluster).
		Run(func(ctx framework.TestContext) {
			ns := namespace.NewOrFail(ctx, ctx, namespace.Config{
				Prefix: "reachability",
				Inject: true,
			})

			// Deploy a and b in different clusters.
			var a, b echo.Instance
			echoboot.NewBuilderOrFail(ctx, ctx).
				With(&a, newEchoConfig("a", ns, ctx.Environment().Clusters()[0], p)).
				With(&b, newEchoConfig("b", ns, ctx.Environment().Clusters()[1], p)).
				BuildOrFail(ctx)

			// Now verify that they can talk to each other.
			for _, src := range []echo.Instance{a, b} {
				for _, dest := range []echo.Instance{a, b} {
					src := src
					dest := dest
					subTestName := fmt.Sprintf("%s->%s://%s:%s%s",
						src.Config().Service,
						"http",
						dest.Config().Service,
						"http",
						"/")

					ctx.NewSubTest(subTestName).
						RunParallel(func(ctx framework.TestContext) {
							if err := retry.UntilSuccess(func() error {
								results, err := src.Call(echo.CallOptions{
									Target:   dest,
									PortName: "http",
									Scheme:   scheme.HTTP,
								})
								if err == nil {
									err = results.CheckOK()
								}
								if err != nil {
									return fmt.Errorf("%s to %s:%s using %s: expected success but failed: %v",
										src, dest, "http", scheme.HTTP, err)
								}
								return nil
							}, retry.Timeout(retryTimeout), retry.Delay(retryDelay)); err != nil {
								t.Fatal(err)
							}
						})
				}
			}
		})
}

func newEchoConfig(service string, ns namespace.Instance, cluster resource.Cluster, p pilot.Instance) echo.Config {
	return echo.Config{
		Pilot:          p,
		Service:        service,
		Namespace:      ns,
		Cluster:        cluster,
		ServiceAccount: true,
		Subsets: []echo.SubsetConfig{
			{
				Version: "v1",
			},
		},
		Ports: []echo.Port{
			{
				Name:     "http",
				Protocol: protocol.HTTP,
				// We use a port > 1024 to not require root
				InstancePort: 8090,
			},
			{
				Name:     "tcp",
				Protocol: protocol.TCP,
			},
			{
				Name:     "grpc",
				Protocol: protocol.GRPC,
			},
		},
	}
}
