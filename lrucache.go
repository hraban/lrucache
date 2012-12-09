// Copyright © 2012 Hraban Luyat <hraban@0brg.net>
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

// Light-weight in-memory LRU (object) cache library for Go.
//
// To use this library, first create a cache:
//
//      c := lrucache.New(1234)
//
// Then, optionally, define a type that implements some of the interfaces:
//
//      type cacheableInt int
//      
//      func (i cacheableInt) OnPurge(why lrucache.PurgeReason) {
//          fmt.Printf("Purging %d\n", i)
//      }
//
// Finally:
//
//     for i := 0; i < 2000; i++ {
//         c.Set(strconv.Itoa(i), cacheableInt(i))
//     }
//
// This will generate the following output:
//
//     Purging 0
//     Purging 1
//     ...
//     Purging 764
//     Purging 765
//
// Note:
//
// * The unit of item sizes is not defined; whatever it is, once the sum
// exceeds the maximum cache size, elements start getting purged until it
// drops below the threshold again.
//
// * These integers are passed by value. Caching pointers is, of course, Okay,
// but be careful when caching a memory location that holds two different
// values at different points in time; updating the value of a pointer after
// caching it will change the cached value.
//
package lrucache

import (
	"errors"
)

type Cache struct {
	maxSize int64
	size    int64
	entries map[string]*cacheEntry
	// Cache operations are pushed down this channel to the main cache loop
	opChan           chan operation
	lruHead, lruTail *cacheEntry
	// If not nil, invoked for every cache miss.
	onMiss func(string) (Cacheable, error)
}

// Anything can be cached!
type Cacheable interface{}

// Optional interface for cached objects. If this interface is not implemented,
// an element is assumed to have size 1.
type SizeAware interface {
	// See Cache.MaxSize() for an explanation of the semantics. Please report a
	// constant size; the cache does not expect objects to change size while
	// they are cached. Items are trusted to report their own size accurately.
	Size() int64
}

func getSize(x Cacheable) int64 {
	if s, ok := x.(SizeAware); ok {
		return s.Size()
	}
	return 1
}

// Reasons for a cached element to be deleted from the cache
type PurgeReason int

const (
	// Cache is growing too large and this is the least used item
	CACHEFULL PurgeReason = iota
	// This item was explicitly deleted using Cache.Delete(id)
	EXPLICITDELETE
	// A new element with the same key is stored (usually indicates an update)
	KEYCOLLISION
)

// Optional interface for cached objects
type NotifyPurge interface {
	// Called once when the element is purged from cache. The argument
	// indicates why.
	//
	// Example use-case: a session cache where sessions are not stored in a
	// database until they are purged from the memory cache. As long as the
	// memory cache is large enough to hold all of them, they expire before the
	// cache grows too large and no database connection is ever needed. This
	// OnPurge implementation would store items to a database iff reason ==
	// CACHEFULL.
	//
	// Called from within a private goroutine, but never called concurrently
	// with other elements' OnPurge(). The entire cache is blocked until this
	// function returns. By all means, feel free to launch a fresh goroutine
	// and return immediately.
	OnPurge(why PurgeReason)
}

// Requests that are passed to the cache managing goroutine
type operation interface{}

type reqSet struct {
	id      string
	payload Cacheable
}

type reqGet struct {
	id string
	// If the key is found the value is pushed down this channel after which it
	// is closed immediately. If the value is not found, OnMiss is called. If
	// that results in an error, nil is pushed down the channel, followed by
	// the error, after which it is closed. If that does not work (OnMiss is
	// not defined, or it returns nil) the channel is closed immediately.
	reply chan Cacheable
}

type reqDelete string

type reqOnMissFunc func(string) (Cacheable, error)

type reqMaxSize int64

type reqGetSize chan<- int64

type cacheEntry struct {
	payload Cacheable
	id      string
	// Pointers for LRU cache
	prev, next *cacheEntry
}

// Only call c.OnPurge() if c implements NotifyPurge.
func safeOnPurge(c Cacheable, why PurgeReason) {
	if t, ok := c.(NotifyPurge); ok {
		t.OnPurge(why)
	}
	return
}

func removeEntry(c *Cache, e *cacheEntry) {
	delete(c.entries, e.id)
	if e.prev == nil {
		c.lruTail = e.next
	} else {
		e.prev.next = e.next
	}
	if e.next == nil {
		c.lruHead = e.prev
	} else {
		e.next.prev = e.prev
	}
	c.size -= getSize(e.payload)
	return
}

// Purge the least recently used from the cache
func purgeLRU(c *Cache) {
	safeOnPurge(c.lruTail.payload, CACHEFULL)
	removeEntry(c, c.lruTail)
	return
}

// Trim the cache until its size <= max size
func trimCache(c *Cache) {
	for c.size > c.maxSize {
		purgeLRU(c)
	}
	return
}

