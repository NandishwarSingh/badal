package httpx_test

import (
	"net/http"
	"os"
	"regexp"
	"testing"
)

// ServeMux panics on an ambiguous pattern pair at registration time, not at
// request time, so a bad route takes the whole API down on deploy and `go
// build` says nothing. /v1/tasks/from-event/{id} against /v1/tasks/{id}/state
// did exactly that.
//
// This reads the patterns straight out of main.go and registers them against a
// throwaway mux, which turns that panic into a failing test.
func TestRoutePatternsDoNotConflict(t *testing.T) {
	src, err := os.ReadFile("../../cmd/badal-api/main.go")
	if err != nil {
		t.Skipf("cannot read main.go: %v", err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	found := re.FindAllStringSubmatch(string(src), -1)
	if len(found) < 20 {
		t.Fatalf("only found %d routes; the regex probably stopped matching", len(found))
	}

	mux := http.NewServeMux()
	for _, m := range found {
		pattern := m[1]
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("route %q conflicts:\n%v", pattern, r)
				}
			}()
			mux.Handle(pattern, http.NotFoundHandler())
		}()
	}
	t.Logf("%d routes registered cleanly", len(found))
}
