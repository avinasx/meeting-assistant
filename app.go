package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/avinasx/meeting-assistant/internal/config"
	"github.com/avinasx/meeting-assistant/internal/docs"
	"github.com/avinasx/meeting-assistant/internal/llm"
	"github.com/avinasx/meeting-assistant/internal/native"
	"github.com/avinasx/meeting-assistant/internal/prompt"
	"github.com/avinasx/meeting-assistant/internal/stealth"
	"github.com/avinasx/meeting-assistant/internal/transcript"
)

const (
	hotkeyToggle = 1
	hotkeyHelp   = 2

	// Screenshot sent to the model: longest side in pixels and JPEG quality.
	// Large enough to read ordinary on-screen text, small enough to upload fast.
	shotMaxDim  = 1920
	shotQuality = 0.75

	transcriptMaxChars = 40_000
	historyKeep        = 5
	historyAnswerChars = 1200
	answerTimeout      = 3 * time.Minute
)

// App is the object bound to the frontend. Its exported methods are callable
// from JavaScript as window.go.main.App.<Method>().
type App struct {
	ctx        context.Context
	cfg        *config.Store
	transcript *transcript.Store
	docs       *docs.Loader

	setupOnce sync.Once
	listenMu  sync.Mutex // serializes starting/stopping capture and the stealth check

	mu             sync.Mutex
	listen         native.ListenResult
	listenNote     string
	visible        bool
	hotkeyErrors   []string
	toggleHotkeyOK bool
	answerSeq      uint64
	answerCancel   context.CancelFunc
	answering      bool
	history        []prompt.Exchange
}

// NewApp creates the application around a settings store.
func NewApp(store *config.Store) *App {
	return &App{
		cfg:        store,
		transcript: transcript.New(4000),
		docs:       docs.NewLoader(native.ExtractText),
		visible:    true,
	}
}

// ---- lifecycle --------------------------------------------------------------

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	native.SetHandlers(native.Handlers{
		Transcript:     a.onTranscript,
		Hotkey:         a.onHotkey,
		Error:          a.onNativeError,
		CaptureStopped: a.onCaptureStopped,
	})
	// Normally the overlay is set up when the page has loaded. Do it anyway
	// after a moment so a page that fails to load cannot leave the window
	// unconfigured.
	go func() {
		time.Sleep(2 * time.Second)
		a.setup()
	}()
}

func (a *App) domReady(context.Context) { a.setup() }

func (a *App) shutdown(context.Context) {
	native.StopListening()
}

func (a *App) setup() {
	a.setupOnce.Do(func() {
		s := a.cfg.Get()
		native.ConfigureOverlay(s.Stealth)
		a.applyHotkeys(s)
		a.emitState()

		// Start listening straight away, but only if that needs no
		// permission prompt; otherwise wait for the user to press Listen.
		if s.AutoListen && native.Supported && a.permissionsGranted(s) {
			go func() {
				if _, err := a.StartListening(); err != nil {
					log.Printf("auto-listen: %v", err)
				}
			}()
		}
	})
}

func (a *App) permissionsGranted(s config.Settings) bool {
	p := native.CheckPermissions()
	if p.Speech != native.PermissionGranted {
		return false
	}
	if s.CaptureMic && p.Microphone != native.PermissionGranted {
		return false
	}
	if s.CaptureSystem && p.Screen != native.PermissionGranted {
		return false
	}
	return s.CaptureMic || s.CaptureSystem
}

func (a *App) emit(event string, data any) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, event, data)
	}
}

// ---- state ------------------------------------------------------------------

// State is the snapshot the UI renders its status from.
type State struct {
	Supported bool `json:"supported"`

	Listening  bool   `json:"listening"`
	Mic        bool   `json:"mic"`
	System     bool   `json:"system"`
	OnDevice   bool   `json:"onDevice"`
	ListenNote string `json:"listenNote"`

	Visible bool    `json:"visible"`
	Stealth bool    `json:"stealth"`
	Opacity float64 `json:"opacity"`

	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Ready     bool   `json:"ready"`
	ReadyHint string `json:"readyHint"`
	Answering bool   `json:"answering"`

	Permissions           map[string]string `json:"permissions"`
	CanRequestPermissions bool              `json:"canRequestPermissions"`

	HotkeyToggle string   `json:"hotkeyToggle"`
	HotkeyHelp   string   `json:"hotkeyHelp"`
	HotkeyErrors []string `json:"hotkeyErrors"`
}

