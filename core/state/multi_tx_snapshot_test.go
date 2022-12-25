package state

import (
	"github.com/ethereum/go-ethereum/common"
	"testing"
)

// TODO: fix - don't work
func TestMultiTxSnapshot(t *testing.T) {
	s := newStateTest()

	before := s.state.IntermediateRoot(true)

	err := s.state.MultiTxSnapshot()
	if err != nil {
		t.Fatal("MultiTxSnapshot failed", err)
	}

	s.state.SetNonce(common.HexToAddress("0x823140710bf13990e4500136726d8b55"), 1)

	err = s.state.MultiTxSnapshotRevert()
	if err != nil {
		t.Fatal("MultiTxSnapshotRevert failed", err)
	}
	after := s.state.IntermediateRoot(true)

	if before != after {
		t.Errorf("expected same root")
	}
}
