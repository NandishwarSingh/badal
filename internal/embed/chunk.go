package embed

import (
	"regexp"
	"strings"
	"unicode"
)

// Structure-aware chunking, small-to-big.
//
// The first version packed paragraphs to 1100 characters with a blind overlap.
// That is fine for prose and wrong for this corpus, which is almost entirely
// university circulars: a heading, a short lead-in, a bullet list of the facts
// that actually matter, then a kilobyte of identical ranking boilerplate. A
// 1100-character window merges three unrelated sections and, often as not,
// spends half its length on the footer.
//
// So: cut the boilerplate, split on the document's own structure, and embed
// small pieces -- roughly a section's worth of one idea. Retrieval wants the
// smallest span that answers the question, because a short chunk's embedding
// is dominated by the thing it is about. Reading wants the surrounding
// section, so that is stored alongside as the parent and handed to a reranker
// or an LLM. Small to search, big to read.

const (
	childTarget = 420
	childMax    = 700
	childMin    = 80
	parentMax   = 2400
	maxChunks   = 60
)

// Piece is one retrieval chunk plus the section it was cut from.
type Piece struct {
	Text   string // embedded and stored as content
	Parent string // enclosing section, stored as parent
	Head   string // section heading, folded into the embedding input
}

var (
	imageTag = regexp.MustCompile(`\[image:[^\]]*\]`)
	fwdSep   = regexp.MustCompile(`^\s*-+\s*(Forwarded message|Original Message)\s*-+\s*$`)
	hdrLine  = regexp.MustCompile(`^(From|To|Cc|Bcc|Date|Sent|Subject|Reply-To)\s*:\s`)
	ruler    = regexp.MustCompile(`^([-_=*·—–]\s*){4,}$`)

	// Every circular from this university ends with the same kilobyte of QS
	// ranking copy. Indexed, it was several chunks per mail of text that is
	// byte-identical across the corpus: it can never distinguish two documents,
	// and it displaces the part that can.
	footerStart = regexp.MustCompile(`(?i)^(vellore institute of technology \(vit\)|disclaimer\b|unsubscribe\b|to unsubscribe|this (e-?mail|message) (is|and any|may contain))`)

	// The same footer also arrives quoted inside reply chains and with the
	// institution's name above the visible line, so anchoring on the opening
	// words missed it. These ranking phrases appear nowhere else in the corpus
	// and are what the block is actually made of.
	footerMark = regexp.MustCompile(`(?i)(qs world university rankings|shanghai arwu|naac accreditation|nirf[^.]{0,20}rank|times higher education)`)

	quotePrefix = regexp.MustCompile(`^[>\s]+`)
	// Trailing matter is allowed after the sign-off word because these arrive as
	// "Regards, and best wishes," at least as often as a bare "Regards,".
	signOff = regexp.MustCompile(`(?i)^((thanks?|warm|best|kind|with)?\s*(&|and)?\s*regards|yours (sincerely|faithfully)|sincerely)\b.{0,30}$`)

	bulletLead = regexp.MustCompile(`^\s*([·•▪◦*\-–—]|\d{1,2}[.)])\s+`)
	sentEnd    = regexp.MustCompile(`([.!?:])\s+`)
)

