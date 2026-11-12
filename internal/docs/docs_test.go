package docs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadTextFiles(t *testing.T) {
	dir := t.TempDir()
	notes := write(t, dir, "notes.md", []byte("\xEF\xBB\xBF# Notes\nPricing is $10."))
	binary := write(t, dir, "photo.bin", []byte{0x89, 'P', 'N', 'G', 0x00, 0xFF})
	empty := write(t, dir, "empty.txt", []byte("  \n"))
	missing := filepath.Join(dir, "gone.txt")

	got := NewLoader(nil).Load([]string{notes, binary, empty, missing, dir})
	if len(got) != 5 {
		t.Fatalf("got %d docs, want one per path", len(got))
	}
	if got[0].Error != "" || got[0].Text != "# Notes\nPricing is $10." || got[0].Name != "notes.md" {
		t.Errorf("notes: %+v", got[0])
	}
	if got[0].Chars != len("# Notes\nPricing is $10.") {
		t.Errorf("Chars = %d", got[0].Chars)
	}
	for i, wantErr := range map[int]string{1: "not a text file", 2: "empty", 3: "not found", 4: "folder"} {
		if !strings.Contains(got[i].Error, wantErr) || got[i].Text != "" {
			t.Errorf("doc %d: error %q, want it to mention %q", i, got[i].Error, wantErr)
		}
	}
}

func TestRichDocumentsUseTheExtractor(t *testing.T) {
	dir := t.TempDir()
	pdf := write(t, dir, "CV.PDF", []byte("%PDF-1.7 binary"))
	docx := write(t, dir, "broken.docx", []byte("PK"))
	scan := write(t, dir, "scan.pdf", []byte("%PDF"))

	calls := 0
	l := NewLoader(func(path string) (string, error) {
		calls++
		switch filepath.Base(path) {
		case "CV.PDF":
			return "Ten years of Go.", nil
		case "scan.pdf":
			return "   ", nil
		}
		return "", errors.New("cannot read document")
	})
	got := l.Load([]string{pdf, docx, scan})
	if got[0].Text != "Ten years of Go." || got[0].Error != "" {
		t.Errorf("pdf: %+v", got[0])
	}
	if got[1].Error != "cannot read document" {
		t.Errorf("docx error = %q", got[1].Error)
	}
	if !strings.Contains(got[2].Error, "no text") {
		t.Errorf("scanned pdf error = %q", got[2].Error)
	}

	// Unchanged files come from the cache, including failures.
	l.Load([]string{pdf, docx, scan})
	if calls != 3 {
		t.Errorf("extractor called %d times, want 3 (second load cached)", calls)
	}

	// Without an extractor, rich formats are reported rather than sent as garbage.
	if d := NewLoader(nil).Load([]string{pdf}); !strings.Contains(d[0].Error, "not supported") {
		t.Errorf("no extractor: %+v", d[0])
	}
}

func TestCacheNoticesChanges(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.txt", []byte("first"))
	l := NewLoader(nil)
	if got := l.Load([]string{p}); got[0].Text != "first" {
		t.Fatalf("got %+v", got[0])
	}
	write(t, dir, "a.txt", []byte("second version"))
	// Make sure the modification time differs even on coarse filesystems.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if got := l.Load([]string{p}); got[0].Text != "second version" {
		t.Errorf("stale cache: %+v", got[0])
	}
}

func TestTruncation(t *testing.T) {
	dir := t.TempDir()
	big := write(t, dir, "big.txt", []byte(strings.Repeat("é", MaxFileChars+50)))
	got := NewLoader(nil).Load([]string{big})
	if !got[0].Truncated || got[0].Chars != MaxFileChars {
		t.Errorf("Chars = %d truncated = %v, want %d and true", got[0].Chars, got[0].Truncated, MaxFileChars)
	}
	if !strings.HasSuffix(got[0].Text, "é") {
		t.Error("truncation split a multi-byte character")
	}

	// The total budget is shared across files.
	var paths []string
	for i := 0; i < 4; i++ {
		paths = append(paths, write(t, dir, string(rune('a'+i))+".txt", []byte(strings.Repeat("x", MaxFileChars))))
	}
	total := 0
	for _, d := range NewLoader(nil).Load(paths) {
		total += d.Chars
	}
	if total != MaxTotalChars {
		t.Errorf("total chars = %d, want the cap %d", total, MaxTotalChars)
	}
}

func TestRender(t *testing.T) {
	out := Render([]Doc{
		{Name: "cv.pdf", Text: "  Ten years of Go.\n"},
		{Name: "bad.pdf", Error: "cannot open PDF"},
		{Name: "long.md", Text: "abc", Truncated: true},
		{Name: "blank.txt", Text: "  "},
	})
	want := "<document name=\"cv.pdf\">\nTen years of Go.\n</document>\n" +
		"<document name=\"long.md\">\nabc\n[document truncated]\n</document>"
	if out != want {
		t.Errorf("Render =\n%s\nwant\n%s", out, want)
	}
	if Render(nil) != "" {
		t.Error("Render(nil) should be empty")
	}
}
