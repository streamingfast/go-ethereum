# Report 3: Block Import from Firehose gRPC

## Section 1: Introduction

Geth, the widely used Ethereum client, currently relies on a peer-to-peer (P2P) network to receive blockchain data. This decentralized approach maintains network resilience and consensus but comes with drawbacks. P2P data propagation can be slow due to network latency, peer availability, and the overhead of maintaining multiple connections. For applications that require fast and reliable access to blockchain data, these delays can become a bottleneck.

A potential improvement is to supplement or replace P2P data flow with a direct gRPC (gRPC Remote Procedure Call) connection. By streaming data through a dedicated, high-performance channel, Geth can bypass many P2P inefficiencies, resulting in lower latency, faster synchronization, and improved scalability for downstream services.
    
## Section 2: Background

Firehose, developed by StreamingFast, is a high-performance data streaming solution that provides real-time blockchain data with minimal latency. Unlike traditional P2P synchronization, Firehose establishes a direct gRPC connection to deliver structured blockchain data streams. This design enables rapid block propagation, efficient filtering, and the ability to handle high-throughput workloads without peer discovery overhead.

## Section 3: Method

### 3.1 Proposed Solution

The proposed solution introduces a new Geth command, `import-from-firehose`, allowing the client to synchronize blockchain data directly from a Firehose gRPC endpoint, bypassing the slower P2P network. When executed, the command connects to a Firehose service using an API token, streams blocks from a given block number, and processes them through a concurrent pipeline.

Blocks arrive in Firehose’s protocol buffer (protobuf) format and must be converted into native Geth `Block` structures before insertion into the blockchain database. A key limitation emerged: Firehose protobuf blocks do not include withdrawal details (`index` and validator `fields`). To fill this gap, an external RPC provider is queried for each block to retrieve missing withdrawal data. Using configurable worker pools and batch processing, the solution maintains high throughput while preserving correct block ordering.

### 3.2 Validation and Performance Metrics

#### Unit-Level Validation

Unit tests compared different types of block conversions from protobuf to Geth against expected outputs to confirm correctness.

#### Integration Testing

Integration tests used three Firehose endpoints: Sepolia, Holesky, and Hoodi. These tests ensured Geth could reliably import blocks without crashing, with internal validations like hash verification catching any inconsistencies.

Example command pattern:

```bash
FIREHOSE_API_TOKEN="" ./geth --datadir <dir> --<network> --vmtrace=firehose import-from-firehose <flags> <firehose-endpoint> <chainId> <start-block> <rpc-provider>
```

Sample Hoodi import:

```bash
FIREHOSE_API_TOKEN="" ./geth --datadir data/hoodi --hoodi --vmtrace=firehose import-from-firehose --batch-size 1000 --end-block 1000000 --worker-count 100 --firehose-buffer-size 100 hoodi.firehose.pinax.network:443 560048 1 "rpc-provider"
```

The results showed successful imports of up to 2,000,000 blocks on Sepolia, 65,000 blocks on Holesky, and 40,000 blocks on Hoodi. Performance varied significantly between networks: Sepolia imports were notably faster because the first two million blocks contained no withdrawals, eliminating the need for additional RPC calls to fill in missing withdrawal data. In contrast, Holesky and Hoodi required frequent external RPC lookups for withdrawal details, resulting in slower overall throughput.

#### Performance Benchmarking

The performance of the new command has been tested in two steps.

First, we verified the performance based on the number of concurrent workers.  
The `import-from-firehose` command was used on the first **1,000,000** blocks for the Sepolia network with **0**, **10**, and **100** concurrent workers.
The goal of this test is to observe the impact that concurrent flushing has on the speed of the block imports. 

```bash
FIREHOSE_API_TOKEN="" time ./geth \
  --datadir data/sepolia \
  --sepolia \
  --vmtrace=firehose \
  --vmtrace.jsonconfig='{"concurrentBlockFlushing":10}' \
  import-from-firehose \
  --batch-size 1000 \
  --worker-count 100 \
  --firehose-buffer-size 100 \
  --end-block 1000000 \
  sepolia.eth.streamingfast.io:443 11155111 1 <rpc-provider>
```

Second, we compared the performance of import-from-firehose against its initial implementation using a P2P connection.
This test was run on Holesky until block 6000. We stopped at block 7000 because starting there, withdrawals require external RPC providers, introducing noise into the measurements.
Here, we want to verify that importing blocks from firehose is indeed an improvement to the P2P implementation.

