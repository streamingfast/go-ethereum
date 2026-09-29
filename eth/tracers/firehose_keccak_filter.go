package tracers

import (
	"bytes"
	"encoding/hex"
	"slices"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
)

// keccakFilterMaxDepth is how many levels of nested hashing are followed from a storage
// key, e.g. `mapping(a => mapping(b => T))` is one level. Nesting that comes from types stayed
// within 5 levels on the Polygon, BSC, Base and Robinhood blocks sampled; hash chains (a key
// derived from the previous hash, as in linked lists) went past 10. Each preimage is visited at
// most once whatever the limit, so a high limit costs nothing.
const keccakFilterMaxDepth = 16

// recordedPreimage is a KECCAK256 preimage recorded during execution: the index of the call
// that computed it, the hash and the preimage bytes. The tracer keeps them as raw bytes until the
// transaction ends so that only the kept ones get hex-encoded.
type recordedPreimage struct {
	callIndex uint32
	hash      common.Hash
	preimage  []byte
}

// attachStorageSlotPreimages fills `Call.KeccakPreimages` with the recorded preimages that
// explain a storage slot, hex-encoding only those. `calls` are all the calls of one transaction
// or system call, and it must run once all of their storage changes are attached. A preimage is
// kept when:
//
//   - a storage change key is its hash, or its hash plus at most 2^64-1 (array elements and
//     struct fields live at `keccak(p) + i`; a random key lands that close to an unrelated
//     hash with probability about 2^-192 per pair), or
//   - its hash, or its hash plus such an offset, appears inside the preimage of a kept
//     entry (nested mappings, a mapping inside a struct or array element, and `string` or
//     `bytes` keys), following at most keccakFilterMaxDepth such levels.
//
// Everything else is dropped: hashes only used to read storage, signatures, CREATE2
// addresses and contract-level hashing. Those can make up tens of MB for a single
// transaction while never matching a storage change.
func attachStorageSlotPreimages(calls []*pbeth.Call, recorded []recordedPreimage) {
	if len(recorded) == 0 {
		return
	}

	kept := storageSlotHashes(calls, recorded)
	if len(kept) == 0 {
		return
	}

	byIndex := make(map[uint32]*pbeth.Call, len(calls))
	for _, call := range calls {
		byIndex[call.Index] = call
	}
	for _, r := range recorded {
		if _, ok := kept[r.hash]; !ok {
			continue
		}
		call := byIndex[r.callIndex]
		if call == nil {
			continue
		}
		if call.KeccakPreimages == nil {
			call.KeccakPreimages = make(map[string]string)
		}
		key := hex.EncodeToString(r.hash[:])
		if _, dup := call.KeccakPreimages[key]; !dup {
			call.KeccakPreimages[key] = hex.EncodeToString(r.preimage)
		}
	}
}

// storageSlotHashes returns the recorded hashes that explain one of the storage change keys
// of `calls`.
func storageSlotHashes(calls []*pbeth.Call, recorded []recordedPreimage) map[common.Hash]struct{} {
	preimages := make(map[common.Hash][]byte, len(recorded))
	for _, r := range recorded {
		if _, seen := preimages[r.hash]; !seen {
			preimages[r.hash] = r.preimage
		}
	}

	sorted := make([]common.Hash, 0, len(preimages))
	for h := range preimages {
		sorted = append(sorted, h)
	}
	slices.SortFunc(sorted, func(a, b common.Hash) int { return bytes.Compare(a[:], b[:]) })

	kept := map[common.Hash]struct{}{}
	var frontier []common.Hash
	keep := func(h common.Hash, into *[]common.Hash) {
		if _, dup := kept[h]; !dup {
			kept[h] = struct{}{}
			*into = append(*into, h)
		}
	}

	for _, call := range calls {
		for _, change := range call.StorageChanges {
			if len(change.Key) != 32 {
				continue
			}
			if base, ok := keccakSlotBase(sorted, common.Hash(change.Key)); ok {
				keep(base, &frontier)
			}
		}
	}

	for depth := 0; depth < keccakFilterMaxDepth && len(frontier) > 0; depth++ {
		var next []common.Hash
		for _, h := range frontier {
			for _, word := range innerKeccakHashCandidates(preimages[h]) {
				if inner, ok := keccakSlotBase(sorted, word); ok {
					keep(inner, &next)
				}
			}
		}
		frontier = next
	}

	return kept
}

// keccakSlotBase returns the largest hash at or below `key` when `key` is less than 2^64
// above it, which covers an exact match.
func keccakSlotBase(sorted []common.Hash, key common.Hash) (common.Hash, bool) {
	i := sort.Search(len(sorted), func(i int) bool { return bytes.Compare(sorted[i][:], key[:]) > 0 })
	if i == 0 {
		return common.Hash{}, false
	}
	base := sorted[i-1]
	distance := new(uint256.Int).Sub(new(uint256.Int).SetBytes32(key[:]), new(uint256.Int).SetBytes32(base[:]))
	return base, distance.IsUint64()
}

// innerKeccakHashCandidates returns the places an inner hash sits in a preimage: each
// 32-byte word (value-type mapping keys, Vyper's slot-first layout) and the last 32 bytes
// (Solidity puts the slot after a `string` or `bytes` key).
func innerKeccakHashCandidates(preimage []byte) []common.Hash {
	candidates := make([]common.Hash, 0, len(preimage)/32+1)
	for off := 0; off+32 <= len(preimage); off += 32 {
		candidates = append(candidates, common.Hash(preimage[off:off+32]))
	}
	if len(preimage) > 32 && len(preimage)%32 != 0 {
		candidates = append(candidates, common.Hash(preimage[len(preimage)-32:]))
	}
	return candidates
}
