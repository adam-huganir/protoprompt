package diffx

import (
	"strings"
	"testing"
)

func TestIdenticalNoHunks(t *testing.T) {
	a := "line1\nline2\nline3"
	h := Diff(a, a)
	if len(h) != 0 {
		t.Fatalf("expected no hunks, got %d", len(h))
	}
}

func TestOneChangedMiddleLine(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		l := "l" + string(rune('A'+i-1))
		if i == 5 {
			l = "original"
		}
		lines = append(lines, l)
	}
	from := strings.Join(lines, "\n")
	lines[4] = "changed"
	to := strings.Join(lines, "\n")

	hunks := Diff(from, to)
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	var dels, adds int
	for _, l := range h.Lines {
		switch l.T {
		case "del":
			dels++
			if l.Text != "original" {
				t.Errorf("del text = %q", l.Text)
			}
		case "add":
			adds++
			if l.Text != "changed" {
				t.Errorf("add text = %q", l.Text)
			}
		}
	}
	if dels != 1 || adds != 1 {
		t.Errorf("expected 1 del + 1 add, got %d/%d", dels, adds)
	}
	// Window: lines 2..8 of from (line 5 changed) → start 2, ctx count 6.
	if h.Start != 2 {
		t.Errorf("hunk.Start = %d, want 2", h.Start)
	}
	if h.Count != 6 {
		t.Errorf("hunk.Count(ctx) = %d, want 6", h.Count)
	}
}

func TestAppendLine(t *testing.T) {
	hunks := Diff("a\nb\nc", "a\nb\nc\nd")
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	h := hunks[0]
	if len(h.Lines) == 0 || h.Lines[len(h.Lines)-1].T != "add" {
		t.Fatalf("expected trailing add, got %+v", h.Lines)
	}
}

func TestEmptyFrom(t *testing.T) {
	hunks := Diff("", "hello\nworld")
	if len(hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d", len(hunks))
	}
	if len(hunks[0].Lines) != 2 {
		t.Fatalf("expected 2 add lines, got %d", len(hunks[0].Lines))
	}
}

func TestTwoHunksApart(t *testing.T) {
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, strings.Repeat("x", 3)+itoa(i))
	}
	from := strings.Join(lines, "\n")
	lines[1] = "CHANGED2"
	lines[15] = "CHANGED16"
	to := strings.Join(lines, "\n")
	hunks := Diff(from, to)
	if len(hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d", len(hunks))
	}
	if hunks[0].Start != 1 {
		t.Errorf("hunk0 start = %d, want 1", hunks[0].Start)
	}
	if hunks[1].Start != 13 {
		t.Errorf("hunk1 start = %d, want 13", hunks[1].Start)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
