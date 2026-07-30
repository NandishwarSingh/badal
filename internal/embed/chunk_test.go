package embed

import (
	"strings"
	"testing"
)

// A real circular, trimmed. Everything here is a shape that broke the first
// chunker: Gmail bold markers mid-word, a forwarded header block, an image
// placeholder, and the institutional footer that is identical across the
// whole corpus.
const circular = `Ref:
VIT/Regr./2026/108
                 30 July 2026

*CIRCULAR*

Sub: New Campus Traffic Arrangements and Road Naming

 To create a safer, more pedestrian-friendly environment in front of the
Technology Tower (TT), a new traffic plan has been introduced.

Effective *Monday, 3 August 2026*, the southern lane of Jimmy Carter Road
will be dedicated exclusively to pedestrians.

*Naming of New Roads*

   1. *Ada Road:* Connecting SJT to the LH-J block, named after *Dr Ada E.
   Yonath*, winner of the 2009 Nobel Prize in Chemistry.
   2. *Whittingham Road:* named after *Sir M. Stanley Whittingham*, winner
   of the 2019 Nobel Prize in Chemistry, renowned for his development of
   lithium-ion batteries.

[image: image.png]

*Registrar*

*Vellore Institute of Technology (VIT), India - - Engineering and
Technology: 119th in the World and 6th in India (QS World University
Rankings by Subject 2026)- Computer Science, Information Systems,
Electrical, and Electronics subject areas are within the Top 100.*`

func TestSplitDropsFooter(t *testing.T) {
	ps := Split("New Campus Traffic Arrangements and Road Naming", circular)
	if len(ps) == 0 {
		t.Fatal("no pieces")
	}
	all := ""
	for _, p := range ps {
		all += p.Text + "\n"
		if strings.Contains(p.Text, "*") {
			t.Errorf("asterisk survived: %q", p.Text)
		}
		if strings.Contains(p.Text, "[image:") {
			t.Errorf("image placeholder survived: %q", p.Text)
		}
	}
	if strings.Contains(all, "QS World University") {
		t.Error("institutional footer was indexed")
	}
	if !strings.Contains(all, "lithium-ion batteries") {
		t.Error("real content was dropped with the footer")
	}
}

func TestSplitKeepsChunksSmallAndParentsBig(t *testing.T) {
	ps := Split("Road Naming", circular)
	for _, p := range ps {
		if len(p.Text) > childMax {
			t.Errorf("chunk over childMax (%d): %q", len(p.Text), trunc(p.Text, 80))
		}
		// Small-to-big only means something if the parent is the wider span.
		if len(p.Parent) < len(p.Text) {
			t.Errorf("parent smaller than its own chunk: %d < %d", len(p.Parent), len(p.Text))
		}
		if !strings.Contains(p.Parent, strings.Split(p.Text, "\n")[0]) {
			t.Errorf("parent does not contain its chunk: %q", trunc(p.Text, 60))
		}
	}
}

func TestSplitFindsHeadings(t *testing.T) {
	ps := Split("Road Naming", circular)
	var heads []string
	for _, p := range ps {
		if p.Head != "" {
			heads = append(heads, p.Head)
		}
	}
	if len(heads) == 0 {
		t.Fatal("no section headings found")
	}
	if !strings.Contains(strings.Join(heads, "|"), "Naming of New Roads") {
		t.Errorf("expected the roads section, got %v", heads)
	}
}

// A forwarded chain: the header block is plumbing, the body under it is the
// actual content and must survive.
func TestSplitDropsForwardHeadersNotBodies(t *testing.T) {
	const fwd = `Please see the trailing mail for details.

---------- Forwarded message ---------
From: Nishanth Corera K <nishanthcorera.k@vit.ac.in>
Date: Wed, Jul 22, 2026 at 10:22 AM
Subject: Fwd: Call For Internship - SolderLab
To: Director TBI <director.tbi@vit.ac.in>

Dear students,

We are excited to announce an internship at SolderLab, working on embedded
systems and PCB design.

*Program Structure*

— Performance-based commission upto Rs 15,000 on project contributions.`

	all := ""
	for _, p := range Split("Call For Internship", fwd) {
		all += p.Text + "\n"
	}
	if strings.Contains(all, "nishanthcorera.k@vit.ac.in") {
		t.Error("forwarded header block was indexed")
	}
	for _, want := range []string{"embedded", "commission upto Rs 15,000"} {
		if !strings.Contains(all, want) {
			t.Errorf("lost body content %q from:\n%s", want, all)
		}
	}
}

// A line that merely begins "Subject:" in running prose is not a header block.
func TestSplitKeepsLoneHeaderLikeLine(t *testing.T) {
	const body = `Dear all,

Subject: this is the topic we will discuss at length during the session
tomorrow, and it matters that the sentence survives intact for retrieval.

Please attend.`
	all := ""
	for _, p := range Split("Session", body) {
		all += p.Text + "\n"
	}
	if !strings.Contains(all, "this is the topic") {
		t.Errorf("dropped a lone header-like line:\n%s", all)
	}
}

func TestSplitEmpty(t *testing.T) {
	if got := Split("", ""); got != nil {
		t.Errorf("want nil, got %v", got)
	}
	// Title-only sources (bare events) still have to produce something.
	if got := Split("International Tiger Day", ""); len(got) != 1 {
		t.Errorf("want 1 piece from title alone, got %d", len(got))
	}
}
