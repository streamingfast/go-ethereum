package misc

import (
	"math/big"

	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
)

// ApplyStateOverrideForks
func ApplyStateOverrideForks(statedb vm.StateDB, cfg *params.ChainConfig, parentTime, time uint64) {
	for index, fork := range cfg.StateOverrideForks {
		if fork.Time == nil || parentTime >= *fork.Time || time < *fork.Time {
			continue
		}

		log.Info("Activating StateOverrideFork", "index", index, "time", *fork.Time)
		for addr, account := range fork.Overrides {
			// no account/contract to override
			if !statedb.Exist(addr) {
				continue
			}

			// Override account nonce.
			if account.Nonce != nil {
				statedb.SetNonce(addr, uint64(*account.Nonce), tracing.NonceChangeUnspecified)
			}
			// Override account(contract) code.
			if account.Code != nil {
				statedb.SetCode(addr, *account.Code, tracing.CodeChangeUnspecified)
			}
			// Override account balance.
			if account.Balance != nil {
				u256Balance, _ := uint256.FromBig((*big.Int)(account.Balance))
				statedb.SetBalance(addr, u256Balance, tracing.BalanceChangeUnspecified)
			}
			// Apply state diff into specified accounts.
			if account.StateDiff != nil {
				for key, value := range account.StateDiff {
					statedb.SetState(addr, key, value)
				}
			}
		}
	}
}
