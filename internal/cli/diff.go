package cli

import "strings"

// diff returns the lines removed from a ("- ") and added in b ("+ "), in
// order, from a longest-common-subsequence alignment.
func diff(a, b string) []string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	// lcs[i][j] is the LCS length of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []string
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			i++
			j++
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, "+ "+y[j])
			j++
		default:
			out = append(out, "- "+x[i])
			i++
		}
	}
	return out
}
