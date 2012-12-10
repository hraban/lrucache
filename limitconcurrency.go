package lrucache

// Process operations concurrently except for those with an identical key.
func nocondupesMainloop(f OnMissHandler, opchan chan reqGet) {
	// Push result of call to wrapped function down this channel
	waiting := map[string]chan replyGet{}
	type fullReply struct {
		replyGet
		id string
	}
	donechan := make(chan fullReply)
	for donechan != nil {
		select {
		// A new subscriber appears!
		case r, ok := <-opchan:
			if !ok {
				// Stop bothering with incoming operations
				opchan = nil
			}
			oldreplychan, inprogress := waiting[r.id]
			newreplychan := make(chan replyGet)
			waiting[r.id] = newreplychan
			if !inprogress {
				// Launch a seed
				// Explicit argument to deal with Go closure semantics
				go func(r reqGet) {
					var reply fullReply
					reply.id = r.id
					reply.val, reply.err = f(r.id)
					donechan <- reply
				}(r)
			}
			// Launch a consumer
			go func(r reqGet) {
				reply := <-newreplychan
				// Pass the result to the waiting call to wrapper
				r.reply <- reply
				close(r.reply)
				if oldreplychan != nil {
					// Forward the reply to the next listener
					oldreplychan <- reply
					close(oldreplychan)
				}
			}(r)
			break
		case full := <-donechan:
			waiting[full.id] <- full.replyGet
			delete(waiting, full.id)
			if opchan == nil && len(waiting) == 0 {
				close(donechan)
				donechan = nil
			}
			break
		}
	}
	return
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
		if key == "" {
			close(opchan)
			return nil, nil
		}
		replychan := make(chan replyGet)
		opchan <- reqGet{key, replychan}
		reply := <-replychan
		return reply.val, reply.err
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
