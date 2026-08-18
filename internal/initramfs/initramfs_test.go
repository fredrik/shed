package initramfs

import (
	"bytes"
	"strconv"
	"testing"
)

// parseNewc walks a newc cpio archive, returning entry names.
func parseNewc(t *testing.T, data []byte) []string {
	t.Helper()
	var names []string
	off := 0
	for {
		if off+110 > len(data) {
			t.Fatalf("truncated header at %d", off)
		}
		hdr := data[off : off+110]
		if string(hdr[:6]) != "070701" {
			t.Fatalf("bad magic at %d: %q", off, hdr[:6])
		}
		field := func(i int) int {
			v, err := strconv.ParseUint(string(hdr[6+i*8:6+(i+1)*8]), 16, 64)
			if err != nil {
				t.Fatalf("bad field %d: %v", i, err)
			}
			return int(v)
		}
		fileSize := field(6)
		nameSize := field(11)
		nameStart := off + 110
		name := string(data[nameStart : nameStart+nameSize-1])
		if name == "TRAILER!!!" {
			return names
		}
		names = append(names, name)
		off = align4(nameStart + nameSize)
		off = align4(off + fileSize)
	}
}

func align4(n int) int {
	if r := n % 4; r != 0 {
		return n + 4 - r
	}
	return n
}

func TestBuildArchiveShape(t *testing.T) {
	var buf bytes.Buffer
	if err := Build(&buf); err != nil {
		t.Fatal(err)
	}
	names := parseNewc(t, buf.Bytes())
	want := map[string]bool{"init": true, "dev/console": true, "dev": true, "proc": true, "sys": true, "newroot": true, "lower": true, "data": true}
	for _, n := range names {
		delete(want, n)
	}
	if len(want) > 0 {
		t.Fatalf("missing entries: %v (got %v)", want, names)
	}
}

func TestInitIsLargestEntry(t *testing.T) {
	var buf bytes.Buffer
	if err := Build(&buf); err != nil {
		t.Fatal(err)
	}
	if len(agentBinary) < 1<<20 {
		t.Fatalf("embedded agent suspiciously small: %d bytes", len(agentBinary))
	}
	if buf.Len() < len(agentBinary) {
		t.Fatal("archive smaller than agent binary")
	}
}
