// Package transcript keeps the rolling, in-memory meeting transcript.
// Nothing here is written to disk.
package transcript

import (
	"strings"
	"sync"
	"time"
)

// Speaker labels.
const (
	Me     = "me"     // the user's microphone
	Others = "others" // system audio: everyone else in the meeting
)

// Line is one utterance. While the recognizer is still refining it, the same
// line (same ID) is updated in place; Final marks it as settled.
type Line struct {
	ID      uint64    `json:"id"`
	Speaker string    `json:"speaker"`
	Text    string    `json:"text"`
	Final   bool      `json:"final"`
	Start   time.Time `json:"start"`
	Updated time.Time `json:"updated"`
}

// Store is a bounded, concurrency-safe list of transcript lines.
type Store struct {
	mu       sync.Mutex
	lines    []Line
	maxLines int
	now      func() time.Time
}

// New returns a store that keeps at most maxLines utterances.
func New(maxLines int) *Store {
	if maxLines <= 0 {
		maxLines = 4000
	}
	return &Store{maxLines: maxLines, now: time.Now}
}

// Upsert adds a line or updates the existing line with the same ID, and
// returns the stored line. Empty text is ignored for new lines.
func (s *Store) Upsert(id uint64, speaker, text string, final bool) (Line, bool) {
	text = strings.TrimSpace(text)
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Recent lines are the ones being updated, so search from the end.
	for i := len(s.lines) - 1; i >= 0; i-- {
		if s.lines[i].ID != id {
			continue
		}
		ln := &s.lines[i]
		if text != "" {
			ln.Text = text
		}
		ln.Final = ln.Final || final
		ln.Updated = now
		return *ln, true
	}
	if text == "" {
		return Line{}, false
	}
	ln := Line{ID: id, Speaker: speaker, Text: text, Final: final, Start: now, Updated: now}
	s.lines = append(s.lines, ln)
	if extra := len(s.lines) - s.maxLines; extra > 0 {
		s.lines = append([]Line(nil), s.lines[extra:]...)
	}
	return ln, true
}

// Clear drops the whole transcript.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = nil
}

// Tail returns up to n of the most recent lines, oldest first.
func (s *Store) Tail(n int) []Line {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if n > 0 && len(s.lines) > n {
		start = len(s.lines) - n
	}
	return append([]Line{}, s.lines[start:]...)
}

// Len returns the number of stored lines.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

// Recent renders the lines updated within the window as prompt text, oldest
// first, dropping the oldest lines if the result would exceed maxChars.
func (s *Store) Recent(window time.Duration, maxChars int) string {
	cutoff := s.now().Add(-window)

	s.mu.Lock()
	var picked []Line
	for _, ln := range s.lines {
		if window > 0 && ln.Updated.Before(cutoff) {
			continue
		}
		picked = append(picked, ln)
	}
	s.mu.Unlock()

	rendered := make([]string, len(picked))
	total := 0
	for i, ln := range picked {
		rendered[i] = "[" + ln.Start.Format("15:04:05") + "] " + SpeakerLabel(ln.Speaker) + ": " + ln.Text
		total += len(rendered[i]) + 1
	}
	first := 0
	for maxChars > 0 && total > maxChars && first < len(rendered)-1 {
		total -= len(rendered[first]) + 1
		first++
	}
	return strings.Join(rendered[first:], "\n")
}

// SpeakerLabel is the name used for a speaker in prompts.
func SpeakerLabel(speaker string) string {
	if speaker == Me {
		return "Me"
	}
	return "Others"
}
