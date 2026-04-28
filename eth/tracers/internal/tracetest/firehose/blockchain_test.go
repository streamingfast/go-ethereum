package firehose_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/tests"
	"github.com/ethereum/go-ethereum/trie"
	"github.com/stretchr/testify/require"
)

func runPrestateBlock(t *testing.T, prestatePath string, hooks *tracing.Hooks) {
	t.Helper()

	prestate := readPrestateData(t, prestatePath)
	context := prestate.Context.toBlockContext(prestate.Genesis)

	testState := tests.MakePreState(rawdb.NewMemoryDatabase(), prestate.Genesis.Alloc, false, rawdb.HashScheme)
	defer testState.Close()

	tx := new(types.Transaction)
	require.NoError(t, rlp.DecodeBytes(common.FromHex(prestate.Input), tx))

	zero := uint64(0)
	rules := prestate.Genesis.Config.Rules(context.BlockNumber, context.Difficulty.Sign() == 0, context.Time)

	header := &types.Header{
		ParentHash: prestate.Genesis.ToBlock().Hash(),
		Number:     context.BlockNumber,
		Difficulty: context.Difficulty,
		Coinbase:   context.Coinbase,
		Time:       context.Time,
		GasLimit:   context.GasLimit,
		BaseFee:    context.BaseFee,
	}
	// Reth rejects nil fields that real blocks carry; set them only when the fork is active.
	if rules.IsCancun {
		header.ParentBeaconRoot = ptr(common.Hash{})
		header.BlobGasUsed = &zero
		header.ExcessBlobGas = &zero
	}

	var withdrawals []*types.Withdrawal
	if rules.IsShanghai {
		withdrawals = []*types.Withdrawal{}
	}

	block := types.NewBlock(header, &types.Body{
		Transactions: []*types.Transaction{tx},
		Withdrawals:  withdrawals,
	}, nil, trie.NewStackTrie(nil))

	if hooks.OnBlockchainInit != nil {
		hooks.OnBlockchainInit(prestate.Genesis.Config)
	}

	if hooks.OnBlockStart != nil {
		hooks.OnBlockStart(tracing.BlockEvent{
			Block: block,
		})
	}

	processor := core.NewStateProcessor(prestate)
	_, err := processor.Process(t.Context(), block, testState.StateDB, vm.Config{Tracer: hooks})
	require.NoError(t, err)

	if hooks.OnBlockEnd != nil {
		hooks.OnBlockEnd(nil)
	}
}

var _ core.Validator = (*ignoreValidateStateValidator)(nil)

type ignoreValidateStateValidator struct {
	core.Validator
}

func (v ignoreValidateStateValidator) ValidateBody(block *types.Block) error {
	return v.Validator.ValidateBody(block)
}

func (v ignoreValidateStateValidator) ValidateState(block *types.Block, state *state.StateDB, res *core.ProcessResult, stateless bool) error {
	return nil
}
