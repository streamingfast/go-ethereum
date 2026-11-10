## polygon-v2.11.0-fh3.0

* Bumped Polygon bor to [v2.11.0](https://github.com/0xPolygon/bor/releases/tag/v2.11.0), with the Uburu hard fork (mainnet block `96787900`, Amoy block `51978200`). From Uburu on, state-sync receivers get less gas and the StateReceiver stores the hash of each failed record, which shows in the traced state-sync calls.
* A failed state-sync fetch from Heimdall now fails the block, which is retried, instead of committing it without state syncs.

## polygon-v2.10.3-fh3.0

* Bumped Polygon bor to [v2.10.3](https://github.com/0xPolygon/bor/releases/tag/v2.10.3).

## polygon-v2.10.2-fh3.0-2

* Fixed a panic on traced state-sync commits that read `GASPRICE`: the state-sync transaction context is now set when a tracer is attached too. This crashed Firehose nodes on Amoy at block `50016192`.

## polygon-v2.10.2-fh3.0-1

* Moved the Polygon Firehose instrumentation to the shared tracer [evm-firehose-tracer-go/v5](https://github.com/streamingfast/evm-firehose-tracer-go). Polygon-specific handling stays in the bor adapter: forced system transaction hashes, merging of system transactions, the state-sync receipt, fee transfer logs, `BalanceChangePolygonBurn` and block-number fork activation. Output changes:
  - Gas changes are no longer recorded; blocks are `ver: 5`.
  - No-op balance, nonce, code and storage changes (old == new) are no longer recorded.
  - Keccak preimages only keep those of at most 256 bytes that explain a storage slot written by the transaction.
  - The pre-Prague backward compatibility mode (`ver: 3` on Polygon mainnet, Amoy and Mumbai) is gone. Reprocessing pre-Prague history gives `ver: 5` blocks.
  - Protocol 3.1 lines: `FIRE INIT 3.1 firehose-evm-tracer/geth …` and a flash block index field (always 0) on `FIRE BLOCK`. Needs fireeth v2.14.4 or later.
  - LIB never trails the block by more than 200 and never exceeds the block number.
  - An output write that keeps failing now panics instead of dropping the block.

## polygon-v2.10.2-fh3.0

* Bumped Polygon bor to [v2.10.2](https://github.com/0xPolygon/bor/releases/tag/v2.10.2). Span-commit and state-sync system transactions now get the tracer of the state they are applied to, so the live tracer only sees them during block import, not from `eth_simulateV1` or historical state regeneration.

## polygon-v2.10.1-fh3.0

* Bumped Polygon bor to [v2.10.1](https://github.com/0xPolygon/bor/releases/tag/v2.10.1), which brings BlockSTM V2 parallel execution (default `parallelevm.enable=true`).
* Restored the live tracer safety from `polygon-v2.9.1-fh3.0`: when a live tracer (e.g. Firehose) is active, ParallelEVM is disabled so execution stays serial, `parallelevm.enforce=true` fails node startup, and `ProcessBlock` skips the parallel processor when `vm.Config.Tracer` is set.

## polygon-v2.10.0-fh3.0

* Bumped Polygon bor to [v2.10.0](https://github.com/0xPolygon/bor/releases/tag/v2.10.0), with the Austin hard fork (mainnet block `91949700`, Amoy block `44120000`). Austin caps the state-sync gas per block and resets the access list before each state-sync record. The header extra data drops `TxDependency`.
* Reverted the v2.9.1 merge: v2.9.1 was never released upstream and v2.10.0 is based on v2.9.0, so this release no longer carries BlockSTM V2 from `polygon-v2.9.1-fh3.0`.

## polygon-v2.9.1-fh3.0

* Bumped Polygon (bor) to upstream [v2.9.1](https://github.com/0xPolygon/bor/releases/tag/v2.9.1), which introduces BlockSTM V2 parallel execution (default `parallelevm.enable=true`).
* Live tracer safety: when a live tracer (e.g. Firehose) is active, ParallelEVM / BlockSTM V2 is automatically disabled so execution stays serial and tracing works. If `parallelevm.enforce=true` is also set, node startup fails with a clear error instead of running an unsupported combination.
* ProcessBlock defense-in-depth: skip the parallel processor when `vm.Config.Tracer` is set.
* Preserved Firehose balance-change reasons (`BalanceChangePolygonBurn`, `BalanceIncreaseRewardTransactionFee`) on the serial and delayed-fee paths after the V2 refactor.
