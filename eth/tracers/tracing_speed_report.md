# Test Report: Blockchain Sync Performance Analysis

## Section 1: Introduction

The primary objective of this summer internship was to develop alternatives to the conventional Geth syncing mechanism. These alternatives include an `import-from-firehose` command that retrieves blocks directly from a Firehose endpoint and a `Firehose poller` that sources blocks from an archive node. This report evaluates the tracing speeds of these methods alongside the standard Geth sync process to identify the most efficient approach in terms of synchronization performance.

## Section 2: Background

### Geth Sync
Geth (Go Ethereum) serves as the official Ethereum client implemented in Go. The standard synchronization process entails establishing connections with peers via the peer-to-peer (P2P) network, sequentially downloading blocks, validating them, and persisting state and transaction data. Various synchronization modes exist, including full, fast, and snap modes, where full synchronization involves retrieving all blocks from the genesis block and re-executing every transaction.

A key limitation of this approach is its dependence on P2P connections, which impose network latency and constrain throughput. The sequential nature of block retrieval and local verification can extend synchronization durations from several hours to days, contingent on network conditions.

The alternatives examined in this report circumvent the P2P layer, enabling accelerated block ingestion and tracing.

### Firehose
Firehose is a streaming service designed for efficient blockchain data delivery, supplying blocks in a structured Protocol Buffers (protobuf) format via gRPC. Direct integration with a Firehose endpoint allows nodes to acquire blocks independent of P2P propagation delays.

Firehose-delivered blocks are protobuf-encoded, necessitating conversion to align with Geth's native block format. Initially, these protobuf blocks lacked withdrawal fields from recent Ethereum hard forks, requiring supplementary RPC calls to an external provider. As part of this internship, enhancements were implemented to incorporate withdrawal data directly into protobuf blocks, obviating external RPC dependencies and enhancing the reliability and completeness of Firehose ingestion.

### Archive Node
An archive node maintains the complete historical record of the Ethereum blockchain, encompassing all states at every block height. Utilizing a Firehose poller to query an archive node facilitates efficient block retrieval without P2P synchronization. The poller supports batched fetching and streaming for subsequent tracing or analytical processing.

A notable challenge was the scarcity and rate-limiting of public archive nodes, prompting the establishment of a local archive node. This setup enabled uninterrupted testing and benchmarking of the Firehose poller, allowing precise measurement of block retrieval efficacy and tracing performance without reliance on external infrastructure.

## Section 3: Method

### 3.1 Proposed Solution

To ascertain the optimal synchronization method, each approach was evaluated under comparable environmental conditions. Tests were conducted on the Hoodi network up to block 100,000, selected for its inclusion of recent hard forks, such as withdrawals, ensuring comprehensive validation of implemented modifications.

1. Baseline Full Node Synchronization (No Tracing): A full node was synchronized without VM tracing to establish baseline performance and quantify tracing-induced overhead. The command executed was:

```bash
time ./geth \
--synctarget=0x3ae141c1953b0c973cafcd3424761d11584b15fb05f68b6caab25d723cfd6bc6 \
  --syncmode=full \
  --hoodi \
  --datadir=data/test-full-node-no-concurrency \
  --db.engine=pebble \
  --state.scheme=path \
  --history.state=0 \
  --authrpc.jwtsecret=jwt.txt \
  --authrpc.addr=0.0.0.0 \
  --authrpc.port=10551 \
  --authrpc.vhosts="*" \
  --http \
  --http.addr=0.0.0.0 \
  --http.api=eth,net,web3,debug \
  --http.port=10545 \
  --http.vhosts="*" \
  --port=40304 \
  --ws.port=10546 \
  --ipcpath=/tmp/geth.ipc \
  > /dev/null
```

2. Full Node Synchronization with VM Tracing and Concurrency: Synchronization was performed with VM tracing enabled and concurrent block flushing (set to 10 workers) to optimize flushing operations. The command executed was:

```bash
time ./geth \
  --vmtrace=firehose \
  --vmtrace.jsonconfig='{"concurrentBlockFlushing":10}' \
  --synctarget=0x3ae141c1953b0c973cafcd3424761d11584b15fb05f68b6caab25d723cfd6bc6 \
  --syncmode=full \
  --hoodi \
  --datadir=data/test-full-node-concurrency \
  --db.engine=pebble \
  --state.scheme=path \
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
  --ipcpath=/tmp/geth.ipc \
  > /dev/null
```

3. Import from Firehose: The import-from-firehose command was invoked using an endpoint incorporating the withdrawal fix, with batch processing and parallel workers for enhanced throughput. The command executed was:

