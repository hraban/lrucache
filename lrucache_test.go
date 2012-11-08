package lrucache

import (
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
