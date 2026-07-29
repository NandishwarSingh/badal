// Package chunks splits content into content-defined chunks addressed by BLAKE3.
//
// FastCDC picks boundaries from a rolling hash of the content rather than at
// fixed offsets. Insert a byte near the start of a file and only the chunk
// containing it changes -- every later chunk keeps its hash, so re-uploading
// costs one chunk instead of the whole file. Deduplication across files falls
// out of the same property.
package chunks

import (
	"encoding/hex"
	"fmt"
	"io"

	fastcdc "github.com/jotfs/fastcdc-go"
	"lukechampine.com/blake3"
)

type Chunk struct {
	Hash   []byte
	Size   int
	Offset int64
	Data   []byte
}

type Options struct{ Min, Avg, Max int }

func DefaultOptions() Options {
	return Options{Min: 256 * 1024, Avg: 1024 * 1024, Max: 4 * 1024 * 1024}
}

// Split streams r into chunks, calling fn for each. It also returns the
// BLAKE3 of the entire content and the total size, so a caller gets the
// whole-blob identity in the same pass.
func Split(r io.Reader, opt Options, fn func(Chunk) error) (wholeHash []byte, total int64, err error) {
	whole := blake3.New(32, nil)
	chunker, err := fastcdc.NewChunker(io.TeeReader(r, whole), fastcdc.Options{
		MinSize:     opt.Min,
		AverageSize: opt.Avg,
		MaxSize:     opt.Max,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("chunker: %w", err)
	}
	var offset int64
	for {
		c, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("chunk: %w", err)
		}
		// chunker reuses its buffer between calls
		data := make([]byte, len(c.Data))
		copy(data, c.Data)

		sum := blake3.Sum256(data)
		if err := fn(Chunk{Hash: sum[:], Size: len(data), Offset: offset, Data: data}); err != nil {
			return nil, 0, err
		}
		offset += int64(len(data))
	}
	return whole.Sum(nil), offset, nil
}

func Hex(h []byte) string { return hex.EncodeToString(h) }

func FromHex(s string) ([]byte, error) { return hex.DecodeString(s) }

// Key is the object-store path for a chunk. The two-level fan-out keeps any
// single prefix from accumulating millions of keys.
func Key(hash []byte) string {
	h := hex.EncodeToString(hash)
	return fmt.Sprintf("chunks/%s/%s/%s", h[0:2], h[2:4], h)
}

// Verify guards against a corrupted or substituted chunk on read.
func Verify(hash, data []byte) bool {
	sum := blake3.Sum256(data)
	return string(sum[:]) == string(hash)
}
