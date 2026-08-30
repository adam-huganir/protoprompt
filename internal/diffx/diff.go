// Package diffx computes line-level unified diffs grouped into hunks.
package diffx

import "strings"

const context = 3

// Line is one line of a hunk. N is the 1-based line number in the "from"
// text, present on context lines only.
type Line struct {
	T    string `json:"t"` // "ctx" | "add" | "del"
	N    *int   `json:"n,omitempty"`
	Text string `json:"text"`
}

// Hunk is one diff region. Start is the 1-based line number in the from text
// where the hunk begins; Count is the number of context lines in it.
type Hunk struct {
	Start int    `json:"start"`
	Count int    `json:"count"`
	Lines []Line `json:"lines"`
}

type op struct {
	kind byte // ' ' ctx, '-' del, '+' add
	text string
}

// Diff returns unified-diff hunks between the from and to texts. Identical
// inputs yield an empty hunk list.
func Diff(from, to string) []Hunk {
	fromLines := splitLines(from)
	toLines := splitLines(to)

	ops := lcsOps(fromLines, toLines)

	// Runs of consecutive changed ops.
	type run struct{ start, end int } // end exclusive
	var runs []run
	for i := 0; i < len(ops); i++ {
		if ops[i].kind == ' ' {
			continue
		}
		r := run{start: i}
		for i < len(ops) && ops[i].kind != ' ' {
			i++
		}
		r.end = i
		runs = append(runs, r)
	}
	if len(runs) == 0 {
		return []Hunk{}
	}

	// Merge runs whose context windows overlap.
	type hunkRange struct{ start, end int } // ops indices, end exclusive
	var ranges []hunkRange
	for _, r := range runs {
		s := r.start - context
		if s < 0 {
			s = 0
		}
		e := r.end + context
		if e > len(ops) {
			e = len(ops)
		}
		if len(ranges) > 0 && s <= ranges[len(ranges)-1].end {
			ranges[len(ranges)-1].end = e
		} else {
			ranges = append(ranges, hunkRange{s, e})
		}
	}

	// lineNo[i] = 1-based line number in the from text for ctx/del ops; for
	// add ops it is the number the *next* from line would get (used only for
	// hunks that begin with an insertion).
	lineNo := make([]int, len(ops))
	li := 0
	for i, o := range ops {
		if o.kind != '+' {
			li++
			lineNo[i] = li
		} else {
			lineNo[i] = li + 1
		}
	}

	hunks := make([]Hunk, 0, len(ranges))
	for _, hr := range ranges {
		h := Hunk{Start: lineNo[hr.start], Lines: []Line{}}
		if h.Start < 1 {
			h.Start = 1
		}
		for i := hr.start; i < hr.end; i++ {
			o := ops[i]
			switch o.kind {
			case ' ':
				n := lineNo[i]
				h.Lines = append(h.Lines, Line{T: "ctx", N: &n, Text: o.text})
				h.Count++
			case '-':
				h.Lines = append(h.Lines, Line{T: "del", Text: o.text})
			case '+':
				h.Lines = append(h.Lines, Line{T: "add", Text: o.text})
			}
		}
		hunks = append(hunks, h)
	}
	return hunks
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// lcsOps builds an edit script via LCS dynamic programming.
func lcsOps(a, b []string) []op {
	n, m := len(a), len(b)
	dp := make([][]int32, n+1)
	for i := range dp {
		dp[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < n && j < m {
		if a[i] == b[j] {
			ops = append(ops, op{' ', a[i]})
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			ops = append(ops, op{'-', a[i]})
			i++
		} else {
			ops = append(ops, op{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, op{'-', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, op{'+', b[j]})
	}
	return ops
}
