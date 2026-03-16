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
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	firehose "github.com/streamingfast/evm-firehose-tracer-go/v4"
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
	ConcurrentBlockFlushing int  `json:"concurrentBlockFlushing"`
	TraceBlockWithdrawals   bool `json:"traceBlockWithdrawals"`

	// Only used for testing, only possible through JSON configuration
	private *privateFirehoseConfig
}

type privateFirehoseConfig struct {
	FlushToTestBuffer  bool `json:"flushToTestBuffer"`
	IgnoreGenesisBlock bool `json:"ignoreGenesisBlock"`
}

// LogKeValues returns a list of key-values to be logged when the config is printed.
func (c *FirehoseConfig) LogKeyValues() []any {
	return []any{
		"config.applyBackwardCompatibility", "false",
		"config.concurrentBlockFlushing", c.ConcurrentBlockFlushing,
		"config.traceBlockWithdrawals", c.TraceBlockWithdrawals,
	}
}

type Firehose struct {
	*firehose.Tracer

	config *FirehoseConfig
	hooks  *tracing.Hooks
}

const FirehoseProtocolVersion = "3.0"

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
			// SkipWithdrawals is handled in OnBlockchainInit directly since it depends on chain ID
		}),

		config: config,
		hooks:  nil,
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
			f.Tracer.OnBlockchainInit("geth", version.Semantic, convertChainConfig(chainConfig), func(config *firehose.Config) {
				if f.config.TraceBlockWithdrawals {
					config.SkipWithdrawals = false
				} else {
					config.SkipWithdrawals = !isChainOneOf(chainConfig.ChainID, hoodiChainID)
				}
			})

			log.Info("Firehose tracer initialized",
				"chain_id", chainConfig.ChainID,
				"apply_backward_compatibility", "false",
				"protocol_version", FirehoseProtocolVersion,
			)
		},
		OnGenesisBlock: func(b *types.Block, alloc types.GenesisAlloc) {
			f.Tracer.OnGenesisBlock(firehose.BlockEvent{Block: convertBlockData(b, b.Header())}, convertGenesisAlloc(alloc))
		},
		OnBlockStart: func(event tracing.BlockEvent) {
			f.OnBlockStart(convertBlockEvent(event))
		},
		OnBlockEnd: f.OnBlockEnd,
		OnSkippedBlock: func(event tracing.BlockEvent) {
			f.OnSkippedBlock(convertBlockEvent(event))
		},
		OnClose: f.OnClose,

		OnTxStart: func(evm *tracing.VMContext, tx *types.Transaction, from common.Address) {
			f.OnTxStart(convertTxEvent(tx, from), newEVMStateReader(evm))
		},
		OnTxEnd: func(receipt *types.Receipt, err error) {
			f.OnTxEnd(convertReceiptData(receipt), err)
		},

		OnEnter: func(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
			f.Tracer.OnCallEnter(depth, typ, [20]byte(from), [20]byte(to), input, gas, value)
		},
		OnExit: f.OnCallExit,

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

		OnGasChange: func(old, new uint64, reason tracing.GasChangeReason) {
			if reason == tracing.GasChangeCallOpCode || reason == tracing.GasChangeIgnored {
				return
			}

			f.Tracer.OnGasChange(old, new, gasChangeReasonFromChain(reason))
		},

		OnSystemCallStart: f.OnSystemCallStart,
		OnSystemCallEnd:   f.OnSystemCallEnd,

		// For a reason yet to be discovered, some transactions panics when trying to
		// compute the keccak hash from a preimage when it comes the time to retrieve
		// the memory location by inspecting the opcode's EVM stack arguments. The panic
		// is an index out of bound error.
		//
		// To avoid the problem altogether, we return to our old Firehose tracer hook directly
		// when the instructions is called within the EVM. That has the added benefit of
		// avoiding re-computing the keccak results of the preimage again since at this location,
		// we have both the result and the preimage.
		//
		// The cons of this is that we need to keep some Geth internal changes to have the hook
		// called. But this is minimal as we do have to maintain a fork and the changes are actually
		// minimal.
		//
		// Comment 11471b22bb0b (search '11471b22bb0b' within the repository to see all related code locations)
		OnKeccakPreimage: func(hash common.Hash, preImage []byte) {
			f.Tracer.OnKeccakPreimage(hash, preImage)
		},
	}
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

var (
	hoodiChainID = params.HoodiChainConfig.ChainID
)

func isChainOneOf(chainID *big.Int, expectedChainIDs ...*big.Int) bool {
	if chainID == nil {
		return false
	}

	for _, expected := range expectedChainIDs {
		if chainID.Cmp(expected) == 0 {
			return true
		}
	}
	return false
}