```bash
FIREHOSE_API_TOKEN="<token>" \
time ./geth \
  --datadir data/test-import-from-firehose \
  --hoodi \
  import-from-firehose \
    --batch-size 100 \
    --end-block 100000 \
    --worker-count 100 \
    --firehose-buffer-size 100 \
    hoodi.eth.streamingfast.io:443 \
    560048 \
  > /dev/null
```

4. Archive Node Poller: The Firehose poller was executed against a local archive node for block retrieval. The command executed was:

```bash
time ./fireeth -c '' start reader-node,merger \
  --reader-node-path=./fireeth \
  --reader-node-arguments="tools poller firehose-tracer-api http://localhost:10545 0 \
    --interval-between-fetch=100ms \
    --max-block-fetch-duration=3s" \
  --reader-node-grpc-listen-addr=:11010 \
  --reader-node-manager-api-addr=:11011 \
  --merger-grpc-listen-addr=:10013 \
  --common-one-block-store-url=./data/test-poller/storage/one-blocks \
  --common-merged-blocks-store-url=./data/test-poller/storage/merged-blocks \
  --common-forked-blocks-store-url=./data/test-poller/storage/forked-blocks
```

### 3.2 Validation and Performance Metrics

The primary performance metric was synchronization duration, measured using the time command from initiation until reaching block 100,000. Lower durations indicate superior efficiency.

## Section 4: Analysis

### 4.1 Results

The specifications of the operating system used for testing are as follows:

Model: Macbook Air \
Processor: Apple M1 chip \
Memory: 8 GB

The observed results are summarized below:

**Test #1: Full Node Synchronization (No VM Tracing)**

```
real    2h 4min 12s
user    4h 45min 55s
sys     9min 44s
```

**Test #2: Full Node Synchronization (With VM Tracing)**

```
real    16h 53min 5s
user    24h 41min 44s
sys     36min 27s
```

**Test #3: Import from firehose**

```
elapsed 2h 18min 3s
user 4h 50min 9s
system 9min 14s
CPU 216%
```

**Test #4: Archive node poller**

```
real    16h 54min 22s
user    15min 2s
sys     10min 29s
```

### 4.2 Discussion

The results demonstrate a contrast in performance across the tested synchronization methods. Test 1, the baseline full node synchronization without VM tracing, completed in approximately 124 minutes (roughly 2 hours), establishing a reference for optimal performance absent tracing overhead. In contrast, Test 2, which included VM tracing, required over 1013 minutes (nearly 17 hours), highlighting tracing as a significant performance bottleneck, even with concurrency optimizations (10 concurrent block flushers).

Test 3, utilizing the import-from-firehose command, demonstrated a substantial improvement, completing in approximately 138 minutes (just over 2 hours and 18 minutes). This represents a dramatic reduction in synchronization time compared to Test 2, underscoring the efficacy of bypassing the P2P layer and leveraging Firehose's parallelized block ingestion via gRPC. The high CPU utilization (216%) indicates efficient resource usage, likely driven by the configured batch size (100) and worker count (100), which maximized throughput.

Test 4, employing the archive node poller, yielded the least favorable performance, requiring over 1014 minutes (nearly 17 hours), comparable to Test 2. Despite bypassing P2P networking, the poller's performance was hindered, potentially by bottlenecks in local archive node access or inefficiencies in the polling mechanism, such as the 100ms interval between fetches or the 3-second maximum block fetch duration.

Operational considerations further contextualize these results. Maintaining a local archive node, as required for Test 4, entails significant setup complexity and resource demands, rendering it less practical for widespread adoption. Conversely, the Firehose approach (Test 3) benefits from a managed, scalable infrastructure, simplifying access to historical data and reducing operational overhead.

## Section 5: Conclusion

From the results obtained, we can conclude that:

- Baseline sync without tracing (Test #1) is relatively fast and serves as a useful reference point, completing in around two hours.
- Syncing with vmtrace enabled (Test #2) introduces a dramatic slowdown, showing that tracing speed is currently the most significant bottleneck in geth.
- Import-from-firehose (Test #3) and polling from an archive node (Test #4) offer promising alternatives to traditional sync by removing reliance on P2P networking. While final timing data is pending, both approaches are expected to outperform Test #2, with firehose showing the highest potential due to parallel block ingestion.
- Practical trade-offs exist: running an archive node is costly and operationally complex, whereas firehose endpoints provide an efficient, managed alternative that can scale with demand.

Overall, this study demonstrates that while traditional geth sync is reliable, tracing performance must be improved for large-scale analysis. Firehose-based methods appear to be the most efficient path forward, both in terms of speed and developer usability.
