package lrucache

import (
	"strconv"
	"testing"
	"time"
)

type varsize int

func (i varsize) Size() int64 {
	return int64(i)
}

type purgeable struct {
	purged bool
	how    bool
}

func (x *purgeable) Size() int64 {
	return 1
}

func (x *purgeable) OnPurge(d bool) {
	x.purged = true
	x.how = d
}

func Test_OnPurge_1(t *testing.T) {
	c := New(1)
	var x, y purgeable
	c.Set("x", &x)
	c.Set("y", &y)
	// Wait for changes to propagate
	time.Sleep(1 * time.Millisecond)
	if !x.purged {
		t.Error("Element was not purged from full cache")
	}
	if x.how {
		t.Error("Element should have been purged but was deleted")
	}
}

func Test_OnPurge_2(t *testing.T) {
	c := New(1)
	var x purgeable
	c.Set("x", &x)
	c.Delete("x")
	time.Sleep(1 * time.Millisecond)
	if !x.purged {
		t.Error("Element was not deleted from cache")
	}
	if !x.how {
		t.Error("Element should have been deleted but was purged")
	}
}

// Just test filling a cache with a type that does not implement NotifyPurge
func Test_safeOnPurge(t *testing.T) {
	c := New(1)
	i := varsize(1)
	j := varsize(1)
	c.Set("i", i)
	c.Set("j", j)
}

func Test_Size(t *testing.T) {
	c := New(100)
	// sum(0..14) = 105
	for i := 1; i < 15; i++ {
		c.Set(strconv.Itoa(i), varsize(i))
	}
	time.Sleep(1 * time.Millisecond)
	// At this point, expect {0, 1, 2, 3} to be purged
	if c.Size() != 99 {
		t.Errorf("Unexpected size: %d", c.Size())
	}
	for i := 0; i < 4; i++ {
		if _, ok := c.Get(strconv.Itoa(i)); ok {
			t.Errorf("Expected %d to be purged", i)
		}
	}
	for i := 4; i < 15; i++ {
		if _, ok := c.Get(strconv.Itoa(i)); !ok {
			t.Errorf("Expected %d to be cached", i)
		}
	}
}
