package vm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// The Firehose tracer records keccak preimages through OnKeccakPreimage. A
// KECCAK256 answered from a shared result cache (filled by the prefetcher)
// must still report its preimage, or the traced block loses it.
func TestKeccak256CacheHitCallsOnKeccakPreimage(t *testing.T) {
	for _, tt := range []struct {
		name string
		size int
		cfg  func() Config
	}{
		{"legacy 64-byte cache", 64, func() Config {
			return Config{Keccak256Cache: NewKeccak256Cache()}
		}},
		{"widened cache", 88, func() Config {
			return Config{EnablePrecompileCache: true, keccakCache: newKeccakStoreForTest()}
		}},
		{"no cache", 88, func() Config { return Config{} }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := make([]byte, tt.size)
			for i := range input {
				input[i] = byte(i + 1)
			}
			want := crypto.Keccak256Hash(input)

			var seen []common.Hash
			cfg := tt.cfg()
			cfg.Tracer = &tracing.Hooks{
				OnKeccakPreimage: func(hash common.Hash, preimage []byte) {
					if string(preimage) != string(input) {
						t.Fatalf("preimage = %x, want %x", preimage, input)
					}
					seen = append(seen, hash)
				},
			}
			statedb, _ := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
			evm := NewEVM(BlockContext{}, statedb, params.TestChainConfig, cfg)

			// First call fills the cache, second call is answered from it.
			for range 2 {
				if got := runKeccak256ForTest(t, evm, input); got != want {
					t.Fatalf("hash = %x, want %x", got, want)
				}
			}
			if len(seen) != 2 || seen[0] != want || seen[1] != want {
				t.Fatalf("OnKeccakPreimage hashes = %x, want [%x %x]", seen, want, want)
			}
		})
	}
}

func runKeccak256ForTest(t *testing.T, evm *EVM, input []byte) common.Hash {
	t.Helper()
	stack := newstack()
	mem := NewMemory()
	mem.Resize(uint64(len(input)))
	mem.Set(0, uint64(len(input)), input)
	stack.push(uint256.NewInt(uint64(len(input))))
	stack.push(uint256.NewInt(0))
	pc := uint64(0)
	if _, err := opKeccak256(&pc, evm, &ScopeContext{mem, stack, nil}); err != nil {
		t.Fatalf("opKeccak256: %v", err)
	}
	return common.Hash(stack.peek().Bytes32())
}
