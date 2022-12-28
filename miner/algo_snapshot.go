package miner

import (
	"errors"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth/tracers/logger"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"math/big"
)

type txApplyChanges struct {
	env      *environment
	gasPool  *core.GasPool
	usedGas  uint64
	profit   *big.Int
	txs      []*types.Transaction
	receipts []*types.Receipt
}

func newEnvChanges(env *environment) (*txApplyChanges, error) {
	err := env.state.MultiTxSnapshot()
	if err != nil {
		return nil, err
	}
	return &txApplyChanges{
		env:      env,
		gasPool:  new(core.GasPool).AddGas(env.gasPool.Gas()),
		usedGas:  env.header.GasUsed,
		profit:   new(big.Int).Set(env.profit),
		txs:      make([]*types.Transaction, 0),
		receipts: make([]*types.Receipt, 0),
	}, nil
}

func (c *txApplyChanges) apply() error {
	err := c.env.state.MultiTxSnapshotDiscard()
	if err != nil {
		return err
	}
	*c.env.gasPool = *c.gasPool
	c.env.header.GasUsed = c.usedGas
	c.env.profit.Set(c.profit)
	c.env.tcount += len(c.txs)
	c.env.txs = append(c.env.txs, c.txs...)
	c.env.receipts = append(c.env.receipts, c.receipts...)
	return nil
}

func (c *txApplyChanges) revert() error {
	return c.env.state.MultiTxSnapshotRevert()
}

func (c *txApplyChanges) commitTx(tx *types.Transaction, chData chainData) (*types.Receipt, int, error) {
	signer := c.env.signer
	sender, err := types.Sender(signer, tx)
	if err != nil {
		return nil, popTx, err
	}

	gasPrice, err := tx.EffectiveGasTip(c.env.header.BaseFee)
	if err != nil {
		return nil, shiftTx, err
	}

	if _, in := chData.blacklist[sender]; in {
		return nil, popTx, errors.New("blacklist violation, tx.sender")
	}

	if to := tx.To(); to != nil {
		if _, in := chData.blacklist[*to]; in {
			return nil, popTx, errors.New("blacklist violation, tx.to")
		}
	}

	cfg := *chData.chain.GetVMConfig()
	touchTracer := logger.NewAccountTouchTracer()
	cfg.Tracer = touchTracer
	cfg.Debug = true

	rec, err := core.ApplyTransaction(chData.chainConfig, chData.chain, &c.env.coinbase, c.gasPool, c.env.state, c.env.header, tx, &c.usedGas, cfg, nil)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrGasLimitReached):
			// Pop the current out-of-gas transaction without shifting in the next from the account
			from, _ := types.Sender(signer, tx)
			log.Trace("Gas limit exceeded for current block", "sender", from)
			return nil, popTx, err

		case errors.Is(err, core.ErrNonceTooLow):
			// New head notification data race between the transaction pool and miner, shift
			from, _ := types.Sender(signer, tx)
			log.Trace("Skipping transaction with low nonce", "sender", from, "nonce", tx.Nonce())
			return nil, shiftTx, err

		case errors.Is(err, core.ErrNonceTooHigh):
			// Reorg notification data race between the transaction pool and miner, skip account =
			from, _ := types.Sender(signer, tx)
			log.Trace("Skipping account with hight nonce", "sender", from, "nonce", tx.Nonce())
			return nil, popTx, err

		case errors.Is(err, core.ErrTxTypeNotSupported):
			// Pop the unsupported transaction without shifting in the next from the account
			from, _ := types.Sender(signer, tx)
			log.Trace("Skipping unsupported transaction type", "sender", from, "type", tx.Type())
			return nil, popTx, err

		default:
			// Strange error, discard the transaction and get the next in line (note, the
			// nonce-too-high clause will prevent us from executing in vain).
			log.Trace("Transaction failed, account skipped", "hash", tx.Hash(), "err", err)
			return nil, shiftTx, err
		}
	}

	for _, address := range touchTracer.TouchedAddresses() {
		if _, in := chData.blacklist[address]; in {
			return nil, popTx, errors.New("blacklist violation, tx trace")
		}
	}

	c.profit.Add(c.profit, new(big.Int).Mul(new(big.Int).SetUint64(rec.GasUsed), gasPrice))
	c.txs = append(c.txs, tx)
	c.receipts = append(c.receipts, rec)

	return rec, shiftTx, nil
}

