package mail

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"strings"

	"github.com/emersion/go-message"
	"github.com/emersion/go-message/charset"
	_ "github.com/emersion/go-message/charset" // registers common encodings
)

// Parsed is the useful shape of a MIME message.
//
// Only text is extracted here. Attachments are recorded but not decoded --
// PDFs and images go through separate paths, and doing that work during
// ingestion would mean a slow attachment could stall the mailbox sync.
type Parsed struct {
	Subject     string
	Text        string
	HTML        string
	Attachments []Attachment
}

type Attachment struct {
	Filename string `json:"filename"`
	MIME     string `json:"mime"`
	Size     int    `json:"size"`
	// Where this part sits in the message, so it can be pulled from the
	// archived source later without re-walking blindly.
	Part string `json:"part"`
}

func (p Parsed) AttachmentsJSON() []byte {
	if p.Attachments == nil {
		p.Attachments = []Attachment{}
	}
	b, _ := json.Marshal(p.Attachments)
	return b
}

func init() { message.CharsetReader = charset.Reader }

// parseMIME never fails: a message we cannot parse still gets archived and
// stored with whatever text could be recovered. Losing a deadline because of
// a malformed multipart boundary would be a worse outcome than a poor snippet.
func parseMIME(raw []byte) Parsed {
	var out Parsed
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && ent == nil {
		out.Text = fallbackText(raw)
		return out
	}
	if dec := new(mime.WordDecoder); dec != nil {
		dec.CharsetReader = charset.Reader
		if s, e := dec.DecodeHeader(ent.Header.Get("Subject")); e == nil {
			out.Subject = s
		}
	}
	walk(ent, "1", &out)
	if out.Text == "" && out.HTML != "" {
		out.Text = htmlToText(out.HTML)
	}
	if out.Text == "" {
		out.Text = fallbackText(raw)
	}
	return out
}

func walk(e *message.Entity, part string, out *Parsed) {
	mediaType, params, _ := e.Header.ContentType()
	disp, dispParams, _ := e.Header.ContentDisposition()

	if mr := e.MultipartReader(); mr != nil {
		i := 0
		for {
			sub, err := mr.NextPart()
			if err != nil {
				return
			}
			i++
			walk(sub, part+"."+itoa(i), out)
		}
	}

	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	isAttachment := strings.EqualFold(disp, "attachment") || filename != ""

	if isAttachment {
		n, _ := io.Copy(io.Discard, e.Body)
		out.Attachments = append(out.Attachments, Attachment{
			Filename: filename, MIME: mediaType, Size: int(n), Part: part,
		})
		return
	}

	body, err := io.ReadAll(io.LimitReader(e.Body, 2<<20))
	if err != nil {
		return
	}
	switch {
	case strings.HasPrefix(mediaType, "text/plain") && out.Text == "":
		out.Text = string(body)
	case strings.HasPrefix(mediaType, "text/html") && out.HTML == "":
		out.HTML = string(body)
	}
}

// htmlToText is intentionally crude. The extractor only needs prose; a real
// HTML parser here would be weight for no gain.
func htmlToText(h string) string {
	var b strings.Builder
	depth, inScript := 0, false
	lower := strings.ToLower(h)
	for i := 0; i < len(h); i++ {
		if h[i] == '<' {
			if strings.HasPrefix(lower[i:], "<script") || strings.HasPrefix(lower[i:], "<style") {
				inScript = true
			}
			if strings.HasPrefix(lower[i:], "</script") || strings.HasPrefix(lower[i:], "</style") {
				inScript = false
			}
			// Block-level tags become newlines so line structure survives,
			// which matters because dates often sit on their own line.
			if strings.HasPrefix(lower[i:], "<br") || strings.HasPrefix(lower[i:], "<p") ||
				strings.HasPrefix(lower[i:], "</p") || strings.HasPrefix(lower[i:], "<div") ||
				strings.HasPrefix(lower[i:], "</div") || strings.HasPrefix(lower[i:], "</tr") ||
				strings.HasPrefix(lower[i:], "<li") {
				b.WriteByte('\n')
			}
			depth++
			continue
		}
		if h[i] == '>' {
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && !inScript {
			b.WriteByte(h[i])
		}
	}
	s := b.String()
	for _, r := range [][2]string{
		{"&nbsp;", " "}, {"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"},
		{"&quot;", `"`}, {"&#39;", "'"}, {"&rsquo;", "'"}, {"&ndash;", "-"},
	} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	lines := strings.Split(s, "\n")
	keep := lines[:0]
	for _, l := range lines {
		if t := strings.TrimSpace(l); t != "" {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "\n")
}

// fallbackText salvages the body of a message we could not parse: everything
// after the first blank line, with obvious header lines dropped.
func fallbackText(raw []byte) string {
	s := string(raw)
	if i := strings.Index(s, "\r\n\r\n"); i >= 0 {
		s = s[i+4:]
	} else if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[i+2:]
	}
	if len(s) > 100000 {
		s = s[:100000]
	}
	return strings.TrimSpace(s)
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return strings.TrimSpace(strings.Replace(strings.Repeat(" ", 0)+string(rune('0'+i/10))+string(rune('0'+i%10)), " ", "", -1))
}
