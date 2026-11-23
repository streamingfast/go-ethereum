// Copyright 2026 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package js

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/dop251/goja"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/tracing"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/core/vm/runtime"
	"github.com/ethereum/go-ethereum/params"
)

func TestToBufCopies(t *testing.T) {
	jsvm := goja.New()
	val := []byte{1, 2, 3}
	buf, err := toBuf(jsvm, jsvm.Get("Uint8Array"), val)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsvm.Set("buf", buf); err != nil {
		t.Fatal(err)
	}
	if _, err := jsvm.RunString("buf[0] = 0xff"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(val, []byte{1, 2, 3}) {
		t.Fatalf("script modified the Go slice: %x", val)
	}
}

func TestCtxInputIsCopy(t *testing.T) {
	input := []byte{1, 2, 3}
	tracer, err := newJsTracer(`{
		step: function() {},
		fault: function() {},
		result: function(ctx) {
			for (var i = 0; i < ctx.input.length; i++) { ctx.input[i] = 0xff; }
			return ctx.input.length;
		}
	}`, nil, nil, params.TestChainConfig)
	if err != nil {
		t.Fatal(err)
	}
	statedb, err := state.New(types.EmptyRootHash, state.NewDatabaseForTesting())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.Call(common.Address{0xc0, 0xde}, input, &runtime.Config{
		ChainConfig: params.TestChainConfig,
		GasLimit:    100_000,
		State:       statedb,
		EVMConfig:   vm.Config{Tracer: tracer.Hooks},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := tracer.GetResult()
	if err != nil {
		t.Fatal(err)
	}
	if string(res) != "3" {
		t.Fatalf("ctx.input length: want 3, have %s", res)
	}
	if !bytes.Equal(input, []byte{1, 2, 3}) {
		t.Fatalf("script modified the call input: %x", input)
	}
}

func TestArbitrumHookArgumentsAreCopies(t *testing.T) {
	tracer, err := newJsTracer(`{
		captureArbitrumTransfer: function() { this.value.setInt64(7); },
		onBalanceChange: function(change) {
			change.prev.setInt64(7);
			change.new.setInt64(7);
		},
		hostio: function(info) {
			info.args[0] = 0xff;
			info.outs[0] = 0xff;
		},
		fault: function() {},
		result: function() { return null; }
	}`, nil, nil, params.TestChainConfig)
	if err != nil {
		t.Fatal(err)
	}
	value, prev, next := big.NewInt(1), big.NewInt(2), big.NewInt(3)
	args, outs := []byte{1}, []byte{2}
	tracer.CaptureArbitrumTransfer(nil, nil, value, true, tracing.BalanceChangeUnspecified)
	tracer.OnBalanceChange(common.Address{}, prev, next, tracing.BalanceChangeUnspecified)
	tracer.CaptureStylusHostio("read_args", args, outs, 0, 0)
	if _, err := tracer.GetResult(); err != nil {
		t.Fatal(err)
	}
	if value.Int64() != 1 || prev.Int64() != 2 || next.Int64() != 3 {
		t.Errorf("script modified balances: value %v, prev %v, new %v", value, prev, next)
	}
	if args[0] != 1 || outs[0] != 2 {
		t.Errorf("script modified hostio buffers: args %x, outs %x", args, outs)
	}
}
