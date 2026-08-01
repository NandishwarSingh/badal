// Package exam covers preparation: past papers, the timetable, the syllabus,
// and the plan that falls out of them.
//
// Exam dates are not stored here. They already exist in events, extracted from
// mail with the span that justified them, so the planner reads those instead
// of asking for the same information twice.
package exam

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Two sources, deliberately different in what they offer.
//
// codechef returns structured metadata -- year, exam, slot, campus, whether an
// answer key is attached -- which is what makes a paper findable by anything
// other than its filename. The VIT-Papers repository has far broader coverage
// but only paths, so everything useful has to be parsed back out of a string
// like "MEE2042 CAT1 WIN 2020.pdf".
const (
	codechefBase = "https://www.papers.codechefvit.com"
	vitPapersAPI = "https://api.github.com/repos/Nirant07/VIT-Papers/git/trees/main?recursive=1"
	vitPapersRaw = "https://raw.githubusercontent.com/Nirant07/VIT-Papers/main/"

	fetchTimeout = 30 * time.Second
	// The tree is one response covering the whole repository, so it is large
	// and worth caching for the session rather than per search.
	treeTTL = 6 * time.Hour
)

type Service struct {
	pool *pgxpool.Pool
	http *http.Client

	tree     []treeEntry
	treeAt   time.Time
	treeLock chan struct{} // a one-slot lock, so a slow fetch blocks rather than stampedes
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{
		pool:     pool,
		http:     &http.Client{Timeout: fetchTimeout},
		treeLock: make(chan struct{}, 1),
	}
}

type Paper struct {
	Source     string `json:"source"`
	SourceKey  string `json:"source_key"`
	CourseCode string `json:"course_code,omitempty"`
	Subject    string `json:"subject,omitempty"`
	Year       string `json:"year,omitempty"`
	Exam       string `json:"exam,omitempty"`
	Slot       string `json:"slot,omitempty"`
	Campus     string `json:"campus,omitempty"`
	HasKey     bool   `json:"has_key"`
	URL        string `json:"url"`
	Bytes      int64  `json:"bytes,omitempty"`
}

// ------------------------------------------------------------- codechef

type ccPaper struct {
	ID       string `json:"_id"`
	Subject  string `json:"subject"`
	Slot     string `json:"slot"`
	Year     string `json:"year"`
	Exam     string `json:"exam"`
	Campus   string `json:"campus"`
	FileURL  string `json:"file_url"`
	Semester string `json:"semester"`
	HasKey   bool   `json:"answer_key_included"`
}

// Subjects lists the catalogue, so a course code can be matched to the exact
// string the API insists on -- "Probability And Statistics [BMAT202L]",
// brackets and capitalisation included.
func (s *Service) Subjects(ctx context.Context) ([]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, codechefBase+"/api/course-list", nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("course list unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("course list returned %d", resp.StatusCode)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("course list malformed")
	}
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out, nil
}

