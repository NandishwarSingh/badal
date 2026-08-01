package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-message"
	"github.com/google/uuid"
	"github.com/ledongthuc/pdf"
)

// AttachmentText pulls readable text out of a message's PDF attachments.
//
// The parts are read back out of the archived MIME rather than stored
// separately at ingest time: a slow or huge attachment must never be able to
// stall the mailbox sync, and most attachments are never needed.
//
// Images are not handled here. They need OCR, which belongs on the Mac worker
// in Phase 4 -- this returns only what can be read without a model.
func (s *Service) AttachmentText(ctx context.Context, id uuid.UUID, maxChars int) (string, error) {
	raw, err := s.Raw(ctx, id)
	if err != nil {
		return "", err
	}
	if maxChars <= 0 {
		maxChars = 8000
	}
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && ent == nil {
		return "", fmt.Errorf("unreadable mime")
	}
	var out strings.Builder
	collectPDFs(ent, &out, maxChars)
	return strings.TrimSpace(out.String()), nil
}

func collectPDFs(e *message.Entity, out *strings.Builder, maxChars int) {
	if out.Len() >= maxChars {
		return
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			sub, err := mr.NextPart()
			if err != nil {
				return
			}
			collectPDFs(sub, out, maxChars)
		}
	}
	mediaType, params, _ := e.Header.ContentType()
	_, dispParams, _ := e.Header.ContentDisposition()
	name := dispParams["filename"]
	if name == "" {
		name = params["name"]
	}
	isPDF := strings.Contains(strings.ToLower(mediaType), "pdf") ||
		strings.HasSuffix(strings.ToLower(name), ".pdf")
	if !isPDF {
		return
	}

	// go-message already decodes the transfer encoding for us, but some mail
	// arrives double-encoded; fall back rather than emitting base64 as "text".
	body, err := io.ReadAll(io.LimitReader(e.Body, 25<<20))
	if err != nil || len(body) == 0 {
		return
	}
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("%PDF")) {
		if dec, e2 := base64.StdEncoding.DecodeString(stripWS(string(body))); e2 == nil {
			body = dec
		} else if dec, e2 := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body))); e2 == nil {
			body = dec
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("%PDF")) {
		return
	}

	txt, err := pdfText(body)
	if err != nil || strings.TrimSpace(txt) == "" {
		return
	}
	if name != "" {
		out.WriteString("\n--- " + name + " ---\n")
	}
	if len(txt) > maxChars-out.Len() {
		txt = txt[:max(0, maxChars-out.Len())]
	}
	out.WriteString(txt)
}

// pdfText recovers plain text. Scanned PDFs contain no text layer and yield
// nothing -- correctly, since guessing at an image is what OCR is for.
func pdfText(b []byte) (txt string, err error) {
	defer func() {
		// The PDF parser panics on some malformed files. A bad attachment must
		// not take down an extraction run.
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf parse panic: %v", r)
		}
	}()
	rd, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	n := rd.NumPage()
	if n > 30 {
		n = 30 // circulars are short; a 500 page file is not worth the time
	}
	for i := 1; i <= n; i++ {
		p := rd.Page(i)
		if p.V.IsNull() {
			continue
		}
		s, err := p.GetPlainText(nil)
		if err != nil {
			continue
		}
		sb.WriteString(s)
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func stripWS(s string) string {
	return strings.NewReplacer("\n", "", "\r", "", " ", "", "\t", "").Replace(s)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// PDFText exposes the extractor to other packages.
//
// Past papers arrive as PDFs and everything downstream -- searching them,
// pulling individual questions out of them -- needs their text. Scanned
// papers have no text layer and correctly yield nothing; that is OCR's job,
// and the worker already does it.
func PDFText(b []byte) (string, error) { return pdfText(b) }
