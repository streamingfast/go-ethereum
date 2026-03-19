package main

import (
	"encoding/hex"
	"encoding/json"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	pbeth "github.com/streamingfast/firehose-ethereum/types/pb/sf/ethereum/type/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestConvertFirehoseBlockToGethBlock(t *testing.T) {
	tests := []struct {
		name    string
		pbBlock *pbeth.Block
		wantErr bool
	}{
		{
			name:    "nil block",
			pbBlock: nil,
			wantErr: true,
		},
		{
			name: "nil header",
			pbBlock: &pbeth.Block{
				Header: nil,
			},
			wantErr: true,
		},
		{
			name: "valid block",
			pbBlock: &pbeth.Block{
				Header: &pbeth.BlockHeader{
					ParentHash:       hash32("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"),
					UncleHash:        hash32("0x02030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021"),
					Coinbase:         addressBytes("0x030405060708090a0b0c0d0e0f10111213141516"),
					StateRoot:        hash32("0x0405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20212223"),
					TransactionsRoot: hash32("0x05060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2021222324"),
					ReceiptRoot:      hash32("0x060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425"),
					LogsBloom:        make([]byte, 256),
					Difficulty:       &pbeth.BigInt{Bytes: []byte{100}},
					Number:           12345,
					GasLimit:         30000000,
					GasUsed:          15000000,
					Timestamp:        timestamppb.New(time.Unix(1634952202, 0)),
					ExtraData:        hexBytes("0x08090a"),
					MixHash:          hash32("0x090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f202122232425262728"),
					Nonce:            123456789,
					BaseFeePerGas:    &pbeth.BigInt{Bytes: big.NewInt(20000000000).Bytes()},
					WithdrawalsRoot:  hash32("0x65666768696a6b6c6d6e6f707172737475767778797a7b7c7d7e7f8081828384"),
					BlobGasUsed:      new(uint64),
					ExcessBlobGas:    new(uint64),
					ParentBeaconRoot: hash32("0xc9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedfe0e1e2e3e4e5e6e7e8"),
					RequestsHash:     hash32("0x9798999a9b9c9d9e9fa0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6"),
				},
				TransactionTraces: []*pbeth.TransactionTrace{
					{
						Type:     0, // LegacyTx
						Hash:     hash32("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"),
						Nonce:    0,
						GasPrice: &pbeth.BigInt{Bytes: big.NewInt(20000000000).Bytes()},
						GasLimit: 21000,
						To:       addressBytes("0x0b0c0d0e0f101112131415161718191a1b1c1d1e"),
						Value:    &pbeth.BigInt{Bytes: big.NewInt(1000000000000000000).Bytes()},
						Input:    []byte{},
						V:        hexBytes("0x1b"),
						R:        hexBytes("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"),
						S:        hexBytes("0x2122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f40"),
						Status:   1,
						GasUsed:  21000,
						Receipt: &pbeth.TransactionReceipt{
							StateRoot:         hash32("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"),
							CumulativeGasUsed: 21000,
							LogsBloom:         make([]byte, 256),
							Logs:              []*pbeth.Log{},
						},
						MaxPriorityFeePerGas:  &pbeth.BigInt{Bytes: []byte{0}},
						MaxFeePerGas:          &pbeth.BigInt{Bytes: []byte{0}},
						AccessList:            []*pbeth.AccessTuple{},
						BlobGasFeeCap:         &pbeth.BigInt{Bytes: []byte{0}},
						BlobHashes:            [][]byte{},
						SetCodeAuthorizations: []*pbeth.SetCodeAuthorization{},
					},
				},
				Uncles: []*pbeth.BlockHeader{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, err := convertFirehoseBlockToGethBlock(tt.pbBlock, big.NewInt(0), "")
			if (err != nil) != tt.wantErr {
				t.Errorf("convertFirehoseBlockToGethBlock() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && block == nil {
				t.Error("convertFirehoseBlockToGethBlock() returned nil block when no error expected")
			}

			// Only do golden file comparison for the valid block case
			if !tt.wantErr {
				type blockForGolden struct {
					Header       *types.Header
					Transactions []*types.Transaction
					Uncles       []*types.Header
				}
				golden := blockForGolden{
					Header:       block.Header(),
					Transactions: block.Transactions(),
					Uncles:       block.Uncles(),
				}
				got, err := json.MarshalIndent(golden, "", "  ")
				if err != nil {
					t.Fatalf("failed to marshal block: %v", err)
				}
				goldenUpdate := os.Getenv("GOLDEN_UPDATE") == "true"
				goldenFile := filepath.Join("testdata", "firehose_block.golden.json")
				if goldenUpdate {
					if err := os.MkdirAll(filepath.Dir(goldenFile), 0755); err != nil {
						t.Fatalf("failed to create testdata dir: %v", err)
					}
					if err := os.WriteFile(goldenFile, got, 0644); err != nil {
						t.Fatalf("failed to write golden file: %v", err)
					}
				} else {
					want, err := os.ReadFile(goldenFile)
					if err != nil {
						t.Fatalf("failed to read golden file: %v", err)
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("block does not match golden file.\nGot:\n%s\nWant:\n%s", got, want)
					}
				}
			}
		})
	}
}

func hexBytes(s string) []byte {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil {
		panic(err) // fine for test code
	}
	return b
}

func hash32(s string) []byte {
	h := common.HexToHash(s)
	return h[:]
}

func addressBytes(s string) []byte {
	a := common.HexToAddress(s)
	return a[:]
}
