// Package chunker splits a byte stream into content-defined chunks using a
// gear rolling hash (FastCDC style), so that a small edit inside a large
// region file only changes the chunks around the edit.
package chunker

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
)

// Chunk size bounds. Average is roughly 1 MiB.
const (
	MinSize = 256 << 10
	AvgBits = 20
	MaxSize = 4 << 20
)

var gear [256]uint64

func init() {
	// Deterministic table so chunk boundaries are identical on every device
	// and every Go version.
	for i := range gear {
		sum := sha256.Sum256([]byte{byte(i), 'w', 'h', 'g', 'e', 'a', 'r'})
		gear[i] = binary.BigEndian.Uint64(sum[:8])
	}
}

// Hash returns the hex SHA-256 of data.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Split reads r to EOF and calls fn for every chunk. The slice passed to fn
// is only valid until fn returns.
func Split(r io.Reader, fn func(data []byte) error) error {
	const mask = (1 << AvgBits) - 1
	buf := make([]byte, MaxSize)
	n := 0 // valid bytes in buf
	eof := false
	for {
		if !eof && n < MaxSize {
			m, err := io.ReadFull(r, buf[n:])
			n += m
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				eof = true
			} else if err != nil {
				return err
			}
		}
		if n == 0 {
			return nil
		}
		cut := n
		if n > MinSize {
			var h uint64
			cut = n
			for i := MinSize; i < n; i++ {
				h = (h << 1) + gear[buf[i]]
				if h&mask == 0 {
					cut = i + 1
					break
				}
			}
		}
		if cut == n && !eof && n == MaxSize {
			cut = MaxSize
		}
		if err := fn(buf[:cut]); err != nil {
			return err
		}
		copy(buf, buf[cut:n])
		n -= cut
	}
}