// GetState returns the current status.
func (a *App) GetState() State { return a.state() }

func (a *App) state() State {
	s := a.cfg.Get()
	p := s.Active()
	perms := native.CheckPermissions()

	st := State{
		Supported: native.Supported,
		Stealth:   s.Stealth,
		Opacity:   s.Opacity,
		Provider:  s.Provider,
		Model:     p.Model,
		Permissions: map[string]string{
			"screen":     perms.Screen.String(),
			"microphone": perms.Microphone.String(),
			"speech":     perms.Speech.String(),
		},
		CanRequestPermissions: native.CanRequestPermissions(),
		HotkeyToggle:          s.HotkeyToggle,
		HotkeyHelp:            s.HotkeyHelp,
	}
	switch {
	case p.APIKey == "" && !(s.Provider == config.OpenAI && p.BaseURL != ""):
		st.ReadyHint = "Add an API key in Settings"
	case p.Model == "":
		st.ReadyHint = "Choose a model in Settings"
	default:
		st.Ready = true
	}

	a.mu.Lock()
	st.Mic, st.System, st.OnDevice = a.listen.Mic, a.listen.System, a.listen.OnDevice
	st.Listening = a.listen.Mic || a.listen.System
	st.ListenNote = a.listenNote
	st.Visible = a.visible
	st.Answering = a.answering
	st.HotkeyErrors = append([]string{}, a.hotkeyErrors...)
	a.mu.Unlock()
	return st
}

func (a *App) emitState() { a.emit("state", a.state()) }

// ---- settings ---------------------------------------------------------------

// SettingsView is what the settings form is filled from. API keys are never
// sent to the page; HasKey says whether one is stored.
type SettingsView struct {
	config.Settings
	HasKey map[string]bool `json:"hasKey"`
	Path   string          `json:"path"`
}

// GetSettings returns the settings with the API keys removed.
func (a *App) GetSettings() SettingsView {
	s := a.cfg.Get()
	view := SettingsView{Settings: s, HasKey: map[string]bool{}, Path: a.cfg.Path()}
	for kind, p := range view.Providers {
		view.HasKey[kind] = p.APIKey != ""
		p.APIKey = ""
	}
	return view
}

// SaveSettings stores the form. An empty API key means "keep the stored
// one"; context files are managed separately and are left untouched.
func (a *App) SaveSettings(in config.Settings) (State, error) {
	old := a.cfg.Get()
	in.Normalize()
	for kind, p := range in.Providers {
		if p.APIKey == "" {
			if prev := old.Providers[kind]; prev != nil {
				p.APIKey = prev.APIKey
			}
		}
	}
	in.ContextFiles = old.ContextFiles

	if native.Supported {
		if _, err := native.ParseHotkey(in.HotkeyToggle); err != nil {
			return a.state(), err
		}
		if _, err := native.ParseHotkey(in.HotkeyHelp); err != nil {
			return a.state(), err
		}
		if strings.EqualFold(strings.TrimSpace(in.HotkeyToggle), strings.TrimSpace(in.HotkeyHelp)) {
			return a.state(), errors.New("the two hotkeys must be different")
		}
	}
	if err := a.cfg.Save(in); err != nil {
		return a.state(), err
	}
	cur := a.cfg.Get()

	if cur.Stealth != old.Stealth {
		native.SetStealth(cur.Stealth)
	}
	if cur.HotkeyToggle != old.HotkeyToggle || cur.HotkeyHelp != old.HotkeyHelp {
		a.applyHotkeys(cur)
	}
	captureChanged := cur.Language != old.Language || cur.CaptureMic != old.CaptureMic || cur.CaptureSystem != old.CaptureSystem
	if captureChanged && a.isListening() {
		// Restart capture with the new sources/language.
		go func() {
			if _, err := a.StartListening(); err != nil {
				log.Printf("restart listening: %v", err)
			}
		}()
	}
	a.emitState()
	return a.state(), nil
}

