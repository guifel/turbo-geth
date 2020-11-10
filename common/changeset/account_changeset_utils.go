package changeset

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/ledgerwatch/turbo-geth/common/dbutils"
	"github.com/ledgerwatch/turbo-geth/ethdb"
)

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

func findInAccountChangeSetBytes(b []byte, k []byte, keyLen int) ([]byte, error) {
	if len(b) == 0 {
		return nil, ErrNotFound
	}

	if len(b) < 8 {
		return nil, fmt.Errorf("decode: input too short (%d bytes)", len(b))
	}

	n := int(binary.BigEndian.Uint32(b[0:]))

	if n == 0 {
		return nil, ErrNotFound
	}

	valOffset := 4 + n*keyLen + 4*n
	if len(b) < valOffset {
		return nil, fmt.Errorf("decode: input too short (%d bytes, expected at least %d bytes)", len(b), valOffset)
	}

	totalValLength := int(binary.BigEndian.Uint32(b[valOffset-4:]))
	if len(b) < valOffset+totalValLength {
		return nil, fmt.Errorf("decode: input too short (%d bytes, expected at least %d bytes)", len(b), valOffset+totalValLength)
	}

	id := sort.Search(n, func(i int) bool {
		res := bytes.Compare(b[4+i*keyLen:4+(i+1)*keyLen], k)
		return res >= 0
	})

	if id >= n {
		return nil, ErrNotFound
	}

	if !bytes.Equal(b[4+id*keyLen:4+(id+1)*keyLen], k) {
		return nil, ErrNotFound
	}

	idx0 := 0
	if id > 0 {
		idx0 = int(binary.BigEndian.Uint32(b[4+n*keyLen+4*(id-1):]))
	}

	idx1 := int(binary.BigEndian.Uint32(b[4+n*keyLen+4*id:]))
	return b[valOffset+idx0 : valOffset+idx1], nil
}

func findInAccountChangeSet(c ethdb.CursorDupSort, blockNumber uint64, key []byte, keyLen int) ([]byte, error) {
	fromDBFormat := FromDBFormat(keyLen)
	k, v, err := c.SeekBothRange(dbutils.EncodeBlockNumber(blockNumber), key)
	if err != nil {
		return nil, err
	}
	_, k, v = fromDBFormat(k, v)
	if !bytes.HasPrefix(k, key) {
		return nil, nil
	}
	return v, nil
}

func encodeAccounts2(blockN uint64, s *ChangeSet, f func(k, v []byte) error) error {
	sort.Sort(s)
	for _, cs := range s.Changes {
		newK := dbutils.EncodeBlockNumber(blockN)
		newV := make([]byte, len(cs.Key)+len(cs.Value))
		copy(newV, cs.Key)
		copy(newV[len(cs.Key):], cs.Value)
		if err := f(newK, newV); err != nil {
			return err
		}
	}
	return nil
}
