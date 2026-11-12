package transcript

import (
	"strings"
	"testing"
	"time"
)

func newAt(start time.Time, max int) (*Store, *time.Time) {
	now := start
	s := New(max)
	s.now = func() time.Time { return now }
	return s, &now
}

func TestUpsertUpdatesInPlace(t *testing.T) {
	s, now := newAt(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), 10)

	if _, ok := s.Upsert(1, Others, "   ", false); ok {
		t.Error("an empty new line was stored")
	}
	s.Upsert(1, Others, "what is", false)
	*now = now.Add(2 * time.Second)
	line, ok := s.Upsert(1, Others, "what is your timeline", true)
	if !ok || line.Text != "what is your timeline" || !line.Final {
		t.Fatalf("line = %+v", line)
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (same ID updates in place)", s.Len())
	}
	if !line.Start.Equal(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) || !line.Updated.Equal(*now) {
		t.Errorf("times wrong: start %v updated %v", line.Start, line.Updated)
	}

	// A late empty final must not erase the text, and Final stays set.
	line, _ = s.Upsert(1, Others, "", false)
	if line.Text != "what is your timeline" || !line.Final {
		t.Errorf("late update damaged the line: %+v", line)
	}
}

func TestBoundedAndTail(t *testing.T) {
	s, _ := newAt(time.Now(), 3)
	for i := uint64(1); i <= 5; i++ {
		s.Upsert(i, Me, strings.Repeat("x", int(i)), true)
	}
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}
	tail := s.Tail(2)
	if len(tail) != 2 || tail[0].ID != 4 || tail[1].ID != 5 {
		t.Errorf("Tail(2) = %+v", tail)
	}
	if all := s.Tail(0); len(all) != 3 || all[0].ID != 3 {
		t.Errorf("Tail(0) = %+v", all)
	}
	s.Clear()
	if s.Len() != 0 {
		t.Error("Clear left lines behind")
	}
}

func TestRecentWindowAndFormat(t *testing.T) {
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s, now := newAt(start, 100)
	s.Upsert(1, Others, "old question", true)
	*now = start.Add(20 * time.Minute)
	s.Upsert(2, Others, "can you walk us through the design", true)
	*now = start.Add(21 * time.Minute)
	s.Upsert(3, Me, "sure", false)

	got := s.Recent(5*time.Minute, 0)
	want := "[10:20:00] Others: can you walk us through the design\n[10:21:00] Me: sure"
	if got != want {
		t.Errorf("Recent =\n%s\nwant\n%s", got, want)
	}
	if all := s.Recent(0, 0); !strings.Contains(all, "old question") {
		t.Errorf("window 0 should include everything, got %q", all)
	}
}

func TestRecentDropsOldestWhenTooLong(t *testing.T) {
	s, _ := newAt(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), 100)
	s.Upsert(1, Others, strings.Repeat("a", 100), true)
	s.Upsert(2, Others, strings.Repeat("b", 100), true)
	s.Upsert(3, Me, "the latest line", true)

	got := s.Recent(time.Hour, 160)
	if strings.Contains(got, "aaa") || !strings.Contains(got, "bbb") || !strings.HasSuffix(got, "the latest line") {
		t.Errorf("expected the oldest line dropped and the newest kept, got %q", got)
	}
	// Even an impossible budget keeps the most recent line.
	if got := s.Recent(time.Hour, 1); !strings.HasSuffix(got, "the latest line") || strings.Contains(got, "\n") {
		t.Errorf("tiny budget: got %q", got)
	}
}