P2P test:
```bash
time geth \
  --vmtrace=firehose \
  --vmtrace.jsonconfig='{"concurrentBlockFlushing":10}' \
  --synctarget=0xa7a0dfd3cc7edc311ade93242ad3a3444d4285a0c350054c48355cc3401f8f52 \
  --syncmode=full \
  --holesky \
  --datadir=./geth \
  --db.engine=pebble \
  --state.scheme=path \
  --port=30305 \
  --authrpc.jwtsecret=jwt.txt \
  --authrpc.addr=0.0.0.0 \
  --authrpc.port=9551 \
  --authrpc.vhosts="*" \
  --http \
  --http.addr=0.0.0.0 \
  --http.api=eth,net,web3 \
  --http.port=9545 \
  --http.vhosts="*" \
  --port=40303 \
  --ws.port=9546 \
  --ipcpath=/tmp/geth.ipc > /dev/null
```

Firehose gRPC test:
```bash
FIREHOSE_API_TOKEN="" time ./geth \
  --datadir data/holesky \
  --holesky \
  --vmtrace=firehose \
  --vmtrace.jsonconfig='{"concurrentBlockFlushing":10}' \
  import-from-firehose \
  --batch-size 1000 \
  --worker-count 100 \
  --firehose-buffer-size 100 \
  --end-block 6000 \
  holesky.eth.streamingfast.io:443 17000 1 <rpc provider> > /dev/null
```

## Section 4: Analysis

The specifications of the operating system used for testing are as follows:

Model: Macbook Air \
Processor: Apple M1 chip \
Memory: 8 GB

Geth version used is 1.16.0-stable

### Import Performance for 1,000,000 Sepolia Blocks

#### 0 Workers
| Run | Real (s) | User (s) | Sys (s) |
|-----|----------|----------|---------|
| 1   | 644.22   | 444.46   | 233.62  |
| 2   | 571.44   | 425.13   | 216.28  |
| 3   | 556.86   | 414.16   | 212.33  |

#### 10 Workers
| Run | Real (s) | User (s) | Sys (s) |
|-----|----------|----------|---------|
| 1   | 385.26   | 240.65   | 116.90  |
| 2   | 333.96   | 235.69   | 116.20  |
| 3   | 381.42   | 239.68   | 114.52  |

#### 100 Workers
| Run | Real (s) | User (s) | Sys (s) |
|-----|----------|----------|---------|
| 1   | 340.99   | 238.91   | 117.93  |
| 2   | 341.67   | 241.39   | 119.21  |
| 3   | 411.02   | 244.40   | 119.64  |

#### Table 1: Average time to import 1,000,000 blocks from Sepolia

| Routines | Real (s) | User (s) | Sys (s) |
|----------|----------|----------|---------|
| 0        | 590.84   | 427.92   | 220.74  |
| 10       | 366.88   | 238.67   | 115.87  |
| 100      | 364.56   | 199.98   | 118.93  |

**Interpretation:**  
Table 1 suggest that concurrency improves performance, reducing import time by ~224 seconds when using 10 workers. Increasing beyond 10 workers yields minimal additional benefit.

---

### Holesky Import Performance (6,000 Blocks)

#### P2P
| Run | Real (s) | User (s) | Sys (s) |
|-----|----------|----------|---------|
| 1   | 35.743   | 90.26    | 5.78    |
| 2   | 35.995   | 88.93    | 5.67    |
| 3   | 35.597   | 95.03    | 7.33    |

#### Firehose gRPC
| Run | Real (s) | User (s) | Sys (s) |
|-----|----------|----------|---------|
| 1   | 27.67    | 79.59    | 3.88    |
| 2   | 29.37    | 82.23    | 4.38    |
| 3   | 29.23    | 82.48    | 4.20    |

#### Table 2: Average time to import 6,000 blocks

| Configuration   | Real (s) | User (s) | Sys (s) |
|-----------------|----------|----------|---------|
| P2P             | 35.78    | 91.41    | 6.26    |
| Firehose gRPC   | 28.76    | 81.43    | 4.15    |

**Interpretation:**  
Table 2 shows that Firehose gRPC outperforms P2P, saving ~7 seconds (~20%) for 6,000 blocks.


## Section 5 Conclusion
The import-from-firehose command demonstrates faster performance than the traditional P2P sync, with an average improvement of about 1.17 seconds per 1,000 blocks. Concurrent block flushing further improves throughput, though increasing worker counts beyond 10 has little effect.

### Limitations
- Block data from Firehose lacks withdrawal details, requiring additional RPC lookups that can bottleneck performance.

### Future Work
- Optimize retrieval of missing withdrawal data by including those fields in the protobuf blocks.
- Benchmark on other hardware and larger datasets to evaluate scalability.
- Investigate further pipeline optimizations to fully utilize available concurrency.
