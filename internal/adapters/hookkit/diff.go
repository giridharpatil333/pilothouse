package hookkit

import (
	"fmt"
	"strings"
)

// maxDiffCells bounds the LCS table. Settings files are a few hundred
// lines; anything bigger gets a whole-file replacement diff instead.
const maxDiffCells = 4 << 20

// Diff returns a unified diff (3 lines of context) from before to after,
// or "" when they are equal. It is for people reading `--dry-run`, not
// for patch(1).
func Diff(name string, before, after string) string {
	if before == after {
		return ""
	}
	a, b := splitLines(before), splitLines(after)
	ops := lineOps(a, b)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s (pilothouse)\n", name, name)
	const ctx = 3
	for i := 0; i < len(ops); {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Grow a hunk around this change, merging changes < 2*ctx apart.
		start := max(i-ctx, 0)
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].kind == ' ' {
				run++
			}
			if run == len(ops) || run-end > 2*ctx {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		writeHunk(&out, ops[start:end])
		i = end
	}
	return out.String()
}

type lineOp struct {
	kind         byte // ' ', '-', '+'
	text         string
	aLine, bLine int // 1-based line numbers before/after this op
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func lineOps(a, b []string) []lineOp {
	var ops []lineOp
	if len(a)*len(b) > maxDiffCells {
		for i, l := range a {
			ops = append(ops, lineOp{'-', l, i + 1, 1})
		}
		for j, l := range b {
			ops = append(ops, lineOp{'+', l, len(a) + 1, j + 1})
		}
		return ops
	}
	// lcs[i][j] = length of the LCS of a[i:] and b[j:].
	lcs := make([][]int32, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i], i + 1, j + 1})
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			// Deletions before insertions, as diff(1) prints them.
			ops = append(ops, lineOp{'-', a[i], i + 1, j + 1})
			i++
		default:
			ops = append(ops, lineOp{'+', b[j], i + 1, j + 1})
			j++
		}
	}
	return ops
}

func writeHunk(out *strings.Builder, ops []lineOp) {
	var na, nb int
	for _, op := range ops {
		if op.kind != '+' {
			na++
		}
		if op.kind != '-' {
			nb++
		}
	}
	aStart, bStart := ops[0].aLine, ops[0].bLine
	if na == 0 {
		aStart--
	}
	if nb == 0 {
		bStart--
	}
	fmt.Fprintf(out, "@@ -%d,%d +%d,%d @@\n", aStart, na, bStart, nb)
	for _, op := range ops {
		out.WriteByte(op.kind)
		out.WriteString(op.text)
		out.WriteByte('\n')
	}
}
