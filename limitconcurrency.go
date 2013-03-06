// Copyright © 2012, 2013 Hraban Luyat <hraban@0brg.net>
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
	"errors"
)

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
				break
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
// The second return value is the quit channel. Send any value down that
// channel to stop the wrapper.  Running operations will complete but it is an
// error to invoke this function after that. Not panic, just an error.
func NoConcurrentDupes(f OnMissHandler) (OnMissHandler, chan<- bool) {
	errClosed := errors.New("NoConcurrentDupes wrapper has been closed")
	opchan := make(chan reqGet)
	go nocondupesMainloop(f, opchan)
	quit := make(chan bool, 1)
	wrap := func(key string) (Cacheable, error) {
		if opchan == nil {
			return nil, errClosed
		}
		select {
		case <-quit:
			close(opchan)
			opchan = nil
			return nil, errClosed
		default:
		}
		replychan := make(chan replyGet)
		opchan <- reqGet{key, replychan}
		reply := <-replychan
		return reply.val, reply.err
	}
	return wrap, quit
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
