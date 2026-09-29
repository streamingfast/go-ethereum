package tracers

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"github.com/stretchr/testify/assert"
)

// recordKeccak hashes preimage, records it on call and returns the hash.
func recordKeccak(call *pbeth.Call, preimage []byte) common.Hash {
	hash := crypto.Keccak256Hash(preimage)
	if call.KeccakPreimages == nil {
		call.KeccakPreimages = map[string]string{}
	}
	call.KeccakPreimages[hex.EncodeToString(hash[:])] = hex.EncodeToString(preimage)
	return hash
}

func storeKey(call *pbeth.Call, key common.Hash) {
	call.StorageChanges = append(call.StorageChanges, &pbeth.StorageChange{
		Address:  common.HexToAddress("0xaa").Bytes(),
		Key:      key.Bytes(),
		OldValue: common.Hash{}.Bytes(),
		NewValue: common.BytesToHash([]byte{1}).Bytes(),
	})
}

func word(v uint64) []byte {
	return common.BigToHash(new(uint256.Int).SetUint64(v).ToBig()).Bytes()
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func addToHash(h common.Hash, offset *uint256.Int) common.Hash {
	return common.Hash(new(uint256.Int).Add(new(uint256.Int).SetBytes32(h[:]), offset).Bytes32())
}

func keptKeccaks(calls []*pbeth.Call) []string {
	var out []string
	for _, call := range calls {
		for hash := range call.KeccakPreimages {
			out = append(out, hash)
		}
	}
	return out
}

func hexHashes(hashes ...common.Hash) []string {
	out := make([]string, len(hashes))
	for i, h := range hashes {
		out[i] = hex.EncodeToString(h[:])
	}
	return out
}

func TestRetainStorageSlotPreimages_KeepsMappingSlotAndDropsUnrelated(t *testing.T) {
	call := &pbeth.Call{}
	slot := recordKeccak(call, concat(word(1), word(0)))
	recordKeccak(call, concat(word(9), word(9)))
	storeKey(call, slot)

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	assert.ElementsMatch(t, hexHashes(slot), keptKeccaks(calls))
}

func TestRetainStorageSlotPreimages_KeepsArrayElementAndStructFieldSlots(t *testing.T) {
	call := &pbeth.Call{}
	base := recordKeccak(call, word(3))
	storeKey(call, addToHash(base, uint256.NewInt(7)))

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	assert.ElementsMatch(t, hexHashes(base), keptKeccaks(calls))
}

func TestRetainStorageSlotPreimages_DropsHashTooFarBelowStorageKey(t *testing.T) {
	call := &pbeth.Call{}
	base := recordKeccak(call, word(3))
	storeKey(call, addToHash(base, new(uint256.Int).Lsh(uint256.NewInt(1), 64)))

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	assert.Empty(t, keptKeccaks(calls))
	assert.Nil(t, call.KeccakPreimages)
}

func TestRetainStorageSlotPreimages_FollowsNestedMappingsAndStringKeys(t *testing.T) {
	call := &pbeth.Call{}
	// mapping(uint => mapping(uint => T)) at slot 2: keccak(k2 . keccak(k1 . 2))
	inner := recordKeccak(call, concat(word(1), word(2)))
	outer := recordKeccak(call, concat(word(5), inner[:]))
	// mapping(string => T) at slot 4 nested under a mapping: keccak("abc" . keccak(k . 4))
	stringParent := recordKeccak(call, concat(word(8), word(4)))
	stringSlot := recordKeccak(call, concat([]byte("abc"), stringParent[:]))
	storeKey(call, outer)
	storeKey(call, stringSlot)

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	assert.ElementsMatch(t, hexHashes(inner, outer, stringParent, stringSlot), keptKeccaks(calls))
}

func TestRetainStorageSlotPreimages_FollowsMappingInsideStructInMapping(t *testing.T) {
	// struct Pool { uint total; mapping(address => uint) shares; }
	// mapping(uint => Pool) pools at slot 3: pools[id].shares[user] is at
	// keccak(user . (keccak(id . 3) + 1)).
	call := &pbeth.Call{}
	pool := recordKeccak(call, concat(word(7), word(3)))
	sharesSlot := addToHash(pool, uint256.NewInt(1))
	share := recordKeccak(call, concat(word(0xee), sharesSlot[:]))
	storeKey(call, share)

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	assert.ElementsMatch(t, hexHashes(pool, share), keptKeccaks(calls))
}

func TestRetainStorageSlotPreimages_KeepsPreimageRecordedInAnotherCall(t *testing.T) {
	hashing, writing := &pbeth.Call{}, &pbeth.Call{}
	slot := recordKeccak(hashing, concat(word(1), word(0)))
	storeKey(writing, slot)

	calls := []*pbeth.Call{hashing, writing}
	retainStorageSlotPreimages(calls)

	assert.ElementsMatch(t, hexHashes(slot), keptKeccaks(calls))
}

func TestRetainStorageSlotPreimages_StopsAfterMaxDepth(t *testing.T) {
	call := &pbeth.Call{}
	chain := []common.Hash{recordKeccak(call, concat(word(1), word(0)))}
	for i := 0; i < keccakFilterMaxDepth+1; i++ {
		parent := chain[len(chain)-1]
		chain = append(chain, recordKeccak(call, concat(word(uint64(i+10)), parent[:])))
	}
	storeKey(call, chain[len(chain)-1])

	calls := []*pbeth.Call{call}
	retainStorageSlotPreimages(calls)

	// The storage key's own entry is depth 0, then keccakFilterMaxDepth levels below it.
	assert.ElementsMatch(t, hexHashes(chain[1:]...), keptKeccaks(calls))
}
