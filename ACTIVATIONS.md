# Activations

Every fork the Polygon PoS chain configs in `params/config.go` define, with its status on Polygon mainnet and Amoy and its effect on the Firehose block. Bor activates forks by block number, so a fork is active once the chain is past its block. Statuses were checked on 2026-11-06 against bor `v2.11.0` (`polygon-v2.11.0-fh3.0`).

Review the entries that are new or whose block changed at every upstream bump, and refresh this file when Polygon announces a fork block between bumps.

## Bor forks (`BorConfig`)

| Fork | Mainnet | Amoy | Effect on the Firehose block |
|------|---------|------|------------------------------|
| Jaipur | Active, #23850000 | Active, #73100 | None. The seal hash covers the base fee. |
| Delhi | Active, #38189056 | Active, #73100 | None. Changes the sprint length, the producer delay and the base fee change denominator. |
| Indore | Active, #44934656 | Active, #73100 | None. `LastStateId` is read from the current state, and state syncs are fetched up to a delay before the block time instead of up to the previous sprint. |
| Ahmedabad | Active, #62278656 | Active, #11865856 | Block-level code changes on `0x…1001` (StateReceiver) and `0x…1010` (MRC20) at the fork block, through `BlockAlloc`. |
| Bhilai | Active, #73440256 | Active, #22765056 | None. Changes the base fee change denominator and the producer timing. |
| Rio | Active, #77414656 | Active, #26272256 | No more span commit system transactions: spans come from Heimdall directly, so sprint-start blocks stop carrying the synthesized `commitSpan` transaction. |
| Madhugiri | Active, #80084800 | Active, #28899616 | State syncs are a real `StateSyncTx` (type `0x7f`) at the end of the block body instead of being implied. The tracer emits it with the hash RPC serves and the receipt from `OnStateSyncReceipt`. |
| MadhugiriPro | Active, #80084800 | Active, #29287400 | None. Adds the P256 precompile, which only shows up as calls to `0x…0100`. |
| Dandeli | Active, #81424000 | Active, #31890000 | None. Changes the base fee computation; the header base fee is copied as is. |
| Lisovo | Active, #83756500 | Active, #33634700 | None. The producer picks the base fee within a bound; the header base fee is copied as is. |
| LisovoPro | Active, #83756500 | Active, #34062000 | None. Removes the KZG point evaluation precompile. |
| Giugliano | Active, #85268500 | Active, #35573500 | None. Gas target and base fee change denominator move into the header extra data, which the block copies as raw bytes. |
| Chicago | Active, #87218600 | Active, #38358000 | None. New precompile set and jump table; calls trace as usual. |
| Valencia | Active, #89531000 | Active, #40776000 | None for the block model. Caps the state-sync bytes per block, so some records move to a later sprint. |
| Austin | Active, #91949700 | Active, #44120000 | None for the block model. Caps the state-sync gas per block and resets the access list before each state-sync record. The header extra data drops `TxDependency`. |
| Hampi | Not scheduled (`nil`) | Not scheduled (`nil`) | An `SSTORE` that cannot pay for the slot access fails before reading the slot. Gas used and the failed call are unchanged, but the call's failure reason becomes `out of gas: not enough gas for slot access` instead of `out of gas`. No new field or type. |
| Uburu | Active, #96787900 | Active, #51978200 | Block-level code change on `0x…1001` (StateReceiver) at the fork block, through `BlockAlloc`; the fork block is not a sprint start on either network. After it, the StateReceiver gives each receiver a bounded amount of gas (from a gas oracle, 160,000 without one) instead of 5,000,000, and stores the hash of each failed record in its own storage. State-sync call trees then show different gas on the receiver call, new storage writes on `0x…1001` for failed records and, when a gas oracle is set, a call to it. No new field or type. |

## Ethereum forks

| Fork | Mainnet | Amoy | Effect on the Firehose block |
|------|---------|------|------------------------------|
| Homestead to Petersburg | Active, #0 | Active, #0 | None. |
| Istanbul, Muir Glacier | Active, #3395000 | Active, #0 | None. |
| Berlin | Active, #14750000 | Active, #0 | Access list transactions (type 1). |
| London | Active, #23850000 | Active, #73100 | Dynamic fee transactions (type 2) and the header base fee. The burnt fee goes to the Polygon burn contract (`BalanceChangePolygonBurn`). |
| Shanghai | Active, #50523000 | Active, #73100 | None: Bor rejects withdrawals. |
| Cancun | Active, #54876000 | Active, #5423600 | None: Bor has no blob schedule and carries no blob transactions or blob header fields. |
| Prague | Active, #73440256 | Active, #22765056 | Set code transactions (type 4, EIP-7702) with their authorizations. Bor rejects requests. |
| Osaka | Not scheduled (`nil`) | Not scheduled (`nil`) | Not reviewed for Bor yet: nothing in the Bor configs schedules it. |
| Verkle | Not scheduled (`nil`) | Not scheduled (`nil`) | Not applicable. |