// clean strips the parts of a mail body that carry no retrievable meaning.
func clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = imageTag.ReplaceAllString(s, " ")
	// Zero-width joiners, the BOM and non-breaking spaces all survive
	// HTML-to-text conversion, then split tokens that should be one word.
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			return -1
		case '\u00a0':
			return ' '
		}
		return r
	}, s)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])

		// A leading "* " is a bullet and becomes "-"; every other asterisk is a
		// Gmail bold marker, and those land mid-word ("*Office **of **the"), so
		// pairing them up produces worse mangling than dropping them.
		t = strings.TrimSpace(bulletStar.ReplaceAllString(t, "- "))
		t = strings.ReplaceAll(t, "*", "")
		t = strings.Join(strings.Fields(t), " ")

		// Footer and closing signature: everything below is boilerplate. Both
		// are position-guarded, because "Regards," halfway down a forwarded
		// chain is a real person signing a real message that still has content
		// under it.
		// Reply quoting splits the footer across "> " markers; normalise them
		// away so it reads as one block again.
		t = quotePrefix.ReplaceAllString(t, "")
		t = strings.ReplaceAll(t, " > ", " ")

		frac := float64(i) / float64(len(lines))
		if t != "" && frac > 0.3 && (footerStart.MatchString(t) || footerMark.MatchString(t)) {
			break
		}
		if t != "" && signOff.MatchString(t) && frac > 0.65 {
			break
		}

		if t == "" {
			out = append(out, "")
			continue
		}
		if fwdSep.MatchString(t) || ruler.MatchString(t) {
			out = append(out, "")
			continue
		}
		// Mail header blocks: dropped only as a run, so a body line that merely
		// begins "Subject: ..." survives.
		if hdrLine.MatchString(t) {
			n := 0
			for j := i; j < len(lines); j++ {
				if hdrLine.MatchString(strings.TrimSpace(strings.ReplaceAll(lines[j], "*", ""))) {
					n++
					continue
				}
				break
			}
			if n >= 2 {
				i += n - 1
				out = append(out, "")
				continue
			}
		}
		out = append(out, t)
	}

	res := strings.Join(out, "\n")
	for strings.Contains(res, "\n\n\n") {
		res = strings.ReplaceAll(res, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(res)
}

var bulletStar = regexp.MustCompile(`^\*\s+`)

// isHeading recognises the section labels this corpus actually uses:
// "CIRCULAR", "Workshop Details", "What You'll Learn", "Prerequisites:".
func isHeading(t string) bool {
	if t == "" || len(t) > 72 || bulletLead.MatchString(t) {
		return false
	}
	letters := 0
	for _, r := range t {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 3 || strings.HasSuffix(t, ".") {
		return false
	}
	if t == strings.ToUpper(t) {
		return true
	}
	if strings.HasSuffix(t, ":") || strings.HasSuffix(t, "?") {
		return true
	}
	// Title Case in a handful of words.
	words := strings.Fields(t)
	if len(words) < 2 || len(words) > 8 {
		return false
	}
	caps := 0
	for _, w := range words {
		for _, r := range w {
			if unicode.IsUpper(r) {
				caps++
			}
			break
		}
	}
	return float64(caps)/float64(len(words)) >= 0.6
}

type section struct {
	head   string
	blocks []string
}

func (s section) text() string {
	b := strings.Join(s.blocks, "\n\n")
	if s.head == "" {
		return b
	}
	return strings.TrimSpace(s.head + "\n\n" + b)
}

// breakHeadings gives a heading its own paragraph. These circulars write
//
//	Naming of New Roads
//	In keeping with our tradition of naming campus roads...
//
// with no blank line between, so paragraph splitting alone leaves the heading
// buried in the body of the block below it and the section boundary is missed.
func breakHeadings(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for i, l := range lines {
		out = append(out, l)
		t := strings.TrimSpace(l)
		if t == "" || !isHeading(t) {
			continue
		}
		if i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			out = append(out, "")
		}
	}
	return strings.Join(out, "\n")
}

// sections splits on the document's own headings. A heading opens a section
// and everything under it belongs to that section until the next one.
func sections(text string) []section {
	var out []section
	cur := section{}
	for _, para := range strings.Split(breakHeadings(text), "\n\n") {
		p := strings.TrimSpace(para)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "\n") && isHeading(p) {
			if cur.head != "" || len(cur.blocks) > 0 {
				out = append(out, cur)
			}
			cur = section{head: strings.TrimSuffix(p, ":")}
			continue
		}
		cur.blocks = append(cur.blocks, p)
	}
	if cur.head != "" || len(cur.blocks) > 0 {
		out = append(out, cur)
	}
	// A heading with nothing under it is a label, not a section: fold it into
	// the next one rather than emitting a chunk that is just a title.
	var merged []section
	for _, s := range out {
		if len(s.blocks) == 0 && len(merged) > 0 {
			continue
		}
		if len(s.blocks) == 0 {
			s.blocks = []string{s.head}
		}
		merged = append(merged, s)
	}
	return merged
}

