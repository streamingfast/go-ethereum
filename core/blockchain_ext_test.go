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
	blockEndErr := error(nil)
	hooks := &tracing.Hooks{
		// This simulates a panic in the tracer
		OnBalanceChange: func(addr common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
			panic("panic")
		},
		OnBlockEnd: func(err error) {
			blockEndCalled = true
			blockEndErr = err
		},
	}

	blockchain.logger = hooks
	blockchain.vmConfig.Tracer = hooks

	blocks := makeBlockChain(blockchain.chainConfig, blockchain.GetBlockByHash(blockchain.CurrentBlock().Hash()), 1, ethash.NewFullFaker(), genDb, 0)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("Expected panic, but got none, ensure that OnBalanceChange hook is called correctly to generate the panic")
			}
		}()

		if _, err := blockchain.InsertChain(blocks); err != nil {
			t.Fatalf("Failed to insert block: %v", err)
		}
	}()

	if !blockEndCalled {
		t.Fatalf("Expected block end to be called on panic, but it was not")
	}

	if blockEndErr == nil {
		t.Fatalf("Expected block end to be called with err, but it was not")
	}

	if blockEndErr.Error() != "panic during block processing: panic" {
		t.Fatalf("Expected block end to be called with recovered error on panic, but got error: %v", blockEndErr)
	}
}
