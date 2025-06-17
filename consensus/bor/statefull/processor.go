package statefull

import (
	"bytes"
	"context"
	"math"
	"math/big"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

var systemAddress = common.HexToAddress("0xffffFFFfFFffffffffffffffFfFFFfffFFFfFFfE")

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
			From:     systemAddress,
			Gas:      math.MaxUint64 / 2,
			GasPrice: big.NewInt(0),
			Value:    big.NewInt(0),
			To:       &toAddress,
			Data:     data,
		},
	}
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
) (uint64, error) {
	nonce := state.GetNonce(msg.From())
	expectedTx := types.NewTx(&types.LegacyTx{
		Nonce:    nonce,
		GasPrice: msg.GasPrice(),
		Gas:      msg.Gas(),
		To:       msg.To(),
		Value:    msg.Value(),
		Data:     msg.Data(),
	})
	signer := types.MakeSigner(chainConfig, header.Number, header.Time)
	expectedHash := signer.Hash(expectedTx)
	state.SetTxContext(expectedHash, 0)

	context := core.NewEVMBlockContext(header, chainContext, nil)

	// Create a new environment which holds all relevant information
	// about the transaction and calling mechanisms.
	evm := vm.NewEVM(context, state, chainConfig, vm.Config{Tracer: tracer})

	var tracingReceipt *types.Receipt
	if tracer != nil {
		if tracer.OnSystemTxStart != nil {
			tracer.OnSystemTxStart()
		}
		if tracer.OnTxStart != nil {
			tracer.OnTxStart(evm.GetVMContext(), expectedTx, msg.From())
		}

		// Defers are last in first out, so OnTxEnd will run before OnSystemTxEnd in this transaction,
		// which is what we want.
		if tracer.OnSystemTxEnd != nil {
			defer func() {
				tracer.OnSystemTxEnd()
			}()
		}
		if tracer.OnTxEnd != nil {
			defer func() {
				tracer.OnTxEnd(tracingReceipt, nil)
			}()
		}
	}

	// nolint : contextcheck
	// Apply the transaction to the current state (included in the env)
	ret, gasLeft, err := evm.Call(
		msg.From(),
		*msg.To(),
		msg.Data(),
		msg.Gas(),
		uint256.NewInt(msg.Value().Uint64()),
		nil,
	)
	gasUsed := msg.Gas() - gasLeft

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

	var root []byte
	tracingReceipt = types.NewReceipt(root, false, gasUsed)
	tracingReceipt.TxHash = expectedTx.Hash()
	tracingReceipt.GasUsed = gasUsed

	tracingReceipt.Logs = state.GetLogs(expectedTx.Hash(), header.Number.Uint64(), header.Hash())
	tracingReceipt.Bloom = types.CreateBloom(tracingReceipt)
	tracingReceipt.BlockHash = header.Hash()
	tracingReceipt.BlockNumber = header.Number
	tracingReceipt.TransactionIndex = uint(state.TxIndex())

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
		nil,
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
