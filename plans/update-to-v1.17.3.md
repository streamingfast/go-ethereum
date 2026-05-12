# Merge Plan: go-ethereum v1.17.1 -> v1.17.3 (Ethereum/Firehose)

## Context
- Repository: streamingfast/go-ethereum
- Base branch: release/geth-v1.17.x-fh3.0
- Last merged upstream version: v1.17.1 (tag: geth-v1.17.1-fh3.0 implied by commit 49dea58942)
- Target upstream version: v1.17.3
- Working branch: release/geth-v1.17.3-fh3.0

## Key Upstream Changes (v1.17.1 -> v1.17.3) - 215 commits, 382 files

### Critical Breaking API Changes
1. **TransferFunc signature change**: `TransferFunc func(StateDB, common.Address, common.Address, *uint256.Int)` -> `TransferFunc func(StateDB, common.Address, common.Address, *uint256.Int, *params.Rules)`
2. **GasBudget type**: EVM methods (Call, CallCode, DelegateCall, StaticCall, create) now use `GasBudget` instead of `uint64` for gas parameters
3. **IsVerkle -> IsUBT rename**: `chainRules.IsVerkle` renamed to `chainRules.IsUBT`
4. **StateDB.Finalise()**: Now returns `*bal.StateAccessList` instead of void
5. **StateDB.GetStorageRoot removed** from hookedStateDB
6. **StateDB.Touch added** to hookedStateDB
7. **StateDB.LogsForBurnAccounts added** to hookedStateDB
8. **core.Message now uses uint256**: `core.Message` gas price fields changed
9. **BlockChainConfig**: `ChainHistoryMode` -> `HistoryPolicy`; Added `BinTrieGroupDepth`, `StatelessSelfValidation`, `EnableWitnessStats`
10. **EVM arena/stackArena**: Added stack arena for memory pooling
11. **EVM.Release()**: New method to return allocated memory
12. **ForkchoiceUpdated**: All variants now take `context.Context` as first parameter

### Files with Conflicts (changed in both upstream and our Firehose fork)
1. `build/ci.go` - minor
2. `cmd/geth/main.go` - minor, flag additions
3. `cmd/utils/flags.go` - OverrideVerkle -> OverrideUBT, new flags
4. `core/blockchain.go` - ChainHistoryMode -> HistoryPolicy, CodeDB, significant changes
5. `core/state/statedb_hooked.go` - GetStorageRoot removed, Finalise return type changed
6. `core/tracing/hooks.go` - minor comment changes + our OnKeccakPreimage hook
7. `core/vm/evm.go` - GasBudget, TransferFunc, IsVerkle->IsUBT - significant!
8. `core/vm/instructions.go` - stack.push -> stack.get() optimization
9. `eth/catalyst/api.go` - ForkchoiceUpdated context.Context param, our Firehose extension
10. `eth/catalyst/simulated_beacon.go` - ForkchoiceUpdated signature
11. `eth/fetcher/tx_fetcher.go` - minor
12. `go.mod` / `go.sum` - dependency updates

## Merge Status

### Branch Creation
- [ ] Create branch release/geth-v1.17.3-fh3.0 from release/geth-v1.17.x-fh3.0
- [ ] Merge upstream tag v1.17.3

### Conflict Resolution
- [ ] go.mod / go.sum
- [ ] core/blockchain.go
- [ ] core/state/statedb_hooked.go
- [ ] core/tracing/hooks.go
- [ ] core/vm/evm.go
- [ ] core/vm/instructions.go
- [ ] cmd/utils/flags.go
- [ ] cmd/geth/main.go
- [ ] eth/catalyst/api.go
- [ ] eth/catalyst/simulated_beacon.go
- [ ] eth/fetcher/tx_fetcher.go
- [ ] build/ci.go

### Build & Tests
- [ ] go build ./...
- [ ] go test ./eth/...
- [ ] go test ./core/...
- [ ] Battlefield tests