// ClearAPIKey removes the stored key of one provider.
func (a *App) ClearAPIKey(kind string) error {
	s := a.cfg.Get()
	p := s.Providers[kind]
	if p == nil {
		return fmt.Errorf("unknown provider %q", kind)
	}
	p.APIKey = ""
	if err := a.cfg.Save(s); err != nil {
		return err
	}
	a.emitState()
	return nil
}

// ListModels asks a provider which models the key can use. apiKey and
// baseURL come from the (possibly unsaved) form; an empty apiKey falls back
// to the stored key.
func (a *App) ListModels(kind, apiKey, baseURL string) ([]string, error) {
	s := a.cfg.Get()
	p := s.Providers[kind]
	if p == nil {
		return nil, fmt.Errorf("unknown provider %q", kind)
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		key = p.APIKey
	}
	provider, err := llm.New(kind, llm.Config{APIKey: key, BaseURL: baseURL})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	return provider.ListModels(ctx)
}

// SpeechLocales lists the languages available for transcription.
func (a *App) SpeechLocales() []string {
	locales := native.SpeechLocales()
	if locales == nil {
		return []string{}
	}
	return locales
}

func (a *App) applyHotkeys(s config.Settings) {
	var errs []string
	register := func(id int, label, spec string) bool {
		hk, err := native.ParseHotkey(spec)
		if err == nil {
			err = native.RegisterHotkey(id, hk)
		}
		if err != nil {
			native.UnregisterHotkey(id)
			errs = append(errs, fmt.Sprintf("%s (%s): %v", label, spec, err))
			return false
		}
		return true
	}
	toggleOK := register(hotkeyToggle, "Show/hide hotkey", s.HotkeyToggle)
	register(hotkeyHelp, "Help hotkey", s.HotkeyHelp)

	a.mu.Lock()
	a.hotkeyErrors = errs
	a.toggleHotkeyOK = toggleOK
	a.mu.Unlock()
}

// ---- context files ----------------------------------------------------------

// ContextFiles returns the reference files with their size or read error.
func (a *App) ContextFiles() []docs.Doc {
	return a.docs.Load(a.cfg.Get().ContextFiles)
}

// AddContextFiles shows a file picker and adds the chosen files.
func (a *App) AddContextFiles() ([]docs.Doc, error) {
	picked, err := runtime.OpenMultipleFilesDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Add context files",
	})
	if err != nil {
		return nil, err
	}
	s := a.cfg.Get()
	have := make(map[string]bool, len(s.ContextFiles))
	for _, p := range s.ContextFiles {
		have[p] = true
	}
	added := false
	for _, p := range picked {
		if p == "" || have[p] {
			continue
		}
		have[p] = true
		s.ContextFiles = append(s.ContextFiles, p)
		added = true
	}
	if added {
		if err := a.cfg.Save(s); err != nil {
			return nil, err
		}
	}
	return a.docs.Load(s.ContextFiles), nil
}

// RemoveContextFile removes one reference file from the list.
func (a *App) RemoveContextFile(path string) ([]docs.Doc, error) {
	s := a.cfg.Get()
	kept := s.ContextFiles[:0]
	for _, p := range s.ContextFiles {
		if p != path {
			kept = append(kept, p)
		}
	}
	s.ContextFiles = kept
	if err := a.cfg.Save(s); err != nil {
		return nil, err
	}
	return a.docs.Load(s.ContextFiles), nil
}

// ---- listening --------------------------------------------------------------

const (
	hintBundle = "Permissions can only be requested by the app bundle. Build it with `make build` and open build/bin/MeetingAssistant.app."
	hintSpeech = "Speech Recognition permission is required. Turn on Meeting Assistant in System Settings → Privacy & Security → Speech Recognition."
	hintMic    = "Microphone: permission not granted (System Settings → Privacy & Security → Microphone)."
	hintScreen = "Meeting audio needs the Screen & System Audio Recording permission. Grant it in System Settings → Privacy & Security, then quit and reopen Meeting Assistant."
)

func (a *App) isListening() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listen.Mic || a.listen.System
}

