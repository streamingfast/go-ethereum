package tracers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	firehose "github.com/streamingfast/evm-firehose-tracer-go/v5"
)

func init() {
	staticFirehoseChainValidationOnInit()

	LiveDirectory.Register("firehose", newFirehoseTracer)
}

func newFirehoseTracer(cfg json.RawMessage) (*tracing.Hooks, error) {
	firehoseTracer, err := NewFirehoseFromRawJSON(cfg)
	if err != nil {
		return nil, err
	}

	return firehoseTracer.TracingHooks(), nil
}

func NewFirehoseFromRawJSON(cfg json.RawMessage) (*Firehose, error) {
	var config FirehoseConfig
	if len([]byte(cfg)) > 0 {
		if err := json.Unmarshal(cfg, &config); err != nil {
			return nil, fmt.Errorf("failed to parse Firehose config: %w", err)
		}

		// Special handling of some "private" fields
		type privateConfigRoot struct {
			Private *privateFirehoseConfig `json:"_private"`
		}

		var privateConfig privateConfigRoot
		if err := json.Unmarshal(cfg, &privateConfig); err != nil {
			log.Error("Firehose failed to parse private config, ignoring", "error", err)
		} else {
			config.private = privateConfig.Private
		}
	}

	return NewFirehose(&config), nil
}

type FirehoseConfig struct {
	ConcurrentBlockFlushing int `json:"concurrentBlockFlushing"`

	// Only used for testing, only possible through JSON configuration
	private *privateFirehoseConfig
}

type privateFirehoseConfig struct {
	FlushToTestBuffer  bool `json:"flushToTestBuffer"`
	IgnoreGenesisBlock bool `json:"ignoreGenesisBlock"`
}

// LogKeyValues returns a list of key-values to be logged when the config is printed.
func (c *FirehoseConfig) LogKeyValues() []any {
	return []any{
		"config.concurrentBlockFlushing", c.ConcurrentBlockFlushing,
	}
}

type Firehose struct {
	*firehose.Tracer

	config      *FirehoseConfig
	hooks       *tracing.Hooks
	chainConfig *params.ChainConfig

	// Transaction state needed to add the root call of Arbitrum transactions that have none
	tx               *types.Transaction
	txFrom           common.Address
	txHasCall        bool
	txSimulatedRoot  bool
	txInSystemCall   bool
}

const FirehoseProtocolVersion = firehose.ProtocolVersion

func NewFirehose(config *FirehoseConfig) *Firehose {
	log.Info("Firehose tracer created", config.LogKeyValues()...)

	var outputWriter io.Writer = os.Stdout
	ignoreGenesisBlock := false
	if config.private != nil {
		ignoreGenesisBlock = config.private.IgnoreGenesisBlock
		if config.private.FlushToTestBuffer {
			outputWriter = bytes.NewBuffer(nil)
		}
	}

	return &Firehose{
		Tracer: firehose.NewTracer(&firehose.Config{
			OutputWriter:             outputWriter,
			IgnoreGenesisBlock:       ignoreGenesisBlock,
			EnableConcurrentFlushing: config.ConcurrentBlockFlushing > 0,
			ConcurrentBufferSize:     config.ConcurrentBlockFlushing,

			// Arbitrum skips transactions that fail (e.g. filtered ones) and carries on with the block
			DropTransactionsWithoutReceipt: true,
		}),

		config: config,
	}
}

func (f *Firehose) TracingHooks() *tracing.Hooks {
	if f.hooks == nil {
		f.hooks = newTracingHooksFromFirehose(f)
	}
	return f.hooks
}

