package auth

import (
	"testing"
	"time"
)

func TestOnlyAFewHashesAreComputedAtOnce(t *testing.T) {
	for range cap(hashing) {
		hashing <- struct{}{}
	}
	done := make(chan struct{})
	go func() {
		CheckNoPassword("x")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a hash was computed while every slot was taken")
	case <-time.After(200 * time.Millisecond):
	}
	<-hashing
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the hash did not go ahead once a slot was free")
	}
	for range cap(hashing) - 1 {
		<-hashing
	}
}
