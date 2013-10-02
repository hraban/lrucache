// Copyright © 2012, 2013 Lrucache contributors, see AUTHORS file
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to
// deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
// sell copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
// FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
// IN THE SOFTWARE.

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
	var main, threads sync.WaitGroup
	main.Add(1)
	typedcounter := func(x string) (Cacheable, error) {
		rawcounter()
		main.Wait()
		return 7878, nil
	}
	safecounter, quit := NoConcurrentDupes(typedcounter)
	defer func() { quit <- true }()
	for i := 0; i < 10; i++ {
		threads.Add(1)
		go func() {
			val, _ := safecounter("foo")
			if val != 7878 {
				t.Error("Unexpected value:", val)
			}
			threads.Done()
		}()
	}
	// Wait a bit to allow all typedcounter calls to increase the counter. This
	// cannot be done deterministically because the entire point is to test how
	// many of them are invoked in the first place.
	time.Sleep(10 * time.Millisecond)
	main.Done()
	threads.Wait()
	count := rawcounter() - 1
	if count != 1 {
		t.Errorf("Function called too often (%d times)", count)
	}
}

func TestNoConcurrentDupes_useStale(t *testing.T) {
	bare := func(id string) (Cacheable, error) {
		return 123, nil
	}
	safe, quit := NoConcurrentDupes(bare)
	quit <- true
	_, err := safe("anything")
	if err == nil {
		t.Error("Expected error when reusing wrapped f after close")
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
	unsafef := func(x string) (Cacheable, error) {
		newi := atomic.AddInt32(&i, 1)
		oldmax := atomic.LoadInt32(&max)
		newmax := maxInt32(oldmax, newi)
		for !atomic.CompareAndSwapInt32(&max, oldmax, newmax) {
			oldmax = atomic.LoadInt32(&max)
		}
		time.Sleep(1 * time.Millisecond)
		wg.Done()
		atomic.AddInt32(&i, -1)
		return nil, nil
	}
	safef := ThrottleConcurrency(unsafef, limit)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go safef("foo")
	}
	wg.Wait()
	if max != limit {
		t.Errorf("Unexpected maximum concurrency: %d (expected %d)", max, limit)
	}
}
