## polygon-v2.11.0-fh3.0

* Bumped Polygon bor to [v2.11.0](https://github.com/0xPolygon/bor/releases/tag/v2.11.0), with the Uburu hard fork (mainnet block `96787900`, Amoy block `51978200`). From Uburu on, state-sync receivers get less gas and the StateReceiver stores the hash of each failed record, which shows in the traced state-sync calls.
* A failed state-sync fetch from Heimdall now fails the block, which is retried, instead of committing it without state syncs.
