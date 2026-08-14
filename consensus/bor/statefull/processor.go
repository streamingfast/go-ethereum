package statefull

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/bor/abi"
	"github.com/ethereum/go-ethereum/consensus/bor/clerk"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
)

type ChainContext struct {
	Chain consensus.ChainHeaderReader
	Bor   consensus.Engine
}

func (c ChainContext) Engine() consensus.Engine {
	return c.Bor
}

func (c ChainContext) GetHeader(hash common.Hash, number uint64) *types.Header {
	return c.Chain.GetHeader(hash, number)
}

func (c ChainContext) Config() *params.ChainConfig {
	return c.Chain.Config()
}

func (c ChainContext) CurrentHeader() *types.Header {
	return c.Chain.CurrentHeader()
}

func (c ChainContext) GetHeaderByHash(hash common.Hash) *types.Header {
	return c.Chain.GetHeaderByHash(hash)
}

func (c ChainContext) GetHeaderByNumber(number uint64) *types.Header {
	return c.Chain.GetHeaderByNumber(number)
}

func (c ChainContext) GetTd(hash common.Hash, number uint64) *big.Int {
	return c.Chain.GetTd(hash, number)
}

// callmsg implements core.Message to allow passing it as a transaction simulator.
type Callmsg struct {
	ethereum.CallMsg
}

func (m Callmsg) From() common.Address { return m.CallMsg.From }
func (m Callmsg) Nonce() uint64        { return 0 }
func (m Callmsg) CheckNonce() bool     { return false }
func (m Callmsg) To() *common.Address  { return m.CallMsg.To }
func (m Callmsg) GasPrice() *big.Int   { return m.CallMsg.GasPrice }
func (m Callmsg) Gas() uint64          { return m.CallMsg.Gas }
func (m Callmsg) Value() *big.Int      { return m.CallMsg.Value }
func (m Callmsg) Data() []byte         { return m.CallMsg.Data }

func GetSystemMessage(toAddress common.Address, data []byte) Callmsg {
	return Callmsg{
		ethereum.CallMsg{
			From:     params.BorSystemAddress,
			Gas:      params.MaxTxGas, // should be more than enough for state-sync related syscalls
			GasPrice: big.NewInt(0),
			Value:    big.NewInt(0),
			To:       &toAddress,
			Data:     data,
		},
	}
}

func getFirehose2CompatibleHash(spanID uint64, msg Callmsg) common.Hash {
	var txHash common.Hash
	sha := sha3.NewLegacyKeccak256().(crypto.KeccakState)
	sha.Reset()
	compatMsg := CompatCallMsg{
		CompatEthCallMsg{
			From:       msg.CallMsg.From,
			To:         msg.CallMsg.To,
			Gas:        msg.CallMsg.Gas,
			GasPrice:   msg.CallMsg.GasPrice,
			GasFeeCap:  msg.CallMsg.GasFeeCap,
			GasTipCap:  msg.CallMsg.GasTipCap,
			Value:      msg.CallMsg.Value,
			Data:       msg.CallMsg.Data,
			AccessList: msg.CallMsg.AccessList,
		},
	}
	rlp.Encode(sha, []interface{}{spanID, compatMsg})
	sha.Read(txHash[:])
	return txHash
}

type CompatEthCallMsg struct {
	From       common.Address   // the sender of the 'transaction'
	To         *common.Address  // the destination contract (nil for contract creation)
	Gas        uint64           // if 0, the call executes with near-infinite gas
	GasPrice   *big.Int         // wei <-> gas exchange ratio
	GasFeeCap  *big.Int         // EIP-1559 fee cap per gas.
	GasTipCap  *big.Int         // EIP-1559 tip per gas.
	Value      *big.Int         // amount of wei sent along with the call
	Data       []byte           // input data, usually an ABI-encoded contract method invocation
	AccessList types.AccessList // EIP-2930 access list.
}
type CompatCallMsg struct {
	CompatEthCallMsg
}

