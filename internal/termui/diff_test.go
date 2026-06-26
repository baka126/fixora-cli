package termui

import (
	"strings"
	"testing"
)

func TestColorDiffColorsOnlyChangedLinesAndHunks(t *testing.T) {
	diff := "--- old\n+++ new\n@@ -1 +1 @@\n-old\n+new\n context\n"
	got := ColorDiff(diff, false)
	if strings.Contains(got, "\033[31m---") || strings.Contains(got, "\033[32m+++") {
		t.Fatalf("file headers should not be colored as changed lines:\n%q", got)
	}
	for _, want := range []string{"\033[36m\033[1m@@ -1 +1 @@", "\033[31m-old", "\033[32m+new"} {
		if !strings.Contains(got, want) {
			t.Fatalf("colored diff missing %q:\n%q", want, got)
		}
	}
}

func TestColorDiffNoColorReturnsOriginal(t *testing.T) {
	diff := "@@ -1 +1 @@\n-old\n+new\n"
	if got := ColorDiff(diff, true); got != diff {
		t.Fatalf("no-color diff changed:\nwant %q\ngot  %q", diff, got)
	}
}

func TestColorDiffWrapsLongVisualLines(t *testing.T) {
	diff := "+image: registry.example.test/" + strings.Repeat("a", 140) + "\n"
	got := wrapDiffForTerminal(diff, wideTextWidth)
	if !strings.Contains(got, "\\\n  ") {
		t.Fatalf("expected long visual line to wrap: %q", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if len(line) > wideTextWidth {
			t.Fatalf("rendered diff line exceeds width %d: %d %q", wideTextWidth, len(line), line)
		}
	}
}

func TestDisplayDiffWrapsWithoutColor(t *testing.T) {
	diff := "+image: registry.example.test/" + strings.Repeat("a", 140) + "\n"
	got := DisplayDiff(diff, true)
	if strings.Contains(got, "\033[") {
		t.Fatalf("no-color display contains ANSI sequence: %q", got)
	}
	if !strings.Contains(got, "\\\n  ") {
		t.Fatalf("expected wrapped no-color diff: %q", got)
	}
}
