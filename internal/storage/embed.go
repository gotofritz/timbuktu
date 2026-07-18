package storage

import (
	"encoding/binary"
	"fmt"
	"math"
)

// Float32SliceToBlob encodes a []float32 as little-endian bytes.
func Float32SliceToBlob(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// BlobToFloat32Slice decodes little-endian bytes back to []float32.
func BlobToFloat32Slice(b []byte) ([]float32, error) {
	if len(b) == 0 {
		return []float32{}, nil
	}
	if len(b)%4 != 0 {
		return nil, fmt.Errorf("storage: blob length %d not a multiple of 4", len(b))
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}
