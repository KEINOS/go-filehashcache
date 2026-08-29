package fhc

import (
	"testing"
)

func BenchmarkDecodeRecord(b *testing.B) {
	rec := new(cacheRecord)
	rec.recordType = recordTypeFile

	data := encodeRecord(*rec)

	for b.Loop() {
		_, _ = decodeRecord(data)
	}
}

func FuzzFHC(f *testing.F) {
	rec := new(cacheRecord)
	rec.recordType = recordTypeFile

	valid := encodeRecord(*rec)
	f.Add(valid)
	f.Add([]byte{})
	f.Add(valid[:8])
	f.Add(append(append([]byte(nil), valid...), 0))
	f.Add(replaceByte(valid, 0, 'X'))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = decodeRecord(data)
	})
}
