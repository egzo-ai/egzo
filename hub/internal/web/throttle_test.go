package web

import (
	"sync"
	"testing"
)

func TestAttemptsSentAllAtOnceAreThrottledLikeOnesInARow(t *testing.T) {
	th := newThrottle()
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for range 50 {
		wg.Go(func() {
			if th.try("admin") == 0 {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if allowed != freeFailures {
		t.Errorf("%d of 50 attempts at once went ahead, want %d", allowed, freeFailures)
	}
	th.reset("admin")
	if th.try("admin") != 0 {
		t.Error("an account is still slowed after a success")
	}
}
