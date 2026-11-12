// Package docs turns the user's context files into prompt text.
package docs

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// MaxFileBytes is the largest file that will be read.
	MaxFileBytes = 25 << 20
	// MaxFileChars caps the text taken from a single file.
	MaxFileChars = 120_000
	// MaxTotalChars caps the text taken from all files together.
	MaxTotalChars = 300_000
)

// Extractor converts a non-plain-text file (PDF, Word, RTF) to text.
type Extractor func(path string) (string, error)

// Doc is one context file as it will be sent to the model.
type Doc struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Chars     int    `json:"chars"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
	Text      string `json:"-"`
}

type cacheEntry struct {
	size    int64
	modTime time.Time
	text    string
	err     string
}

// Loader reads context files, caching the extracted text until a file changes.
type Loader struct {
	extract Extractor
	mu      sync.Mutex
	cache   map[string]cacheEntry
}

// NewLoader returns a loader; extract may be nil if rich formats are unsupported.
func NewLoader(extract Extractor) *Loader {
	return &Loader{extract: extract, cache: map[string]cacheEntry{}}
}

// Load returns one Doc per path, in order. Files that cannot be read are
// returned with Error set and no text, so the UI can show the problem.
func (l *Loader) Load(paths []string) []Doc {
	out := make([]Doc, 0, len(paths))
	remaining := MaxTotalChars
	for _, p := range paths {
		d := Doc{Path: p, Name: filepath.Base(p)}
		text, err := l.text(p)
		if err != "" {
			d.Error = err
			out = append(out, d)
			continue
		}
		text, cut := truncateRunes(text, MaxFileChars)
		if n := utf8.RuneCountInString(text); n > remaining {
			text, _ = truncateRunes(text, remaining)
			cut = true
		}
		d.Text = text
		d.Chars = utf8.RuneCountInString(text)
		d.Truncated = cut
		remaining -= d.Chars
		out = append(out, d)
	}
	return out
}

// Render formats the readable docs as a prompt section. It returns "" when
// there is nothing to include.
func Render(list []Doc) string {
	var b strings.Builder
	for _, d := range list {
		if d.Error != "" || strings.TrimSpace(d.Text) == "" {
			continue
		}
		fmt.Fprintf(&b, "<document name=%q>\n%s\n", d.Name, strings.TrimSpace(d.Text))
		if d.Truncated {
			b.WriteString("[document truncated]\n")
		}
		b.WriteString("</document>\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (l *Loader) text(path string) (text string, errMsg string) {
	info, err := os.Stat(path)
	if err != nil {
		return "", "file not found"
	}
	if info.IsDir() {
		return "", "is a folder"
	}
	if info.Size() > MaxFileBytes {
		return "", fmt.Sprintf("larger than %d MB", MaxFileBytes>>20)
	}

	l.mu.Lock()
	if c, ok := l.cache[path]; ok && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		l.mu.Unlock()
		return c.text, c.err
	}
	l.mu.Unlock()

	text, errMsg = l.read(path)

	l.mu.Lock()
	l.cache[path] = cacheEntry{size: info.Size(), modTime: info.ModTime(), text: text, err: errMsg}
	l.mu.Unlock()
	return text, errMsg
}

func (l *Loader) read(path string) (string, string) {
	if IsRichDocument(path) {
		if l.extract == nil {
			return "", "this file type is not supported on this platform"
		}
		text, err := l.extract(path)
		if err != nil {
			return "", err.Error()
		}
		if strings.TrimSpace(text) == "" {
			return "", "no text could be extracted (scanned document?)"
		}
		return text, ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "cannot read file"
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", "not a text file (supported: text, Markdown, code, CSV, JSON, PDF, DOCX, RTF)"
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return "", "file is empty"
	}
	return string(data), ""
}

// IsRichDocument reports whether the file needs the native text extractor.
func IsRichDocument(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".docx", ".doc", ".rtf", ".odt":
		return true
	}
	return false
}

func truncateRunes(s string, max int) (string, bool) {
	if max <= 0 {
		return "", s != ""
	}
	if len(s) <= max {
		return s, false // byte length is an upper bound on rune count
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i], true
		}
		n++
	}
	return s, false
}