// pack fills chunks up to childTarget on block boundaries, never splitting a
// bullet item away from its own text.
func pack(blocks []string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	add := func(b string) {
		if cur.Len() > 0 && cur.Len()+len(b)+2 > childTarget {
			flush()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(b)
	}
	for _, b := range blocks {
		if len(b) <= childMax {
			add(b)
			continue
		}
		// Oversized block: bullet runs split between items, prose on sentences.
		flush()
		for _, part := range splitBlock(b) {
			add(part)
		}
	}
	flush()

	// Merge a runt tail into its predecessor rather than indexing a fragment.
	if n := len(out); n > 1 && len(out[n-1]) < childMin {
		out[n-2] += "\n" + out[n-1]
		out = out[:n-1]
	}
	return out
}

func splitBlock(b string) []string {
	lines := strings.Split(b, "\n")
	isList := 0
	for _, l := range lines {
		if bulletLead.MatchString(l) {
			isList++
		}
	}
	units := lines
	if isList*2 < len(lines) {
		// Mostly prose: sentences are the safe boundary.
		units = sentEnd.Split(b, -1)
		marks := sentEnd.FindAllStringSubmatch(b, -1)
		for i := range units {
			if i < len(marks) {
				units[i] += marks[i][1]
			}
		}
	}
	var out []string
	var cur strings.Builder
	for _, u := range units {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if cur.Len() > 0 && cur.Len()+len(u)+1 > childTarget {
			out = append(out, strings.TrimSpace(cur.String()))
			cur.Reset()
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(u)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// window returns the slice of a long section surrounding one chunk, so the
// parent stays useful without carrying a whole forwarded thread.
func window(full, child string, max int) string {
	i := strings.Index(full, child)
	if i < 0 {
		return full[:max]
	}
	pad := (max - len(child)) / 2
	if pad < 0 {
		pad = 0
	}
	lo, hi := i-pad, i+len(child)+pad
	if lo < 0 {
		lo = 0
	}
	if hi > len(full) {
		hi = len(full)
	}
	if j := strings.IndexByte(full[lo:hi], ' '); lo > 0 && j > 0 {
		lo += j
	}
	return strings.TrimSpace(full[lo:hi])
}

// Split turns a document into retrieval chunks paired with their section.
func Split(title, body string) []Piece {
	text := clean(body)
	if strings.TrimSpace(text) == "" {
		text = clean(title)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var pieces []Piece
	for _, sec := range sections(text) {
		full := sec.text()
		for _, c := range pack(sec.blocks) {
			p := full
			if len(p) > parentMax {
				p = window(full, c, parentMax)
			}
			pieces = append(pieces, Piece{Text: c, Parent: p, Head: sec.head})
		}
	}
	pieces = absorbRunts(pieces)
	if len(pieces) > maxChunks {
		pieces = pieces[:maxChunks]
	}
	return pieces
}

// absorbRunts folds fragments into a neighbour. Section boundaries leave
// stragglers -- a lone "Registrar", a reference number, a date line -- and
// indexing those as their own chunk gives the search a row whose embedding
// describes nothing and which can still outrank a real passage on a short
// query. They keep the neighbour's section, since that is where they sit.
func absorbRunts(ps []Piece) []Piece {
	if len(ps) < 2 {
		return ps
	}
	out := make([]Piece, 0, len(ps))
	for _, p := range ps {
		if len(p.Text) >= childMin {
			out = append(out, p)
			continue
		}
		if n := len(out); n > 0 {
			out[n-1].Text += "\n" + p.Text
			out[n-1].Parent = joinParent(out[n-1].Parent, p.Parent)
			continue
		}
		// Nothing before it: hold and prepend to whatever comes next.
		out = append(out, p)
	}
	for i := 0; i+1 < len(out); i++ {
		if len(out[i].Text) < childMin {
			out[i+1].Text = out[i].Text + "\n" + out[i+1].Text
			out[i+1].Parent = joinParent(out[i].Parent, out[i+1].Parent)
			out = append(out[:i], out[i+1:]...)
			i--
		}
	}
	return out
}

// joinParent widens a parent to still cover a chunk that was merged in from the
// neighbouring section. A parent that no longer contains its own chunk would
// hand a citation the wrong passage.
func joinParent(a, b string) string {
	switch {
	case a == b || strings.Contains(a, b):
		return a
	case strings.Contains(b, a):
		return b
	}
	j := strings.TrimSpace(a + "\n\n" + b)
	if len(j) > parentMax {
		j = j[:parentMax]
	}
	return j
}
