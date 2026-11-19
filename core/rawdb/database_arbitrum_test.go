//go:build (arm64 || amd64) && !openbsd

package rawdb

import (
	"encoding/binary"
	"testing"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/stretchr/testify/require"
)

// TestSafeDeleteRangeCloseMidScan closes the database while SafeDeleteRange
// is iterating it in hash scheme mode, as the filtermaps cleanup can when a
// node shuts down during startup. The scan must end with an error, not crash.
func TestSafeDeleteRangeCloseMidScan(t *testing.T) {
	dir := t.TempDir()
	db, err := pebble.New(dir, 16, 16, "", false, nil)
	require.NoError(t, err)
	batch := db.NewBatch()
	for i := uint64(0); i < 50_000; i++ {
		key := binary.BigEndian.AppendUint64([]byte{0x01}, i)
		require.NoError(t, batch.Put(key, key))
		if batch.ValueSize() >= ethdb.IdealBatchSize {
			require.NoError(t, batch.Write())
			batch.Reset()
		}
	}
	require.NoError(t, batch.Write())

	var (
		scanning = make(chan struct{})
		closed   = make(chan struct{})
		result   = make(chan error)
	)
	go func() {
		first := true
		result <- SafeDeleteRange(db, []byte{0x01}, []byte{0x02}, true, func(bool) bool {
			// Hold the scan, with its iterator live, until the database is closed.
			if first {
				first = false
				close(scanning)
				<-closed
			}
			return false
		})
	}()
	<-scanning
	// The live iterator outlasts Close's wait, so the close is deferred to its release.
	require.Error(t, db.Close())
	close(closed)
	require.Error(t, <-result)

	// SafeDeleteRange released the iterator, so pebble is closed and can be reopened.
	db, err = pebble.New(dir, 16, 16, "", false, nil)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}
