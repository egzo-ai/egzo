package web

import (
	"sync"
	"time"
)

// throttle slows password guessing per account: after a few failures in a row, the next try waits, twice as long after each
// further failure, up to a minute. There is no lockout and no limit per address: the hub is not meant to be reachable from the
// internet, and whoever can reach it can at worst slow the operator's own sign-in. Kept in memory; a restart forgets it.
type throttle struct {
	mu       sync.Mutex
	accounts map[string]*failures
	now      func() time.Time
}

type failures struct {
	n    int
	last time.Time
}

const (
	freeFailures = 3
	maxWait      = time.Minute
)

func newThrottle() *throttle { return &throttle{accounts: map[string]*failures{}, now: time.Now} }

func (t *throttle) delay(n int) time.Duration {
	if n < freeFailures {
		return 0
	}
	d := time.Second << min(n-freeFailures, 6)
	return min(d, maxWait)
}

// wait is how long the account must still wait before its next try.
func (t *throttle) wait(account string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	f := t.accounts[account]
	if f == nil {
		return 0
	}
	return max(f.last.Add(t.delay(f.n)).Sub(t.now()), 0)
}

func (t *throttle) fail(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for k, f := range t.accounts { // forget accounts quiet for an hour
		if now.Sub(f.last) > time.Hour {
			delete(t.accounts, k)
		}
	}
	f := t.accounts[account]
	if f == nil {
		f = &failures{}
		t.accounts[account] = f
	}
	f.n++
	f.last = now
}

func (t *throttle) reset(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.accounts, account)
}
