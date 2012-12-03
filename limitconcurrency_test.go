package lrucache

import (
	"sync/atomic"
	"testing"
	"time"
)

func counter() func() int {
	var i int32
	return func() int {
		return int(atomic.AddInt32(&i, 1))
	}
}

func TestNoConcurrentDupes(t *testing.T) {
	rawcounter := counter()
	wait := make(chan bool)
	typedcounter := func(x string) Cacheable {
		rawcounter()
		wait <- <-wait
		return nil
	}
	safecounter := NoConcurrentDupes(typedcounter)
	for i := 0; i < 10; i++ {
		go safecounter("foo")
	}
	// Wait a bit to allow all typedcounter calls to increase the counter. This
	// cannot be done deterministically because the entire point is to test how
	// many of them are invoked in the first place.
	time.Sleep(10 * time.Millisecond)
	wait <- true
	count := rawcounter() - 1
	if count != 1 {
		t.Errorf("Function called too often (%d times)", count)
	}
}
