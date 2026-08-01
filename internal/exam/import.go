package exam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/NandishwarSingh/badal/internal/mail"
)

// Fetching papers and keeping them.
//
// The sources are a university society's site and one student's repository.
// Either could vanish, and a paper you cannot open the night before an exam is
// worth nothing, so the PDF is copied into blob storage and the text pulled
// out of it. After that badal owns the material.

// Blobs stores a fetched paper. Narrow on purpose: this package has no need
// for the rest of the object store.
type Blobs interface {
	Put(ctx context.Context, key string, data []byte) error
}

const (
	maxPaperBytes = 24 << 20
	fetchPause    = 120 * time.Millisecond // courteous to both hosts

	// Importing seventy-three papers in one request killed the API. The box
	// has one core and 4GB, and the PDF parser allocates several times a
	// file's size while it works -- a run of 3MB scanned papers is enough to
	// be OOM-killed mid-import. Batching keeps peak memory flat and makes the
	// work resumable, which it has to be anyway because the process can die.
	defaultBatch = 15

	// Above this a PDF is almost always a scan, which has no text layer to
	// find. Parsing it costs the most memory and returns nothing.
	maxParseBytes = 4 << 20

	// Enough to hold a question paper. Papers with a full answer key run
	// longer, and the excess is worth keeping out of a row that gets read
	// on every search.
	maxTextChars = 120 << 10
)

type ImportResult struct {
	Found     int      `json:"found"`
	Remaining int      `json:"remaining"` // run again to continue
	Stored    int      `json:"stored"`
	Skipped   int      `json:"skipped"` // already held
	WithText  int      `json:"with_text"`
	Failed    int      `json:"failed"`
	Problems  []string `json:"problems,omitempty"`
}

// Import pulls every paper for one course and keeps it.
//
// Idempotent on (source, source_key): re-running adds what is new and leaves
// the rest alone, so this can be repeated whenever a source publishes more.
func (s *Service) Import(ctx context.Context, blobs Blobs, courseCode, subject string, batch int) (ImportResult, error) {
	var r ImportResult
	if batch <= 0 || batch > 60 {
		batch = defaultBatch
	}
	papers, problems := s.Find(ctx, courseCode, subject)
	r.Found = len(papers)
	r.Problems = problems

	for _, p := range papers {
		if r.Stored >= batch {
			r.Remaining = len(papers) - r.Stored - r.Skipped
			break
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		default:
		}

		var already bool
		err := s.pool.QueryRow(ctx,
			`SELECT blob_path <> '' FROM papers WHERE source=$1 AND source_key=$2`,
			p.Source, p.SourceKey).Scan(&already)
		if err == nil && already {
			r.Skipped++
			continue
		}

		body, ferr := s.fetch(ctx, p.URL)
		if ferr != nil {
			r.Failed++
			r.Problems = append(r.Problems, shortName(p)+": "+ferr.Error())
			continue
		}

		// Content addressed, so the same paper listed twice under different
		// metadata is stored once.
		sum := sha256.Sum256(body)
		key := "papers/" + hex.EncodeToString(sum[:])[:32] + ".pdf"
		if err := blobs.Put(ctx, key, body); err != nil {
			r.Failed++
			r.Problems = append(r.Problems, shortName(p)+": store failed")
			continue
		}

		// A scanned paper has no text layer and yields nothing. That is not a
		// failure -- the PDF is still readable, it just cannot be searched
		// until the worker OCRs it.
		text := ""
		if len(body) <= maxParseBytes {
			text = mail.PDFTextSafe(ctx, body)
		}
		if len(text) > maxTextChars {
			text = text[:maxTextChars]
		}
		if strings.TrimSpace(text) != "" {
			r.WithText++
		}

		if _, err := s.pool.Exec(ctx, `
			INSERT INTO papers (source, source_key, course_code, subject_name, year, exam,
			                    slot, campus, has_key, remote_url, blob_path, bytes,
			                    text_content, extracted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, CASE WHEN $13<>'' THEN now() END)
			ON CONFLICT (source, source_key) DO UPDATE
			   SET blob_path=EXCLUDED.blob_path, bytes=EXCLUDED.bytes,
			       text_content=EXCLUDED.text_content, extracted_at=EXCLUDED.extracted_at`,
			p.Source, p.SourceKey, p.CourseCode, p.Subject, p.Year, p.Exam,
			p.Slot, p.Campus, p.HasKey, p.URL, key, len(body), text); err != nil {
			r.Failed++
			r.Problems = append(r.Problems, shortName(p)+": "+err.Error())
			continue
		}
		r.Stored++
		time.Sleep(fetchPause)
	}
	slog.Info("papers imported", "course", courseCode, "found", r.Found,
		"stored", r.Stored, "skipped", r.Skipped, "failed", r.Failed)
	return r, nil
}