func (s *Service) codechefPapers(ctx context.Context, subject string) ([]Paper, error) {
	u := codechefBase + "/api/papers?subject=" + url.QueryEscape(subject)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("papers unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("papers returned %d", resp.StatusCode)
	}
	var env struct {
		Papers []ccPaper `json:"papers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&env); err != nil {
		return nil, fmt.Errorf("papers malformed")
	}
	out := make([]Paper, 0, len(env.Papers))
	for _, p := range env.Papers {
		out = append(out, Paper{
			Source: "codechef", SourceKey: p.ID,
			CourseCode: codeFrom(p.Subject), Subject: p.Subject,
			Year: p.Year, Exam: p.Exam, Slot: p.Slot, Campus: p.Campus,
			HasKey: p.HasKey, URL: p.FileURL,
		})
	}
	return out, nil
}

// ------------------------------------------------------------ vitpapers

type treeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func (s *Service) loadTree(ctx context.Context) ([]treeEntry, error) {
	if time.Since(s.treeAt) < treeTTL && s.tree != nil {
		return s.tree, nil
	}
	// One in at a time: the tree is half a megabyte and several searches
	// arriving together should share one fetch, not race for it.
	select {
	case s.treeLock <- struct{}{}:
		defer func() { <-s.treeLock }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if time.Since(s.treeAt) < treeTTL && s.tree != nil {
		return s.tree, nil
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, vitPapersAPI, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paper index unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Unauthenticated GitHub allows 60 requests an hour per address, which
		// the cache above keeps us far below -- but say so plainly if hit.
		return nil, fmt.Errorf("paper index returned %d", resp.StatusCode)
	}
	var env struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&env); err != nil {
		return nil, fmt.Errorf("paper index malformed")
	}
	if env.Truncated {
		// Worth knowing rather than silently returning a partial catalogue.
		return nil, fmt.Errorf("paper index truncated by the provider")
	}
	s.tree, s.treeAt = env.Tree, time.Now()
	return s.tree, nil
}

var (
	// "[PMCA502L] Java Programming/24FALL/B1+TB1/ETH/FAT/FAT java.pdf"
	codeRe = regexp.MustCompile(`\[?([A-Z]{3,4}\d{3,4}[A-Z]?)\]?`)
	examRe = regexp.MustCompile(`(?i)\b(CAT[\s-]?[12]|FAT|QUIZ[\s-]?[123]?)\b`)
	yearRe = regexp.MustCompile(`\b(20\d{2})\b|\b(\d{2})(FALL|WIN|SUM)\b`)
)

func (s *Service) vitPapers(ctx context.Context, code string) ([]Paper, error) {
	tree, err := s.loadTree(ctx)
	if err != nil {
		return nil, err
	}
	needle := strings.ToUpper(strings.TrimSpace(code))
	out := []Paper{}
	for _, e := range tree {
		if e.Type != "blob" || !strings.HasSuffix(strings.ToLower(e.Path), ".pdf") {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToUpper(e.Path), needle) {
			continue
		}
		out = append(out, Paper{
			Source: "vitpapers", SourceKey: e.Path,
			CourseCode: firstMatch(codeRe, e.Path),
			Exam:       normaliseExam(firstMatch(examRe, e.Path)),
			Year:       firstMatch(yearRe, e.Path),
			// Every path segment is escaped separately: the repository has
			// folders like "B1+TB1", and a raw + in a URL means a space.
			URL:   vitPapersRaw + escapePath(e.Path),
			Bytes: e.Size,
			// A key is only ever implied by the filename here.
			HasKey: strings.Contains(strings.ToLower(e.Path), "key") ||
				strings.Contains(strings.ToLower(e.Path), "solution"),
		})
	}
	return out, nil
}

// ---------------------------------------------------------------- search

// Find returns everything both sources hold for a course.
//
// A failure in one source is not a failure of the search: the repository being
// rate-limited should not hide the structured results, and vice versa.
func (s *Service) Find(ctx context.Context, code, subject string) ([]Paper, []string) {
	var out []Paper
	var problems []string

	if subject != "" {
		if p, err := s.codechefPapers(ctx, subject); err != nil {
			problems = append(problems, "codechef: "+err.Error())
		} else {
			out = append(out, p...)
		}
	}
	if code != "" {
		if p, err := s.vitPapers(ctx, code); err != nil {
			problems = append(problems, "vit-papers: "+err.Error())
		} else {
			out = append(out, p...)
		}
	}
	return out, problems
}

// ---------------------------------------------------------------- helpers

func codeFrom(subject string) string { return firstMatch(codeRe, subject) }

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if g != "" {
			return strings.ToUpper(g)
		}
	}
	return ""
}

func normaliseExam(s string) string {
	up := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "-", ""))
	switch {
	case strings.HasPrefix(up, "CAT1"):
		return "CAT-1"
	case strings.HasPrefix(up, "CAT2"):
		return "CAT-2"
	case up == "FAT":
		return "FAT"
	case strings.HasPrefix(up, "QUIZ"):
		return "Quiz"
	}
	return ""
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}
