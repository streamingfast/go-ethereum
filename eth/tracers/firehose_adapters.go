package tracers

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	firehose "github.com/streamingfast/evm-firehose-tracer-go"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
)

// Type conversion helpers for go-ethereum to shared tracer types
// These are simple casts since common.Address and common.Hash are type aliases for [20]byte and [32]byte

//go:inline
func toBigIntBytes32(i *big.Int) [32]byte {
	var result [32]byte
	if i != nil {
		bytes := i.Bytes()
		copy(result[32-len(bytes):], bytes)
	}
	return result
}

func convertGenesisAlloc(alloc types.GenesisAlloc) firehose.GenesisAlloc {
	firehoseAlloc := make(firehose.GenesisAlloc, len(alloc))
	for addr, account := range alloc {
		storage := make(map[[32]byte][32]byte, len(account.Storage))
		for k, v := range account.Storage {
			storage[[32]byte(k)] = [32]byte(v)
		}

		firehoseAlloc[addr] = firehose.GenesisAccount{
			Balance: account.Balance,
			Code:    account.Code,
			Nonce:   account.Nonce,
			Storage: storage,
		}
	}

	return firehoseAlloc
}

// convertChainConfig converts params.ChainConfig to firehose.ChainConfig
func convertChainConfig(cfg *params.ChainConfig) *firehose.ChainConfig {
	return &firehose.ChainConfig{
		ChainID:             cfg.ChainID,
		ShanghaiTime:        cfg.ShanghaiTime,
		CancunTime:          cfg.CancunTime,
		PragueTime:          cfg.PragueTime,
		VerkleTime:          nil,
		SetCodeAuthRecovery: firehose.DefaultSetCodeAuthRecovery,
	}
}

// convertBlockEvent converts tracing.BlockEvent to firehose.BlockEvent
func convertBlockEvent(event tracing.BlockEvent) firehose.BlockEvent {
	block := event.Block
	header := block.Header()

	return firehose.BlockEvent{
		Block:     convertBlockData(block, header),
		Finalized: convertFinalizedBlock(event.Finalized),
	}
}

// convertBlockData converts block and header to firehose.BlockData
func convertBlockData(block *types.Block, header *types.Header) firehose.BlockData {
	data := firehose.BlockData{
		Number:      block.NumberU64(),
		Hash:        [32]byte(block.Hash()),
		ParentHash:  [32]byte(header.ParentHash),
		UncleHash:   [32]byte(header.UncleHash),
		Coinbase:    [20]byte(header.Coinbase),
		Root:        [32]byte(header.Root),
		TxHash:      [32]byte(header.TxHash),
		ReceiptHash: [32]byte(header.ReceiptHash),
		Bloom:       header.Bloom[:],
		Difficulty:  header.Difficulty,
		GasLimit:    header.GasLimit,
		GasUsed:     header.GasUsed,
		Time:        header.Time,
		Extra:       header.Extra,
		MixDigest:   [32]byte(header.MixDigest),
		Nonce:       header.Nonce.Uint64(),
		BaseFee:     header.BaseFee,
		Size:        block.Size(),
		IsMerge:     block.Difficulty().Sign() == 0,
	}

	if header.WithdrawalsHash != nil {
		hash := [32]byte(*header.WithdrawalsHash)
		data.WithdrawalsRoot = &hash
	}

	if header.BlobGasUsed != nil {
		data.BlobGasUsed = header.BlobGasUsed
	}
	if header.ExcessBlobGas != nil {
		data.ExcessBlobGas = header.ExcessBlobGas
	}

	if header.ParentBeaconRoot != nil {
		hash := [32]byte(*header.ParentBeaconRoot)
		data.ParentBeaconRoot = &hash
	}

	if header.RequestsHash != nil {
		hash := [32]byte(*header.RequestsHash)
		data.RequestsHash = &hash
	}

	for _, uncle := range block.Uncles() {
		data.Uncles = append(data.Uncles, convertUncleData(uncle))
	}

	if withdrawals := block.Withdrawals(); withdrawals != nil {
		data.Withdrawals = make([]firehose.WithdrawalData, len(withdrawals))
		for i, w := range withdrawals {
			data.Withdrawals[i] = firehose.WithdrawalData{
				Index:          w.Index,
				ValidatorIndex: w.Validator,
				Address:        [20]byte(w.Address),
				Amount:         w.Amount,
			}
		}
	}

	return data
}

