## geth-v1.17.x-fh3.0

### Operator Notes

Due to the block model changes which we couldn't record in a backward compatible way, we had to put in place a block version 5 model. Ideally, we would activate the block version 5 at exact time of fork. Due to the impossibility to do so, what we ask from operators is to deploy the release as close as possible to the fork point, to reduce the impact.

We will check to see if we can provide some instructions about a potential deployment pattern with `firehose-core` for deploying the new version is a way that it will produce blocks only when the hard-fork activates, stay tuned.

### Changes

* Bump to geth [1.17.1](https://github.com/ethereum/go-ethereum/releases/tag/v1.17.1)

  This brings a new Ethereum Block version (`ver = 5` in the Firehose Ethereum Block protobuf model) that has the following semantic changes versus version 4:
  - `GasChanges` has been removed from the `Call` object and is not traced anymore, this was announced in January and is now effective in block version 5.
  - Bug fix where some `CodeChange` were emitted without a real code change (e.g. that `CodeChange.prev == CodeChange.new`), those are not emitted in block version 5.
  - Self destructs tracing is now handled drastically differently than in block version 4 fixing some bugs along the way.
    - While in block version 4 all self-destruct related changes (CodeChange, BalanceChange, etc) were all done at time of `SELFDESTRUCT` opcode, this is not true anymore in block version 5 where some changes are now deferred to when the transaction is finalized. This fixes some inconsistencies that could happened.

  Outside of `GasChanges`, there is no real differences in the actual output more around in which order some of the changes are emitted so everyone should be able to accept version 5 like if this was version 4.

## geth-v1.16.9-fh3.0

* Bump to geth [1.16.9](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.9)

## geth-v1.16.8-fh3.0

* Bump to geth [1.16.8](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.8)

## geth-v1.16.7-fh3.0

* Bump to geth [1.16.7](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.7), adjusted some of tracing changes made there to keep Firehose 3.0 model compatibility around `CodeChange`.

## geth-v1.16.5-fh3.0

* Bump to geth [1.16.5](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.5).

## geth-v1.16.4-fh3.0

* Bump to geth [1.16.4](https://github.com/ethereum/go-ethereum/releases/tag/v1.16.4).
