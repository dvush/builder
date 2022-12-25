package state

import (
	"github.com/ethereum/go-ethereum/common"
	"math/big"
)

// TODO: selfdestructs, account creations
// TODO: actual revert logic

type multiTxSnapshot struct {
	invalid bool

	numLogsAdded map[common.Hash]int

	accountStorage  map[common.Address]map[common.Hash]*common.Hash
	accountBalance  map[common.Address]*big.Int
	accountNonce    map[common.Address]uint64
	accountCode     map[common.Address][]byte
	accountCodeHash map[common.Address][]byte

	accountsNotPending map[common.Address]struct{}
	accountsNotDirty   map[common.Address]struct{}
}

func newMultiTxSnapshot() *multiTxSnapshot {
	return &multiTxSnapshot{
		numLogsAdded:    make(map[common.Hash]int),
		accountStorage:  make(map[common.Address]map[common.Hash]*common.Hash),
		accountBalance:  make(map[common.Address]*big.Int),
		accountNonce:    make(map[common.Address]uint64),
		accountCode:     make(map[common.Address][]byte),
		accountCodeHash: make(map[common.Address][]byte),
	}
}

// updateFromJournal updates the snapshot with the changes from the journal.
func (s *multiTxSnapshot) updateFromJournal(journal *journal) {
	for _, entry := range journal.entries {
		switch entry := entry.(type) {
		case *balanceChange:
			s.updateBalanceChange(entry)
		case *nonceChange:
			s.updateNonceChange(entry)
		case *codeChange:
			s.updateCodeChange(entry)
		case *addLogChange:
			s.numLogsAdded[entry.txhash]++
		}
	}
}

// updateBalanceChange updates the snapshot with the balance change.
func (s *multiTxSnapshot) updateBalanceChange(change *balanceChange) {
	if _, ok := s.accountBalance[*change.account]; !ok {
		s.accountBalance[*change.account] = change.prev
	}
}

// updateNonceChange updates the snapshot with the nonce change.
func (s *multiTxSnapshot) updateNonceChange(change *nonceChange) {
	if _, ok := s.accountNonce[*change.account]; !ok {
		s.accountNonce[*change.account] = change.prev
	}
}

// updateCodeChange updates the snapshot with the code change.
func (s *multiTxSnapshot) updateCodeChange(change *codeChange) {
	if _, ok := s.accountCode[*change.account]; !ok {
		s.accountCode[*change.account] = change.prevcode
		s.accountCodeHash[*change.account] = change.prevhash
	}
}

// updatePendingStorage updates the snapshot with the pending storage change.
func (s *multiTxSnapshot) updatePendingStorage(address common.Address, key, value common.Hash, ok bool) {
	if _, ok := s.accountStorage[address]; !ok {
		s.accountStorage[address] = make(map[common.Hash]*common.Hash)
	}
	if _, ok := s.accountStorage[address][key]; ok {
		return
	}
	if ok {
		s.accountStorage[address][key] = &value
	} else {
		s.accountStorage[address][key] = nil
	}
}

// updatePendingStatus updates the snapshot with previous pending status.
func (s *multiTxSnapshot) updatePendingStatus(address common.Address, pending, dirty bool) {
	if !pending {
		s.accountsNotPending[address] = struct{}{}
	}
	if !dirty {
		s.accountsNotDirty[address] = struct{}{}
	}
}

// revertState reverts the state to the snapshot.
func (s *multiTxSnapshot) revertState(st *StateDB) {
	// TODO
}
