// Copyright 2018 Istio Authors
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

package pilot

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/template"

	"istio.io/istio/tests/util"
)

var (
	conflictServiceTemplate = `
apiVersion: v1
kind: Namespace
metadata:
  name: {{.Namespace}}
---
apiVersion: v1
kind: Service
metadata:
  name: {{.ServiceName}}
  namespace: {{.Namespace}}
spec:
  clusterIP: None
  ports:
  - name: someport
    port: 15151
    protocol: TCP
    targetPort: somepodport
  selector:
    app: conflictapp
  sessionAffinity: None
  type: ClusterIP
`
)

func TestListenerConflicts(t *testing.T) {
	if !tc.Kube.IsClusterWide() {
		t.Skip("Cluster-wide deployment is required for this test so that pilot will observe all namespaces.")
	}

	// Deploy 2 headless TCP services with conflicting ports.
	cases := []struct {
		ServiceName string
		Namespace   string
	}{
		{
			ServiceName: fmt.Sprintf("service1"),
			Namespace:   fmt.Sprintf("%s-conflict-1", tc.Info.RunID),
		},
		{
			ServiceName: fmt.Sprintf("service2"),
			Namespace:   fmt.Sprintf("%s-conflict-2", tc.Info.RunID),
		},
	}
	for _, c := range cases {
		yaml := getConflictServiceYaml(c, t)
		kubeApplyConflictService(yaml, t)
		defer util.KubeDeleteContents("", yaml, tc.Kube.KubeConfig)
	}

	for cluster := range tc.Kube.Clusters {
		testName := fmt.Sprintf("%s_%s", cases[0].ServiceName, cases[1].ServiceName)
		runRetriableTest(t, cluster, testName, 5, func() error {
			// Scrape the pilot logs and verify that the newer service is filtered in favor of the older service.
			logs := getPilotLogs(t)
			lines := strings.Split(logs, "\n")
			secondServiceFiltered := false
			for _, line := range lines {
				if strings.Contains(line, "omitting filterchain") {

					// Nothing from the first service should be filtered.
					if strings.Contains(line, cases[0].ServiceName) {
						return fmt.Errorf("first headless TCP service unexpectedly filtered")
					}

					// Check if the second service was filtered.
					if strings.Contains(line, cases[1].ServiceName) {
						secondServiceFiltered = true
					}
				}
			}

			if !secondServiceFiltered {
				return fmt.Errorf("second headless TCP service was not filtered")
			}
			return nil
		})
	}
}

func getPilotLogs(t *testing.T) string {
	builder := strings.Builder{}
	pods := getPilotPods(t)
	for _, pod := range pods {
		builder.WriteString(util.GetPodLogs(tc.Kube.Namespace, pod, "discovery", false, false, tc.Kube.KubeConfig))
	}
	return builder.String()
}

func getPilotPods(t *testing.T) []string {
	res, err := util.Shell("kubectl get pods -n %s --kubeconfig=%s --selector istio=pilot -o=jsonpath='{range .items[*]}{.metadata.name}{\" \"}'",
		tc.Kube.Namespace, tc.Kube.KubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(res, " ")
}

func kubeApplyConflictService(yaml string, t *testing.T) {
	t.Helper()
	if err := util.KubeApplyContents("", yaml, tc.Kube.KubeConfig); err != nil {
		t.Fatal(err)
	}
}

func getConflictServiceYaml(params interface{}, t *testing.T) string {
	t.Helper()
	var tmp bytes.Buffer
	err := template.Must(template.New("inject").Parse(conflictServiceTemplate)).Execute(&tmp, params)
	if err != nil {
		t.Fatal(err)
	}
	return tmp.String()
}
