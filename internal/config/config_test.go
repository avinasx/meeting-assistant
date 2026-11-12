package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMissingFileGivesDefaults(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "nope", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if s.Provider != Gemini || !s.Stealth || !s.CaptureMic || !s.CaptureSystem || !s.IncludeScreenshot {
		t.Errorf("unexpected defaults: %+v", s)
	}
	for _, kind := range ProviderKinds {
		if s.Providers[kind] == nil {
			t.Errorf("default settings lack provider %q", kind)
		}
	}
	if s.AnswerPrompt == "" {
		t.Error("default answer prompt is empty")
	}
}

func TestSaveAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "settings.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	s.Provider = Anthropic
	s.Providers[Anthropic].APIKey = "  secret-key \n"
	s.Providers[Anthropic].Model = "claude-x"
	s.Providers[OpenAI].BaseURL = "http://localhost:11434/v1/"
	s.AnswerPrompt = "Be blunt."
	s.ContextFiles = []string{"/tmp/cv.pdf"}
	s.Stealth = false
	if err := st.Save(s); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings file mode = %o, want 600 because it holds API keys", perm)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got := again.Get()
	if got.Provider != Anthropic || got.Active().APIKey != "secret-key" || got.Active().Model != "claude-x" {
		t.Errorf("provider settings not round-tripped: %+v", got.Active())
	}
	if got.Providers[OpenAI].BaseURL != "http://localhost:11434/v1" {
		t.Errorf("base URL = %q, want trailing slash trimmed", got.Providers[OpenAI].BaseURL)
	}
	if got.AnswerPrompt != "Be blunt." || len(got.ContextFiles) != 1 || got.Stealth {
		t.Errorf("settings not round-tripped: %+v", got)
	}
}

func TestOpenKeepsDefaultsForMissingKeys(t *testing.T) {
	// A file written by an older version that knows only a few keys.
	path := filepath.Join(t.TempDir(), "settings.json")
	old := map[string]any{
		"provider":  "openai",
		"providers": map[string]any{"openai": map[string]any{"apiKey": "k", "model": "m"}},
		"stealth":   false,
	}
	raw, _ := json.Marshal(old)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Get()
	if s.Provider != OpenAI || s.Active().APIKey != "k" {
		t.Errorf("stored values lost: %+v", s)
	}
	if s.Stealth {
		t.Error("explicit stealth=false was overridden by the default")
	}
	if !s.CaptureMic || !s.IncludeScreenshot || s.TranscriptMinutes != 15 || s.HotkeyHelp == "" {
		t.Errorf("missing keys did not fall back to defaults: %+v", s)
	}
	if s.Providers[Gemini] == nil || s.Providers[Gemini].Model == "" {
		t.Errorf("provider missing from the file was not filled in: %+v", s.Providers[Gemini])
	}
}

func TestOpenCorruptFileIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("corrupt settings were silently replaced; API keys could be lost")
	}
}

func TestNormalizeClamps(t *testing.T) {
	s := Settings{Provider: "bogus", Opacity: 5, TranscriptMinutes: -3, MaxOutputTokens: -1,
		Providers: map[string]*Provider{Gemini: {Reasoning: "extreme"}}}
	s.Normalize()
	if s.Provider != Gemini || s.Opacity != 1 || s.TranscriptMinutes != 15 || s.MaxOutputTokens != 0 {
		t.Errorf("not clamped: %+v", s)
	}
	if s.Providers[Gemini].Reasoning != "" {
		t.Errorf("invalid reasoning kept: %q", s.Providers[Gemini].Reasoning)
	}
	if s.Language == "" || s.HotkeyToggle == "" || s.ContextFiles == nil {
		t.Errorf("empty values not defaulted: %+v", s)
	}
	s.Opacity = 0.01
	s.Normalize()
	if s.Opacity != 0.25 {
		t.Errorf("opacity = %v, want the 0.25 floor so the window stays findable", s.Opacity)
	}
}

func TestGetReturnsACopy(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "settings.json"))
	a := st.Get()
	a.Providers[Gemini].APIKey = "leak"
	a.ContextFiles = append(a.ContextFiles, "x")
	b := st.Get()
	if b.Providers[Gemini].APIKey != "" || len(b.ContextFiles) != 0 {
		t.Error("mutating the result of Get changed the stored settings")
	}
}