func newTracingHooksFromFirehose(f *Firehose) *tracing.Hooks {
	return &tracing.Hooks{
		OnBlockchainInit: func(chainConfig *params.ChainConfig) {
			f.chainConfig = chainConfig
			f.Tracer.OnBlockchainInit("arbitrum", version.WithMeta, &firehose.ChainConfig{
				ChainID:             chainConfig.ChainID,
				SetCodeAuthRecovery: firehose.DefaultSetCodeAuthRecovery,
			}, nil)

			log.Info("Firehose tracer initialized", append([]any{
				"chain_id", chainConfig.ChainID,
				"protocol_version", FirehoseProtocolVersion,
			}, f.Tracer.GetConfig().LogKeyValues()...)...)
		},
		OnGenesisBlock: func(b *types.Block, alloc types.GenesisAlloc) {
			f.Tracer.OnGenesisBlock(f.convertBlockEvent(tracing.BlockEvent{Block: b}), convertGenesisAlloc(alloc))
		},
		OnBlockStart: func(event tracing.BlockEvent) {
			f.Tracer.OnBlockStart(f.convertBlockEvent(event))
		},
		OnBlockEnd: f.Tracer.OnBlockEnd,
		OnClose:    f.Tracer.OnClose,

		OnTxStart: f.onTxStart,
		OnTxEnd:   f.onTxEnd,

		OnEnter: func(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
			if !f.txInSystemCall {
				f.txHasCall = true
			}
			f.Tracer.OnCallEnter(depth, typ, [20]byte(from), [20]byte(to), input, gas, value)
		},
		OnExit: f.Tracer.OnCallExit,

		OnOpcode: func(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, rData []byte, depth int, err error) {
			f.Tracer.OnOpcode(pc, op, gas, cost, rData, depth, err)
		},
		OnFault: func(pc uint64, op byte, gas, cost uint64, scope tracing.OpContext, depth int, err error) {
			f.Tracer.OnOpcodeFault(pc, op, gas, cost, depth, err)
		},

		OnBalanceChange: func(a common.Address, prev, new *big.Int, reason tracing.BalanceChangeReason) {
			f.Tracer.OnBalanceChange([20]byte(a), prev, new, balanceChangeReasonFromChain(reason))
		},
		OnNonceChange: func(a common.Address, prev, new uint64) {
			f.Tracer.OnNonceChange([20]byte(a), prev, new)
		},
		OnCodeChange: func(a common.Address, prevCodeHash common.Hash, prev []byte, codeHash common.Hash, code []byte) {
			f.Tracer.OnCodeChange(a, prevCodeHash, codeHash, prev, code)
		},
		OnStorageChange: func(a common.Address, k, prev, new common.Hash) {
			f.Tracer.OnStorageChange(a, k, prev, new)
		},
		OnLog: func(l *types.Log) {
			f.Tracer.OnLog([20]byte(l.Address), convertHashSlice(l.Topics), l.Data, uint32(l.Index))
		},

		OnSystemCallStart: func() {
			f.txInSystemCall = true
			f.Tracer.OnSystemCallStart()
		},
		OnSystemCallEnd: func() {
			f.Tracer.OnSystemCallEnd()
			f.txInSystemCall = false
		},

		// Called directly by the KECCAK256 instruction, which has both the hash and the preimage at hand
		OnKeccakPreimage: func(hash common.Hash, preimage []byte) {
			f.Tracer.OnKeccakPreimage(hash, preimage)
		},
	}
}

// convertBlockEvent converts the block event, giving the block's fork rules computed the way
// the EVM does, Arbitrum activates forks from the ArbOS version found in the block header
func (f *Firehose) convertBlockEvent(event tracing.BlockEvent) firehose.BlockEvent {
	out := convertBlockEvent(event)

	header := event.Block.Header()
	arbOSVersion := types.DeserializeHeaderExtraInformation(header).ArbOSFormatVersion

	// Must stay in sync with how core.NewEVMBlockContext sets `Random`, which defines isMerge for the EVM
	isMerge := header.Difficulty.Sign() == 0 || arbOSVersion > 0

	rules := f.chainConfig.Rules(header.Number, isMerge, header.Time, arbOSVersion)
	out.Rules = &firehose.Rules{
		ChainID:    rules.ChainID,
		IsMerge:    rules.IsMerge,
		IsShanghai: rules.IsShanghai,
		IsCancun:   rules.IsCancun,
		IsPrague:   rules.IsPrague,
		IsVerkle:   rules.IsVerkle,
	}

	return out
}

func (f *Firehose) onTxStart(evm *tracing.VMContext, tx *types.Transaction, from common.Address) {
	f.tx = tx
	f.txFrom = from
	f.txHasCall = false
	f.txSimulatedRoot = false

	f.Tracer.OnTxStart(convertTxEvent(tx, from), newEVMStateReader(evm))

	// Those Arbitrum transactions are applied by ArbOS without an EVM root call, a root call
	// is added so their state changes have a call to belong to.
	switch tx.Type() {
	case types.ArbitrumDepositTxType, types.ArbitrumSubmitRetryableTxType, types.ArbitrumInternalTxType:
		f.txSimulatedRoot = true
		f.txHasCall = true
		f.Tracer.OnCallEnter(0, byte(vm.CALL), [20]byte(from), [20]byte(*tx.To()), tx.Data(), tx.Gas(), tx.Value())
	}
}