func (s *Service) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPaperBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxPaperBytes {
		return nil, fmt.Errorf("larger than %dMB", maxPaperBytes>>20)
	}
	// Both hosts answer an expired link with an HTML error page and a 200.
	if len(body) < 5 || string(body[:4]) != "%PDF" {
		return nil, fmt.Errorf("not a pdf")
	}
	return body, nil
}

func shortName(p Paper) string {
	n := strings.TrimSpace(strings.Join([]string{p.Exam, p.Year, p.Slot}, " "))
	if n == "" {
		n = p.SourceKey
	}
	if len(n) > 40 {
		n = n[:40]
	}
	return n
}

// ReextractResult reports a backfill pass.
type ReextractResult struct {
	Tried     int `json:"tried"`
	Recovered int `json:"recovered"`
	StillNone int `json:"still_none"` // genuinely a scan, or unreadable
	Remaining int `json:"remaining"`
}

// Reextract re-reads papers that were stored with no text.
//
// Needed because the first extractor returned an empty string with no error on
// files a working one reads fine, so "no text" in the table means two different
// things: a scan with nothing to find, and a paper that was simply misread.
// This tells them apart the only way available -- by trying again with the
// better reader.
//
// It re-fetches rather than reading blob storage back, because the blob
// interface here only writes, and a backfill that runs a handful of times is
// not worth widening it for.
func (s *Service) Reextract(ctx context.Context, batch int) (ReextractResult, error) {
	var r ReextractResult
	if batch <= 0 || batch > 60 {
		batch = defaultBatch
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, remote_url FROM papers
		 WHERE text_content = '' AND remote_url <> '' AND bytes <= $1
		   AND extracted_at IS NULL
		 ORDER BY created_at
		 LIMIT $2`, maxParseBytes, batch)
	if err != nil {
		return r, err
	}
	type job struct {
		id  string
		url string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.url); err != nil {
			rows.Close()
			return r, err
		}
		jobs = append(jobs, j)
	}
	rows.Close()

	for _, j := range jobs {
		r.Tried++
		body, ferr := s.fetch(ctx, j.url)
		if ferr != nil {
			r.StillNone++
			continue
		}
		text := mail.PDFTextSafe(ctx, body)
		if len(text) > maxTextChars {
			text = text[:maxTextChars]
		}
		if strings.TrimSpace(text) == "" {
			r.StillNone++
			// Stamped so a scan is not retried forever; text_content stays
			// empty, which is what OCR will look for.
			s.pool.Exec(ctx, `UPDATE papers SET extracted_at=now() WHERE id=$1`, j.id)
			time.Sleep(fetchPause)
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`UPDATE papers SET text_content=$2, extracted_at=now() WHERE id=$1`, j.id, text); err != nil {
			r.StillNone++
			continue
		}
		r.Recovered++
		time.Sleep(fetchPause)
	}

	s.pool.QueryRow(ctx, `
		SELECT count(*) FROM papers
		 WHERE text_content = '' AND remote_url <> '' AND bytes <= $1
		   AND extracted_at IS NULL`, maxParseBytes).Scan(&r.Remaining)

	slog.Info("papers re-extracted", "tried", r.Tried, "recovered", r.Recovered,
		"still_none", r.StillNone, "remaining", r.Remaining)
	return r, nil
}
