// Copyright © Hraban Luyat <hraban@0brg.net>
//
// License for use of this code is detailed in the LICENSE file

package lrucache

import (
	"errors"
	"math/rand"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"
)

type varsize int

func (i varsize) Size() int64 {
	return int64(i)
}

type purgeable struct {
	purged bool
	why    PurgeReason
}

func (x *purgeable) Size() int64 {
	return 1
}

func (x *purgeable) OnPurge(why PurgeReason) {
	x.purged = true
	x.why = why
}

func syncCache(c *Cache) {
	c.Get("imblueifIweregreenIwoulddie")
}

func TestOnPurge_1(t *testing.T) {
	c := New(1)
	var x, y purgeable
	c.Set("x", &x)
	c.Set("y", &y)
	syncCache(c)
	if !x.purged {
		t.Error("Element was not purged from full cache")
	}
	if x.why != CACHEFULL {
		t.Error("Element should have been purged but was deleted")
	}

	checkDLL(t, c)
}

func TestOnPurge_2(t *testing.T) {
	c := New(1)
	var x purgeable
	c.Set("x", &x)
	c.Delete("x")
	syncCache(c)
	if !x.purged {
		t.Error("Element was not deleted from cache")
	}
	if x.why != EXPLICITDELETE {
		t.Error("Element should have been deleted but was purged")
	}

	checkDLL(t, c)
}

// Just test filling a cache with a type that does not implement NotifyPurge
func TestsafeOnPurge(t *testing.T) {
	c := New(1)
	defer c.Close()
	i := varsize(1)
	j := varsize(1)
	c.Set("i", i)
	c.Set("j", j)
	syncCache(c)

	checkDLL(t, c)
}

func TestSize(t *testing.T) {
	c := New(100)
	defer c.Close()
	// sum(0..14) = 105
	for i := 1; i < 15; i++ {
		c.Set(strconv.Itoa(i), varsize(i))
	}
	syncCache(c)
	// At this point, expect {0, 1, 2, 3} to be purged
	if c.Size() != 99 {
		t.Errorf("Unexpected size: %d", c.Size())
	}
	for i := 0; i < 4; i++ {
		if _, err := c.Get(strconv.Itoa(i)); err != ErrNotFound {
			t.Errorf("Expected %d to be purged", i)
		}
	}
	for i := 4; i < 15; i++ {
		if _, err := c.Get(strconv.Itoa(i)); err != nil {
			t.Errorf("Expected %d to be cached", i)
		}
	}

	checkDLL(t, c)
}

func TestOnMiss(t *testing.T) {
	c := New(10)
	defer c.Close()
	// Expected cache misses (arbitrary value)
	misses := map[string]int{}
	for i := 5; i < 10; i++ {
		misses[strconv.Itoa(i)] = 0
	}
	c.OnMiss(func(id string) (Cacheable, error) {
		if _, ok := misses[id]; !ok {
			return nil, nil
		}
		delete(misses, id)
		i, err := strconv.Atoi(id)
		if err != nil {
			return nil, errors.New("Illegal id: " + id)
		}
		return i, nil
	})
	for i := 0; i < 5; i++ {
		c.Set(strconv.Itoa(i), i)
	}
	for i := 0; i < 10; i++ {
		x, err := c.Get(strconv.Itoa(i))
		switch err {
		case nil:
			break
		case ErrNotFound:
			t.Errorf("Unexpected cache miss for %d", i)
			continue
		default:
			t.Fatal(err)
		}
		if j := x.(int); j != i {
			t.Errorf("Illegal cache value: expected %d, got %d", i, j)
		}
	}
	for k := range misses {
		t.Errorf("Expected %s to miss", k)
	}

	checkDLL(t, c)
}

func TestOnMissDuplicate(t *testing.T) {
	c := New(10)
	defer c.Close()
	onmisscount := 0
	c.OnMiss(func(id string) (Cacheable, error) {
		onmisscount += 1
		return onmisscount, nil
	})
	c.Get("key")
	c.Get("key")
	c.Get("key")
	c.Get("key")
	c.Get("key")
	o, _ := c.Get("key")
	count := o.(int)
	if count != 1 {
		t.Error("OnMiss handler was not called exactly once:", count)
	}
}

