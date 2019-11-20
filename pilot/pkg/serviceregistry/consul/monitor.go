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

package consul

import (
	"time"

	"github.com/hashicorp/consul/api"

	"istio.io/pkg/log"
)

// Monitor handles service and instance changes
type Monitor interface {
	Start(<-chan struct{})
}

type consulMonitor struct {
	discovery *api.Client
	handler   func()
}

const (
	refreshIdleTime    = 5 * time.Second
	periodicCheckTime  = 2 * time.Second
	blockQueryWaitTime = 10 * time.Minute
)

func (m *consulMonitor) Start(stop <-chan struct{}) {
	change := make(chan struct{})
	go m.watch(change, stop)
	go m.updateRecord(change, stop)
}

func (m *consulMonitor) watch(change chan struct{}, stop <-chan struct{}) {
	var consulWaitIndex uint64

	for {
		select {
		case <-stop:
			return
		default:
			queryOptions := api.QueryOptions{
				WaitIndex: consulWaitIndex,
				WaitTime:  blockQueryWaitTime,
			}
			// This Consul REST API will block until service changes or timeout
			_, queryMeta, err := m.discovery.Catalog().Services(&queryOptions)
			if err != nil {
				log.Warnf("Could not fetch services: %v", err)
			} else if consulWaitIndex != queryMeta.LastIndex {
				consulWaitIndex = queryMeta.LastIndex
				change <- struct{}{}
			}
			time.Sleep(periodicCheckTime)
		}
	}
}

func (m *consulMonitor) updateRecord(change <-chan struct{}, stop <-chan struct{}) {
	lastChange := int64(0)
	ticker := time.NewTicker(periodicCheckTime)

	for {
		select {
		case <-change:
			lastChange = time.Now().Unix()
		case <-ticker.C:
			currentTime := time.Now().Unix()
			if lastChange > 0 && currentTime-lastChange > int64(refreshIdleTime.Seconds()) {
				log.Infof("Consul service changed")
				go m.handler()
				lastChange = int64(0)
			}
		case <-stop:
			ticker.Stop()
			return
		}
	}
}
