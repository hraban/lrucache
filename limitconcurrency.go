package lrucache

// Process operations concurrently except for those with an identical key.
func nocondupesMainloop(f OnMissHandler, opchan chan reqGet) {
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
			var err Cacheable
			if inprogress {
				// Already waiting for a call to complete, subscribe to result
				result = <-reschan
				err = <-reschan
				// Pass the result to the waiting call to wrapper
				r.reply <- result
				r.reply <- err
				close(r.reply)
			} else {
				// Get result from wrapped function directly
				result, err = f(r.id)
			}
			reschan <- result
			reschan <- err
		}(r)
	}
}

// Concurrent duplicate calls (same arg) are unified into one call. The result
// is returned to all callers by the wrapper. Intended for wrapping OnMiss
// handlers.
//
// Call with the empty string to terminate. Running operations will complete
// but it is an error to invoke this function after that.
func NoConcurrentDupes(f OnMissHandler) OnMissHandler {
	opchan := make(chan reqGet)
	go nocondupesMainloop(f, opchan)
	return func(key string) (Cacheable, error) {
		reschan := make(chan Cacheable)
		if key == "" {
			close(opchan)
			return nil, nil
		}
		opchan <- reqGet{key, reschan}
		res := <-reschan
		var err error
		if res2 := <-reschan; res2 != nil {
			err = res2.(error)
		}
		return res, err
	}
}

// Wrapper function that limits the number of concurrent calls to f. Intended
// for wrapping OnMiss handlers.
func ThrottleConcurrency(f OnMissHandler, maxconcurrent uint) OnMissHandler {
	block := make(chan int, maxconcurrent)
	return func(key string) (Cacheable, error) {
		block <- 58008
		res, err := f(key)
		<-block
		return res, err
	}
}
