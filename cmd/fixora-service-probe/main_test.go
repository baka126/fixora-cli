package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestProbeMainRejectsUnsafeJSON(t *testing.T) {
	for _, input := range []string{`{"secret":"synthetic-private"}`, `{} {}`, strings.Repeat("x", 64<<10+1)} {
		var out, errout bytes.Buffer
		if code := run(nil, strings.NewReader(input), &out, &errout); code == 0 {
			t.Fatal("unsafe input")
		}
		if strings.Contains(errout.String(), "synthetic-private") {
			t.Fatal("input leaked")
		}
	}
}
