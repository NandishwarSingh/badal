package roadmap

import "testing"

func TestSplitByHeading(t *testing.T) {
	md := "# A\n" + rep("x", 5000) + "\n## B\n" + rep("y", 5000) + "\n## C\nshort"
	chunks := splitByHeading(md, 8000)
	if len(chunks) < 2 {
		t.Fatalf("expected splitting, got %d chunks", len(chunks))
	}
	// every heading must survive somewhere
	joined := ""
	for _, c := range chunks {
		joined += c
	}
	for _, h := range []string{"# A", "## B", "## C"} {
		if !contains(joined, h) {
			t.Fatalf("heading %q lost in chunking", h)
		}
	}
}

func TestDedupe(t *testing.T) {
	in := []genItem{
		{Title: "Linear Regression", Kind: "topic", Depth: 0},
		{Title: "linear regression", Kind: "topic", Depth: 0}, // dup, same depth
		{Title: "Gradient Descent", Kind: "leaf", Depth: 1},
	}
	out := dedupe(in)
	if len(out) != 2 {
		t.Fatalf("dedupe: want 2, got %d", len(out))
	}
}

func TestAppendMissing(t *testing.T) {
	md := "# Bayes Theorem\ntext\n## Naive Bayes\nmore\n## Hidden Markov Models\nx"
	items := []genItem{{Title: "Bayes Theorem", Kind: "topic", Depth: 0}, {Title: "Naive Bayes", Kind: "leaf", Depth: 1}}
	out := appendMissing(items, md)
	// HMM was missing -> must be appended
	found := false
	for _, it := range out {
		if norm(it.Title) == norm("Hidden Markov Models") {
			found = true
		}
	}
	if !found {
		t.Fatalf("coverage check dropped a missing heading")
	}
}

func rep(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}
func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
