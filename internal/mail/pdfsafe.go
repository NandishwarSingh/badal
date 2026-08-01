package mail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Extracting text from an untrusted PDF happens in a child process, under an
// address-space limit, using poppler.
//
// Two separate problems forced this. A 545KB past paper made the in-process
// Go parser allocate 3.5GB and the kernel killed the API, which on a 4GB box
// takes everything else with it -- and it died on the same file every retry,
// so the import could never walk past it. Then, on the papers that did not
// crash, the same parser returned an empty string with no error: pdftotext
// reads 2168 characters out of the file below where it reads none. Silence
// that looks like "this paper is a scan" is worse than a failure, because
// nothing downstream knows to try again.
//
// poppler is the reference implementation and gets both right. The Go parser
// stays as a fallback for hosts without it, still in the child, because a
// runaway parse must cost one paper rather than the process.
const (
	extractLimitKB = 600 << 10 // 600MB of address space, ~10x an honest parse
	extractTimeout = 45 * time.Second
)

// PDFTextSafe extracts text in a bounded child process. It never returns an
// error that should stop a caller: a paper whose text cannot be read is still
// worth holding as a PDF, and a scan legitimately has no text at all.
func PDFTextSafe(ctx context.Context, b []byte) string {
	ctx, cancel := context.WithTimeout(ctx, extractTimeout)
	defer cancel()
	if t := runBounded(ctx, b, "pdftotext", "-q", "-enc", "UTF-8", "-", "-"); t != "" {
		return t
	}
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	return runBounded(ctx, b, self, "-extract-pdf")
}

// runBounded pipes the PDF through a command whose address space is capped.
// ulimit has to be applied by a shell between fork and exec; Go's SysProcAttr
// has no portable way to set RLIMIT_AS on a child.
func runBounded(ctx context.Context, b []byte, name string, args ...string) string {
	quoted := make([]string, 0, len(args))
	for i := range args {
		quoted = append(quoted, fmt.Sprintf("\"$%d\"", i+1))
	}
	script := fmt.Sprintf("ulimit -v %d 2>/dev/null; exec \"$0\" %s",
		extractLimitKB, strings.Join(quoted, " "))

	shArgs := append([]string{"-c", script, name}, args...)
	cmd := exec.CommandContext(ctx, "/bin/sh", shArgs...)
	cmd.Stdin = bytes.NewReader(b)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

// ExtractPDFMode is the child side, run when the binary is invoked with
// -extract-pdf: read a PDF on stdin, write its text to stdout.
func ExtractPDFMode() int {
	b, err := readAll(os.Stdin)
	if err != nil {
		return 1
	}
	text, err := PDFText(b)
	if err != nil {
		return 2
	}
	os.Stdout.WriteString(text)
	return 0
}

func readAll(f *os.File) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(f)
	return buf.Bytes(), err
}
