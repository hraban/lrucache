package lrucache

import (
	"sync"
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

func maxInt32(x, y int32) int32 {
	if x < y {
		return y
	}
	return x
}

func TestThrottleConcurrency(t *testing.T) {
	var i, max int32
	const limit = 3
	var wg sync.WaitGroup
	unsafef := func(x string) Cacheable {
		newi := atomic.AddInt32(&i, 1)
		oldmax := atomic.LoadInt32(&max)
		newmax := maxInt32(oldmax, newi)
		for !atomic.CompareAndSwapInt32(&max, oldmax, newmax) {
			oldmax = atomic.LoadInt32(&max)
		}
		time.Sleep(1 * time.Millisecond)
		wg.Done()
		atomic.AddInt32(&i, -1)
		return nil
	}
	safef := ThrottleConcurrency(unsafef, limit)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go safef("foo")
	}
	wg.Wait()
	if max > limit {
		t.Errorf("Too many concurrent calls detected (%d > %d)", max, limit)
	}
}
