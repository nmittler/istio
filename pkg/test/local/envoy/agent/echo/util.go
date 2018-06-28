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

package echo

import "istio.io/istio/pkg/test/local/envoy/agent"
import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"istio.io/istio/pilot/pkg/model"
	echo_service "istio.io/istio/pkg/test/service/echo"
	"istio.io/istio/pkg/test/service/echo/proto"
)

// ForwardRequestToAgent sends an proto.ForwardEchoRequest to the given agent and parses the responses.
func ForwardRequestToAgent(a *agent.Agent, req *proto.ForwardEchoRequest) ([]*echo_service.ParsedResponse, error) {
	grpcPortA, err := agent.FindFirstPortForProtocol(a.GetProxy(), model.ProtocolGRPC)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", grpcPortA.ApplicationPort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := proto.NewEchoTestServiceClient(conn)
	resp, err := client.ForwardEcho(context.Background(), req)
	if err != nil {
		return nil, err
	}
	parsedResponses := echo_service.ParseForwardedResponse(resp)
	if len(parsedResponses) != 1 {
		return nil, fmt.Errorf("unexpected number of responses: %d", len(parsedResponses))
	}
	return parsedResponses, nil
}