// apply message
func ApplyMessage(
	_ context.Context,
	msg Callmsg,
	state vm.StateDB,
	header *types.Header,
	chainConfig *params.ChainConfig,
	chainContext core.ChainContext,
	tracer *tracing.Hooks,
	spanID uint64,
) (uint64, error) {
	initialGas := msg.Gas()

	blockContext := core.NewEVMBlockContext(header, chainContext, &header.Coinbase)

	// Create a new environment which holds all relevant information
	// about the transaction and calling mechanisms.
	vmenv := vm.NewEVM(blockContext, state, chainConfig, vm.Config{Tracer: tracer})

	var tx *types.Transaction
	if tracer != nil {
		tx = types.NewTx(&types.LegacyTx{
			Nonce:    msg.Nonce(),
			GasPrice: msg.GasPrice(),
			Gas:      msg.Gas(),
			To:       msg.To(),
			Value:    msg.Value(),
			Data:     msg.Data(),
		})

		switch {
		case tracer.OnTxStartWithHash != nil: // firehose has this hook that allows forcing a hash to some special system transactions
			txHash := getFirehose2CompatibleHash(spanID, msg)
			tracer.OnTxStartWithHash(vmenv.GetVMContext(), tx, msg.From(), txHash)
		case tracer.OnTxStart != nil:
			tracer.OnTxStart(vmenv.GetVMContext(), tx, msg.From())
		}
		state.Inner().SetTxContext(tx.Hash(), 0)
	} else {
		vmenv.SetTxContext(core.NewEVMTxContextForStateSync())
	}

	// nolint : contextcheck
	// Apply the transaction to the current state (included in the env)
	ret, gasLeft, err := vmenv.Call(
		msg.From(),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		uint256.NewInt(msg.Value().Uint64()),
	)

	success := big.NewInt(5).SetBytes(ret)

	validatorContract := common.HexToAddress(chainConfig.Bor.ValidatorContract)

	// if success == 0 and msg.To() != validatorContractAddress, log Error
	// if msg.To() == validatorContractAddress, its committing a span and we don't get any return value
	if success.Cmp(big.NewInt(0)) == 0 && !bytes.Equal(msg.To().Bytes(), validatorContract.Bytes()) {
		log.Error("message execution failed on contract", "msgData", msg.Data)
	}

	// If there's error committing span, log it here. It won't be reported before because the return value is empty.
	if bytes.Equal(msg.To().Bytes(), validatorContract.Bytes()) && err != nil {
		log.Error("message execution failed on contract", "err", err)
	}

	// Update the state with pending changes
	if err != nil {
		state.Finalise(true)
	}

	gasUsed := initialGas - gasLeft

	if tracer != nil {
		blockHash := header.Hash()
		cumulativeGasUsed := gasUsed

		receipt := types.NewReceipt(nil, err != nil, cumulativeGasUsed)
		receipt.TxHash = tx.Hash()
		receipt.GasUsed = gasUsed

		if msg.To() == nil {
			receipt.ContractAddress = crypto.CreateAddress(vmenv.TxContext.Origin, tx.Nonce())
		}

		receipt.Logs = state.GetLogs(tx.Hash(), header.Number.Uint64(), blockHash, header.Time)
		receipt.Bloom = types.CreateBloom(receipt)
		receipt.BlockHash = blockHash
		receipt.BlockNumber = header.Number
		receipt.TransactionIndex = 0
		tracer.OnTxEnd(receipt, nil)
	}

	return gasUsed, nil
}

func ApplyBorMessage(vmenv *vm.EVM, msg Callmsg) (*core.ExecutionResult, error) {
	initialGas := msg.Gas()

	// Apply the transaction to the current state (included in the env)
	ret, gasLeft, err := vmenv.Call(
		msg.From(),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		uint256.NewInt(msg.Value().Uint64()),
	)
	// Update the state with pending changes
	if err != nil {
		vmenv.StateDB.Finalise(true)
	}

	gasUsed := initialGas - gasLeft

	return &core.ExecutionResult{
		UsedGas:    gasUsed,
		Err:        err,
		ReturnData: ret,
	}, nil
}