// convertUncleData converts uncle header to firehose.UncleData
func convertUncleData(uncle *types.Header) firehose.UncleData {
	return firehose.UncleData{
		Hash:        [32]byte(uncle.Hash()),
		ParentHash:  [32]byte(uncle.ParentHash),
		UncleHash:   [32]byte(uncle.UncleHash),
		Coinbase:    [20]byte(uncle.Coinbase),
		Root:        [32]byte(uncle.Root),
		TxHash:      [32]byte(uncle.TxHash),
		ReceiptHash: [32]byte(uncle.ReceiptHash),
		Bloom:       uncle.Bloom[:],
		Difficulty:  uncle.Difficulty,
		Number:      uncle.Number.Uint64(),
		GasLimit:    uncle.GasLimit,
		GasUsed:     uncle.GasUsed,
		Time:        uncle.Time,
		Extra:       uncle.Extra,
		MixDigest:   [32]byte(uncle.MixDigest),
		Nonce:       uncle.Nonce.Uint64(),
		BaseFee:     uncle.BaseFee,
	}
}

// convertFinalizedBlock converts finalized block reference
func convertFinalizedBlock(finalized *types.Header) *firehose.FinalizedBlockRef {
	if finalized == nil {
		return nil
	}
	return &firehose.FinalizedBlockRef{
		Number: finalized.Number.Uint64(),
		Hash:   [32]byte(finalized.Hash()),
	}
}

// convertTxEvent converts go-ethereum transaction to firehose.TxEvent
func convertTxEvent(tx *types.Transaction, from common.Address) firehose.TxEvent {
	v, r, s := tx.RawSignatureValues()

	var toPtr *[20]byte
	if to := tx.To(); to != nil {
		toPtr = (*[20]byte)(to)
	}

	event := firehose.TxEvent{
		Type:     tx.Type(),
		Hash:     [32]byte(tx.Hash()),
		From:     [20]byte(from),
		To:       toPtr,
		Input:    tx.Data(),
		Value:    tx.Value(),
		Gas:      tx.Gas(),
		GasPrice: tx.GasPrice(),
		Nonce:    tx.Nonce(),
		V:        v.Bytes(),
		R:        toBigIntBytes32(r),
		S:        toBigIntBytes32(s),
	}

	if tx.Type() >= types.DynamicFeeTxType {
		event.MaxFeePerGas = tx.GasFeeCap()
		event.MaxPriorityFeePerGas = tx.GasTipCap()
	}

	if accessList := tx.AccessList(); len(accessList) > 0 {
		event.AccessList = make(firehose.AccessList, len(accessList))
		for i, tuple := range accessList {
			event.AccessList[i] = firehose.AccessTuple{
				Address:     [20]byte(tuple.Address),
				StorageKeys: convertHashSlice(tuple.StorageKeys),
			}
		}
	}

	if tx.Type() == types.BlobTxType {
		event.BlobGasFeeCap = tx.BlobGasFeeCap()
		if hashes := tx.BlobHashes(); len(hashes) > 0 {
			event.BlobHashes = make([][32]byte, len(hashes))
			for i, hash := range hashes {
				event.BlobHashes[i] = [32]byte(hash)
			}
		}
	}

	if tx.Type() == types.SetCodeTxType {
		if auths := tx.SetCodeAuthorizations(); len(auths) > 0 {
			event.SetCodeAuthorizations = make([]firehose.SetCodeAuthorization, len(auths))
			for i, auth := range auths {
				event.SetCodeAuthorizations[i] = firehose.SetCodeAuthorization{
					ChainID: auth.ChainID.Bytes32(),
					Address: [20]byte(auth.Address),
					Nonce:   auth.Nonce,
					V:       uint32(auth.V),
					R:       auth.R.Bytes32(),
					S:       auth.S.Bytes32(),
				}
			}
		}
	}

	return event
}