// Not safe for use in concurrent goroutines
func directSet(c *Cache, req reqSet) {
	// Overwrite old entry
	if old, ok := c.entries[req.id]; ok {
		safeOnPurge(old.payload, KEYCOLLISION)
		removeEntry(c, old)
	}
	e := cacheEntry{payload: req.payload, id: req.id}
	c.entries[req.id] = &e
	size := getSize(e.payload)
	if size == 0 {
		return
	}
	if c.lruTail == nil {
		c.lruTail = &e
		c.lruHead = &e
		e.next = nil
		e.prev = nil
	} else {
		c.lruHead.next = &e
		e.prev = c.lruHead
		c.lruHead = &e
	}
	c.size += size
	trimCache(c)
	return
}

// Not safe for use in concurrent goroutines
func directDelete(c *Cache, req reqDelete) {
	id := string(req)
	e, ok := c.entries[id]
	if ok {
		safeOnPurge(e.payload, EXPLICITDELETE)
		if getSize(e.payload) != 0 {
			removeEntry(c, e)
		}
	}
	return
}

// Handle a cache miss from outside the main goroutine
func handleCacheMiss(c *Cache, req reqGet) {
	if c.onMiss != nil {
		p, err := c.onMiss(req.id)
		switch {
		case err != nil:
			req.reply <- nil
			req.reply <- err
			break
		case p == nil:
			break
		default:
			// Push new value back into cache (normally, thus safely)
			c.Set(req.id, p)
			// After that is done, this Get is finally complete
			req.reply <- p
			break
		}
	}
	close(req.reply)
	return
}

// Not safe for use in concurrent goroutines
func directGet(c *Cache, req reqGet) {
	e, ok := c.entries[req.id]
	if !ok {
		go handleCacheMiss(c, req)
		return
	}
	req.reply <- e.payload
	close(req.reply)
	if e.next == nil {
		return
	}
	// Put element at the start of the LRU list
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.lruTail = e.next
	}
	e.next.prev = e.prev
	e.prev = c.lruHead
	c.lruHead = e
	return
}

func (c *Cache) Init(maxsize int64) {
	c.maxSize = maxsize
	c.opChan = make(chan operation)
	c.entries = map[string]*cacheEntry{}
	go func() {
		for op := range c.opChan {
			switch req := op.(type) {
			case reqSet:
				directSet(c, req)
			case reqDelete:
				directDelete(c, req)
			case reqGet:
				directGet(c, req)
			case reqOnMissFunc:
				c.onMiss = req
			case reqMaxSize:
				c.maxSize = int64(req)
				trimCache(c)
			case reqGetSize:
				req <- c.size
				close(req)
			default:
				panic("Illegal cache operation")
			}
		}
	}()
	return
}

// Store this item in cache. Panics if the cacheable is nil.
func (c *Cache) Set(id string, p Cacheable) {
	if p == nil {
		panic("Cacheable value must not be nil")
	}
	c.opChan <- reqSet{payload: p, id: id}
	return
}

var ErrNotFound = errors.New("Key not found in cache")

func (c *Cache) Get(id string) (Cacheable, error) {
	req := reqGet{id: id, reply: make(chan Cacheable)}
	c.opChan <- req
	e, ok := <-req.reply
	if !ok {
		return nil, ErrNotFound
	}
	if e == nil {
		return nil, (<-req.reply).(error)
	}
	return e, nil
}

func (c *Cache) Delete(id string) {
	c.opChan <- reqDelete(id)
}

func (c *Cache) Close() error {
	close(c.opChan)
	return nil
}

// Used to populate the cache if an entry is not found. If result is not nil,
// it is stored in cache and returned from Get. Call with f is nil to clear. If
// the function returns a non-nil error, that error is directly returned from
// the Get() call that caused it to be invoked. Note that it is legal to return
// (nil, nil): that just means the specific key could not be found. It will be
// treated as a Get() to an unknown key without an OnMiss handler set.
func (c *Cache) OnMiss(f func(string) (Cacheable, error)) {
	c.opChan <- reqOnMissFunc(f)
}

// Feel free to change this whenever. The units are not bytes but just whatever
// unit it is that your cache entries return from Size(). If (roughly) all
// cached items are going to be (roughly) the same size it makes sense to
// return 1 from Size() and set maxSize to the maximum number of elements you
// want to allow in cache.
func (c *Cache) MaxSize(i int64) {
	c.opChan <- reqMaxSize(i)
}

func (c *Cache) Size() int64 {
	reply := make(chan int64)
	c.opChan <- reqGetSize(reply)
	return <-reply
}

// Create and initialize a new cache, ready for use.
func New(maxsize int64) *Cache {
	var c Cache
	c.Init(maxsize)
	return &c
}

// Shared cache for configuration-less use

var sharedCache Cache

// Only necessary if you plan on using non-methods Get and Set.
func InitShared(maxsize int64) {
	sharedCache.Init(maxsize)
}

func Get(id string) (Cacheable, error) {
	return sharedCache.Get(id)
}

func Set(id string, c Cacheable) {
	sharedCache.Set(id, c)
	return
}

func Delete(id string) {
	sharedCache.Delete(id)
	return
}
