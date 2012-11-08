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
// Then define a type that implements the Cacheable interface:
//
//      type cacheableInt int
//      
//      func (i cacheableInt) OnPurge(deleted bool) {
//          fmt.Printf("Purging %d\n", i)
//      }
//      
//      func (i cacheableInt) Size() int64 {
//          return 1
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
// * The integers are passed by value. Caching pointers is, of course, Okay,
// but be careful when caching a memory location that holds two different
// values at different points in time; updating the value of a pointer after
// caching it will change the cached value.
//
package lrucache

type Cache struct {
	// Feel free to change this whenever. The units are not bytes but just
	// whatever unit it is that your cache entries return from Size(). If
	// (roughly) all cached items are going to be (roughly) the same size it
	// makes sense to return 1 from Size() and set MaxSize to the maximum number
	// of elements you want to allow in cache.
	MaxSize int64
	size    int64
	entries map[string]*cacheEntry
	// Cache operations are pushed down this channel to the main cache loop
	opChan           chan operation
	lruHead, lruTail *cacheEntry
}

func (c *Cache) Size() int64 {
	return c.size
}

// Anything that implements this interface can be stored in a cache. Two
// different types can share the same cache, all that matters is that they
// implement this interface.
type Cacheable interface {
	// See Cache.MaxSize for an explanation
	Size() int64
}

type NotifyPurge interface {
	Cacheable
	// Called once when the element is purged from cache. The deleted boolean
	// indicates whether this call was the result of a call to Cache.Delete to
	// explicitly delete this item.  Possible reasons for this method to get
	// called:
	//
	// * Cache is growing too large and this is the least used item (deleted =
	// false)
	//
	// * This item was explicitly deleted using Cache.Delete(id) (deleted =
	// true)
	//
	// * A new element with the same key is stored (deleted = false)
	//
	// For most types of cached elements, this can just be a NOP. A real
	// example is a session cache where sessions are not stored in a database
	// until they are purged from the memory cache. As long as the memory cache
	// is large enough to hold all of them, they expire before the cache grows
	// too large and no database connection is ever needed. This OnPurge
	// implementation would store items to a database iff deleted == false.
	//
	// Called from within a private goroutine, but never called concurrently
	// with other elements' OnPurge().
	OnPurge(deleted bool)
}

// Requests that are passed to the cache managing goroutine
type operation interface{}

type reqSet struct {
	id      string
	payload Cacheable
}

type reqGet struct {
	id string
	// Cache goroutine pushes result down this channel (if any) and closes it
	reply chan Cacheable
}

type reqDelete struct {
	id string
}

// Used only for testing
type reqPing chan (bool)

type cacheEntry struct {
	payload Cacheable
	id      string
	// Pointers for LRU cache
	prev, next *cacheEntry
}

// Only call c.OnPurge() if c implements NotifyPurge.
func safeOnPurge(c Cacheable, deleted bool) {
	if t, ok := c.(NotifyPurge); ok {
		t.OnPurge(deleted)
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
	c.size -= e.payload.Size()
	return
}

// Purge the least recently used from the cache
func purgeLRU(c *Cache) {
	safeOnPurge(c.lruTail.payload, false)
	removeEntry(c, c.lruTail)
	return
}

// Trim the cache until its size <= max size
func trimCache(c *Cache) {
	for c.size > c.MaxSize {
		purgeLRU(c)
	}
	return
}

// Not safe for use in concurrent goroutines
func directSet(c *Cache, req reqSet) {
	// Overwrite old entry
	if old, ok := c.entries[req.id]; ok {
		safeOnPurge(old.payload, false)
		removeEntry(c, old)
	}
	e := cacheEntry{payload: req.payload, id: req.id}
	if len(c.entries) == 0 {
		c.lruTail = &e
		c.lruHead = &e
		e.next = nil
		e.prev = nil
	} else {
		c.lruHead.next = &e
		e.prev = c.lruHead
		c.lruHead = &e
	}
	c.size += e.payload.Size()
	c.entries[req.id] = &e
	trimCache(c)
	return
}

// Not safe for use in concurrent goroutines
func directDelete(c *Cache, req reqDelete) {
	e, ok := c.entries[req.id]
	if ok {
		safeOnPurge(e.payload, true)
		removeEntry(c, e)
	}
	return
}

// Not safe for use in concurrent goroutines
func directGet(c *Cache, req reqGet) {
	e, ok := c.entries[req.id]
	if ok {
		req.reply <- e.payload
	}
	close(req.reply)
	if !ok || e.next == nil {
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
	c.MaxSize = maxsize
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
			case reqPing:
				req <- true
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

func (c *Cache) Get(id string) (Cacheable, bool) {
	req := reqGet{id: id, reply: make(chan Cacheable)}
	c.opChan <- req
	e, ok := <-req.reply
	return e, ok
}

func (c *Cache) Delete(id string) {
	c.opChan <- reqDelete{id: id}
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

func Get(id string) (Cacheable, bool) {
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