// convertHashSlice converts []common.Hash to [][32]byte
func convertHashSlice(keys []common.Hash) [][32]byte {
	result := make([][32]byte, len(keys))
	for i, key := range keys {
		result[i] = [32]byte(key)
	}
	return result
}

// convertReceiptData converts types.Receipt to firehose.ReceiptData
func convertReceiptData(receipt *types.Receipt) *firehose.ReceiptData {
	if receipt == nil {
		return nil
	}

	data := &firehose.ReceiptData{
		TransactionIndex:  uint32(receipt.TransactionIndex),
		GasUsed:           receipt.GasUsed,
		Status:            receipt.Status,
		CumulativeGasUsed: receipt.CumulativeGasUsed,
		LogsBloom:         receipt.Bloom,
	}

	if len(receipt.Logs) > 0 {
		data.Logs = make([]firehose.LogData, len(receipt.Logs))
		for i, log := range receipt.Logs {
			data.Logs[i] = convertLogData(log)
		}
	}

	if receipt.BlobGasUsed > 0 {
		data.BlobGasUsed = receipt.BlobGasUsed
		data.BlobGasPrice = receipt.BlobGasPrice
	}

	data.StateRoot = receipt.PostState

	return data
}

// convertLogData converts types.Log to firehose.LogData
func convertLogData(log *types.Log) firehose.LogData {
	data := firehose.LogData{
		Address:    [20]byte(log.Address),
		Data:       log.Data,
		BlockIndex: uint32(log.Index),
	}

	if len(log.Topics) > 0 {
		data.Topics = make([][32]byte, len(log.Topics))
		for i, topic := range log.Topics {
			data.Topics[i] = [32]byte(topic)
		}
	}

	return data
}

var balanceChangeReasonToPb = map[tracing.BalanceChangeReason]pbeth.BalanceChange_Reason{
	tracing.BalanceIncreaseRewardMineUncle:      pbeth.BalanceChange_REASON_REWARD_MINE_UNCLE,
	tracing.BalanceIncreaseRewardMineBlock:      pbeth.BalanceChange_REASON_REWARD_MINE_BLOCK,
	tracing.BalanceIncreaseDaoContract:          pbeth.BalanceChange_REASON_DAO_REFUND_CONTRACT,
	tracing.BalanceDecreaseDaoAccount:           pbeth.BalanceChange_REASON_DAO_ADJUST_BALANCE,
	tracing.BalanceChangeTransfer:               pbeth.BalanceChange_REASON_TRANSFER,
	tracing.BalanceIncreaseGenesisBalance:       pbeth.BalanceChange_REASON_GENESIS_BALANCE,
	tracing.BalanceDecreaseGasBuy:               pbeth.BalanceChange_REASON_GAS_BUY,
	tracing.BalanceIncreaseRewardTransactionFee: pbeth.BalanceChange_REASON_REWARD_TRANSACTION_FEE,
	tracing.BalanceIncreaseGasReturn:            pbeth.BalanceChange_REASON_GAS_REFUND,
	tracing.BalanceChangeTouchAccount:           pbeth.BalanceChange_REASON_TOUCH_ACCOUNT,
	tracing.BalanceIncreaseSelfdestruct:         pbeth.BalanceChange_REASON_SUICIDE_REFUND,
	tracing.BalanceDecreaseSelfdestruct:         pbeth.BalanceChange_REASON_SUICIDE_WITHDRAW,
	tracing.BalanceDecreaseSelfdestructBurn:     pbeth.BalanceChange_REASON_BURN,
	tracing.BalanceIncreaseWithdrawal:           pbeth.BalanceChange_REASON_WITHDRAWAL,

	tracing.BalanceChangeUnspecified: pbeth.BalanceChange_REASON_UNKNOWN,
}