func (f *Firehose) onTxEnd(receipt *types.Receipt, err error) {
	if receipt != nil {
		switch {
		case f.txSimulatedRoot:
			f.Tracer.OnCallExit(0, nil, receipt.GasUsed, err, err != nil)

		case !f.txHasCall:
			// Arbitrum's `TxProcessor.RevertedTxHook` can skip EVM execution entirely (on-chain
			// filtered transactions and hardcoded `core.RevertedTxGasUsed` hashes) leaving a receipt
			// but no call, a root call is synthesized like for the Arbitrum transactions above.
			callErr := err
			if callErr == nil && receipt.Status == types.ReceiptStatusFailed {
				callErr = errors.New("transaction skipped EVM execution (Arbitrum RevertedTxHook, filtered or hardcoded reverted transaction)")
			}

			var to common.Address
			if f.tx.To() != nil {
				to = *f.tx.To()
			}

			f.Tracer.OnCallEnter(0, byte(vm.CALL), [20]byte(f.txFrom), [20]byte(to), f.tx.Data(), f.tx.Gas(), f.tx.Value())
			f.Tracer.OnCallExit(0, nil, receipt.GasUsed, callErr, receipt.Status == types.ReceiptStatusFailed)
		}
	}

	f.Tracer.OnTxEnd(convertReceiptData(receipt), err)

	f.tx = nil
	f.txHasCall = false
	f.txSimulatedRoot = false
}

var errFirehoseUnknownType = errors.New("firehose unknown tx type")
var sanitizeRegexp = regexp.MustCompile(`[\t( ){2,}]+`)

func staticFirehoseChainValidationOnInit() {
	firehoseKnownTxTypes := map[byte]bool{
		types.LegacyTxType:     true,
		types.AccessListTxType: true,
		types.DynamicFeeTxType: true,
		types.BlobTxType:       true,
		types.SetCodeTxType:    true,
		// these generate an error when trying to EncodeRLP
		//types.ArbitrumDepositTxType:         true,
		//types.ArbitrumUnsignedTxType:        true,
		//types.ArbitrumContractTxType:        true,
		//types.ArbitrumRetryTxType:           true,
		//types.ArbitrumSubmitRetryableTxType: true,
		//types.ArbitrumInternalTxType:        true,
		//types.ArbitrumLegacyTxType:          true,
	}

	for txType := byte(0); txType < 255; txType++ {
		err := validateFirehoseKnownTransactionType(txType, firehoseKnownTxTypes[txType])
		if err != nil {
			panic(fmt.Errorf(sanitizeRegexp.ReplaceAllString(`
				If you see this panic message, it comes from a sanity check of Firehose instrumentation
				around Ethereum transaction types.

				Over time, Ethereum added new transaction types but there is no easy way for Firehose to
				report a compile time check that a new transaction's type must be handled. As such, we
				have a runtime check at initialization of the process that encode/decode each possible
				transaction's receipt and check proper handling.

				This panic means that a transaction that Firehose don't know about has most probably
				been added and you must take **great care** to instrument it. One of the most important place
				to look is in 'firehose.StartTransaction' where it should be properly handled. Think
				carefully, read the EIP and ensure that any new "semantic" the transactions type's is
				bringing is handled and instrumented (it might affect Block and other execution units also).

				For example, when London fork appeared, semantic of 'GasPrice' changed and it required
				a different computation for 'GasPrice' when 'DynamicFeeTx' transaction were added. If you determined
				it was indeed a new transaction's type, fix 'firehoseKnownTxTypes' variable above to include it
				as a known Firehose type (after proper instrumentation of course).

				It's also possible the test itself is now flaky, we do 'receipt := types.Receipt{Type: <type>}'
				then 'buffer := receipt.EncodeRLP(...)' and then 'receipt.DecodeRLP(buffer)'. This should catch
				new transaction types but could be now generate false positive.

				Received error: %w
			`, " "), err))
		}
	}
}

func validateFirehoseKnownTransactionType(txType byte, isKnownFirehoseTxType bool) error {
	writerBuffer := bytes.NewBuffer(nil)

	receipt := types.Receipt{Type: txType}
	err := receipt.EncodeRLP(writerBuffer)
	if err != nil {
		if err == types.ErrTxTypeNotSupported {
			if isKnownFirehoseTxType {
				return fmt.Errorf("firehose known type but encoding RLP of receipt led to 'types.ErrTxTypeNotSupported'")
			}

			// It's not a known type and encoding reported the same, so validation is OK
			return nil
		}

		// All other cases results in an error as we should have been able to encode it to RLP
		return fmt.Errorf("encoding RLP: %w", err)
	}

	readerBuffer := bytes.NewBuffer(writerBuffer.Bytes())
	err = receipt.DecodeRLP(rlp.NewStream(readerBuffer, 0))
	if err != nil {
		if err == types.ErrTxTypeNotSupported {
			if isKnownFirehoseTxType {
				return fmt.Errorf("firehose known type but decoding of RLP of receipt led to 'types.ErrTxTypeNotSupported'")
			}

			// It's not a known type and decoding reported the same, so validation is OK
			return nil
		}

		// All other cases results in an error as we should have been able to decode it from RLP
		return fmt.Errorf("decoding RLP: %w", err)
	}

	// If we reach here, encoding/decoding accepted the transaction's type, so let's ensure we expected the same
	if !isKnownFirehoseTxType {
		return fmt.Errorf("unknown tx type value %d: %w", txType, errFirehoseUnknownType)
	}

	return nil
}
