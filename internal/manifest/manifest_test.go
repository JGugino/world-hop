package manifest

import (
	"strings"
	"testing"
	"time"
)

func sample() *Manifest {
	h := strings.Repeat("a", 64)
	p := strings.Repeat("b", 64)
	return &Manifest{
		Version: Version, WorldID: "My-World", DeviceID: "abc123", Generation: 1,
		CreatedAt: time.Unix(1, 0).UTC(),
		Files:     []File{{Path: "region/r.0.0.mca", Size: 10, Chunks: []string{h}}},
		Chunks:    map[string]ChunkLoc{h: {Pack: p, Offset: 0, Length: 10}},
	}
}

func TestRoundTrip(t *testing.T) {
	b, err := sample().Marshal()
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name() != "manifest-000000000001-abc123.json" {
		t.Fatal(m.Name())
	}
}

func TestRejects(t *testing.T) {
	for name, mut := range map[string]func(*Manifest){
		"traversal":   func(m *Manifest) { m.Files[0].Path = "../evil" },
		"absolute":    func(m *Manifest) { m.Files[0].Path = "/etc/passwd" },
		"backslash":   func(m *Manifest) { m.Files[0].Path = `a\b` },
		"lock":        func(m *Manifest) { m.Files[0].Path = "data/session.lock" },
		"size":        func(m *Manifest) { m.Files[0].Size = 11 },
		"unknown":     func(m *Manifest) { m.Files[0].Chunks = []string{strings.Repeat("c", 64)} },
		"badworld":    func(m *Manifest) { m.WorldID = "../x" },
		"dup":         func(m *Manifest) { m.Files = append(m.Files, m.Files[0]) },
		"badparent":   func(m *Manifest) { m.Parents = []string{"../../x"} },
		"generation0": func(m *Manifest) { m.Generation = 0 },
	} {
		m := sample()
		mut(m)
		b, _ := m.Marshal()
		if _, err := Parse(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func FuzzParse(f *testing.F) {
	b, _ := sample().Marshal()
	f.Add(b)
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := Parse(data)
		if err != nil {
			return
		}
		for _, file := range m.Files { // anything accepted must be safe
			if !SafePath(file.Path) {
				t.Fatalf("accepted unsafe path %q", file.Path)
			}
		}
	})
}
