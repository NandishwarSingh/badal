// Command badal-eval measures retrieval quality against a fixed query set.
//
// It exists because "does this feel better" is not a usable signal for ranking
// changes. Reranking was nearly shipped on reputation alone and turned out to
// hurt; this is how that kind of question gets settled with numbers.
//
// Ground truth is expressed as regexes over result titles rather than fixed
// IDs, so the file survives re-indexing and new mail arriving.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

type spec struct {
	Queries []struct {
		Q      string   `json:"q"`
		Expect []string `json:"expect"`
	} `json:"queries"`
}

type hit struct {
	Title      string `json:"title"`
	SourceType string `json:"source_type"`
}

func main() {
	var (
		api   = flag.String("api", envOr("BADAL_API", "https://badal.nandishwarsingh.com"), "api base")
		token = flag.String("token", "", "device token")
		file  = flag.String("queries", "eval/queries.json", "ground truth file")
		modes = flag.String("modes", "semantic,hybrid-plain,hybrid", "comma separated: "+
			"hybrid is the shipped default, hybrid-plain forces fusion without reranking")
		k     = flag.Int("k", 5, "cut-off for recall@k")
		label = flag.String("label", "", "tag for this run")
		v     = flag.Bool("v", false, "list misses")
	)
	flag.Parse()
	if *token == "" {
		fmt.Fprintln(os.Stderr, "need -token")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var s spec
	if err := json.Unmarshal(raw, &s); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if *label != "" {
		fmt.Printf("\n  %s   (%d queries)\n", *label, len(s.Queries))
	}
	fmt.Printf("  %-16s %7s %7s %8s %8s\n", "mode", "R@1", fmt.Sprintf("R@%d", *k), "MRR", "p50 ms")
	fmt.Printf("  %s\n", strings.Repeat("-", 52))

	for _, mode := range strings.Split(*modes, ",") {
		// An empty rerank param means "send none", which is what the app does
		// and therefore the only setting that measures what users get.
		m, rerank := mode, ""
		switch {
		case strings.HasSuffix(mode, "-plain"):
			m, rerank = strings.TrimSuffix(mode, "-plain"), "0"
		case strings.HasSuffix(mode, "+rerank"):
			m, rerank = strings.TrimSuffix(mode, "+rerank"), "1"
		}
		var r1, rk, mrrSum float64
		var lats []float64
		var misses []string

		for _, q := range s.Queries {
			u := fmt.Sprintf("%s/v1/search/all?q=%s&mode=%s&limit=%d",
				*api, url.QueryEscape(q.Q), m, *k)
			if rerank != "" {
				u += "&rerank=" + rerank
			}
			t0 := time.Now()
			hits, err := fetch(u, *token)
			lats = append(lats, float64(time.Since(t0).Milliseconds()))
			if err != nil {
				misses = append(misses, q.Q+"  (request failed)")
				continue
			}
			// Rank of the first result whose title matches any expected pattern.
			rank := 0
			for i, h := range hits {
				if matches(h.Title, q.Expect) {
					rank = i + 1
					break
				}
			}
			switch {
			case rank == 1:
				r1++
				rk++
				mrrSum++
			case rank > 0:
				rk++
				mrrSum += 1 / float64(rank)
			default:
				misses = append(misses, q.Q)
			}
		}
		n := float64(len(s.Queries))
		sort.Float64s(lats)
		p50 := 0.0
		if len(lats) > 0 {
			p50 = lats[len(lats)/2]
		}
		fmt.Printf("  %-16s %6.0f%% %6.0f%% %8.3f %8.0f\n",
			mode, r1/n*100, rk/n*100, mrrSum/n, p50)
		if *v {
			for _, ms := range misses {
				fmt.Printf("       miss: %s\n", ms)
			}
		}
	}
	fmt.Println()
}

func matches(title string, pats []string) bool {
	for _, p := range pats {
		if re, err := regexp.Compile(p); err == nil && re.MatchString(title) {
			return true
		}
	}
	return false
}

func fetch(u, token string) ([]hit, error) {
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Hits []hit `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Hits, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
