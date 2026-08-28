package fhc

import (
	"testing"
)

func BenchmarkDecodeRecord(b *testing.B) {
	data := encodeRecord(cacheRecord{recordType: recordTypeFile})

	for b.Loop() {
		_, _ = decodeRecord(data)
	}
}

func FuzzFHC(f *testing.F) {
	valid := encodeRecord(cacheRecord{recordType: recordTypeFile})
	f.Add(valid)
	f.Add([]byte{})
	f.Add(valid[:8])
	f.Add(append(append([]byte(nil), valid...), 0))
	f.Add(replaceByte(valid, 0, 'X'))

	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = decodeRecord(data)
	})
}
