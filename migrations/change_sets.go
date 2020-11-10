package migrations

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ledgerwatch/turbo-geth/common"
	"github.com/ledgerwatch/turbo-geth/common/changeset"
	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/common/etl"
	"github.com/ledgerwatch/turbo-geth/ethdb"
	"github.com/ledgerwatch/turbo-geth/log"
)

var accChangeSetDupSort = Migration{
	Name: "acc_change_set_dup_sort_14",
	Up: func(db ethdb.Database, tmpdir string, progress []byte, CommitProgress etl.LoadCommitHandler) (err error) {
		logEvery := time.NewTicker(30 * time.Second)
		defer logEvery.Stop()
		logPrefix := "change_set_dup_sort"

		const loadStep = "load"

		changeSetBucket := dbutils.PlainAccountChangeSetBucket
		cmp := db.(ethdb.HasTx).Tx().Comparator(dbutils.PlainStorageChangeSetBucket)
		buf := etl.NewSortableBuffer(etl.BufferOptimalSize * 4)
		buf.SetComparator(cmp)
		newK := make([]byte, 8+20)

		collectorR, err1 := etl.NewCollectorFromFiles(tmpdir)
		if err1 != nil {
			return err1
		}
		switch string(progress) {
		case "":
			// can't use files if progress field not set, clear them
			if collectorR != nil {
				collectorR.Close(logPrefix)
				collectorR = nil
			}

		case loadStep:
			if collectorR == nil {
				return ErrMigrationETLFilesDeleted
			}
			defer func() {
				// don't clean if error or panic happened
				if err != nil {
					return
				}
				if rec := recover(); rec != nil {
					panic(rec)
				}
				collectorR.Close(logPrefix)
			}()
			goto LoadStep
		}

		collectorR = etl.NewCriticalCollector(tmpdir, buf)
		defer func() {
			// don't clean if error or panic happened
			if err != nil {
				return
			}
			if rec := recover(); rec != nil {
				panic(rec)
			}
			collectorR.Close(logPrefix)
		}()

		if err = db.Walk(changeSetBucket, nil, 0, func(kk, changesetBytes []byte) (bool, error) {
			blockNum, _ := dbutils.DecodeTimestamp(kk)

			select {
			default:
			case <-logEvery.C:
				log.Info(fmt.Sprintf("[%s] Progress2", logPrefix), "blockNum", blockNum)
			}

			binary.BigEndian.PutUint64(newK, blockNum)
			if err = accountChangeSetPlainBytesOld(changesetBytes).Walk(func(k, v []byte) error {
				newV := make([]byte, len(k)+len(v))
				copy(newV, k)
				copy(newV[len(k):], k)
				return collectorR.Collect(newK, newV)
			}); err != nil {
				return false, err
			}

			return true, nil
		}); err != nil {
			return err
		}

		if err = db.(ethdb.BucketsMigrator).ClearBuckets(dbutils.PlainAccountChangeSetBucket); err != nil {
			return fmt.Errorf("clearing the receipt bucket: %w", err)
		}

		// Commit clearing of the bucket - freelist should now be written to the database
		if err = CommitProgress(db, []byte(loadStep), false); err != nil {
			return fmt.Errorf("committing the removal of receipt table: %w", err)
		}

	LoadStep:
		// Commit again
		if err = CommitProgress(db, []byte(loadStep), false); err != nil {
			return fmt.Errorf("committing the removal of receipt table: %w", err)
		}
		// Now transaction would have been re-opened, and we should be re-using the space
		if err = collectorR.Load(logPrefix, db, dbutils.PlainAccountChangeSetBucket, etl.IdentityLoadFunc, etl.TransformArgs{
			OnLoadCommit: CommitProgress,
		}); err != nil {
			return fmt.Errorf("loading the transformed data back into the receipts table: %w", err)
		}
		return nil
	},
}

