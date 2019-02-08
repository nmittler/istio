// Copyright 2019 Istio Authors
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

package queue_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"istio.io/istio/galley/pkg/runtime/projections/serviceentry/queue"
	"istio.io/istio/pilot/pkg/model"
)

var (
	emptyHandler = func(_ interface{}, _ model.Event) error {
		return nil
	}
)

func BenchmarkQueue(b *testing.B) {

	cases := []processor{
		newChannelProcessor(),
		newQueueProcessor(),
		newDirectProcessor(),
	}

	for _, producers := range []int{1, 5, 10} {
		for _, c := range cases {
			testName := fmt.Sprintf("%s_%dp", c.name(), producers)
			b.Run(testName, func(b *testing.B) {
				b.StopTimer()
				c.start(producers, b.N)
				b.StartTimer()

				c.awaitResults()

				b.StopTimer()
				c.stop()
				b.StartTimer()
			})
		}
	}
}

type processor interface {
	name() string
	start(numProducers, numMessages int)
	stop()
	awaitResults()
}

type channelProcessor struct {
	ch        chan queue.Task
	handler   queue.Handler
	remaining int
}

func newChannelProcessor() processor {
	ch := make(chan queue.Task, 1024)
	handler := func(obj interface{}, event model.Event) error {
		ch <- queue.NewTask(emptyHandler, obj, event)
		return nil
	}

	return &channelProcessor{
		ch:      ch,
		handler: handler,
	}
}

func (p *channelProcessor) name() string {
	return "channel"
}

func (p *channelProcessor) start(numProducers, numMessages int) {
	p.remaining = numProducers * numMessages
	for i := 0; i < numProducers; i++ {
		runProducer(numMessages, p.handler)
	}
}

func (p *channelProcessor) stop() {
}

func (p *channelProcessor) awaitResults() {
	for p.remaining > 0 {
		<-p.ch
		p.remaining--
	}
}

type queueProcessor struct {
	q      queue.Queue
	wg     *sync.WaitGroup
	stopCh chan struct{}
}

func newQueueProcessor() processor {
	return &queueProcessor{
		stopCh: make(chan struct{}),
	}
}

func (p *queueProcessor) name() string {
	return "queue"
}

func (p *queueProcessor) start(numProducers, numMessages int) {
	p.wg = &sync.WaitGroup{}
	p.wg.Add(numProducers * numMessages)

	p.q = queue.NewQueue(1 * time.Microsecond)
	handler := func(_ interface{}, _ model.Event) error {
		p.wg.Done()
		return nil
	}

	producer := func(o interface{}, e model.Event) error {
		p.q.Push(queue.NewTask(handler, o, e))
		return nil
	}

	go p.q.Run(p.stopCh)

	time.Sleep(500 * time.Millisecond)

	for i := 0; i < numProducers; i++ {
		runProducer(numMessages, producer)
	}
}

func (p *queueProcessor) stop() {
	p.stopCh <- struct{}{}
}

func (p *queueProcessor) awaitResults() {
	p.wg.Wait()
}

type directProcessor struct {
	wg *sync.WaitGroup
}

func newDirectProcessor() processor {
	return &directProcessor{}
}

func (p *directProcessor) name() string {
	return "direct"
}

func (p *directProcessor) start(numProducers, numMessages int) {
	p.wg = &sync.WaitGroup{}
	p.wg.Add(numProducers * numMessages)

	handler := func(obj interface{}, event model.Event) error {
		p.wg.Done()
		return nil
	}

	for i := 0; i < numProducers; i++ {
		runProducer(numMessages, handler)
	}
}

func (p *directProcessor) stop() {
}

func (p *directProcessor) awaitResults() {
	p.wg.Wait()
}

func runProducer(numMessages int, producer queue.Handler) {
	go func() {
		for i := 0; i < numMessages; i++ {
			producer(struct{}{}, model.EventAdd)
		}
	}()
}
