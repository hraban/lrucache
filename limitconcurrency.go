package lrucache

// Concurrent duplicate calls (same arg) are unified into one call. The result
// is returned to all callers by the wrapper.
//
// Call with the empty string to terminate. Running operations will complete
// but it is an error to invoke this function after that.
func NoConcurrentDupes(f func(string) Cacheable) func(string) Cacheable {
	type req struct {
		key        string
		resultChan chan Cacheable
	}
	opchan := make(chan req)
	go func() {
		// Push result of call to wrapped function down this channel
		waiting := map[string]chan Cacheable{}
		for r := range opchan {
			if r.key == "" {
				close(opchan)
				break
			}
			reschan, inprogress := waiting[r.key]
			if !inprogress {
				reschan = make(chan Cacheable)
				waiting[r.key] = reschan
			}
			// Explicit argument to deal with Go closure semantics
			go func(r req) {
				var result Cacheable
				if inprogress {
					// Already waiting for a call to complete, subscribe to result
					result = <-reschan
					// Pass the result to the waiting call to wrapper
					r.resultChan <- result
					close(r.resultChan)
				} else {
					// Get result from wrapped function directly
					result = f(r.key)
				}
				reschan <- result
			}(r)
		}
	}()
	return func(key string) Cacheable {
		res := make(chan Cacheable)
		opchan <- req{key, res}
		return <-res
	}
}