func balanceChangeReasonFromChain(reason tracing.BalanceChangeReason) pbeth.BalanceChange_Reason {
	if r, ok := balanceChangeReasonToPb[reason]; ok {
		return r
	}

	panic(fmt.Errorf("unknown tracer balance change reason value '%d', check state.BalanceChangeReason so see to which constant it refers to", reason))
}

var gasChangeReasonToPb = map[tracing.GasChangeReason]pbeth.GasChange_Reason{
	tracing.GasChangeTxInitialBalance:              pbeth.GasChange_REASON_TX_INITIAL_BALANCE,
	tracing.GasChangeTxRefunds:                     pbeth.GasChange_REASON_TX_REFUNDS,
	tracing.GasChangeTxLeftOverReturned:            pbeth.GasChange_REASON_TX_LEFT_OVER_RETURNED,
	tracing.GasChangeCallInitialBalance:            pbeth.GasChange_REASON_CALL_INITIAL_BALANCE,
	tracing.GasChangeCallLeftOverReturned:          pbeth.GasChange_REASON_CALL_LEFT_OVER_RETURNED,
	tracing.GasChangeTxIntrinsicGas:                pbeth.GasChange_REASON_INTRINSIC_GAS,
	tracing.GasChangeCallContractCreation:          pbeth.GasChange_REASON_CONTRACT_CREATION,
	tracing.GasChangeCallContractCreation2:         pbeth.GasChange_REASON_CONTRACT_CREATION2,
	tracing.GasChangeCallCodeStorage:               pbeth.GasChange_REASON_CODE_STORAGE,
	tracing.GasChangeCallPrecompiledContract:       pbeth.GasChange_REASON_PRECOMPILED_CONTRACT,
	tracing.GasChangeCallStorageColdAccess:         pbeth.GasChange_REASON_STATE_COLD_ACCESS,
	tracing.GasChangeCallLeftOverRefunded:          pbeth.GasChange_REASON_REFUND_AFTER_EXECUTION,
	tracing.GasChangeCallFailedExecution:           pbeth.GasChange_REASON_FAILED_EXECUTION,
	tracing.GasChangeWitnessContractInit:           pbeth.GasChange_REASON_WITNESS_CONTRACT_INIT,
	tracing.GasChangeWitnessContractCreation:       pbeth.GasChange_REASON_WITNESS_CONTRACT_CREATION,
	tracing.GasChangeWitnessCodeChunk:              pbeth.GasChange_REASON_WITNESS_CODE_CHUNK,
	tracing.GasChangeWitnessContractCollisionCheck: pbeth.GasChange_REASON_WITNESS_CONTRACT_COLLISION_CHECK,
	tracing.GasChangeTxDataFloor:                   pbeth.GasChange_REASON_TX_DATA_FLOOR,

	// Ignored, we track them manually, [newGasChange] ensure that we panic if we see Unknown
	tracing.GasChangeCallOpCode: pbeth.GasChange_REASON_UNKNOWN,
}

func gasChangeReasonFromChain(reason tracing.GasChangeReason) pbeth.GasChange_Reason {
	if r, ok := gasChangeReasonToPb[reason]; ok {
		if r == pbeth.GasChange_REASON_UNKNOWN {
			panic(fmt.Errorf("tracer gas change reason value '%d' mapped to %s which is not accepted", reason, r))
		}

		return r
	}

	panic(fmt.Errorf("unknown tracer gas change reason value '%d', check vm.GasChangeReason so see to which constant it refers to", reason))
}

// evmStateReader adapts tracing.VMContext.StateDB to firehose.StateReader
type evmStateReader struct {
	evm *tracing.VMContext
}

func newEVMStateReader(evm *tracing.VMContext) firehose.StateReader {
	return &evmStateReader{evm: evm}
}

func (r *evmStateReader) GetCode(addr [20]byte) []byte {
	return r.evm.StateDB.GetCode(common.Address(addr))
}

func (r *evmStateReader) GetNonce(addr [20]byte) uint64 {
	return r.evm.StateDB.GetNonce(common.Address(addr))
}

func (r *evmStateReader) Exist(addr [20]byte) bool {
	return r.evm.StateDB.Exist(common.Address(addr))
}