// StartListening starts (or restarts) audio capture and transcription,
// asking for permissions when needed.
func (a *App) StartListening() (State, error) {
	a.listenMu.Lock()
	err := a.startListening()
	a.listenMu.Unlock()
	a.emitState()
	return a.state(), err
}

func (a *App) startListening() error {
	if !native.Supported {
		return native.ErrUnsupported
	}
	s := a.cfg.Get()
	if !s.CaptureMic && !s.CaptureSystem {
		return errors.New("both audio sources are turned off in Settings")
	}
	perms := native.CheckPermissions()
	canAsk := native.CanRequestPermissions()
	var notes []string

	if perms.Speech != native.PermissionGranted {
		if !canAsk {
			return errors.New(hintBundle)
		}
		if native.RequestSpeechPermission() != native.PermissionGranted {
			return errors.New(hintSpeech)
		}
	}

	mic := s.CaptureMic
	if mic && perms.Microphone != native.PermissionGranted {
		if !canAsk || native.RequestMicrophonePermission() != native.PermissionGranted {
			mic = false
			notes = append(notes, hintMic)
		}
	}

	system := s.CaptureSystem
	if system && perms.Screen != native.PermissionGranted {
		// The preflight answer can be stale until the app restarts, so ask
		// and then try anyway; a real refusal surfaces as a start error.
		native.RequestScreenPermission()
	}

	var res native.ListenResult
	if mic || system {
		var err error
		res, err = native.StartListening(s.Language, mic, system)
		if err != nil {
			notes = append(notes, err.Error())
		}
	}
	if system && !res.System && perms.Screen != native.PermissionGranted {
		notes = append(notes, hintScreen)
	}
	note := strings.Join(notes, "\n")

	a.mu.Lock()
	a.listen = res
	a.listenNote = note
	a.mu.Unlock()

	if !res.Mic && !res.System {
		if note == "" {
			note = "could not start listening"
		}
		return errors.New(note)
	}
	return nil
}

// StopListening stops audio capture and transcription.
func (a *App) StopListening() State {
	a.listenMu.Lock()
	native.StopListening()
	a.mu.Lock()
	a.listen = native.ListenResult{}
	a.listenNote = ""
	a.mu.Unlock()
	a.listenMu.Unlock()
	a.emitState()
	return a.state()
}

// Transcript returns the most recent transcript lines.
func (a *App) Transcript() []transcript.Line { return a.transcript.Tail(300) }

// ClearTranscript forgets the transcript and the earlier answers.
func (a *App) ClearTranscript() {
	a.transcript.Clear()
	a.mu.Lock()
	a.history = nil
	a.mu.Unlock()
	a.emit("transcript:cleared", nil)
}

// OpenPrivacySettings opens a Privacy & Security pane of System Settings:
// "screen", "microphone" or "speech".
func (a *App) OpenPrivacySettings(pane string) { native.OpenPrivacySettings(pane) }

func (a *App) onTranscript(source native.Source, lineID uint64, text string, final bool) {
	speaker := transcript.Others
	if source == native.SourceMic {
		speaker = transcript.Me
	}
	if line, ok := a.transcript.Upsert(lineID, speaker, text, final); ok {
		a.emit("transcript", line)
	}
}

func sourceName(source native.Source) string {
	if source == native.SourceMic {
		return "Microphone"
	}
	return "Meeting audio"
}

func (a *App) onNativeError(source native.Source, message string) {
	note := sourceName(source) + ": " + message
	log.Print(note)
	a.mu.Lock()
	a.listenNote = note
	a.mu.Unlock()
	a.emitState()
}

func (a *App) onCaptureStopped(source native.Source) {
	a.mu.Lock()
	if source == native.SourceMic {
		a.listen.Mic = false
	} else {
		a.listen.System = false
	}
	a.mu.Unlock()
	a.emitState()
}

// ---- answering --------------------------------------------------------------

// Help answers "what do I need right now?" from the current screen, the
// recent transcript and the context files.
func (a *App) Help() { a.startAnswer("") }

// Ask answers a typed question with the same context as Help.
func (a *App) Ask(question string) { a.startAnswer(strings.TrimSpace(question)) }

