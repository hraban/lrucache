package lrucache

import (
	"sync"
)

// Process operations concurrently except for those with an identical key.
func nocondupesMainloop(f func(string) Cacheable, opchan chan reqGet) {
	// Push result of call to wrapped function down this channel
	waiting := map[string]chan Cacheable{}
	for r := range opchan {
		reschan, inprogress := waiting[r.id]
		if !inprogress {
			reschan = make(chan Cacheable)
			waiting[r.id] = reschan
		}
		// Explicit argument to deal with Go closure semantics
		go func(r reqGet) {
			var result Cacheable
			if inprogress {
				// Already waiting for a call to complete, subscribe to result
				result = <-reschan
				// Pass the result to the waiting call to wrapper
				r.reply <- result
				close(r.reply)
			} else {
				// Get result from wrapped function directly
				result = f(r.id)
			}
			reschan <- result
		}(r)
	}
}

// Concurrent duplicate calls (same arg) are unified into one call. The result
// is returned to all callers by the wrapper. Intended for wrapping OnMiss
// handlers.
//
// Call with the empty string to terminate. Running operations will complete
// but it is an error to invoke this function after that.
func NoConcurrentDupes(f func(string) Cacheable) func(string) Cacheable {
	opchan := make(chan reqGet)
	go nocondupesMainloop(f, opchan)
	return func(key string) Cacheable {
		res := make(chan Cacheable)
		if key == "" {
			close(opchan)
			return nil
		}
		opchan <- reqGet{key, res}
		return <-res
	}
}

// Wrapper function that limits the number of concurrent calls to f. Intended
// for wrapping OnMiss handlers.
func ThrottleConcurrency(f func(string) Cacheable, maxconcurrent uint) func(string) Cacheable {
	c := sync.NewCond(&sync.Mutex{})
	n := 0
	return func(key string) Cacheable {
		c.L.Lock()
		// See, if I put n += 1 above this for loop and check for > only,
		// suddenly no more than maxconcurrent calls are ever executed at all.
		// I really do not understand why that is, which is not a good sign.
		// What is going on?
		for n >= int(maxconcurrent) {
			c.Wait()
		}
		n += 1
		c.L.Unlock()
		res := f(key)
		c.L.Lock()
		n -= 1
		c.L.Unlock()
		c.Signal()
		return res
	}
}
