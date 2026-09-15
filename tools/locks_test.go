package tools

import (
	"sync"
	"testing"
	"time"
)

// released fails a test whose locks still track a record once every hold
// has been let go.
func released(t *testing.T, l *writeLocks) {
	t.Helper()

	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.records) != 0 {
		t.Errorf("records left behind: %v", l.records)
	}
}

// Holds on one record queue; holds on others do not wait for them; sets
// taken in opposite orders cannot deadlock; a sweep waits for every hold and
// every hold waits for a sweep; and nothing is left behind.
func TestWriteLocks(t *testing.T) {
	t.Parallel()

	t.Run("one record, one at a time", func(t *testing.T) {
		t.Parallel()

		var l writeLocks
		var mu sync.Mutex
		inside, most := 0, 0
		var wg sync.WaitGroup
		for range 20 {
			wg.Go(func() {
				defer l.hold("item:a")()
				mu.Lock()
				inside++
				most = max(most, inside)
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inside--
				mu.Unlock()
			})
		}
		wg.Wait()
		if most != 1 {
			t.Errorf("%d holds of one record at once", most)
		}
		released(t, &l)
	})

	t.Run("other records do not wait", func(t *testing.T) {
		t.Parallel()

		var l writeLocks
		release := l.hold("item:a")
		done := make(chan struct{})
		go func() {
			l.hold("item:b")()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("a hold of item:b waited on item:a")
		}
		release()
		released(t, &l)
	})

	t.Run("overlapping sets in either order", func(t *testing.T) {
		t.Parallel()

		var l writeLocks
		var wg sync.WaitGroup
		for i := range 50 {
			wg.Go(func() {
				if i%2 == 0 {
					l.hold(itemKeys("x", "y", "z")...)()
				} else {
					l.hold(itemKeys("z", "y", "x", "x")...)()
				}
			})
		}
		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Fatal("deadlocked")
		}
		released(t, &l)
	})

	t.Run("a sweep and the holds exclude each other", func(t *testing.T) {
		t.Parallel()

		var l writeLocks
		release := l.hold("item:a")
		swept := make(chan struct{})
		go func() {
			l.holdAll()()
			close(swept)
		}()
		select {
		case <-swept:
			t.Fatal("the sweep ran while a record was held")
		case <-time.After(50 * time.Millisecond):
		}
		release()
		<-swept

		releaseAll := l.holdAll()
		held := make(chan struct{})
		go func() {
			l.hold("item:c")()
			close(held)
		}()
		select {
		case <-held:
			t.Fatal("a record was held during a sweep")
		case <-time.After(50 * time.Millisecond):
		}
		releaseAll()
		<-held
		released(t, &l)
	})
}
