package changeset

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/ledgerwatch/turbo-geth/common"
	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/ethdb"
)

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
		return nil, ErrFindValue
	}
	return b[valsPointer+lenOfValStart : valsPointer+lenOfValEnd], nil
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
		incarnation := DefaultIncarnation
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

func findInStorageChangeSet(b []byte, keyPrefixLen int, k []byte) ([]byte, error) {
	return doSearch(
		b,
		keyPrefixLen,
		k[0:keyPrefixLen],
		k[keyPrefixLen+common.IncarnationLength:keyPrefixLen+common.HashLength+common.IncarnationLength],
		binary.BigEndian.Uint64(k[keyPrefixLen:]), /* incarnation */
	)
}

func findWithoutIncarnationInStorageChangeSet(b []byte, keyPrefixLen int, addrBytesToFind []byte, keyBytesToFind []byte) ([]byte, error) {
	return doSearch(
		b,
		keyPrefixLen,
		addrBytesToFind,
		keyBytesToFind,
		0, /* incarnation */
	)
}

func doSearch(
	b []byte,
	keyPrefixLen int,
	addrBytesToFind []byte,
	keyBytesToFind []byte,
	incarnation uint64,
) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrNotFound
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("decode: input too short (%d bytes)", len(b))
	}

	numOfUniqueElements := int(binary.BigEndian.Uint32(b))
	if numOfUniqueElements == 0 {
		return nil, ErrNotFound
	}
	incarnatonsInfo := 4 + numOfUniqueElements*(keyPrefixLen+4)
	numOfElements := int(binary.BigEndian.Uint32(b[incarnatonsInfo-4:]))
	numOfNotDefaultIncarnations := int(binary.BigEndian.Uint32(b[incarnatonsInfo:]))
	incarnationsStart := incarnatonsInfo + 4
	keysStart := incarnationsStart + numOfNotDefaultIncarnations*12
	valsInfoStart := keysStart + numOfElements*common.HashLength

	addrID := sort.Search(numOfUniqueElements, func(i int) bool {
		addrBytes := b[4+i*(4+keyPrefixLen) : 4+i*(4+keyPrefixLen)+keyPrefixLen]
		cmp := bytes.Compare(addrBytes, addrBytesToFind)
		return cmp >= 0
	})

	if addrID == numOfUniqueElements {
		return nil, ErrNotFound
	}
	if !bytes.Equal(b[4+addrID*(4+keyPrefixLen):4+addrID*(4+keyPrefixLen)+keyPrefixLen], addrBytesToFind) {
		return nil, ErrNotFound
	}

	numOfIncarnationsForThisAddress := 1
	for tryAddrID := addrID + 1; tryAddrID < numOfUniqueElements; tryAddrID++ {
		if !bytes.Equal(b[4+tryAddrID*(4+keyPrefixLen):4+tryAddrID*(4+keyPrefixLen)+keyPrefixLen], addrBytesToFind) {
			break
		} else {
			numOfIncarnationsForThisAddress++
		}
	}

	if incarnation > 0 {
		found := false

		for i := 0; i < numOfIncarnationsForThisAddress; i++ {
			// Find incarnation
			incIndex := sort.Search(numOfNotDefaultIncarnations, func(i int) bool {
				id := int(binary.BigEndian.Uint32(b[incarnationsStart+12*i:]))
				return id >= addrID
			})
			var foundIncarnation uint64 = DefaultIncarnation
			if incIndex < numOfNotDefaultIncarnations && int(binary.BigEndian.Uint32(b[incarnationsStart+12*incIndex:])) == addrID {
				foundIncarnation = binary.BigEndian.Uint64(b[incarnationsStart+12*incIndex+4:])
			}

			if foundIncarnation == incarnation {
				found = true
				break
			} else {
				addrID++
			}
		}

		if !found {
			return nil, ErrNotFound
		}
	}

	from := 0
	if addrID > 0 {
		from = int(binary.BigEndian.Uint32(b[4+addrID*(keyPrefixLen+4)-4:]))
	}
	to := int(binary.BigEndian.Uint32(b[4+addrID*(keyPrefixLen+4)+keyPrefixLen:]))
	keyIndex := sort.Search(to-from, func(i int) bool {
		index := from + i
		key := b[keysStart+common.HashLength*index : keysStart+common.HashLength*index+common.HashLength]
		cmp := bytes.Compare(key, keyBytesToFind)
		return cmp >= 0
	})
	index := from + keyIndex
	if index == to {
		return nil, ErrNotFound
	}
	if !bytes.Equal(b[keysStart+common.HashLength*index:keysStart+common.HashLength*index+common.HashLength], keyBytesToFind) {
		return nil, ErrNotFound
	}
	return findValue(b[valsInfoStart:], index)
}

