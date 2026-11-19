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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pebblev1 "github.com/cockroachdb/pebble"
	vfsv1 "github.com/cockroachdb/pebble/vfs"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethdb"
)

type testDB struct {
	ethdb.KeyValueStore
	iterators    *liveIterators
	errClosed    error       // the pebble version's ErrClosed
	pebbleClosed func() bool // whether the underlying pebble database is closed
}

// openWithTables opens an in-memory database of the given pebble version
// holding n keys flushed to table files, so iterators hold table readers.
func openWithTables(t *testing.T, version int, n int) *testDB {
	t.Helper()
	var (
		db    testDB
		flush func() error
	)
	switch version {
	case 1:
		pdb, err := pebblev1.Open("", &pebblev1.Options{FS: vfsv1.NewMem()})
		if err != nil {
			t.Fatal(err)
		}
		wrapped := &Database{db: pdb}
		db.KeyValueStore, db.iterators, db.errClosed, flush = wrapped, &wrapped.iterators, pebblev1.ErrClosed, pdb.Flush
		db.pebbleClosed = func() (closed bool) {
			// Pebble panics on any use after Close.
			defer func() { closed = recover() != nil }()
			pdb.Get(nil)
			return
		}
	default:
		t.Fatalf("unknown pebble version %d", version)
	}
	batch := db.NewBatch()
	for i := 0; i < n; i++ {
		key := binary.BigEndian.AppendUint64(nil, uint64(i))
		if err := batch.Put(key, bytes.Repeat(key, 4)); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Write(); err != nil {
		t.Fatal(err)
	}
	if err := flush(); err != nil {
		t.Fatal(err)
	}
	return &db
}

func setReleaseTimeout(t *testing.T, timeout time.Duration) {
	old := iteratorReleaseTimeout
	iteratorReleaseTimeout = timeout
	t.Cleanup(func() { iteratorReleaseTimeout = old })
}

func TestIteratorOutlivingClose(t *testing.T) {
	setReleaseTimeout(t, 10*time.Millisecond)
	for _, version := range []int{1} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openWithTables(t, version, 100)
			it := db.NewIterator(nil, nil)
			if !it.Next() {
				t.Fatalf("Next on a populated database returned false, err: %v", it.Error())
			}
			key, value := common.CopyBytes(it.Key()), common.CopyBytes(it.Value())
			if err := db.Close(); !errors.Is(err, errCloseDeferred) {
				t.Fatalf("Close with a live iterator: got %v, want %v", err, errCloseDeferred)
			}
			if db.pebbleClosed() {
				t.Fatal("pebble closed under a live iterator")
			}

			// A caller between Next and Key or Value still sees the entry Next
			// moved to.
			if !bytes.Equal(it.Key(), key) || !bytes.Equal(it.Value(), value) {
				t.Fatalf("entry after Close: got %x=%x, want %x=%x", it.Key(), it.Value(), key, value)
			}
			if it.Next() {
				t.Fatal("Next after Close returned true")
			}
			if err := it.Error(); !errors.Is(err, db.errClosed) {
				t.Fatalf("Error after Close: got %v, want %v", err, db.errClosed)
			}
			if it.Key() != nil || it.Value() != nil {
				t.Fatalf("stopped iterator returned %x=%x, want nil", it.Key(), it.Value())
			}

			late := db.NewIterator(nil, nil)
			if late.Next() {
				t.Fatal("Next on an iterator created after Close returned true")
			}
			if err := late.Error(); !errors.Is(err, db.errClosed) {
				t.Fatalf("Error on an iterator created after Close: got %v, want %v", err, db.errClosed)
			}
			late.Release()

			it.Release()
			if !db.pebbleClosed() {
				t.Fatal("pebble still open after the last iterator was released")
			}
			it.Release()
		})
	}
}

func TestCloseWaitsForRelease(t *testing.T) {
	// Long enough that only the release can end Close's wait in time.
	setReleaseTimeout(t, time.Minute)
	for _, version := range []int{1} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openWithTables(t, version, 100)
			it := db.NewIterator(nil, nil)
			if !it.Next() {
				t.Fatalf("Next on a populated database returned false, err: %v", it.Error())
			}
			closed := make(chan error)
			go func() { closed <- db.Close() }()
			for !db.iterators.stopped.Load() {
				runtime.Gosched()
			}
			if it.Next() {
				t.Fatal("Next during Close returned true")
			}
			if err := it.Error(); !errors.Is(err, db.errClosed) {
				t.Fatalf("Error during Close: got %v, want %v", err, db.errClosed)
			}
			it.Release()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatalf("Close: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Close still waiting after the last iterator was released")
			}
			if !db.pebbleClosed() {
				t.Fatal("pebble still open after Close returned")
			}
		})
	}
}

