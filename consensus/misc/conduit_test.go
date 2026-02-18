package misc

import (
	"encoding/json"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// TestApplyStateOverrideForks
func TestApplyStateOverrideForks(t *testing.T) {
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	require.NoError(t, err)

	var cfg params.ChainConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"stateOverrideForks": [
			{
			"time": 1,
			"overrides": {
				"0x4200000000000000000000000000000000000000": {
				"nonce": "0x42",
				"balance": "0x56bc75e2d63100000"
				}
			}
			}
		]
	}`), &cfg))

	t.Run("target doesn't exist", func(t *testing.T) {
		ApplyStateOverrideForks(statedb, &cfg, 0, 0)
		require.Equal(t, types.EmptyRootHash, statedb.IntermediateRoot(false))
	})

	targetAddr := common.HexToAddress("0x4200000000000000000000000000000000000000")
	statedb.CreateAccount(targetAddr)
	require.Zero(t, statedb.GetNonce(targetAddr))

	t.Run("not yet", func(t *testing.T) {
		ApplyStateOverrideForks(statedb, &cfg, 0, 0)
		require.Zero(t, statedb.GetNonce(targetAddr))
	})

	t.Run("trigger fork", func(t *testing.T) {
		ApplyStateOverrideForks(statedb, &cfg, 0, 1)
		require.Equal(t, statedb.GetNonce(targetAddr), uint64(0x42))
		bal, err := uint256.FromHex("0x56bc75e2d63100000")
		require.NoError(t, err)
		require.Equal(t, statedb.GetBalance(targetAddr), bal)
	})

	statedb.SetNonce(targetAddr, 37, tracing.NonceChangeUnspecified)
	require.Equal(t, statedb.GetNonce(targetAddr), uint64(37))

	t.Run("past fork", func(t *testing.T) {
		ApplyStateOverrideForks(statedb, &cfg, 1, 1)
		require.Equal(t, statedb.GetNonce(targetAddr), uint64(37))

		ApplyStateOverrideForks(statedb, &cfg, 1, 2)
		require.Equal(t, statedb.GetNonce(targetAddr), uint64(37))
	})
}
