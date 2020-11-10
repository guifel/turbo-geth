package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/c2h5oh/datasize"
	"github.com/ledgerwatch/turbo-geth/cmd/utils"
	"github.com/ledgerwatch/turbo-geth/common"
	"github.com/ledgerwatch/turbo-geth/common/changeset"
	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/core"
	"github.com/ledgerwatch/turbo-geth/core/state"
	"github.com/ledgerwatch/turbo-geth/eth/stagedsync"
	"github.com/ledgerwatch/turbo-geth/eth/stagedsync/stages"
	"github.com/ledgerwatch/turbo-geth/ethdb"
	"github.com/ledgerwatch/turbo-geth/log"
	"github.com/spf13/cobra"
)

var stateStags = &cobra.Command{
	Use: "state_stages",
	Short: `Move all StateStages (which happen after senders) forward. 
			Stops at StageSenders progress or at "--block".
			Each iteration test will move forward "--unwind_every" blocks, then unwind "--unwind" blocks.
			Use reset_state command to re-run this test.
			When finish all cycles, does comparison to "--reference_chaindata" if flag provided.
		`,
	Example: "go run ./cmd/integration state_stages --chaindata=... --verbosity=3 --unwind=100 --unwind_every=100000 --block=2000000",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := utils.RootContext()
		db := openDatabase(chaindata, true)
		defer db.Close()

		if err := syncBySmallSteps(db, ctx); err != nil {
			log.Error("Error", "err", err)
			return err
		}

		if referenceChaindata != "" {
			if err := compareStates(ctx, chaindata, referenceChaindata); err != nil {
				log.Error(err.Error())
				return err
			}

		}
		return nil
	},
}

func init() {
	withChaindata(stateStags)
	withReferenceChaindata(stateStags)
	withUnwind(stateStags)
	withUnwindEvery(stateStags)
	withBlock(stateStags)
	withBatchSize(stateStags)

	rootCmd.AddCommand(stateStags)
}

func syncBySmallSteps(db ethdb.Database, ctx context.Context) error {
	core.UsePlainStateExecution = true

	sm, err := ethdb.GetStorageModeFromDB(db)
	if err != nil {
		panic(err)
	}

	ch := ctx.Done()

	expectedAccountChanges := make(map[uint64]*changeset.ChangeSet)
	expectedStorageChanges := make(map[uint64]*changeset.ChangeSet)
	changeSetHook := func(blockNum uint64, csw *state.ChangeSetWriter) {
		accountChanges, err := csw.GetAccountChanges()
		if err != nil {
			panic(err)
		}
		expectedAccountChanges[blockNum] = accountChanges

		storageChanges, err := csw.GetStorageChanges()
		if err != nil {
			panic(err)
		}
		if storageChanges.Len() > 0 {
			expectedStorageChanges[blockNum] = storageChanges
		}
	}

	var tx ethdb.DbWithPendingMutations = ethdb.NewTxDbWithoutTransaction(db, ethdb.RW)
	defer tx.Rollback()

	cc, bc, st, progress := newSync(ch, db, tx, changeSetHook)
	defer bc.Stop()
	cc.SetDB(tx)

	tx, err = tx.Begin(ctx, ethdb.RO)
	if err != nil {
		return err
	}

	st.DisableStages(stages.Headers, stages.BlockHashes, stages.Bodies, stages.Senders)
	_ = st.SetCurrentStage(stages.Execution)

	senderStageProgress := progress(stages.Senders).BlockNumber

	var stopAt = senderStageProgress
	if block > 0 && block < senderStageProgress {
		stopAt = block
	}

	var batchSize datasize.ByteSize
	must(batchSize.UnmarshalText([]byte(batchSizeStr)))
	for progress(stages.Execution).BlockNumber < stopAt || (unwind <= unwindEvery) {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if err := tx.CommitAndBegin(context.Background()); err != nil {
			return err
		}

		// All stages forward to `execStage + unwindEvery` block
		execAtBlock := progress(stages.Execution).BlockNumber
		execToBlock := execAtBlock - unwind + unwindEvery
		if execToBlock > stopAt {
			execToBlock = stopAt + 1
			unwind = 0
		}

		// set block limit of execute stage
		st.MockExecFunc(stages.Execution, func(stageState *stagedsync.StageState, unwinder stagedsync.Unwinder) error {
			if err := stagedsync.SpawnExecuteBlocksStage(
				stageState, tx,
				bc.Config(), cc, bc.GetVMConfig(),
				ch,
				stagedsync.ExecuteBlockStageParams{
					ToBlock:       execToBlock, // limit execution to the specified block
					WriteReceipts: sm.Receipts,
					BatchSize:     int(batchSize),
					ChangeSetHook: changeSetHook,
				}); err != nil {
				return fmt.Errorf("spawnExecuteBlocksStage: %w", err)
			}
			return nil
		})

		if err := st.Run(db, tx); err != nil {
			return err
		}

		for blockN := range expectedAccountChanges {
			if err := checkChangeSet(tx, blockN, expectedAccountChanges[blockN], expectedStorageChanges[blockN]); err != nil {
				return err
			}
			delete(expectedAccountChanges, blockN)
			delete(expectedStorageChanges, blockN)
		}

		if err := checkHistory(tx, dbutils.AccountChangeSetBucket2, execAtBlock); err != nil {
			return err
		}
		if err := checkHistory(tx, dbutils.StorageChangeSetBucket2, execAtBlock); err != nil {
			return err
		}

		// Unwind all stages to `execStage - unwind` block
		if unwind == 0 {
			continue
		}

		execStage := progress(stages.Execution)
		to := execStage.BlockNumber - unwind

		if err := st.UnwindTo(to, tx); err != nil {
			return err
		}
	}

	return nil
}

