## LRUCACHE

or maybe it should be called...

THREAD-SAFE DICTIONARY!!

orr....

WHO NEEDS MEMCACHED??

whatever you call it, this is what it is:

## Features

Lrucache is a dictionary with:

* O(1) get
* O(1) set
* O(1) delete
* safe for concurrent goroutines
* configurable maximum size
* purges least recently used element when full
* elements can report their own size (if they wanna)
* everything is cacheable (`interface{}`)
* it's a front for your persistent storage by using OnMiss hooks

I have to pee (no seriously it's making hard to concentrate but I'm sitting in
a hostel here and I don't wanna have to log out and back in again it's a lot of
work so I'm just trying to burn through this README but tbh this paragraph is
not helping) so I'll leave it here check below for more info

API on godoc: <http://go.pkgdoc.org/github.com/hraban/lrucache>

The licensing terms are described in the file LICENSE.

That O(1) thing is a lie, by the way. It's only as O(1) as Go's dictionary
implementation. Point is just that it's not worse than you'd expect, like I'd
suddenly be using a singly linked list for lookup or something. Pff haha.