type contractKeys struct {
	AddrBytes   []byte // either a hash of address or raw address
	Incarnation uint64
	Keys        [][]byte
	Vals        [][]byte
}

func walkReverse(c ethdb.CursorDupSort, from, to uint64, keyPrefixLen int, f func(blockNum uint64, k, v []byte) error) error {
	_, _, err := c.Seek(dbutils.EncodeBlockNumber(to + 1))
	if err != nil {
		return err
	}
	fromDBFormat := FromDBFormat(keyPrefixLen)
	var blockNum uint64
	for k, v, err := c.Prev(); k != nil; k, v, err = c.Prev() {
		if err != nil {
			return err
		}
		blockNum, k, v = fromDBFormat(k, v)
		if blockNum < from {
			break
		}

		err = f(blockNum, k, v)
		if err != nil {
			return err
		}
	}

	return nil
}

func walk(c ethdb.CursorDupSort, from, to uint64, keyPrefixLen int, f func(blockN uint64, k, v []byte) error) error {
	fromDBFormat := FromDBFormat(keyPrefixLen)
	var blockNum uint64
	for k, v, err := c.Seek(dbutils.EncodeBlockNumber(from)); k != nil; k, v, err = c.Next() {
		if err != nil {
			return err
		}
		blockNum, k, v = fromDBFormat(k, v)
		if blockNum > to {
			break
		}

		err = f(blockNum, k, v)
		if err != nil {
			return err
		}
	}

	return nil
}

func findInStorageChangeSet2(c ethdb.CursorDupSort, blockNumber uint64, keyPrefixLen int, k []byte) ([]byte, error) {
	return doSearch2(
		c, blockNumber,
		keyPrefixLen,
		k[:keyPrefixLen],
		k[keyPrefixLen+common.IncarnationLength:keyPrefixLen+common.HashLength+common.IncarnationLength],
		binary.BigEndian.Uint64(k[keyPrefixLen:]), /* incarnation */
	)
}

func findWithoutIncarnationInStorageChangeSet2(c ethdb.CursorDupSort, blockNumber uint64, keyPrefixLen int, addrBytesToFind []byte, keyBytesToFind []byte) ([]byte, error) {
	return doSearch2(
		c, blockNumber,
		keyPrefixLen,
		addrBytesToFind,
		keyBytesToFind,
		0, /* incarnation */
	)
}

func doSearch2(
	c ethdb.CursorDupSort,
	blockNumber uint64,
	keyPrefixLen int,
	addrBytesToFind []byte,
	keyBytesToFind []byte,
	incarnation uint64,
) ([]byte, error) {
	fromDBFormat := FromDBFormat(keyPrefixLen)
	if incarnation == 0 {
		seek := make([]byte, 8+keyPrefixLen)
		binary.BigEndian.PutUint64(seek, blockNumber)
		copy(seek[8:], addrBytesToFind)
		for k, v, err := c.Seek(seek); k != nil; k, v, err = c.Next() {
			if err != nil {
				return nil, err
			}
			_, k, v = fromDBFormat(k, v)
			if !bytes.HasPrefix(k, addrBytesToFind) {
				return nil, ErrNotFound
			}

			stHash := k[keyPrefixLen+common.IncarnationLength:]
			if bytes.Equal(stHash, keyBytesToFind) {
				return v, nil
			}
		}
		return nil, ErrNotFound
	}

	seek := make([]byte, 8+keyPrefixLen+common.IncarnationLength)
	binary.BigEndian.PutUint64(seek, blockNumber)
	copy(seek[8:], addrBytesToFind)
	binary.BigEndian.PutUint64(seek[8+keyPrefixLen:], incarnation)
	k, v, err := c.SeekBothRange(seek, keyBytesToFind)
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, ErrNotFound
	}
	if !bytes.HasPrefix(v, keyBytesToFind) {
		return nil, ErrNotFound
	}
	_, _, v = fromDBFormat(k, v)
	return v, nil
}

func encodeStorage2(blockN uint64, s *ChangeSet, keyPrefixLen uint32, f func(k, v []byte) error) error {
	sort.Sort(s)
	keyPart := keyPrefixLen + common.IncarnationLength
	for _, cs := range s.Changes {
		newK := make([]byte, 8+keyPart)
		binary.BigEndian.PutUint64(newK, blockN)
		copy(newK[8:], cs.Key[:keyPart])
		newV := make([]byte, 0, common.HashLength+len(cs.Value))
		newV = append(append(newV, cs.Key[keyPart:]...), cs.Value...)
		if err := f(newK, newV); err != nil {
			return err
		}
	}
	return nil
}
