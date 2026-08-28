package core

import (
	"context"
	"math/big"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
)

func TestTracingBlockEndNotCalledOnPanic(t *testing.T) {
	// Bor runs the state processors in their own goroutines (see
	// BlockChain.ProcessBlock), so a panic raised by a tracer hook, like
	// firehose, takes the whole process down instead of unwinding through
	// InsertChain, and the recover below can never observe it. Re-enabling this
	// needs ProcessBlock to catch the panic and re-raise it on the caller's
	// goroutine.
	t.Skip("tracer panics cannot reach InsertChain: ProcessBlock runs processors in goroutines")

	genDb, _, blockchain, err := newCanonical(ethash.NewFaker(), 0, true, "path")
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	blockEndCalled := false
	hooks := &tracing.Hooks{
		// This simulates a panic in the tracer
		OnBalanceChange: func(addr common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
			panic("panic")
		},
		OnBlockEnd: func(err error) {
			blockEndCalled = true
		},
	}

	blockchain.logger = hooks
	blockchain.GetVMConfig().Tracer = hooks

	blocks := makeBlockChain(blockchain.chainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), 1, ethash.NewFullFaker(), genDb, 0)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("Expected panic, but got none, ensure that OnBalanceChange hook is called correctly to generate the panic")
			}
		}()

		if _, err := blockchain.InsertChain(blocks, false); err != nil {
			t.Fatalf("Failed to insert block: %v", err)
		}
	}()

	if blockEndCalled {
		t.Fatalf("OnBlockEnd should not be called on panic within the tracer")
	}
}

// recordingProcessor delegates to inner and remembers whether it ran.
type recordingProcessor struct {
	inner Processor
	ran   atomic.Bool
}

func (p *recordingProcessor) Process(block *types.Block, statedb *state.StateDB, cfg vm.Config, author *common.Address, interruptCtx context.Context) (*ProcessResult, error) {
	p.ran.Store(true)
	return p.inner.Process(block, statedb, cfg, author, interruptCtx)
}

// TestParallelProcessorSkippedWhenTracerActive pins that BlockSTM never runs
// while a live tracer is installed. Its workers execute transactions
// concurrently and re-execute them on validation failure, so tracer hooks
// would fire from several goroutines, for discarded incarnations, and out of
// transaction order — none of which the hooks support.
func TestParallelProcessorSkippedWhenTracerActive(t *testing.T) {
	genDb, _, blockchain, err := newCanonical(ethash.NewFaker(), 0, true, "path")
	if err != nil {
		t.Fatalf("failed to create pristine chain: %v", err)
	}
	defer blockchain.Stop()

	parallel := &recordingProcessor{inner: blockchain.processor}
	blockchain.parallelProcessor = parallel

	insertNext := func() {
		t.Helper()
		parent := blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash())
		blocks := makeBlockChain(blockchain.chainConfig, parent, 1, ethash.NewFullFaker(), genDb, 0)
		if _, err := blockchain.InsertChain(blocks, false); err != nil {
			t.Fatalf("failed to insert block: %v", err)
		}
	}

	insertNext()
	if !parallel.ran.Load() {
		t.Fatal("parallel processor did not run with no tracer installed")
	}

	parallel.ran.Store(false)
	blockchain.GetVMConfig().Tracer = &tracing.Hooks{}

	insertNext()
	if parallel.ran.Load() {
		t.Fatal("parallel processor ran while a tracer was installed")
	}
}
