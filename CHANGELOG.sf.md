## geth-v1.16.9-fh3.0-2

* Fixed the Firehose block header "LIB" (last irreversible / finalized block) which regressed to `0` in `geth-v1.16.9-fh3.0-1`, after the tracer was extracted to the shared [`evm-firehose-tracer-go`](https://github.com/streamingfast/evm-firehose-tracer-go) library that hardcoded the header's `lib_num` field. Bumped the shared tracer to [v4.0.5](https://github.com/streamingfast/evm-firehose-tracer-go/releases/tag/v4.0.5), which derives the LIB from the block's finalized reference and falls back to `blockNum - 200` when finality is unknown (tunable, or disabled with `0`, via the `FORCE_FINALIZED_BLOCK_ABOVE_THRESHOLD` environment variable).

## geth-v1.16.9-fh3.0-1

* Bump to geth [1.16.9](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.9)

## geth-v1.16.8-fh3.0

* Bump to geth [1.16.8](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.8)

## geth-v1.16.7-fh3.0

* Bump to geth [1.16.7](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.7), adjusted some of tracing changes made there to keep Firehose 3.0 model compatibility around `CodeChange`.

## geth-v1.16.5-fh3.0

* Bump to geth [1.16.5](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.5).

## geth-v1.16.4-fh3.0

* Bump to geth [1.16.4](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.4).