// PrepareStateSyncContext resets transaction-scoped state before each post-Austin record.
func PrepareStateSyncContext(state vm.StateDB, chainConfig *params.ChainConfig, blockNumber *big.Int, blockTime uint64, coinbase, stateReceiver common.Address) {
	if chainConfig.Bor == nil || !chainConfig.Bor.IsAustin(blockNumber) {
		return
	}
	rules := chainConfig.Rules(blockNumber, false, blockTime)
	state.Prepare(rules, params.BorSystemAddress, coinbase, &stateReceiver, vm.ActivePrecompiles(rules), nil)
}

// ApplyStateSyncEvents replays all state-sync events from a StateSyncTx against the EVM. This
// method is generally used for tracing. It tries to mimic the exact things which happen when
// a state-sync is processed in a live network (via CommitState).
//
// ctx is checked between events so callers can abort the loop on deadline / client disconnect.
func ApplyStateSyncEvents(ctx context.Context, vmenv *vm.EVM, tx *types.Transaction, message *core.Message, stateReceiverContract common.Address) (*core.ExecutionResult, error) {
	events := tx.GetStateSyncData()
	if len(events) == 0 {
		return &core.ExecutionResult{UsedGas: 0, ReturnData: nil}, nil
	}

	// Set tx context so that opcodes like GASPRICE don't panic.
	vmenv.SetTxContext(core.NewEVMTxContext(message))

	// The actual state-sync transaction uses event time but because we don't have
	// it here, we use the block time. The calldata will be different than what
	// was constructed while executing the transaction but it'll be deterministic
	// in every run.
	stateReceiverABI := abi.StateReceiver()
	// syncTime can be reused across calls as ABI Pack does not retain a reference
	syncTime := new(big.Int).SetUint64(vmenv.Context.Time)

	var totalGasUsed uint64
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		PrepareStateSyncContext(vmenv.StateDB, vmenv.ChainConfig(), vmenv.Context.BlockNumber, vmenv.Context.Time, vmenv.Context.Coinbase, stateReceiverContract)
		gasUsed, err := commitStateSyncEvent(vmenv, event, stateReceiverABI, stateReceiverContract, syncTime)
		if err != nil {
			return nil, err
		}
		totalGasUsed += gasUsed
	}

	return &core.ExecutionResult{
		UsedGas: totalGasUsed,
		Err:     nil,
	}, nil
}

// commitStateSyncEvent encodes a single bridge event, packs the commitState calldata, and
// applies the system call. A reverted EVM call does not surface as an error — production
// semantics allow individual events to fail silently; the trace records the revert. Only
// encoding / ABI failures (which indicate a programming bug, not a runtime condition)
// return an error.
func commitStateSyncEvent(vmenv *vm.EVM, event *types.StateSyncData, stateReceiverABI abi.ABI, stateReceiverContract common.Address, syncTime *big.Int) (uint64, error) {
	// Convert StateSyncData to EventRecord (matching CommitState's BuildEventRecord).
	// LogIndex and ChainID are not used by commitState but are required for RLP encoding.
	record := &clerk.EventRecord{
		ID:       event.ID,
		Contract: event.Contract,
		Data:     event.Data,
		TxHash:   event.TxHash,
	}
	recordBytes, err := rlp.EncodeToBytes(record)
	if err != nil {
		return 0, fmt.Errorf("failed to RLP encode state-sync event %d: %w", event.ID, err)
	}

	// ABI-pack commitState(uint256 syncTime, bytes recordBytes)
	data, err := stateReceiverABI.Pack("commitState", syncTime, recordBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to ABI pack commitState for event %d: %w", event.ID, err)
	}

	result, _ := ApplyBorMessage(vmenv, GetSystemMessage(stateReceiverContract, data))
	if result.Err != nil {
		log.Debug("state-sync event reverted during trace replay", "eventID", event.ID, "err", result.Err)
	}
	return result.UsedGas, nil
}
