// Copyright © Hraban Luyat <hraban@0brg.net>
//
// License for use of this code is detailed in the LICENSE file

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
	"sync"
)

// A function that generates a fresh entry on "cache miss". See the Cache.OnMiss
// method.
type OnMissHandler func(string) (Cacheable, error)

// Cache is a single object containing the full state of a cache.
//
// All access to this object through its public methods is gated through a
// single mutex. It is, therefore, safe for concurrent use, although it will not
// actually offer any parallel performance benefits.
type Cache struct {
	// Everything on this struct is accessed through the lock. I'm sure there is a
	// more efficient way of doing this, but it's a good start.
	lock sync.RWMutex
	// We could technically make this concurrently accessible through atomic
	// operations, but it seems hardly worth it.
	size    int64
	maxSize int64
	entries map[string]*cacheEntry
	// most recently used entry
	mostRU *cacheEntry
	// least recently used entry
	leastRU *cacheEntry
	// If not nil, invoked for every cache miss.
	onMiss OnMissHandler
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

type cacheEntry struct {
	payload Cacheable
	id      string
	// youngest older entry (age being usage) (DLL pointer)
	older *cacheEntry
	// oldest younger entry (age being usage) (DLL pointer)
	younger *cacheEntry
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
	if e.older == nil {
		c.leastRU = e.younger
	} else {
		e.older.younger = e.younger
	}
	if e.younger == nil {
		c.mostRU = e.older
	} else {
		e.younger.older = e.older
	}
	c.size -= getSize(e.payload)
	return
}

// purgeLRU removes the least recently used from the cache
func purgeLRU(c *Cache) {
	safeOnPurge(c.leastRU.payload, CACHEFULL)
	removeEntry(c, c.leastRU)
	return
}

// trimCache removes elements from the cache until its size <= max size
func trimCache(c *Cache) {
	if c.maxSize <= 0 {
		return
	}
	for c.size > c.maxSize {
		purgeLRU(c)
	}
	return
}

// directSet sets an entry in the cache without managing locks
func directSet(c *Cache, id string, payload Cacheable) {
	// Overwrite old entry
	if old, ok := c.entries[id]; ok {
		safeOnPurge(old.payload, KEYCOLLISION)
		removeEntry(c, old)
	}
	e := cacheEntry{payload: payload, id: id}
	c.entries[id] = &e
	size := getSize(payload)
	if size == 0 {
		return
	}
	if c.leastRU == nil { // aka "if this is the first entry..."
		// init DLL
		c.leastRU = &e
		c.mostRU = &e
		e.younger = nil
		e.older = nil
	} else {
		// e is younger than the old "most recently used"
		c.mostRU.younger = &e
		e.older = c.mostRU
		c.mostRU = &e
	}
	c.size += size
	trimCache(c)
	return
}

// handleCacheMiss calls the onMiss handler (if any) and stores the result
func handleCacheMiss(c *Cache, id string) (Cacheable, error) {
	var val Cacheable
	var err error = ErrNotFound
	c.lock.RLock()
	onmiss := c.onMiss
	c.lock.RUnlock()
	if onmiss != nil {
		val, err = onmiss(id)
		if err == nil {
			if val != nil {
				c.lock.Lock()
				defer c.lock.Unlock()
				directSet(c, id, val)
			} else {
				err = ErrNotFound
			}
		}
	}
	return val, err
}

func (c *Cache) Init(maxsize int64) {
	c.maxSize = maxsize
	c.entries = map[string]*cacheEntry{}
	return
}

// Set stores an item in cache. Panics if the cacheable is nil. It can, however, be
// an interface pointer to nil.
// TODO: write a test for the above.
func (c *Cache) Set(id string, p Cacheable) {
	if p == nil {
		panic("Cacheable value must not be nil")
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	directSet(c, id, p)
}

var ErrNotFound = errors.New("Key not found in cache")

// Get fetches an element from the cache.
//
// Updates the cache to mark this element as least recently used. If no element
// is found for this id, a registered onmiss handler will be called.
func (c *Cache) Get(id string) (Cacheable, error) {
	// A Get still modifies the cache in an LRU, so we need a write lock
	c.lock.Lock()
	// WARNING!! No deferred Unlock! Do not panic!
	e, ok := c.entries[id]
	if !ok {
		// We don't want to lock the entire cache while handling the cache miss
		c.lock.Unlock()
		return handleCacheMiss(c, id)
	}
	defer c.lock.Unlock()

	if e.younger == nil {
		// I'm already the fresh kid on the block
		return e.payload, nil
	}
	// Put element at the start of the LRU list
	if e.older != nil {
		e.older.younger = e.younger
	} else {
		// If nobody was older than me, my younger sibling is now the oldest.
		c.leastRU = e.younger
	}
	e.younger.older = e.older
	e.older = c.mostRU  // my elder is whoever used to be youngest
	c.mostRU = e        // I'm the newest one now
	e.younger = nil     // nobody's younger than me
	e.older.younger = e //

	return e.payload, nil
}

func (c *Cache) Delete(id string) {
	c.lock.Lock()
	defer c.lock.Unlock()

	e, ok := c.entries[id]
	if ok {
		safeOnPurge(e.payload, EXPLICITDELETE)
		if getSize(e.payload) != 0 {
			removeEntry(c, e)
		}
	}
	return
}

// OnMiss stores a callback for handling Gets to unknown keys.
//
// Say you're looking for entry "bob", but there is no such entry in your
// cache. Do you always handle that in the same way? Get "bob" from disk or S3?
// Then you could register an OnMiss handler which does this for you. Make this
// your "persistent storage lookup" function, hook it up to your cache right
// here and it will be called automatically next ime you're looking for bob. The
// advantage is that you can expect Get() calls to resolve.
//
// The Get() call invoking this OnMiss will always return whatever value is
// returned from the OnMiss handler, error or not.
//
// If the function returns a non-nil error, that error is directly returned
// from the Get() call that caused it to be invoked.  Otherwise, if the function
// return value is not nil, it is stored in cache.
//
// To remove a previously set OnMiss handler, call OnMiss(nil).
//
// Return (nil, nil) to indicate the specific key could not be found. It will
// be treated as a Get() to an unknown key without an OnMiss handler set.
//
// The synchronization lock which controls access to the entire cache is
// released before calling this function. The benefit is that a long running
// OnMiss handler call won't block the cache. The downside is that calling Get
// concurrently with the same key, before the first OnMiss call returns, will
// invoke another OnMiss call; the last one to return will have its value stored
// in the cache. To avoid this, wrap the OnMiss handler in a NoConcurrentDupes.
func (c *Cache) OnMiss(f OnMissHandler) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.onMiss = f
}

// MaxSize updates the maximum size of all cached elements.
//
// The size of the cache is the sum of calling .Size() on every individual
// element. If an element has no such method, the default is 1. Notably, the
// size has nothing to do with bytes in memory (unless the individual cached
// entries have a .Size method which returns their size, in bytes). If (roughly)
// all cached items are going to be (roughly) the same size it makes sense to
// set maxSize to the maximum number of elements you want to allow in cache. To
// remove the limit altogether set a maximum size of 0. No elements will be
// purged with reason CACHEFULL until the next call to MaxSize.
//
// Can be changed at any point during the cache's lifetime.
func (c *Cache) MaxSize(i int64) {
	c.lock.Lock()
	defer c.lock.Unlock()
	c.maxSize = i
	trimCache(c)
}

func (c *Cache) Size() int64 {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return c.size
}

// Close is an obsolete explicit closer method.
//
// Kept around for backwards compatibility, but not necessary anymore.
func (c *Cache) Close() error {
	return nil
}

// Create and initialize a new cache, ready for use.
func New(maxsize int64) *Cache {
	var mem Cache
	c := &mem
	c.Init(maxsize)
	// Go's SetFinalizer cannot be unit tested, so basically it's a joke.
	//runtime.SetFinalizer(c, finalizeCache)
	return c
}

// Shared cache for configuration-less use

var sharedCache = New(0)

// Get an element from the shared cache.
func Get(id string) (Cacheable, error) {
	return sharedCache.Get(id)
}

// Put an object in the shared cache (requires no configuration).
func Set(id string, c Cacheable) {
	sharedCache.Set(id, c)
	return
}

// Delete an item from the shared cache.
func Delete(id string) {
	sharedCache.Delete(id)
	return
}

// A shared cache is available immediately for all users of this library. By
// default, there is no size limit. Use this function to change that.
func MaxSize(size int64) {
	sharedCache.MaxSize(size)
	return
}
