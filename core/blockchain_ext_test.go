package core

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/ethash"
	"github.com/ethereum/go-ethereum/core/tracing"
)

func TestTracingBlockEndNotCalledOnPanic(t *testing.T) {
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
