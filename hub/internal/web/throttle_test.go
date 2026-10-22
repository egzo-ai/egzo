package web

import (
	"fmt"
	"sync"
	"testing"
	"time"
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

func TestGuessesAtMadeUpUsernamesTakeBoundedMemory(t *testing.T) {
	th := newThrottle()
	now := time.Now()
	th.now = func() time.Time { return now }
	for i := range maxAccounts + 500 {
		now = now.Add(time.Millisecond)
		th.try(fmt.Sprintf("made-up-%d", i))
	}
	if len(th.accounts) > maxAccounts {
		t.Errorf("%d accounts remembered, at most %d", len(th.accounts), maxAccounts)
	}
	if _, ok := th.accounts["made-up-0"]; ok {
		t.Error("the account quiet the longest was kept")
	}
	now = now.Add(2 * time.Hour)
	th.try("someone")
	if len(th.accounts) != 1 {
		t.Errorf("accounts quiet for an hour are still remembered: %d", len(th.accounts))
	}
}
