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

func TestProbeMainValidatesResponderMode(t *testing.T) {
	for _, args := range [][]string{
		{"--tcp-listen", "127.0.0.1:8080", "--response-nonce", strings.Repeat("a", 32)},
		{"--tcp-listen", ":8080"},
		{"--response-nonce", strings.Repeat("a", 32)},
		{"--tcp-listen", ":0", "--response-nonce", strings.Repeat("z", 32)},
		{"--tcp-listen", ":8080", "--response-nonce", strings.Repeat("a", 32), "--request-json", `{}`},
		{"--listen", "--request-json", `{}`},
	} {
		var out, errout bytes.Buffer
		if code := run(args, strings.NewReader(""), &out, &errout); code != 2 {
			t.Fatalf("args %v returned %d: %s", args, code, errout.String())
		}
	}
}
