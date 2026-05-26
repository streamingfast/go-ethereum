package statefull

import (
	"bytes"
	"context"
	"math/big"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
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

// get system message
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