var storageChangeSetDupSort = Migration{
	Name: "storage_change_set_dup_sort_18",
	Up: func(db ethdb.Database, tmpdir string, progress []byte, CommitProgress etl.LoadCommitHandler) (err error) {
		logEvery := time.NewTicker(30 * time.Second)
		defer logEvery.Stop()
		logPrefix := "storage_change_set_dup_sort"

		const loadStep = "load"
		changeSetBucket := dbutils.PlainStorageChangeSetBucket
		cmp := db.(ethdb.HasTx).Tx().Comparator(dbutils.PlainStorageChangeSetBucket)
		buf := etl.NewSortableBuffer(etl.BufferOptimalSize * 4)
		buf.SetComparator(cmp)
		newK := make([]byte, 8+20)
		newV := make([]byte, 8+32+4096)

		collectorR, err1 := etl.NewCollectorFromFiles(tmpdir)
		if err1 != nil {
			return err1
		}
		switch string(progress) {
		case "":
			// can't use files if progress field not set, clear them
			if collectorR != nil {
				collectorR.Close(logPrefix)
				collectorR = nil
			}

		case loadStep:
			if collectorR == nil {
				return ErrMigrationETLFilesDeleted
			}
			defer func() {
				// don't clean if error or panic happened
				if err != nil {
					return
				}
				if rec := recover(); rec != nil {
					panic(rec)
				}
				collectorR.Close(logPrefix)
			}()
			goto LoadStep
		}

		collectorR = etl.NewCriticalCollector(tmpdir, buf)
		defer func() {
			// don't clean if error or panic happened
			if err != nil {
				return
			}
			if rec := recover(); rec != nil {
				panic(rec)
			}
			collectorR.Close(logPrefix)
		}()

		if err = db.Walk(changeSetBucket, nil, 0, func(kk, changesetBytes []byte) (bool, error) {
			blockNum, _ := dbutils.DecodeTimestamp(kk)

			select {
			default:
			case <-logEvery.C:
				log.Info(fmt.Sprintf("[%s] Progress", logPrefix), "blockNum", blockNum)
			}

			binary.BigEndian.PutUint64(newK, blockNum)
			if err = storageChangeSetPlainBytesOld(changesetBytes).Walk(func(k, v []byte) error {
				copy(newK[8:], k[:20+8])

				newV = newV[:32+len(v)]
				copy(newV, k[20+8:])
				copy(newV[32:], v)
				return collectorR.Collect(newK, newV)
			}); err != nil {
				return false, err
			}

			return true, nil
		}); err != nil {
			return err
		}

		if err = db.(ethdb.BucketsMigrator).ClearBuckets(dbutils.PlainStorageChangeSetBucket); err != nil {
			return fmt.Errorf("clearing the receipt bucket: %w", err)
		}

		// Commit clearing of the bucket - freelist should now be written to the database
		if err = CommitProgress(db, []byte(loadStep), false); err != nil {
			return fmt.Errorf("committing the removal of receipt table: %w", err)
		}

	LoadStep:
		// Commit again
		if err = CommitProgress(db, []byte(loadStep), false); err != nil {
			return fmt.Errorf("committing the removal of receipt table: %w", err)
		}
		// Now transaction would have been re-opened, and we should be re-using the space
		if err = collectorR.Load(logPrefix, db, dbutils.PlainStorageChangeSetBucket, etl.IdentityLoadFunc, etl.TransformArgs{
			OnLoadCommit: CommitProgress,
			Comparator:   cmp,
		}); err != nil {
			return fmt.Errorf("loading the transformed data back into the receipts table: %w", err)
		}
		return nil
	},
}

type accountChangeSetPlainBytesOld []byte

func (b accountChangeSetPlainBytesOld) Walk(f func(k, v []byte) error) error {
	return walkAccountChangeSet(b, common.AddressLength, f)
}

// walkAccountChangeSet iterates the account bytes with the keys of provided size
func walkAccountChangeSet(b []byte, keyLen uint32, f func(k, v []byte) error) error {
	if len(b) == 0 {
		return nil
	}
	if len(b) < 4 {
		return fmt.Errorf("decode: input too short (%d bytes)", len(b))
	}

	n := binary.BigEndian.Uint32(b[0:4])

	if n == 0 {
		return nil
	}
	valOffset := 4 + n*keyLen + 4*n
	if uint32(len(b)) < valOffset {
		fmt.Println("walkAccounts account")
		return fmt.Errorf("decode: input too short (%d bytes, expected at least %d bytes)", len(b), valOffset)
	}

	totalValLength := binary.BigEndian.Uint32(b[valOffset-4 : valOffset])
	if uint32(len(b)) < valOffset+totalValLength {
		return fmt.Errorf("decode: input too short (%d bytes, expected at least %d bytes)", len(b), valOffset+totalValLength)
	}

	for i := uint32(0); i < n; i++ {
		key := b[4+i*keyLen : 4+(i+1)*keyLen]
		idx0 := uint32(0)
		if i > 0 {
			idx0 = binary.BigEndian.Uint32(b[4+n*keyLen+4*(i-1) : 4+n*keyLen+4*i])
		}
		idx1 := binary.BigEndian.Uint32(b[4+n*keyLen+4*i : 4+n*keyLen+4*(i+1)])
		val := b[valOffset+idx0 : valOffset+idx1]

		err := f(key, val)
		if err != nil {
			return err
		}
	}
	return nil
}

type storageChangeSetPlainBytesOld []byte

func (b storageChangeSetPlainBytesOld) Walk(f func(k, v []byte) error) error {
	return walkStorageChangeSet(b, common.AddressLength, f)
}

