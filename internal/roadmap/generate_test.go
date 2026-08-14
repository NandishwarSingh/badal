package roadmap

import "testing"

func TestParseSectionsNesting(t *testing.T) {
	md := "# Unit 1\nintro\n## Topic A\nbody a\n## Topic B\nbody b\n# Unit 2\n### Deep\nskipped a level"
	root := parseSections(md)
	if len(root.children) != 2 {
		t.Fatalf("want 2 top-level units, got %d", len(root.children))
	}
	u1 := root.children[0]
	if u1.title != "Unit 1" || len(u1.children) != 2 {
		t.Fatalf("Unit 1 should have 2 subtopics, got %d", len(u1.children))
	}
	if u1.children[0].title != "Topic A" {
		t.Fatalf("first subtopic wrong: %q", u1.children[0].title)
	}
	// Level skip (# then ###) must not panic and must still nest under Unit 2.
	u2 := root.children[1]
	if u2.title != "Unit 2" || len(u2.children) != 1 || u2.children[0].title != "Deep" {
		t.Fatalf("level-skip nesting wrong: %+v", u2)
	}
}

func TestParseSectionsNoHeadings(t *testing.T) {
	root := parseSections("just some text\nno headings here")
	if len(root.children) != 0 {
		t.Fatalf("expected no heading children")
	}
	if !meaningful(cleanBody(root.body.String())) {
		t.Fatalf("root body should be meaningful for the flat fallback")
	}
}

func TestCleanBodyStripsComments(t *testing.T) {
	got := cleanBody("<!-- source: x.pdf -->\nReal content here indeed yes")
	if got == "" || contains(got, "source:") {
		t.Fatalf("comment not stripped: %q", got)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