func TestIteratorExhaustedBeforeClose(t *testing.T) {
	setReleaseTimeout(t, 10*time.Millisecond)
	for _, version := range []int{1} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openWithTables(t, version, 10)
			it := db.NewIterator(nil, nil)
			for it.Next() {
			}
			db.Close()

			// The scan finished before Close, so it is not reported as cut short.
			if err := it.Error(); err != nil {
				t.Fatalf("Error after a complete scan: %v", err)
			}
			it.Release()
			if !db.pebbleClosed() {
				t.Fatal("pebble still open after the last iterator was released")
			}
		})
	}
}

func TestCloseWithoutLiveIterators(t *testing.T) {
	for _, version := range []int{1} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openWithTables(t, version, 10)
			it := db.NewIterator(nil, nil)
			it.Next()
			it.Release()
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if !db.pebbleClosed() {
				t.Fatal("pebble still open after Close without live iterators")
			}
			late := db.NewIterator(nil, nil)
			if late.Next() || !errors.Is(late.Error(), db.errClosed) {
				t.Fatalf("iterator created after Close: Next true or Error %v, want %v", late.Error(), db.errClosed)
			}
			late.Release()
			if err := db.SyncKeyValue(); !errors.Is(err, db.errClosed) {
				t.Fatalf("SyncKeyValue after Close: got %v, want %v", err, db.errClosed)
			}
			if err := db.Compact(nil, nil); !errors.Is(err, db.errClosed) {
				t.Fatalf("Compact after Close: got %v, want %v", err, db.errClosed)
			}
			if _, err := db.Stat(); !errors.Is(err, db.errClosed) {
				t.Fatalf("Stat after Close: got %v, want %v", err, db.errClosed)
			}
		})
	}
}

// TestIteratorCloseDuringScan closes the database while scanners are iterating
// and releasing iterators. Run with -race to check the synchronization.
func TestIteratorCloseDuringScan(t *testing.T) {
	for _, version := range []int{1} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			db := openWithTables(t, version, 10_000)

			const scanners = 8
			var (
				scanning sync.WaitGroup
				done     sync.WaitGroup
				errs     = make(chan error, scanners)
			)
			scanning.Add(scanners)
			done.Add(scanners)
			for i := 0; i < scanners; i++ {
				go func() {
					defer done.Done()
					first := true
					// Rescan until a scan ends with an error, which can
					// only come from Close.
					for {
						it := db.NewIterator(nil, nil)
						for it.Next() {
							if first {
								scanning.Done()
								first = false
							}
							if len(it.Key()) != 8 || len(it.Value()) != 32 {
								errs <- errors.New("unexpected entry")
								it.Release()
								return
							}
						}
						err := it.Error()
						it.Release()
						if err != nil {
							errs <- err
							return
						}
					}
				}()
			}
			scanning.Wait()
			if err := db.Close(); err != nil && !errors.Is(err, errCloseDeferred) {
				t.Fatalf("Close: %v", err)
			}
			done.Wait()
			close(errs)
			for err := range errs {
				if !errors.Is(err, db.errClosed) {
					t.Fatalf("scan ended with %v, want %v", err, db.errClosed)
				}
			}
			if !db.pebbleClosed() {
				t.Fatal("pebble still open after every iterator was released")
			}
		})
	}
}

// TestLiveIteratorsCloseOnce races the last release against stop giving up on
// it, and checks that exactly one of them closes pebble.
func TestLiveIteratorsCloseOnce(t *testing.T) {
	setReleaseTimeout(t, 0)
	for i := 0; i < 2000; i++ {
		iteratorReleaseTimeout = time.Duration(i%50) * time.Microsecond
		var (
			l        liveIterators
			closes   atomic.Int32
			released = make(chan struct{})
		)
		l.add()
		go func() {
			defer close(released)
			time.Sleep(time.Duration(i%37) * time.Microsecond)
			l.release()
		}()
		if l.stop(func() { closes.Add(1) }) == 0 {
			closes.Add(1)
		}
		<-released
		if n := closes.Load(); n != 1 {
			t.Fatalf("round %d: pebble closed %d times, want once", i, n)
		}
	}
}