func walkStorageChangeSet(b []byte, keyPrefixLen int, f func(k, v []byte) error) error {
	if len(b) == 0 {
		return nil
	}

	if len(b) < 4 {
		return fmt.Errorf("decode: input too short (%d bytes)", len(b))
	}

	numOfUniqueElements := int(binary.BigEndian.Uint32(b))
	if numOfUniqueElements == 0 {
		return nil
	}
	incarnatonsInfo := 4 + numOfUniqueElements*(keyPrefixLen+4)
	numOfNotDefaultIncarnations := int(binary.BigEndian.Uint32(b[incarnatonsInfo:]))
	incarnatonsStart := incarnatonsInfo + 4

	notDefaultIncarnations := make(map[uint32]uint64, numOfNotDefaultIncarnations)
	if numOfNotDefaultIncarnations > 0 {
		for i := 0; i < numOfNotDefaultIncarnations; i++ {
			notDefaultIncarnations[binary.BigEndian.Uint32(b[incarnatonsStart+i*12:])] = binary.BigEndian.Uint64(b[incarnatonsStart+i*12+4:])
		}
	}

	keysStart := incarnatonsStart + numOfNotDefaultIncarnations*12
	numOfElements := int(binary.BigEndian.Uint32(b[incarnatonsInfo-4:]))
	valsInfoStart := keysStart + numOfElements*common.HashLength

	var addressHashID uint32
	var id int
	k := make([]byte, keyPrefixLen+common.HashLength+common.IncarnationLength)
	for i := 0; i < numOfUniqueElements; i++ {
		var (
			startKeys int
			endKeys   int
		)

		if i > 0 {
			startKeys = int(binary.BigEndian.Uint32(b[4+i*(keyPrefixLen)+(i-1)*4 : 4+i*(keyPrefixLen)+(i)*4]))
		}
		endKeys = int(binary.BigEndian.Uint32(b[4+(i+1)*(keyPrefixLen)+i*4:]))
		addrBytes := b[4+i*(keyPrefixLen)+i*4:] // hash or raw address
		incarnation := changeset.DefaultIncarnation
		if inc, ok := notDefaultIncarnations[addressHashID]; ok {
			incarnation = inc
		}

		for j := startKeys; j < endKeys; j++ {
			copy(k[:keyPrefixLen], addrBytes[:keyPrefixLen])
			binary.BigEndian.PutUint64(k[keyPrefixLen:], incarnation)
			copy(k[keyPrefixLen+common.IncarnationLength:keyPrefixLen+common.HashLength+common.IncarnationLength], b[keysStart+j*common.HashLength:])
			val, innerErr := findValue(b[valsInfoStart:], id)
			if innerErr != nil {
				return innerErr
			}
			err := f(k, val)
			if err != nil {
				return err
			}
			id++
		}
		addressHashID++
	}

	return nil
}

func findValue(b []byte, i int) ([]byte, error) {
	numOfUint8 := int(binary.BigEndian.Uint32(b[0:]))
	numOfUint16 := int(binary.BigEndian.Uint32(b[4:]))
	numOfUint32 := int(binary.BigEndian.Uint32(b[8:]))
	//after num of values
	lenOfValsStartPointer := 12
	valsPointer := lenOfValsStartPointer + numOfUint8 + numOfUint16*2 + numOfUint32*4
	var (
		lenOfValStart int
		lenOfValEnd   int
	)

	switch {
	case i < numOfUint8:
		lenOfValEnd = int(b[lenOfValsStartPointer+i])
		if i > 0 {
			lenOfValStart = int(b[lenOfValsStartPointer+i-1])
		}
	case i < numOfUint8+numOfUint16:
		one := (i-numOfUint8)*2 + numOfUint8
		lenOfValEnd = int(binary.BigEndian.Uint16(b[lenOfValsStartPointer+one : lenOfValsStartPointer+one+2]))
		if i-1 < numOfUint8 {
			lenOfValStart = int(b[lenOfValsStartPointer+i-1])
		} else {
			one = (i-1)*2 - numOfUint8
			lenOfValStart = int(binary.BigEndian.Uint16(b[lenOfValsStartPointer+one : lenOfValsStartPointer+one+2]))
		}
	case i < numOfUint8+numOfUint16+numOfUint32:
		one := lenOfValsStartPointer + numOfUint8 + numOfUint16*2 + (i-numOfUint8-numOfUint16)*4
		lenOfValEnd = int(binary.BigEndian.Uint32(b[one : one+4]))
		if i-1 < numOfUint8+numOfUint16 {
			one = lenOfValsStartPointer + (i-1)*2 - numOfUint8
			lenOfValStart = int(binary.BigEndian.Uint16(b[one : one+2]))
		} else {
			one = lenOfValsStartPointer + numOfUint8 + numOfUint16*2 + (i-1-numOfUint8-numOfUint16)*4
			lenOfValStart = int(binary.BigEndian.Uint32(b[one : one+4]))
		}
	default:
		return nil, changeset.ErrFindValue
	}
	return b[valsPointer+lenOfValStart : valsPointer+lenOfValEnd], nil
}
