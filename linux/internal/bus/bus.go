// Package bus is Sentinel's in-process event bus: typed topics, fan-out to
// any number of subscribers, and publishers that never block.
//
// Each subscriber has one bounded FIFO queue (covering every topic it
// handles) drained by its own goroutine, so a subscriber sees messages in
// publish order, per topic and across its topics. When a subscriber falls
// behind and its queue is full, the oldest queued message is dropped and
// counted (logged at most every 10 s), like the MQTT bridge's queue: a slow
// broker or push relay can never hold up detection or an HTTP handler.
//
// The bus is management-side plumbing (MQTT, push, the coming v2 WebSocket
// stream); the recording side reports through link.Sink instead.
package bus

import (
	"log/slog"
	"sync"
	"time"
)

// Topic names a message stream whose payloads have type T.
type Topic[T any] struct{ name string }

// NewTopic declares a topic. Names are conventionally "noun.verb"
// ("event.new", "alarm.state").
func NewTopic[T any](name string) Topic[T] { return Topic[T]{name: name} }

func (t Topic[T]) Name() string { return t.name }

// DefaultQueue is the queue length used when Subscribe is given 0.
const DefaultQueue = 1024

type Bus struct {
	mu     sync.RWMutex
	subs   map[string][]*Subscriber // topic name → subscribers
	all    []*Subscriber
	closed bool
}

func New() *Bus { return &Bus{subs: map[string][]*Subscriber{}} }

type delivery struct {
	topic string
	value any
}

// Subscriber is one consumer: a bounded queue and the goroutine draining it.
type Subscriber struct {
	name string
	bus  *Bus

	mu       sync.Mutex
	queue    []delivery // ring buffer
	head, n  int
	handlers map[string]func(any)
	dropped  uint64
	lastDrop time.Time
	busy     bool
	closed   bool
	idle     *sync.Cond

	wake chan struct{}
	done chan struct{}
}

// Subscribe creates a subscriber with a queue of queueLen messages (0 =
// DefaultQueue) and starts its goroutine. Register topics with Handle.
func (b *Bus) Subscribe(name string, queueLen int) *Subscriber {
	if queueLen <= 0 {
		queueLen = DefaultQueue
	}
	s := &Subscriber{
		name: name, bus: b,
		queue:    make([]delivery, queueLen),
		handlers: map[string]func(any){},
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	s.idle = sync.NewCond(&s.mu)
	b.mu.Lock()
	if b.closed {
		s.closed = true
		close(s.done)
	} else {
		b.all = append(b.all, s)
	}
	b.mu.Unlock()
	if !s.closed {
		go s.run()
	}
	return s
}

// Handle registers fn for topic t on subscriber s. fn runs on the
// subscriber's goroutine, one message at a time.
func Handle[T any](s *Subscriber, t Topic[T], fn func(T)) {
	s.mu.Lock()
	_, existed := s.handlers[t.name]
	s.handlers[t.name] = func(v any) { fn(v.(T)) }
	s.mu.Unlock()
	if existed {
		return
	}
	b := s.bus
	b.mu.Lock()
	if !b.closed {
		b.subs[t.name] = append(b.subs[t.name], s)
	}
	b.mu.Unlock()
}

// Publish hands v to every subscriber of t. It never blocks on a subscriber.
func Publish[T any](b *Bus, t Topic[T], v T) {
	if b == nil {
		return
	}
	b.mu.RLock()
	subs := b.subs[t.name]
	b.mu.RUnlock()
	for _, s := range subs {
		s.enqueue(delivery{topic: t.name, value: v})
	}
}

func (s *Subscriber) enqueue(d delivery) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	size := len(s.queue)
	if s.n == size { // full: drop the oldest
		s.head = (s.head + 1) % size
		s.n--
		s.dropped++
		if time.Since(s.lastDrop) > 10*time.Second {
			s.lastDrop = time.Now()
			slog.Warn("event bus: subscriber queue full; dropped oldest messages", "subscriber", s.name, "dropped_total", s.dropped)
		}
	}
	s.queue[(s.head+s.n)%size] = d
	s.n++
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Subscriber) run() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for s.n == 0 {
			s.busy = false
			s.idle.Broadcast()
			if s.closed {
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			<-s.wake
			s.mu.Lock()
		}
		d := s.queue[s.head]
		s.queue[s.head] = delivery{}
		s.head = (s.head + 1) % len(s.queue)
		s.n--
		s.busy = true
		fn := s.handlers[d.topic]
		s.mu.Unlock()
		if fn != nil {
			s.call(fn, d)
		}
	}
}

func (s *Subscriber) call(fn func(any), d delivery) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("event bus: subscriber panicked", "subscriber", s.name, "topic", d.topic, "panic", r)
		}
	}()
	fn(d.value)
}

// Dropped is how many messages this subscriber lost to a full queue.
func (s *Subscriber) Dropped() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Drain waits until everything queued so far has been handled (tests,
// shutdown). It returns early if the subscriber is closed.
func (s *Subscriber) Drain() {
	s.mu.Lock()
	for (s.n > 0 || s.busy) && !s.closed {
		s.idle.Wait()
	}
	s.mu.Unlock()
}

// Close stops the subscriber after the message being handled; queued
// messages are discarded.
func (s *Subscriber) Close() {
	b := s.bus
	b.mu.Lock()
	// New slices, never filtered in place: Publish may be ranging over the
	// old ones without the lock.
	for name, list := range b.subs {
		b.subs[name] = without(list, s)
	}
	b.all = without(b.all, s)
	b.mu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.n = 0
	s.idle.Broadcast()
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
	<-s.done
}

func without(list []*Subscriber, s *Subscriber) []*Subscriber {
	out := make([]*Subscriber, 0, len(list))
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// Close stops every subscriber. Publishing afterwards is a no-op.
func (b *Bus) Close() {
	b.mu.Lock()
	b.closed = true
	all := b.all
	b.all = nil
	b.mu.Unlock()
	for _, s := range all {
		s.Close()
	}
}