func TestOnMissError(t *testing.T) {
	c := New(10)
	defer c.Close()
	myerr := errors.New("some error")
	onmisscount := 0
	c.OnMiss(func(id string) (Cacheable, error) {
		onmisscount += 1
		return onmisscount, myerr
	})
	c.Get("key")
	c.Get("key")
	c.Get("key")
	o, err := c.Get("key")
	if err != myerr {
		t.Error("Unexpected error from OnMiss through Get:", err)
	}
	count, ok := o.(int)
	if !ok {
		t.Fatal("Value not returned from Get when OnMiss returns error")
	}
	if count != 4 {
		t.Error("OnMiss handler was not called repeatedly on error:", count)
	}
}

// Test that a long running OnMiss doesn't block other Set calls
func TestOnMissConcurrentGetSet(t *testing.T) {
	c := New(10)
	defer c.Close()
	ch := make(chan string)
	var outer sync.WaitGroup
	// If key foo is requested but not cached, read it from the channel
	c.OnMiss(func(id string) (Cacheable, error) {
		if id == "foo" {
			// Indicate that we want a value
			ch <- ""
			id = <-ch
		}
		return id, nil
	})
	outer.Add(1)
	go func() {
		result, err := c.Get("foo")
		if err != nil {
			t.Errorf(`First Get("foo") error: %v`, err)
		}
		if result != "outside" {
			t.Errorf(`First Get("foo") expected "outside", got: %v`, result)
		}
		outer.Done()
	}()
	<-ch
	// Now we know for sure: a goroutine is blocking on c.Get("foo").
	// But other cache operations should be unaffected:
	c.Set("bar", "quux")
	// Unlock that poor blocked goroutine
	ch <- "outside"
	outer.Wait()
	result, err := c.Get("foo")
	switch {
	case err != nil:
		t.Fatalf(`Second Get("foo") error: %v`, err)
	case result != "outside":
		t.Fatalf(`Second Get("foo") expected "outside", got: %v`, result)
	}

	checkDLL(t, c)
}

// Test that a long blocking OnMiss handler doesn't block other Gets.
func TestOnMissConcurrentGetGet(t *testing.T) {
	c := New(10)
	onMissSync := make(chan string)
	var outer sync.WaitGroup
	c.OnMiss(func(id string) (Cacheable, error) {
		if id == "foo" {
			// Indicate that we want a value
			onMissSync <- ""
			id = <-onMissSync
		}
		return id, nil
	})
	outer.Add(1)
	go func() {
		result, err := c.Get("foo")
		if err != nil {
			t.Errorf(`First Get("foo") error: %v`, err)
		}
		if result != "outside" {
			t.Errorf(`First Get("foo") expected "outside", got: %v`, result)
		}
		outer.Done()
	}()
	<-onMissSync
	// The first (outer) Get("foo") call should be blocked now
	result, err := c.Get("bar")
	if err != nil {
		t.Fatalf(`Inner Get("bar") error: %v`, err)
	}
	if result != "bar" {
		t.Fatalf(`Inner Get("bar") expected "bar", got: %v`, result)
	}
	// The second (inner) Get call is done, go back to the outer
	onMissSync <- "outside"
	outer.Wait()
	result, err = c.Get("foo")
	if err != nil {
		t.Fatalf(`Second Get("foo") error: %v`, err)
	}
	if result != "outside" {
		t.Fatalf(`Second Get("foo") expected "outside", got: %v`, result)
	}

	checkDLL(t, c)
}

func TestZeroSize(t *testing.T) {
	c := New(2)
	defer c.Close()
	c.Set("a", varsize(0))
	c.Set("b", varsize(1))
	c.Set("c", varsize(2))
	if _, err := c.Get("a"); err != nil {
		t.Error("Purged element with size=0; should have left in cache")
	}
	c.Delete("a")
	c.Set("d", varsize(2))
	if _, err := c.Get("c"); err != ErrNotFound {
		t.Error("Kept `c' around for too long after removing empty element")
	}
	if _, err := c.Get("d"); err != nil {
		t.Error("Failed to cache `d' after removing empty element")
	}

	checkDLL(t, c)
}

