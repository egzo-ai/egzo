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
	pruned   time.Time
	now      func() time.Time
}

type failures struct {
	n    int
	last time.Time
}

const (
	freeFailures = 3
	maxWait      = time.Minute
	// maxAccounts bounds the memory that guesses at made-up usernames take. When it is reached the account quiet the longest is
	// forgotten. Pushing out an account under attack would take this many attempts at other names since its last one, which at
	// the rate the hub checks passwords (two at a time) is several minutes: much longer than the longest wait.
	maxAccounts = 10_000
)

func newThrottle() *throttle { return &throttle{accounts: map[string]*failures{}, now: time.Now} }

func (t *throttle) delay(n int) time.Duration {
	if n < freeFailures {
		return 0
	}
	d := time.Second << min(n-freeFailures, 6)
	return min(d, maxWait)
}

// try counts an attempt at the account before its password is checked, and is how long the account must still wait when it is
// refused (0 when the attempt may go ahead). Counting first is what makes the delay hold against attempts sent all at once: each
// one sees the ones before it, though none has finished. A success resets the count.
func (t *throttle) try(account string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	f := t.accounts[account]
	if f != nil {
		if wait := f.last.Add(t.delay(f.n)).Sub(now); wait > 0 {
			return wait
		}
	}
	if f == nil {
		t.makeRoom(now)
		f = &failures{}
		t.accounts[account] = f
	}
	f.n++
	f.last = now
	return 0
}

// makeRoom forgets accounts quiet for an hour, once a minute at most, and the one quiet the longest when there are too many.
func (t *throttle) makeRoom(now time.Time) {
	if now.Sub(t.pruned) > time.Minute {
		t.pruned = now
		for k, f := range t.accounts {
			if now.Sub(f.last) > time.Hour {
				delete(t.accounts, k)
			}
		}
	}
	if len(t.accounts) < maxAccounts {
		return
	}
	oldest := ""
	for k, f := range t.accounts {
		if oldest == "" || f.last.Before(t.accounts[oldest].last) {
			oldest = k
		}
	}
	delete(t.accounts, oldest)
}

func (t *throttle) reset(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.accounts, account)
}
