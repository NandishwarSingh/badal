package mail

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"

	"github.com/emersion/go-message"
	"github.com/google/uuid"
)

type Image struct {
	Filename string
	MIME     string
	Data     []byte
}

// Images pulls image attachments out of the archived MIME.
//
// Read back at extraction time rather than stored at ingest, same as PDFs: a
// mailbox sync must never be held up by an attachment nobody has asked for.
//
// Tiny images are skipped -- signature logos and tracking pixels are the bulk
// of what arrives, and paying a vision model to read a 2KB footer icon is
// pure waste.
func (s *Service) Images(ctx context.Context, id uuid.UUID, maxCount, minBytes int) ([]Image, error) {
	raw, err := s.Raw(ctx, id)
	if err != nil {
		return nil, err
	}
	if maxCount <= 0 {
		maxCount = 3
	}
	if minBytes <= 0 {
		minBytes = 12000
	}
	ent, err := message.Read(bytes.NewReader(raw))
	if err != nil && ent == nil {
		return nil, nil
	}
	var out []Image
	collectImages(ent, &out, maxCount, minBytes)
	return out, nil
}

func collectImages(e *message.Entity, out *[]Image, maxCount, minBytes int) {
	if len(*out) >= maxCount {
		return
	}
	if mr := e.MultipartReader(); mr != nil {
		for {
			sub, err := mr.NextPart()
			if err != nil {
				return
			}
			collectImages(sub, out, maxCount, minBytes)
		}
	}
	mediaType, params, _ := e.Header.ContentType()
	if !strings.HasPrefix(strings.ToLower(mediaType), "image/") {
		return
	}
	// Animated and vector formats are not worth a vision call.
	switch strings.ToLower(mediaType) {
	case "image/gif", "image/svg+xml", "image/x-icon":
		return
	}
	_, dispParams, _ := e.Header.ContentDisposition()
	name := dispParams["filename"]
	if name == "" {
		name = params["name"]
	}

	body, err := io.ReadAll(io.LimitReader(e.Body, 12<<20))
	if err != nil || len(body) == 0 {
		return
	}
	if !looksBinary(body) {
		if dec, e2 := base64.StdEncoding.DecodeString(stripWS(string(body))); e2 == nil && len(dec) > 0 {
			body = dec
		} else if dec, e2 := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body))); e2 == nil {
			body = dec
		}
	}
	if len(body) < minBytes {
		return
	}
	*out = append(*out, Image{Filename: name, MIME: strings.ToLower(mediaType), Data: body})
}

// looksBinary distinguishes already-decoded bytes from a base64 blob that
// go-message handed back verbatim.
func looksBinary(b []byte) bool {
	n := len(b)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 || b[i] > 0x7e && b[i] < 0xa0 {
			return true
		}
	}
	return false
}
