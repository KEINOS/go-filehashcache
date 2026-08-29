package fhc

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
)

const (
	cacheRecordSize      = 40
	recordTypeFile       = byte(1)
	recordTypeDirectory  = byte(2)
	algorithmXXH3        = byte(1)
	nanosecondsPerSecond = 1_000_000_000
)

var errInvalidCacheRecord = errors.New("invalid cache record")

type cacheRecord struct {
	contentHash        uint64
	size               uint64
	directoryHash      uint64
	directEntryCount   uint64
	recursiveFileCount uint64
	mtimeNsec          uint32
	mtimeSec           int64
	recordType         byte
}

func encodeRecord(record cacheRecord) []byte {
	data := make([]byte, cacheRecordSize)
	copy(data[0:4], "FHC1")
	data[4] = record.recordType
	data[5] = algorithmXXH3

	switch record.recordType {
	case recordTypeFile:
		binary.BigEndian.PutUint64(data[8:16], record.contentHash)
		binary.BigEndian.PutUint64(data[16:24], record.size)
		// The format requires the two's-complement representation.
		binary.BigEndian.PutUint64(data[24:32], uint64(record.mtimeSec)) //nolint:gosec
		binary.BigEndian.PutUint32(data[32:36], record.mtimeNsec)
	case recordTypeDirectory:
		binary.BigEndian.PutUint64(data[8:16], record.directoryHash)
		binary.BigEndian.PutUint64(data[16:24], record.recursiveFileCount)
		binary.BigEndian.PutUint64(data[24:32], record.directEntryCount)
	}

	checksum := crc32.Checksum(data[:36], crc32.MakeTable(crc32.Castagnoli))
	binary.BigEndian.PutUint32(data[36:40], checksum)

	return data
}

func decodeRecord(data []byte) (cacheRecord, error) {
	if !validRecordEnvelope(data) {
		return cacheRecord{}, errInvalidCacheRecord
	}

	if data[4] == recordTypeFile {
		return decodeFileRecord(data)
	}

	return decodeDirectoryRecord(data)
}

func validRecordEnvelope(data []byte) bool {
	if len(data) != cacheRecordSize {
		return false
	}

	if string(data[:4]) != "FHC1" || (data[4] != recordTypeFile && data[4] != recordTypeDirectory) {
		return false
	}

	if data[5] != algorithmXXH3 || data[6] != 0 || data[7] != 0 {
		return false
	}

	wantChecksum := crc32.Checksum(data[:36], crc32.MakeTable(crc32.Castagnoli))

	return binary.BigEndian.Uint32(data[36:40]) == wantChecksum
}

func decodeFileRecord(data []byte) (cacheRecord, error) {
	rec := new(cacheRecord)
	rec.recordType = recordTypeFile
	rec.contentHash = binary.BigEndian.Uint64(data[8:16])
	rec.size = binary.BigEndian.Uint64(data[16:24])
	// The format stores signed seconds as a two's-complement uint64.
	rec.mtimeSec = int64(binary.BigEndian.Uint64(data[24:32])) //nolint:gosec
	rec.mtimeNsec = binary.BigEndian.Uint32(data[32:36])

	if rec.mtimeNsec >= nanosecondsPerSecond {
		return cacheRecord{}, errInvalidCacheRecord
	}

	return *rec, nil
}

func decodeDirectoryRecord(data []byte) (cacheRecord, error) {
	if data[32] != 0 || data[33] != 0 || data[34] != 0 || data[35] != 0 {
		return cacheRecord{}, errInvalidCacheRecord
	}

	rec := new(cacheRecord)
	rec.recordType = recordTypeDirectory
	rec.directoryHash = binary.BigEndian.Uint64(data[8:16])
	rec.recursiveFileCount = binary.BigEndian.Uint64(data[16:24])
	rec.directEntryCount = binary.BigEndian.Uint64(data[24:32])

	return *rec, nil
}
