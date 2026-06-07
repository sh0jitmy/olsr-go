// Copyright 2026 The olsrd-go Authors
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

package eventbus

import (
	"context"
	"sync"
	"time"
)

type EventType string

const (
	EventInterfaceUpdate EventType = "interface_update"
	EventNeighborUpdate  EventType = "neighbor_update"
	EventTopologyUpdate  EventType = "topology_update"
	EventSPFTrigger      EventType = "spf_trigger"
	EventRouteInstall    EventType = "route_install"
)

type Event struct {
	Type EventType
	Data interface{}
}

type Subscription struct {
	ch     chan Event
	evType EventType
}

func (s *Subscription) Out() <-chan Event {
	return s.ch
}

type EventBus struct {
	mu           sync.RWMutex
	subs         map[EventType][]*Subscription
	bufferSize   int
	writeTimeout time.Duration
}

func NewEventBus(bufferSize int, writeTimeout time.Duration) *EventBus {
	return &EventBus{
		subs:         make(map[EventType][]*Subscription),
		bufferSize:   bufferSize,
		writeTimeout: writeTimeout,
	}
}

// Subscribe returns a Subscription for a given EventType.
func (b *EventBus) Subscribe(evType EventType) *Subscription {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub := &Subscription{
		ch:     make(chan Event, b.bufferSize),
		evType: evType,
	}
	b.subs[evType] = append(b.subs[evType], sub)
	return sub
}

// Unsubscribe removes a subscription and closes its channel.
func (b *EventBus) Unsubscribe(sub *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()

	list, exists := b.subs[sub.evType]
	if !exists {
		return
	}

	for i, s := range list {
		if s == sub {
			// Remove from slice
			b.subs[sub.evType] = append(list[:i], list[i+1:]...)
			close(s.ch)
			break
		}
	}
}

// Publish distributes an event to all subscribers of its type.
// It respects context cancellation and uses a write timeout to avoid blocking.
func (b *EventBus) Publish(ctx context.Context, ev Event) {
	b.mu.RLock()
	subs, exists := b.subs[ev.Type]
	if !exists || len(subs) == 0 {
		b.mu.RUnlock()
		return
	}

	// Make a local copy of subscribers to avoid holding read lock during send
	localSubs := make([]*Subscription, len(subs))
	copy(localSubs, subs)
	b.mu.RUnlock()

	for _, sub := range localSubs {
		select {
		case sub.ch <- ev:
		case <-ctx.Done():
			return
		case <-time.After(b.writeTimeout):
			// Timeout to apply backpressure without locking up the bus
			// In production, this warns of slow receivers
		}
	}
}