func checkDLL(t *testing.T, c *Cache) {
	if c.mostRU == nil && c.leastRU == nil {
		return
	}
	if c.mostRU.younger != nil {
		t.Fatal("cache inconsistent: most recently used element has a younger sibling")
	}
	if c.leastRU.older != nil {
		t.Fatal("cache inconsistent: least recently used element has an older sibling")
	}
	for p := c.mostRU; p != c.leastRU; p = p.older {
		if p.older.younger != p {
			t.Fatalf("cache inconsistent: older-younger sibling relation violated")
		}
	}
}

func leakingGoroutinesHelper() {
	c := New(100)
	c.Set("foo", 123)
}

func TestLeakingGoroutines(t *testing.T) {
	n := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		leakingGoroutinesHelper()
	}
	// seduce the garbage collector
	time.Sleep(time.Second)
	runtime.GC()
	runtime.Gosched()
	time.Sleep(time.Second)
	n2 := runtime.NumGoroutine()
	leak := n2 - n
	// TODO: Why is this 1 no matter how many caches are created and cleaned up?
	// To see what I mean run this test in isolation (go test -run TestLeak);
	// leak will always be exactly 1.  When run alongside other tests their
	// garbage is also cleaned up here so leak can be less than 0---that's okay.
	if leak > 1 { // I'd like to test >0 here :(
		// Not .Error because garbage collection is not spec'ed yet (see doc for
		// New()).
		t.Log("leaking goroutines:", leak)
		//panic("dumping goroutine stacks")
	}
}

func benchmarkGet(b *testing.B, conc int) {
	b.StopTimer()
	// Size doesn't matter (that's what she said)
	c := New(1000)
	defer c.Close()
	c.Set("x", 1)
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/conc; i++ {
				c.Get("x")
			}
			syncCache(c)
			wg.Done()
		}()
	}
	wg.Wait()
}

func benchmarkSet(b *testing.B, conc int) {
	b.StopTimer()
	// Size matters.
	c := New(int64(b.N) / 4)
	defer c.Close()
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/conc; i++ {
				c.Set(strconv.Itoa(i), i)
			}
			wg.Done()
		}()
	}
	wg.Wait()
	syncCache(c)
}

func benchmarkAll(b *testing.B, conc int) {
	b.StopTimer()
	// Size is definitely important, but what is the right size?
	c := New(int64(b.N) / 4)
	defer c.Close()
	syncCache(c)
	var wg sync.WaitGroup
	wg.Add(conc)
	b.StartTimer()
	for i := 0; i < conc; i++ {
		go func() {
			for i := 0; i < b.N/3/conc; i++ {
				c.Set(strconv.Itoa(rand.Int()), 1)
				c.Get(strconv.Itoa(rand.Int()))
				c.Delete(strconv.Itoa(rand.Int()))
			}
			wg.Done()
		}()
	}
	wg.Wait()
	syncCache(c)
}

func BenchmarkGet(b *testing.B) {
	benchmarkGet(b, 1)
}

func Benchmark10ConcurrentGet(b *testing.B) {
	benchmarkGet(b, 10)
}

func Benchmark100ConcurrentGet(b *testing.B) {
	benchmarkGet(b, 100)
}

func Benchmark1KConcurrentGet(b *testing.B) {
	benchmarkGet(b, 1000)
}

func Benchmark10KConcurrentGet(b *testing.B) {
	benchmarkGet(b, 10000)
}

func BenchmarkSet(b *testing.B) {
	benchmarkSet(b, 1)
}

func Benchmark10ConcurrentSet(b *testing.B) {
	benchmarkSet(b, 10)
}

func Benchmark100ConcurrentSet(b *testing.B) {
	benchmarkSet(b, 100)
}

func Benchmark1KConcurrentSet(b *testing.B) {
	benchmarkSet(b, 10000)
}

func Benchmark10KConcurrentSet(b *testing.B) {
	benchmarkSet(b, 10000)
}

func BenchmarkAll(b *testing.B) {
	benchmarkAll(b, 1)
}

func Benchmark10ConcurrentAll(b *testing.B) {
	benchmarkAll(b, 10)
}

func Benchmark100ConcurrentAll(b *testing.B) {
	benchmarkAll(b, 100)
}

func Benchmark1KConcurrentAll(b *testing.B) {
	benchmarkAll(b, 1000)
}

func Benchmark10KConcurrentAll(b *testing.B) {
	benchmarkAll(b, 10000)
}
