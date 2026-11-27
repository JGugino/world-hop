// Package chunker splits a byte stream into content-defined chunks using a
// gear rolling hash (FastCDC style), so that a small edit inside a large
// region file only changes the chunks around the edit.
//
// Why: world files are big and mostly unchanged between syncs. Cutting at fixed sizes
// would shift every boundary after an insertion; cutting where the content says to (a
// boundary when the rolling hash of recent bytes matches a pattern) keeps unchanged
// regions in identical chunks, so only the changed chunks are uploaded.
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
	// MinSize, AvgBits and MaxSize bound the chunk size: never smaller than 256 KiB
	// (except at the end of a file), about 1 MiB on average (a boundary when the low
	// AvgBits bits of the hash are zero), and never larger than 4 MiB.
	MinSize = 256 << 10
	AvgBits = 20
	MaxSize = 4 << 20
)

// gear is the table of 256 random-looking numbers the rolling hash mixes in per
// byte. It is derived from SHA-256, not random at startup, so every computer cuts
// the same data at the same places.
var gear [256]uint64

// init fills the gear table deterministically (see gear).
func init() {
	// Deterministic table so chunk boundaries are identical on every device
	// and every Go version.
	for i := range gear {
		sum := sha256.Sum256([]byte{byte(i), 'w', 'h', 'g', 'e', 'a', 'r'})
		gear[i] = binary.BigEndian.Uint64(sum[:8])
	}
}

// Hash returns the hex SHA-256 of data. It is the chunk's identity, and a pack's name.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Split reads r to EOF and calls fn for every chunk. The slice passed to fn
// is only valid until fn returns (the buffer is reused), so fn must copy what it keeps.
// Reading in fixed buffers keeps memory use at about MaxSize however large the file is.
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
