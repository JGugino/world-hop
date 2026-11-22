package chunker

import (
	"bytes"
	"math/rand"
	"testing"
)

func randBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func split(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var out [][]byte
	if err := Split(bytes.NewReader(data), func(c []byte) error {
		out = append(out, append([]byte(nil), c...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRoundTripAndBounds(t *testing.T) {
	for _, n := range []int{0, 1, MinSize, MinSize + 1, MaxSize, MaxSize + 1, 10 << 20} {
		data := randBytes(int64(n), n)
		chunks := split(t, data)
		var re []byte
		for i, c := range chunks {
			if len(c) > MaxSize || (len(c) < MinSize && i != len(chunks)-1) {
				t.Fatalf("n=%d chunk %d bad size %d", n, i, len(c))
			}
			re = append(re, c...)
		}
		if !bytes.Equal(re, data) {
			t.Fatalf("n=%d reassembly differs", n)
		}
	}
}

func TestDeterministic(t *testing.T) {
	data := randBytes(1, 6<<20)
	a, b := split(t, data), split(t, data)
	if len(a) != len(b) {
		t.Fatal("different chunk counts")
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatal("different chunks")
		}
	}
}

func TestInsertionOnlyChangesNearbyChunks(t *testing.T) {
	data := randBytes(2, 16<<20)
	edited := append(append(append([]byte(nil), data[:5<<20]...), []byte("inserted")...), data[5<<20:]...)
	have := map[string]bool{}
	for _, c := range split(t, data) {
		have[Hash(c)] = true
	}
	changed := 0
	cs := split(t, edited)
	for _, c := range cs {
		if !have[Hash(c)] {
			changed++
		}
	}
	if changed > 3 || changed == len(cs) {
		t.Fatalf("%d of %d chunks changed after a tiny insertion", changed, len(cs))
	}
}
