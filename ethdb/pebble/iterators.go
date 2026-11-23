// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

//go:build (arm64 || amd64) && !openbsd

package pebble

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// iteratorReleaseTimeout is how long Close waits for live iterators to be
// released before leaving the close of pebble to the last of them.
var iteratorReleaseTimeout = 100 * time.Millisecond

// errCloseDeferred is returned by Close when live iterators outlast the wait,
// as pebble's own close would report them leaked.
var errCloseDeferred = errors.New("database close deferred until live iterators are released")

// liveIterators tracks a database's unreleased iterators. Pebble must not be
// closed while any of them is open, so Close stops them and waits for their
// owners to release them.
type liveIterators struct {
	stopped atomic.Bool // Set by Close, iterators report ErrClosed from then on

	lock     sync.Mutex
	count    int           // Number of unreleased iterators
	released chan struct{} // Closed when the count drops to zero after stop
	onLast   func()        // Close of pebble, left to the last release when stop gave up
}

func (l *liveIterators) add() {
	l.lock.Lock()
	l.count++
	l.lock.Unlock()
}

func (l *liveIterators) live() bool {
	l.lock.Lock()
	defer l.lock.Unlock()
	return l.count > 0
}

func (l *liveIterators) release() {
	l.lock.Lock()
	l.count--
	var onLast func()
	if l.count == 0 {
		if l.released != nil {
			close(l.released)
			l.released = nil
		}
		onLast, l.onLast = l.onLast, nil
	}
	l.lock.Unlock()
	if onLast != nil {
		onLast()
	}
}

// stop makes live iterators report ErrClosed and waits for them to be
// released. It returns how many are still live, in which case closePebble
// runs on the last release.
func (l *liveIterators) stop(closePebble func()) int {
	l.stopped.Store(true)
	l.lock.Lock()
	if l.count == 0 {
		l.lock.Unlock()
		return 0
	}
	released := make(chan struct{})
	l.released = released
	l.lock.Unlock()

	timer := time.NewTimer(iteratorReleaseTimeout)
	defer timer.Stop()
	select {
	case <-released:
		return 0
	case <-timer.C:
	}
	l.lock.Lock()
	defer l.lock.Unlock()
	if l.count > 0 {
		l.onLast = closePebble
	}
	return l.count
}

// errorIterator is an exhausted iterator that reports err, returned when an
// iterator cannot be created.
type errorIterator struct {
	err error
}

func (it *errorIterator) Next() bool    { return false }
func (it *errorIterator) Error() error  { return it.err }
func (it *errorIterator) Key() []byte   { return nil }
func (it *errorIterator) Value() []byte { return nil }
func (it *errorIterator) Release()      {}
