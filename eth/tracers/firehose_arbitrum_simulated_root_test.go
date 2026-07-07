package tracers

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/params"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression guard for the arbitrum-simulated transaction types (deposit / submit-retryable /
// internal) at ArbOS > 40.
//
// For those types the Firehose tracer opens the transaction's root call itself in OnTxStart (see
// firehose.go, the `callStart("root", ...)` in the arbitrum switch). Nitro's TxProcessor then
// records the actual ArbOS execution as a *nested* frame by firing an OnEnter/OnExit pair at
// depth 0 while that simulated root is still active — `startTracer` for deposit/internal/submit,
// and the auto-redeem `MockCall` for submit-retryable. The firehose output is therefore TWO calls:
// the empty simulated root plus the nested execution frame carrying the real gas/state.
//
// The reverted go-ethereum tag arbitrum-v3.11.2-fh added a guard that ignored ANY depth-0
// OnCallEnter while a root was already active. It could not distinguish this legitimate nested
// frame from nitro's redundant `emitSkippedCallFrame` re-entry, so it dropped the frame: the
// two-call trace collapsed into one and the real gas/state merged up into the empty root. This
// shipped undetected because no test pinned the two-call shape. This test does.
//
// It reproduces the shape at the tracer-hook boundary (the invariant firehose must uphold),
// independent of which nitro tx type or ArbOS version triggers it.
func TestFirehose_ArbitrumSimulatedRoot_KeepsNestedExecutionFrame(t *testing.T) {
	f := NewFirehose(&FirehoseConfig{
		private: &privateFirehoseConfig{FlushToTestBuffer: true},
	})

	f.OnBlockchainInit(params.TestChainConfig)
	f.OnBlockStart(blockEvent(100))

	// A submit-retryable is one of the three types for which OnTxStart opens a simulated root.
	tx := types.NewTx(&types.ArbitrumSubmitRetryableTx{
		ChainId:          big.NewInt(1),
		RequestId:        common.Hash{},
		From:             from,
		L1BaseFee:        big.NewInt(0),
		DepositValue:     big.NewInt(0),
		GasFeeCap:        big.NewInt(0),
		Gas:              0,
		RetryTo:          &to,
		RetryValue:       big.NewInt(0),
		Beneficiary:      to,
		MaxSubmissionFee: big.NewInt(0),
		FeeRefundAddr:    to,
		RetryData:        []byte{0x6b, 0xf6, 0xa4, 0x2d},
	})
	f.OnTxStart(&tracing.VMContext{}, tx, from)

	// Nitro records the real execution as a nested frame: a depth-0 OnEnter/OnExit pair fired
	// while the simulated root is still open (here a successful frame consuming gas, mirroring the
	// auto-redeem MockCall / startTracer close). It MUST be recorded as a separate nested call.
	f.OnCallEnter(0, byte(vm.CALL), from, to, tx.Data(), 163200, big.NewInt(0))
	f.OnCallExit(0, nil, 163200, nil, false)

	receipt := &types.Receipt{
		Type:             types.ArbitrumSubmitRetryableTxType,
		Status:           types.ReceiptStatusSuccessful,
		GasUsed:          163200,
		TransactionIndex: 0,
	}
	require.NotPanics(t, func() { f.OnTxEnd(receipt, nil) })

	require.Len(t, f.block.TransactionTraces, 1)
	trace := f.block.TransactionTraces[0]

	// The core invariant: simulated root + nested execution frame = two calls, never collapsed.
	require.Len(t, trace.Calls, 2, "simulated arbitrum root must keep its nested execution frame (a collapse to one call is the regression)")

	root := trace.Calls[0]
	nested := trace.Calls[1]
	assert.Equal(t, uint32(0), root.Depth, "root call must be at depth 0")
	assert.Equal(t, uint32(1), nested.Depth, "execution frame must be nested one level under the root")
	assert.Equal(t, root.Index, nested.ParentIndex, "the nested frame's parent must be the simulated root")
	assert.Equal(t, uint64(163200), nested.GasConsumed, "the real gas must stay on the nested frame, not merge into the empty root")

	require.NotPanics(t, func() { f.OnBlockEnd(nil) })
}
