## polygon-v2.9.1-fh3.0

* Bumped Polygon (bor) to upstream [v2.9.1](https://github.com/0xPolygon/bor/releases/tag/v2.9.1), which introduces BlockSTM V2 parallel execution (default `parallelevm.enable=true`).
* Live tracer safety: when a live tracer (e.g. Firehose) is active, ParallelEVM / BlockSTM V2 is automatically disabled so execution stays serial and tracing works. If `parallelevm.enforce=true` is also set, node startup fails with a clear error instead of running an unsupported combination.
* ProcessBlock defense-in-depth: skip the parallel processor when `vm.Config.Tracer` is set.
* Preserved Firehose balance-change reasons (`BalanceChangePolygonBurn`, `BalanceIncreaseRewardTransactionFee`) on the serial and delayed-fee paths after the V2 refactor.
