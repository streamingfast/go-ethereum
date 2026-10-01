package tracers

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/internal/version"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	firehose "github.com/streamingfast/evm-firehose-tracer-go/v5"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
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

	config *FirehoseConfig
	hooks  *tracing.Hooks

	// stateSyncReceipt is the receipt of the post-Madhugiri state-sync transaction of the
	// current block, it replaces the synthetic receipt of the merged system transaction.
	stateSyncReceipt *types.Receipt
}

// FirehoseProtocolVersion is the Firehose version used in release image tags (e.g. `-fh3.0`), the
// version written on the Firehose output is firehose.ProtocolVersion.
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

	f := &Firehose{config: config}
	f.Tracer = firehose.NewTracer(&firehose.Config{
		OutputWriter:             outputWriter,
		IgnoreGenesisBlock:       ignoreGenesisBlock,
		EnableConcurrentFlushing: config.ConcurrentBlockFlushing > 0,
		ConcurrentBufferSize:     config.ConcurrentBlockFlushing,

		// Polygon's fee transfer log is kept in the receipt even when the transaction reverts
		IsNeverRevertedLog: isPolygonFeeTransferLog,
		BeforeBlockFlush:   f.combinePolygonSystemTransactions,
	})

	return f
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
			nodeVersion, _ := version.Info()
			f.Tracer.OnBlockchainInit("geth", nodeVersion, convertChainConfig(chainConfig), nil)

			log.Info("Firehose tracer initialized", append([]any{
				"chain_id", chainConfig.ChainID,
				"protocol_version", firehose.ProtocolVersion,
			}, f.Tracer.GetConfig().LogKeyValues()...)...)
		},
		OnGenesisBlock: func(b *types.Block, alloc types.GenesisAlloc) {
			f.Tracer.OnGenesisBlock(firehose.BlockEvent{Block: convertBlockData(b, b.Header())}, convertGenesisAlloc(alloc))
		},
		OnBlockStart: func(event tracing.BlockEvent) {
			f.stateSyncReceipt = nil
			f.Tracer.OnBlockStart(convertBlockEvent(event))
		},
		OnBlockEnd: f.Tracer.OnBlockEnd,
		OnSkippedBlock: func(event tracing.BlockEvent) {
			// Blocks that are skipped from blockchain that were known and should contain 0 transactions.
			// It happened in the past, on Polygon if I recall right, that we missed block because some block
			// went in this code path.
			//
			// See https://github.com/streamingfast/go-ethereum/blob/a46903cf0cad829479ded66b369017914bf82314/core/blockchain.go#L1797-L1814
			if event.Block.Transactions().Len() > 0 {
				panic(fmt.Sprintf("The tracer received an `OnSkippedBlock` block #%d (%s) with %d transactions, this according to core/blockchain.go should never happen and is an error",
					event.Block.NumberU64(),
					event.Block.Hash().Hex(),
					event.Block.Transactions().Len(),
				))
			}

			f.stateSyncReceipt = nil
			f.Tracer.OnSkippedBlock(convertBlockEvent(event))
		},
		OnClose: f.Tracer.OnClose,

		OnTxStart: func(evm *tracing.VMContext, tx *types.Transaction, from common.Address) {
			f.Tracer.OnTxStart(convertTxEvent(tx, tx.Hash(), from), newEVMStateReader(evm))
		},
		// Polygon system transactions are traced with a deterministic hash kept from Firehose 2.x
		OnTxStartWithHash: func(evm *tracing.VMContext, tx *types.Transaction, from common.Address, hash common.Hash) {
			f.Tracer.OnTxStart(convertTxEvent(tx, hash, from), newEVMStateReader(evm))
		},
		OnTxEnd: func(receipt *types.Receipt, err error) {
			f.Tracer.OnTxEnd(convertReceiptData(receipt), err)
		},

		OnEnter: func(depth int, typ byte, from common.Address, to common.Address, input []byte, gas uint64, value *big.Int) {
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

		OnSystemCallStart: f.Tracer.OnSystemCallStart,
		OnSystemCallEnd:   f.Tracer.OnSystemCallEnd,

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

		OnStateSyncReceipt: func(_ *types.Transaction, receipt *types.Receipt) {
			f.stateSyncReceipt = receipt
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
		types.StateSyncTxType:  true,
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
	polygonSystemAddress        = common.HexToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfE")
	polygonStateReceiverAddress = common.HexToAddress("0x0000000000000000000000000000000000001001")
	polygonValidatorContract    = common.HexToAddress("0x0000000000000000000000000000000000001000")
	nullAddress                 = common.HexToAddress("0x0000000000000000000000000000000000000000")
	bigIntZero                  = pbeth.BigIntFromBytes(nil)

	polygonFeeAddress        = core.GetFeeAddress()
	polygonTransferFeeLogSig = core.GetTransferFeeLogSig()
)

// isPolygonFeeTransferLog reports Polygon fee transfer logs, which are recorded to the chain's
// state even when the call that emitted them is reverted.
func isPolygonFeeTransferLog(log *pbeth.Log) bool {
	return bytes.Equal(log.Address, polygonFeeAddress[:]) && len(log.Topics) == 4 && bytes.Equal(log.Topics[0], polygonTransferFeeLogSig[:])
}

type BloomFilter [256]byte

// combinePolygonSystemTransactions will identify transactions that are "system transactions" and merge them into a single transaction with a predictive name, like the `bor` client does.
// It reorders the calls and logs to match expected output from RPC API.
func (f *Firehose) combinePolygonSystemTransactions(block *pbeth.Block) {
	stateSyncReceipt := f.stateSyncReceipt
	f.stateSyncReceipt = nil

	var systemTransactionsToMerge []*pbeth.TransactionTrace
	var unmergeableSystemTransactions []*pbeth.TransactionTrace
	var out []*pbeth.TransactionTrace
	normalTransactions := make([]*pbeth.TransactionTrace, 0, len(block.TransactionTraces))

	highestTrxIndex := int64(-1) // negative so that next one is 0 if no normal transaction is met
	for _, trace := range block.TransactionTraces {
		if bytes.Equal(trace.From, polygonSystemAddress.Bytes()) {
			if bytes.Equal(trace.To, polygonStateReceiverAddress.Bytes()) {
				systemTransactionsToMerge = append(systemTransactionsToMerge, trace)
				continue
			}
			if bytes.Equal(trace.To, polygonValidatorContract.Bytes()) {
				unmergeableSystemTransactions = append(unmergeableSystemTransactions, trace)
				continue
			}
			// no other know case for polygon
		}
		if int64(trace.Index) > highestTrxIndex {
			highestTrxIndex = int64(trace.Index)
		}
		normalTransactions = append(normalTransactions, trace)
	}

	out = normalTransactions
	if systemTransactionsToMerge == nil && unmergeableSystemTransactions == nil {
		return
	}
	if systemTransactionsToMerge != nil {
		var allCalls []*pbeth.Call
		var allLogs []*pbeth.Log
		var beginOrdinal uint64
		var seenFirstBeginOrdinal bool

		var seenFirstCallOrdinal bool
		var lowestCallBeginOrdinal uint64
		var highestCallEndOrdinal uint64

		var endOrdinal uint64
		var callIdxOffset = uint32(1) // initial offset for all calls because of artificial top level call

		for _, trace := range systemTransactionsToMerge {
			var trxLogs []*pbeth.Log
			if !seenFirstBeginOrdinal || trace.BeginOrdinal < beginOrdinal {
				beginOrdinal = trace.BeginOrdinal
				seenFirstBeginOrdinal = true
			}

			if trace.EndOrdinal > endOrdinal {
				endOrdinal = trace.EndOrdinal
			}
			highestCallIndex := callIdxOffset
			for _, call := range trace.Calls {
				if !seenFirstCallOrdinal || call.BeginOrdinal < lowestCallBeginOrdinal {
					lowestCallBeginOrdinal = call.BeginOrdinal
					seenFirstCallOrdinal = true
				}
				if call.EndOrdinal > highestCallEndOrdinal {
					highestCallEndOrdinal = call.EndOrdinal
				}

				call.Index += callIdxOffset

				// all top level calls must be children of the very first (artificial) call.
				call.Depth += 1
				if call.ParentIndex == 0 {
					call.ParentIndex = 1
				} else {
					call.ParentIndex += callIdxOffset
				}
				if call.Index > highestCallIndex {
					highestCallIndex = call.Index
				}
				allCalls = append(allCalls, call)
				// the receipt.logs on these transactions is not populated before
				for _, log := range call.Logs {
					if !call.StateReverted || isPolygonFeeTransferLog(log) {
						trxLogs = append(trxLogs, log)
					}
				}
			}
			callIdxOffset = highestCallIndex

			sort.Slice(trxLogs, func(i, j int) bool {
				return trxLogs[i].BlockIndex < trxLogs[j].BlockIndex
			})
			allLogs = append(allLogs, trxLogs...)
		}
		artificialTopLevelCall := &pbeth.Call{
			Index:        1,
			ParentIndex:  0,
			Depth:        0,
			CallType:     pbeth.CallType_CALL,
			GasLimit:     0,
			GasConsumed:  0,
			Caller:       nullAddress.Bytes(),
			Address:      nullAddress.Bytes(),
			Value:        bigIntZero,
			Input:        nil,
			BeginOrdinal: lowestCallBeginOrdinal,
			EndOrdinal:   highestCallEndOrdinal,
		}
		allCalls = append([]*pbeth.Call{artificialTopLevelCall}, allCalls...)

		txType := pbeth.TransactionTrace_TRX_TYPE_LEGACY
		mergedHash := computePolygonHash(block.Number, block.Hash)
		receipt := &pbeth.TransactionReceipt{
			Logs:      allLogs,
			LogsBloom: computeLogsBloom(allLogs),
			// CumulativeGasUsed // Reported as empty from the API. does not impact much because it is the last transaction in the block, this is reset every block.
			// StateRoot // Deprecated EIP 658
		}

		if stateSyncReceipt != nil {
			firehoseInfo("using state sync receipt for hash %s instead of %s", stateSyncReceipt.TxHash.String(), hex.EncodeToString(mergedHash))
			mergedHash = stateSyncReceipt.TxHash[:]
			txType = pbeth.TransactionTrace_TRX_TYPE_POLYGON_STATE_SYNC
			receipt = newStateSyncTxReceipt(stateSyncReceipt)
		}
		mergedSystemTrx := &pbeth.TransactionTrace{
			Hash:         mergedHash,
			From:         nullAddress.Bytes(),
			To:           nullAddress.Bytes(),
			Nonce:        0,
			GasPrice:     bigIntZero,
			GasLimit:     0,
			Value:        bigIntZero,
			Index:        uint32(highestTrxIndex + 1),
			Input:        nil,
			GasUsed:      0,
			Type:         txType,
			BeginOrdinal: beginOrdinal,
			EndOrdinal:   endOrdinal,
			Calls:        allCalls,
			Status:       pbeth.TransactionTraceStatus_SUCCEEDED,
			Receipt:      receipt,
		}
		out = append(out, mergedSystemTrx)
		highestTrxIndex++
	}
	for _, tx := range unmergeableSystemTransactions {
		tx.Index = uint32(highestTrxIndex + 1)
		out = append(out, tx)
		highestTrxIndex++
	}

	block.TransactionTraces = out
}

// newStateSyncTxReceipt converts the receipt of a post-Madhugiri state-sync transaction. Ordinals
// of its logs are left unset, the state-sync logs are not paired with call logs.
func newStateSyncTxReceipt(receipt *types.Receipt) *pbeth.TransactionReceipt {
	out := &pbeth.TransactionReceipt{
		StateRoot:         receipt.PostState,
		CumulativeGasUsed: receipt.CumulativeGasUsed,
		LogsBloom:         receipt.Bloom[:],
	}

	if len(receipt.Logs) > 0 {
		out.Logs = make([]*pbeth.Log, len(receipt.Logs))
		for i, log := range receipt.Logs {
			var topics [][]byte
			if len(log.Topics) > 0 {
				topics = make([][]byte, len(log.Topics))
				for j, topic := range log.Topics {
					topics[j] = topic.Bytes()
				}
			}

			out.Logs[i] = &pbeth.Log{
				Address:    log.Address.Bytes(),
				Topics:     topics,
				Data:       log.Data,
				Index:      uint32(i),
				BlockIndex: uint32(log.Index),
			}
		}
	}

	return out
}

func (b *BloomFilter) add(data []byte) {
	hash := crypto.Keccak256(data)
	b[256-uint((binary.BigEndian.Uint16(hash)&0x7ff)>>3)-1] |= byte(1 << (hash[1] & 0x7))
	b[256-uint((binary.BigEndian.Uint16(hash[2:])&0x7ff)>>3)-1] |= byte(1 << (hash[3] & 0x7))
	b[256-uint((binary.BigEndian.Uint16(hash[4:])&0x7ff)>>3)-1] |= byte(1 << (hash[5] & 0x7))
}

func computeLogsBloom(logs []*pbeth.Log) []byte {
	var bf = new(BloomFilter)
	for _, log := range logs {
		bf.add(log.Address)
		for _, topic := range log.Topics {
			bf.add(topic)
		}
	}
	return bf[:]
}

func computePolygonHash(blockNum uint64, blockHash []byte) []byte {
	enc := make([]byte, 8)
	binary.BigEndian.PutUint64(enc, blockNum)
	key := append(append([]byte("matic-bor-receipt-"), enc...), blockHash...)
	return crypto.Keccak256(key)
}

// Here what you can expect from the debugging levels:
// - Info == block start/end + trx start/end
// - Debug == Info + call start/end + error
// - Trace == Debug + state db changes, log, balance, nonce, code, storage
// - TraceFull == Trace + opcode
var firehoseTracerLogLevel = strings.ToLower(os.Getenv("FIREHOSE_ETHEREUM_TRACER_LOG_LEVEL"))
var isFirehoseInfoEnabled = firehoseTracerLogLevel == "info" || firehoseTracerLogLevel == "debug" || firehoseTracerLogLevel == "trace" || firehoseTracerLogLevel == "trace_full"

func firehoseInfo(msg string, args ...interface{}) {
	if isFirehoseInfoEnabled {
		fmt.Fprintf(os.Stderr, "[Firehose] "+msg+"\n", args...)
	}
}
