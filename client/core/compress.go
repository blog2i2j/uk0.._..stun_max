package core

import (
	"bytes"
	"compress/flate"
	"io"
	"sync"
)

// Compression level: 1 = fastest (best for real-time tunnel)
const compressLevel = flate.BestSpeed

// compressBuf pool to avoid allocation per packet
var compressBufPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

// Compress compresses data using deflate. Returns compressed data with a 1-byte header:
// [0x01][compressed data] — compressed
// [0x00][raw data]        — not compressed (data already compressed or too small)
func Compress(data []byte) []byte {
	// Don't compress tiny payloads (overhead > savings)
	if len(data) < 128 {
		out := make([]byte, 1+len(data))
		out[0] = 0x00 // not compressed
		copy(out[1:], data)
		return out
	}

	buf := compressBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	buf.WriteByte(0x01) // compressed marker

	w, err := flate.NewWriter(buf, compressLevel)
	if err != nil {
		buf.Reset()
		compressBufPool.Put(buf)
		out := make([]byte, 1+len(data))
		out[0] = 0x00
		copy(out[1:], data)
		return out
	}
	w.Write(data)
	w.Close()

	// If compressed is larger than original, send raw
	if buf.Len() >= len(data)+1 {
		compressBufPool.Put(buf)
		out := make([]byte, 1+len(data))
		out[0] = 0x00
		copy(out[1:], data)
		return out
	}

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	compressBufPool.Put(buf)
	return result
}

// Decompress decompresses data. Reads the 1-byte header to determine format.
func Decompress(data []byte) ([]byte, error) {
	if len(data) < 1 {
		return data, nil
	}

	if data[0] == 0x00 {
		// Not compressed
		return data[1:], nil
	}

	// Compressed with deflate
	r := flate.NewReader(bytes.NewReader(data[1:]))
	defer r.Close()
	return io.ReadAll(r)
}
