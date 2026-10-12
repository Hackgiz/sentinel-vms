package bus

import (
	"sync"
	"testing"
	"time"
)

var (
	numbers = NewTopic[int]("test.number")
	words   = NewTopic[string]("test.word")
)

func TestFanOutToEverySubscriber(t *testing.T) {
	b := New()
	defer b.Close()
	var mu sync.Mutex
	got := map[string][]int{}
	var subs []*Subscriber
	for _, name := range []string{"a", "b", "c"} {
		name := name
		s := b.Subscribe(name, 0)
		Handle(s, numbers, func(n int) {
			mu.Lock()
			got[name] = append(got[name], n)
			mu.Unlock()
		})
		subs = append(subs, s)
	}
	for i := 0; i < 100; i++ {
		Publish(b, numbers, i)
	}
	for _, s := range subs {
		s.Drain()
	}
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"a", "b", "c"} {
		if len(got[name]) != 100 {
			t.Fatalf("%s got %d messages", name, len(got[name]))
		}
	}
}

func TestOrderingPerTopicAndAcrossTopics(t *testing.T) {
	b := New()
	defer b.Close()
	s := b.Subscribe("ordered", 0)
	var seq []string
	Handle(s, numbers, func(int) { t.Error("replaced handler called") })
	var nums []int
	Handle(s, numbers, func(n int) { nums = append(nums, n) }) // re-registering replaces
	Handle(s, words, func(w string) { seq = append(seq, w) })
	for i := 0; i < 500; i++ {
		Publish(b, numbers, i)
		if i%100 == 0 {
			Publish(b, words, "w")
		}
	}
	s.Drain()
	for i, n := range nums {
		if n != i {
			t.Fatalf("message %d out of order: %d", i, n)
		}
	}
	if len(nums) != 500 || len(seq) != 5 {
		t.Fatalf("got %d numbers, %d words", len(nums), len(seq))
	}
}

func TestInterleavedTopicsKeepPublishOrder(t *testing.T) {
	b := New()
	defer b.Close()
	s := b.Subscribe("mixed", 0)
	var got []string
	Handle(s, numbers, func(int) { got = append(got, "num") })
	Handle(s, words, func(w string) { got = append(got, w) })
	Publish(b, words, "first")
	Publish(b, numbers, 1)
	Publish(b, words, "last")
	s.Drain()
	if len(got) != 3 || got[0] != "first" || got[1] != "num" || got[2] != "last" {
		t.Fatalf("order %v", got)
	}
}

func TestSlowSubscriberNeverBlocksPublisher(t *testing.T) {
	b := New()
	defer b.Close()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	slow := b.Subscribe("slow", 4)
	var slowGot []int
	Handle(slow, numbers, func(n int) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		slowGot = append(slowGot, n)
	})
	fast := b.Subscribe("fast", 2000)
	var fastGot int
	Handle(fast, numbers, func(int) { fastGot++ })

	start := time.Now()
	// Wait until the slow handler holds the first message, so the queue
	// fills behind it.
	Publish(b, numbers, 0)
	<-entered
	for i := 1; i < 1000; i++ {
		Publish(b, numbers, i)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("publishing took %v with a stuck subscriber", d)
	}
	fast.Drain()
	if fastGot != 1000 {
		t.Fatalf("fast subscriber got %d", fastGot)
	}
	close(release)
	slow.Drain()
	// One message was in the handler when the queue filled; the queue kept
	// the newest 4. Everything else was dropped and counted.
	if len(slowGot) != 5 || slowGot[len(slowGot)-1] != 999 {
		t.Fatalf("slow subscriber got %v", slowGot)
	}
	if d := slow.Dropped(); d != 995 {
		t.Fatalf("dropped = %d, want 995", d)
	}
	if fast.Dropped() != 0 {
		t.Fatal("fast subscriber dropped messages")
	}
}

func TestPanickingHandlerKeepsSubscriberAlive(t *testing.T) {
	b := New()
	defer b.Close()
	s := b.Subscribe("panicky", 0)
	var got []int
	Handle(s, numbers, func(n int) {
		if n == 1 {
			panic("boom")
		}
		got = append(got, n)
	})
	for i := 0; i < 3; i++ {
		Publish(b, numbers, i)
	}
	s.Drain()
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestUnsubscribedTopicsAndClose(t *testing.T) {
	b := New()
	s := b.Subscribe("words-only", 0)
	n := 0
	Handle(s, words, func(string) { n++ })
	Publish(b, numbers, 1) // nobody listens: fine
	Publish(b, words, "x")
	s.Drain()
	s.Close()
	Publish(b, words, "after close")
	b.Close()
	Publish(b, words, "after bus close")
	var nilBus *Bus
	Publish(nilBus, words, "nil bus")
	if n != 1 {
		t.Fatalf("handled %d", n)
	}
	late := b.Subscribe("late", 0) // subscribing to a closed bus is inert
	Handle(late, words, func(string) { t.Error("closed bus delivered") })
	Publish(b, words, "x")
	late.Drain()
}

// Subscribers that come and go (one per WebSocket client, say) are fully
// forgotten, and closing one while others publish is race-free.
func TestCloseWhilePublishingForgetsSubscriber(t *testing.T) {
	b := New()
	defer b.Close()
	keep := b.Subscribe("keep", 0)
	Handle(keep, numbers, func(int) {})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				Publish(b, numbers, i)
			}
		}
	}()
	for i := 0; i < 200; i++ {
		s := b.Subscribe("churn", 4)
		Handle(s, numbers, func(int) {})
		s.Close()
	}
	close(stop)
	wg.Wait()
	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.all) != 1 || len(b.subs[numbers.Name()]) != 1 {
		t.Fatalf("closed subscribers kept: all=%d subs=%d", len(b.all), len(b.subs[numbers.Name()]))
	}
}