func (c *txApplyChanges) commitBundle(bundle *types.SimulatedBundle, chData chainData) error {
	var (
		profitBefore   = new(big.Int).Set(c.profit)
		coinbaseBefore = new(big.Int).Set(c.env.state.GetBalance(c.env.coinbase))
		gasUsedBefore  = c.usedGas
	)

	for _, tx := range bundle.OriginalBundle.Txs {
		receipt, _, err := c.commitTx(tx, chData)

		if err != nil {
			log.Trace("Bundle tx error", "bundle", bundle.OriginalBundle.Hash, "tx", tx.Hash(), "err", err)
			return err
		}

		if receipt.Status != types.ReceiptStatusSuccessful && !bundle.OriginalBundle.RevertingHash(tx.Hash()) {
			log.Trace("Bundle tx failed", "bundle", bundle.OriginalBundle.Hash, "tx", tx.Hash(), "err", err)
			return errors.New("bundle tx revert")
		}
	}

	var (
		bundleProfit = new(big.Int).Sub(c.env.state.GetBalance(c.env.coinbase), coinbaseBefore)
		gasUsed      = c.usedGas - gasUsedBefore

		effGP    = new(big.Int).Div(bundleProfit, new(big.Int).SetUint64(gasUsed))
		simEffGP = new(big.Int).Set(bundle.MevGasPrice)
	)

	// allow >-1% divergence
	effGP.Mul(effGP, big.NewInt(100))
	simEffGP.Mul(simEffGP, big.NewInt(99))
	if simEffGP.Cmp(effGP) > 0 {
		log.Trace("Bundle underpays after inclusion", "bundle", bundle.OriginalBundle.Hash)
		return errors.New("bundle underpays")
	}

	c.profit.Add(profitBefore, bundleProfit)
	return nil
}

type greedyBuilderSnapshot struct {
	inputEnvironment *environment
	chainData        chainData
	interrupt        *int32
}

func newGreedyBuilderSnapshot(chain *core.BlockChain, chainConfig *params.ChainConfig, blacklist map[common.Address]struct{}, env *environment, interrupt *int32) *greedyBuilderSnapshot {
	return &greedyBuilderSnapshot{
		inputEnvironment: env,
		chainData:        chainData{chainConfig, chain, blacklist},
		interrupt:        interrupt,
	}
}

func (b *greedyBuilderSnapshot) buildBlock(simBundles []types.SimulatedBundle, transactions map[common.Address]types.Transactions) (*environment, []types.SimulatedBundle) {
	orders := types.NewTransactionsByPriceAndNonce(b.inputEnvironment.signer, transactions, simBundles, b.inputEnvironment.header.BaseFee)
	var usedBundles []types.SimulatedBundle

	for {
		order := orders.Peek()
		if order == nil {
			break
		}

		orderFailed := false
		changes, err := newEnvChanges(b.inputEnvironment)
		if err != nil {
			log.Error("Failed to create env changes", "err", err)
			return b.inputEnvironment, usedBundles
		}
		if order.Tx() != nil {
			receipt, skip, err := changes.commitTx(order.Tx(), b.chainData)
			switch skip {
			case shiftTx:
				orders.Shift()
			case popTx:
				orders.Pop()
			}

			if err != nil {
				log.Trace("could not apply tx", "hash", order.Tx().Hash(), "err", err)
				orderFailed = true
			} else {
				effGapPrice, err := order.Tx().EffectiveGasTip(b.inputEnvironment.header.BaseFee)
				if err == nil {
					log.Trace("Included tx", "EGP", effGapPrice.String(), "gasUsed", receipt.GasUsed)
				}
			}
		} else if bundle := order.Bundle(); bundle != nil {
			err = changes.commitBundle(bundle, b.chainData)
			orders.Pop()
			if err != nil {
				log.Trace("Could not apply bundle", "bundle", bundle.OriginalBundle.Hash, "err", err)
				orderFailed = true
			} else {
				log.Trace("Included bundle", "bundleEGP", bundle.MevGasPrice.String(), "gasUsed", bundle.TotalGasUsed, "ethToCoinbase", ethIntToFloat(bundle.TotalEth))
				usedBundles = append(usedBundles, *bundle)
			}
		}

		if orderFailed {
			err = changes.revert()
			if err != nil {
				log.Error("Failed to revert changes", "err", err)
				return b.inputEnvironment, usedBundles
			}
		} else {
			err = changes.apply()
			if err != nil {
				log.Error("Failed to apply changes", "err", err)
				return b.inputEnvironment, usedBundles
			}
		}
	}
	return b.inputEnvironment, usedBundles
}