// CancelAnswer stops the answer currently being generated.
func (a *App) CancelAnswer() {
	a.mu.Lock()
	cancel := a.answerCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type answerStart struct {
	ID       uint64 `json:"id"`
	Question string `json:"question"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type answerDelta struct {
	ID   uint64 `json:"id"`
	Text string `json:"text"`
}

type answerEnd struct {
	ID        uint64 `json:"id"`
	Cancelled bool   `json:"cancelled"`
	Error     string `json:"error,omitempty"`
	Notice    string `json:"notice,omitempty"`
	FirstMS   int64  `json:"firstMs"`
	TotalMS   int64  `json:"totalMs"`
}

func (a *App) startAnswer(question string) {
	s := a.cfg.Get()
	ctx, cancel := context.WithTimeout(a.ctx, answerTimeout)

	a.mu.Lock()
	if a.answerCancel != nil {
		a.answerCancel() // a new request replaces the one in flight
	}
	a.answerSeq++
	id := a.answerSeq
	a.answerCancel = cancel
	a.answering = true
	history := append([]prompt.Exchange(nil), a.history...)
	a.mu.Unlock()

	a.emit("answer:start", answerStart{ID: id, Question: question, Provider: s.Provider, Model: s.Active().Model})
	a.emitState()
	go a.runAnswer(ctx, cancel, id, question, s, history)
}

func (a *App) runAnswer(ctx context.Context, cancel context.CancelFunc, id uint64, question string, s config.Settings, history []prompt.Exchange) {
	defer cancel()
	started := time.Now()
	end := answerEnd{ID: id}

	finish := func() {
		end.TotalMS = time.Since(started).Milliseconds()
		a.mu.Lock()
		if a.answerSeq == id {
			a.answerCancel = nil
			a.answering = false
		}
		a.mu.Unlock()
		a.emit("answer:end", end)
		a.emitState()
	}

	p := s.Active()
	provider, err := llm.New(s.Provider, llm.Config{
		APIKey:    p.APIKey,
		Model:     p.Model,
		BaseURL:   p.BaseURL,
		Reasoning: p.Reasoning,
	})
	if err != nil {
		end.Error = err.Error()
		finish()
		return
	}

	// Grab the screen while the context files are being read.
	var shot []byte
	var shotErr error
	var wg sync.WaitGroup
	if s.IncludeScreenshot && native.Supported {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shot, shotErr = native.CaptureScreen(native.OverlayDisplay(), true, shotMaxDim, shotQuality)
		}()
	}
	documents := docs.Render(a.docs.Load(s.ContextFiles))
	wg.Wait()
	if shotErr != nil {
		end.Notice = "Answered without the screen: " + shotErr.Error()
	}

	system, user := prompt.Build(prompt.Input{
		AnswerPrompt:  s.AnswerPrompt,
		Documents:     documents,
		Transcript:    a.transcript.Recent(time.Duration(s.TranscriptMinutes)*time.Minute, transcriptMaxChars),
		Listening:     a.isListening(),
		History:       history,
		Question:      question,
		HasScreenshot: len(shot) > 0,
		Now:           time.Now(),
	})
	req := llm.Request{System: system, Text: user, MaxTokens: s.MaxOutputTokens}
	if len(shot) > 0 {
		req.Images = []llm.Image{{MIME: "image/jpeg", Data: shot}}
	}

	var answer strings.Builder
	err = provider.Stream(ctx, req, func(text string) {
		if end.FirstMS == 0 {
			end.FirstMS = time.Since(started).Milliseconds()
		}
		answer.WriteString(text)
		a.emit("answer:delta", answerDelta{ID: id, Text: text})
	})

	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		end.Cancelled = true
	case errors.Is(err, context.DeadlineExceeded):
		end.Error = "The model took too long to answer."
	default:
		end.Error = err.Error()
	}

	if text := strings.TrimSpace(answer.String()); text != "" && !end.Cancelled {
		a.mu.Lock()
		a.history = append(a.history, prompt.Exchange{
			Time:     started,
			Question: question,
			Answer:   prompt.Truncate(text, historyAnswerChars),
		})
		if extra := len(a.history) - historyKeep; extra > 0 {
			a.history = append([]prompt.Exchange(nil), a.history[extra:]...)
		}
		a.mu.Unlock()
	}
	finish()
}

// ---- window -----------------------------------------------------------------

func (a *App) onHotkey(id int) {
	// Called on the UI thread: hand off immediately.
	switch id {
	case hotkeyToggle:
		go a.toggleVisible()
	case hotkeyHelp:
		go func() {
			a.setVisible(true)
			a.Help()
		}()
	}
}

func (a *App) setVisible(visible bool) {
	a.mu.Lock()
	changed := a.visible != visible
	a.visible = visible
	a.mu.Unlock()
	native.SetVisible(visible)
	if changed {
		a.emitState()
	}
}

func (a *App) toggleVisible() {
	a.mu.Lock()
	next := !a.visible
	a.mu.Unlock()
	a.setVisible(next)
}

// Hide hides the overlay. It refuses when the show/hide hotkey could not be
// registered, because the app has no Dock icon or menu to bring it back.
func (a *App) Hide() error {
	a.mu.Lock()
	ok := a.toggleHotkeyOK
	a.mu.Unlock()
	if !ok {
		return errors.New("cannot hide: the show/hide hotkey is not registered, so there would be no way to bring the window back")
	}
	a.setVisible(false)
	return nil
}

// Quit exits the application.
func (a *App) Quit() { runtime.Quit(a.ctx) }

// StealthResult is the outcome of the screen-capture visibility check.
type StealthResult struct {
	Verdict string `json:"verdict"` // "hidden", "visible" or "inconclusive"
	Message string `json:"message"`
}

// StealthCheck measures whether the overlay appears in a screen capture.
//
// The page must be showing the magenta test pattern while this runs. The
// display is captured twice through ScreenCaptureKit — the mechanism meeting
// apps use for screen sharing — without the app excluding its own window:
// first with hiding switched off, which must show the pattern and proves the
// measurement works, then with hiding switched on.
func (a *App) StealthCheck() (StealthResult, error) {
	if !native.Supported {
		return StealthResult{}, native.ErrUnsupported
	}
	a.listenMu.Lock()
	defer a.listenMu.Unlock()

	frame, ok := native.OverlayFrame()
	if !ok {
		return StealthResult{}, errors.New("cannot locate the overlay window")
	}
	display := native.OverlayDisplay()
	// Whatever happens, leave the window in the state the settings ask for.
	defer func() { native.SetStealth(a.cfg.Get().Stealth) }()

	measure := func(hide bool) (float64, error) {
		native.SetStealth(hide)
		time.Sleep(300 * time.Millisecond) // let the window server apply it
		shot, err := native.CaptureScreen(display, false, 1280, 0.9)
		if err != nil {
			return 0, err
		}
		img, err := jpeg.Decode(bytes.NewReader(shot))
		if err != nil {
			return 0, fmt.Errorf("decode capture: %w", err)
		}
		cov, ok := stealth.Coverage(img, stealth.Rect{X: frame.X, Y: frame.Y, W: frame.W, H: frame.H})
		if !ok {
			return 0, errors.New("the overlay is off-screen; move it fully onto the display and retry")
		}
		return cov, nil
	}

	control, err := measure(false)
	if err != nil {
		return StealthResult{}, err
	}
	if stealth.Judge(control) != stealth.Visible {
		return StealthResult{
			Verdict: string(stealth.Inconclusive),
			Message: "The test pattern was not detected even with hiding switched off, so the check cannot tell either way. Make sure nothing covers the overlay and try again.",
		}, nil
	}
	hidden, err := measure(true)
	if err != nil {
		return StealthResult{}, err
	}

	res := StealthResult{Verdict: string(stealth.Judge(hidden))}
	switch stealth.Verdict(res.Verdict) {
	case stealth.Hidden:
		res.Message = "Hidden: with hiding on, the overlay did not appear in a ScreenCaptureKit capture of this display."
	case stealth.Visible:
		res.Message = "VISIBLE: with hiding on, the overlay still appeared in a ScreenCaptureKit capture. On this macOS version other people would see it when you share this screen."
	default:
		res.Message = fmt.Sprintf("Inconclusive: %.0f%% of the overlay showed up in the capture. Try again without moving the window.", hidden*100)
	}
	return res, nil
}
