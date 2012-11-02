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

// RAMcache, a light-weight LRU based memory cache library for Go.
//
// To use this library, first create a cache:
//
//      c := ramcache.New(1234)
//
// Now define a type that implements the Cacheable interface:
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
// Note:
//
// * The unit of item sizes is not defined; whatever it is, once the sum
// exceeds the maximum cache size, elements start getting purged until it
// drops below the threshold again.
//
// * The integers are passed by value. Caching pointers is, of course, Okay,
// but be careful not to cache a memory location that holds two different
// values at different points in time; updating the value of a pointer after
// caching it will change the cached value.
//
// Now start storing values:
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
package ramcache

type Cache struct {
	// Feel free to change this whenever. The units are not bytes but just
	// whatever unit it is that your cache entries return from Size(). If
	// (roughly) all cached items are going to be (roughly) the same size it
	// makes sense to return 1 from Size() and set MaxSize to the maximum number
	// of elements you want to allow in cache.
	MaxSize          int64
	size             int64
	entries          map[string]*cacheEntry
	setChan          chan cacheEntry
	getChan          chan readReq
	lruHead, lruTail *cacheEntry
}

// Anything that implements this interface can be stored in a cache. Two
// different types can share the same cache, all that matters is that they
// implement this interface.
type Cacheable interface {
	// See Cache.MaxSize for an explanation
	Size() int64
	// Called once when the element is purged from cache. The deleted boolean
	// indicates whether this call was the result of a call to Cache.Delete to
	// explicitly delete this item.  Possible reasons for this method to get
	// called:
	//
	// - Cache is growing too large and this is the least used item (deleted =
	//   false)
	//
	// - This item was explicitly deleted using Cache.Delete(id) (deleted =
	//   true)
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

type cacheEntry struct {
	payload Cacheable
	id      string
	// Pointers for LRU cache
	prev, next *cacheEntry
}

type readReq struct {
	id    string
	reply chan Cacheable
}

// Purge the least recently used from the cache
func purgeLRU(c *Cache) {
	c.lruTail.payload.OnPurge(false)
	c.size -= c.lruTail.payload.Size()
	delete(c.entries, c.lruTail.id)
	c.lruTail = c.lruTail.next
	if c.lruTail == nil {
		// Not something you would expect to happen. This happens when the head
		// item alone is larger than the entire cache.
		c.lruHead = nil
	} else {
		c.lruTail.prev = nil
	}
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
func directSet(c *Cache, e cacheEntry) {
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
	c.entries[e.id] = &e
	trimCache(c)
	return
}

// Not safe for use in concurrent goroutines
func directGet(c *Cache, id string) (Cacheable, bool) {
	e, ok := c.entries[id]
	if ok && e.next != nil {
		if e.prev != nil {
			e.prev.next = e.next
		}
		e.next.prev = e.prev
		e.prev = c.lruTail
		c.lruTail = e
	}
	return e.payload, ok
}

func (c *Cache) Init(maxsize int64) {
	c.MaxSize = maxsize
	c.setChan = make(chan cacheEntry)
	c.getChan = make(chan readReq)
	c.entries = map[string]*cacheEntry{}
	go func() {
		for {
			select {
			case e := <-c.setChan:
				if e.payload == nil {
					delete(c.entries, e.id)
				} else {
					directSet(c, e)
				}
				break
			case req := <-c.getChan:
				p, ok := directGet(c, req.id)
				if ok {
					req.reply <- p
				}
				close(req.reply)
				break
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
	c.setChan <- cacheEntry{payload: p, id: id}
	return
}

func (c *Cache) Get(id string) (Cacheable, bool) {
	req := readReq{id: id, reply: make(chan Cacheable)}
	c.getChan <- req
	e, ok := <-req.reply
	return e, ok
}

func (c *Cache) Delete(id string) {
	c.setChan <- cacheEntry{payload: nil, id: id}
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
