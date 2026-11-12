// Package config loads and saves the user's settings.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Provider kinds.
const (
	Gemini    = "gemini"
	Anthropic = "anthropic"
	OpenAI    = "openai"
)

// ProviderKinds lists the supported providers in display order.
var ProviderKinds = []string{Gemini, Anthropic, OpenAI}

// Provider holds the per-provider connection settings.
type Provider struct {
	APIKey string `json:"apiKey"`
	Model  string `json:"model"`
	// BaseURL overrides the provider's default endpoint. For "openai" this
	// makes any OpenAI-compatible server usable (OpenRouter, Ollama, ...).
	BaseURL string `json:"baseUrl"`
	// Reasoning is the thinking effort ("", "low", "medium", "high").
	// Empty means "do not send the parameter".
	Reasoning string `json:"reasoning"`
}

// Settings is everything the user can configure.
type Settings struct {
	Provider  string               `json:"provider"`
	Providers map[string]*Provider `json:"providers"`

	// AnswerPrompt is the user's instruction for how answers should be given.
	AnswerPrompt string `json:"answerPrompt"`
	// ContextFiles are absolute paths of reference files sent with each request.
	ContextFiles []string `json:"contextFiles"`

	// Language is the speech recognition locale, e.g. "en-US".
	Language      string `json:"language"`
	CaptureMic    bool   `json:"captureMic"`
	CaptureSystem bool   `json:"captureSystem"`
	// AutoListen starts transcription on launch when permissions are granted.
	AutoListen bool `json:"autoListen"`

	IncludeScreenshot bool `json:"includeScreenshot"`
	// TranscriptMinutes is how much recent transcript is sent with a request.
	TranscriptMinutes int `json:"transcriptMinutes"`
	// MaxOutputTokens caps the answer length; 0 uses the provider default.
	MaxOutputTokens int `json:"maxOutputTokens"`

	// Stealth excludes the window from screen capture and screen sharing.
	Stealth bool    `json:"stealth"`
	Opacity float64 `json:"opacity"`

	HotkeyToggle string `json:"hotkeyToggle"`
	HotkeyHelp   string `json:"hotkeyHelp"`
}

// DefaultAnswerPrompt is the initial answer-style instruction.
const DefaultAnswerPrompt = "Answer in first person, as words I can say out loud. " +
	"Lead with the direct answer in one or two sentences, then at most three short supporting bullets. " +
	"Use plain, natural spoken language."

// Default returns the settings used on first launch.
func Default() Settings {
	return Settings{
		Provider: Gemini,
		Providers: map[string]*Provider{
			Gemini:    {Model: "gemini-3.8-flash", Reasoning: "low"},
			Anthropic: {},
			OpenAI:    {},
		},
		AnswerPrompt:      DefaultAnswerPrompt,
		ContextFiles:      []string{},
		Language:          "en-US",
		CaptureMic:        true,
		CaptureSystem:     true,
		AutoListen:        true,
		IncludeScreenshot: true,
		TranscriptMinutes: 15,
		MaxOutputTokens:   0,
		Stealth:           true,
		Opacity:           0.82,
		HotkeyToggle:      "cmd+shift+backslash",
		HotkeyHelp:        "cmd+shift+return",
	}
}

// Normalize fills in missing values and clamps out-of-range ones so that a
// hand-edited or older settings file still yields a usable configuration.
func (s *Settings) Normalize() {
	def := Default()
	if !validKind(s.Provider) {
		s.Provider = def.Provider
	}
	if s.Providers == nil {
		s.Providers = map[string]*Provider{}
	}
	for _, kind := range ProviderKinds {
		p := s.Providers[kind]
		if p == nil {
			p = &Provider{}
			*p = *def.Providers[kind]
			s.Providers[kind] = p
		}
		p.APIKey = strings.TrimSpace(p.APIKey)
		p.Model = strings.TrimSpace(p.Model)
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		switch p.Reasoning {
		case "", "low", "medium", "high":
		default:
			p.Reasoning = ""
		}
	}
	if s.ContextFiles == nil {
		s.ContextFiles = []string{}
	}
	if strings.TrimSpace(s.Language) == "" {
		s.Language = def.Language
	}
	if s.TranscriptMinutes < 1 {
		s.TranscriptMinutes = def.TranscriptMinutes
	}
	if s.TranscriptMinutes > 180 {
		s.TranscriptMinutes = 180
	}
	if s.MaxOutputTokens < 0 {
		s.MaxOutputTokens = 0
	}
	if s.Opacity < 0.25 {
		s.Opacity = 0.25
	}
	if s.Opacity > 1 {
		s.Opacity = 1
	}
	if strings.TrimSpace(s.HotkeyToggle) == "" {
		s.HotkeyToggle = def.HotkeyToggle
	}
	if strings.TrimSpace(s.HotkeyHelp) == "" {
		s.HotkeyHelp = def.HotkeyHelp
	}
}

// Active returns the settings of the selected provider.
func (s *Settings) Active() *Provider {
	if p := s.Providers[s.Provider]; p != nil {
		return p
	}
	return &Provider{}
}

// Clone returns a deep copy.
func (s Settings) Clone() Settings {
	out := s
	out.Providers = make(map[string]*Provider, len(s.Providers))
	for k, v := range s.Providers {
		if v == nil {
			continue
		}
		p := *v
		out.Providers[k] = &p
	}
	out.ContextFiles = append([]string{}, s.ContextFiles...)
	return out
}

func validKind(kind string) bool {
	for _, k := range ProviderKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Store persists settings to a JSON file. The file contains API keys, so it
// is written with owner-only permissions.
type Store struct {
	path string
	mu   sync.Mutex
	cur  Settings
}

// DefaultPath returns the settings location inside the user's config dir
// (~/Library/Application Support/MeetingAssistant/settings.json on macOS).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w", err)
	}
	return filepath.Join(dir, "MeetingAssistant", "settings.json"), nil
}

// Open loads settings from path, falling back to defaults when the file does
// not exist yet. A file that exists but cannot be parsed is reported as an
// error rather than silently replaced, so API keys are never lost that way.
func Open(path string) (*Store, error) {
	st := &Store{path: path, cur: Default()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	loaded := Default()
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil, fmt.Errorf("parse settings %s: %w", path, err)
	}
	loaded.Normalize()
	st.cur = loaded
	return st, nil
}

// Path returns the settings file location.
func (st *Store) Path() string { return st.path }

// Get returns a copy of the current settings.
func (st *Store) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur.Clone()
}

// Save validates, stores and writes the settings.
func (st *Store) Save(s Settings) error {
	s = s.Clone()
	s.Normalize()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(st.path), 0o700); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(st.path), ".settings-*.tmp")
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	if err := os.Rename(tmpName, st.path); err != nil {
		cleanup()
		return fmt.Errorf("write settings: %w", err)
	}
	st.cur = s
	return nil
}