func checkChangeSet(db ethdb.Getter, blockNum uint64, expectedAccountChanges *changeset.ChangeSet, expectedStorageChanges *changeset.ChangeSet) error {
	i := 0
	err := ethdb.WalkChangeSetByBlock(db, false /* storage */, blockNum, func(kk, k, v []byte) error {
		c := expectedAccountChanges.Changes[i]
		i++
		if bytes.Equal(c.Key, k) && bytes.Equal(c.Value, v) {
			return nil
		}

		fmt.Printf("Unexpected account changes in block %d\nIn the database: ======================\n", blockNum)
		fmt.Printf("0x%x: %x\n", k, v)
		fmt.Printf("Expected: ==========================\n")
		fmt.Printf("0x%x %x\n", c.Key, c.Value)
		return fmt.Errorf("check change set failed")
	})
	if err != nil {
		return err
	}

	err = ethdb.WalkChangeSetByBlock(db, true /* storage */, blockNum, func(kk, k, v []byte) error {
		c := expectedStorageChanges.Changes[i]
		i++
		if bytes.Equal(c.Key, k) && bytes.Equal(c.Value, v) {
			return nil
		}

		fmt.Printf("Unexpected storage changes in block %d\nIn the database: ======================\n", blockNum)
		fmt.Printf("0x%x: %x\n", k, v)
		fmt.Printf("Expected: ==========================\n")
		fmt.Printf("0x%x %x\n", c.Key, c.Value)
		return fmt.Errorf("check change set failed")
	})
	if err != nil {
		return err
	}
	//equal := true
	//if !bytes.Equal(dbStorageChanges, expectedStorageChanges) {
	//	var addrs [][]byte
	//	var keys [][]byte
	//	var vals [][]byte
	//	if err = changeset.StorageChangeSetPlainBytes(dbStorageChanges).Walk(func(k, v []byte) error {
	//		addrs = append(addrs, common.CopyBytes(k[:common.AddressLength]))
	//		keys = append(keys, common.CopyBytes(k[common.AddressLength+common.IncarnationLength:]))
	//		vals = append(vals, common.CopyBytes(v))
	//		return nil
	//	}); err != nil {
	//		return err
	//	}
	//	i := 0
	//	if err = changeset.StorageChangeSetPlainBytes(expectedStorageChanges).Walk(func(k, v []byte) error {
	//		if !equal {
	//			return nil
	//		}
	//		if i >= len(addrs) {
	//			equal = false
	//			return nil
	//		}
	//		if !bytes.Equal(k[:common.AddressLength], addrs[i]) {
	//			equal = false
	//			return nil
	//		}
	//		if !bytes.Equal(k[common.AddressLength+common.IncarnationLength:], keys[i]) {
	//			equal = false
	//			return nil
	//		}
	//		if !bytes.Equal(v, vals[i]) {
	//			equal = false
	//			return nil
	//		}
	//		i++
	//		return nil
	//	}); err != nil {
	//		return err
	//	}
	//}
	//if !equal {
	//	fmt.Printf("Unexpected storage changes in block %d\nIn the database: ======================\n", blockNum)
	//	if err = changeset.StorageChangeSetPlainBytes(dbStorageChanges).Walk(func(k, v []byte) error {
	//		fmt.Printf("0x%x: [%x]\n", k, v)
	//		return nil
	//	}); err != nil {
	//		return err
	//	}
	//	fmt.Printf("Expected: ==========================\n")
	//	if err = changeset.StorageChangeSetPlainBytes(expectedStorageChanges).Walk(func(k, v []byte) error {
	//		fmt.Printf("0x%x: [%x]\n", k, v)
	//		return nil
	//	}); err != nil {
	//		return err
	//	}
	//	return fmt.Errorf("check change set failed")
	//}
	return nil
}

func checkHistory(db ethdb.Getter, changeSetBucket string, blockNum uint64) error {
	currentKey := dbutils.EncodeTimestamp(blockNum)

	var walker func([]byte) changeset.Walker
	if dbutils.AccountChangeSetBucket2 == changeSetBucket {
		walker = func(cs []byte) changeset.Walker {
			return changeset.AccountChangeSetBytes(cs)
		}
	}

	if dbutils.StorageChangeSetBucket2 == changeSetBucket {
		walker = func(cs []byte) changeset.Walker {
			return changeset.StorageChangeSetBytes(cs)
		}
	}

	vv, ok := changeset.Mapper[changeSetBucket]
	if !ok {
		return errors.New("unknown bucket type")
	}

	if err := db.Walk(changeSetBucket, currentKey, 0, func(k, v []byte) (b bool, e error) {
		blockNum, _ := dbutils.DecodeTimestamp(k)
		if err := walker(v).Walk(func(key, val []byte) error {
			indexBytes, innerErr := db.GetIndexChunk(vv.IndexBucket, key, blockNum)
			if innerErr != nil {
				return innerErr
			}

			index := dbutils.WrapHistoryIndex(indexBytes)
			if findVal, _, ok := index.Search(blockNum); !ok {
				return fmt.Errorf("%v,%v,%v", blockNum, findVal, common.Bytes2Hex(key))
			}
			return nil
		}); err != nil {
			return false, err
		}
		return true, nil
	}); err != nil {
		return err
	}
	return nil
}
