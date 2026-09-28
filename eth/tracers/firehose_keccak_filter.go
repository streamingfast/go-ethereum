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
// key, e.g. `mapping(a => mapping(b => T))` is one level.
const keccakFilterMaxDepth = 16

// retainStorageSlotPreimages reduces `Call.KeccakPreimages` to the preimages that explain a
// storage slot. `calls` are all the calls of one transaction or system call, and it must
// run once all of their storage changes are attached. A preimage is kept when:
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
func retainStorageSlotPreimages(calls []*pbeth.Call) {
	preimages := map[common.Hash][]byte{}
	for _, call := range calls {
		for hash, preimage := range call.KeccakPreimages {
			h, ok := decodeKeccakHash(hash)
			if !ok {
				continue
			}
			if _, seen := preimages[h]; seen {
				continue
			}
			if data, err := hex.DecodeString(preimage); err == nil {
				preimages[h] = data
			}
		}
	}
	if len(preimages) == 0 {
		return
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

	for _, call := range calls {
		for hash := range call.KeccakPreimages {
			h, ok := decodeKeccakHash(hash)
			if _, isKept := kept[h]; !ok || !isKept {
				delete(call.KeccakPreimages, hash)
			}
		}
		if len(call.KeccakPreimages) == 0 {
			call.KeccakPreimages = nil
		}
	}
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

func decodeKeccakHash(hash string) (common.Hash, bool) {
	var out common.Hash
	if len(hash) != 64 {
		return out, false
	}
	if _, err := hex.Decode(out[:], []byte(hash)); err != nil {
		return out, false
	}
	return out, true
}
