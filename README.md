# meeting-assistant

A floating, translucent overlay for macOS that listens to a meeting, looks at your screen, and tells you what to say. Written in Go (Wails for the window, cgo for the macOS APIs).

- **Help button**: captures your current screen and sends it with the recent transcript and your context files to the model; the answer streams into the overlay.
- **Always-on context**: meeting audio ("Others") and your microphone ("Me") are transcribed continuously, so nothing has to be gathered when you press Help except one screenshot.
- **Model agnostic**: Google Gemini, Anthropic, or any OpenAI-compatible endpoint (OpenAI, OpenRouter, Ollama, ...). Provider, API key and model are chosen in Settings.
- **Settings**: answer-style prompt, context files (text, Markdown, code, CSV, JSON, PDF, DOCX, RTF), transcription language, opacity, hotkeys.
- **Hidden**: no Dock icon, no Cmd-Tab entry, and the window asks macOS to exclude it from screen capture. See "Status" below before relying on that.

## Status: built, not yet run

This was developed on a managed Mac whose security policy (Santa) blocks every locally compiled binary and bans `codesign`. As a result:

| Check | Result |
| --- | --- |
| `go build` of the app bundle | passes |
| `go vet` (including test files) | passes |
| Objective-C with `-Wall -Wextra` | no warnings |
| Frontend tests under Node (`make test-frontend`) | pass |
| Go unit tests (`make test-go`) | written, **never executed** |
| Launching the app | **never done** |

Everything that needs the app running is therefore unverified: the overlay window, screen capture, audio capture, speech recognition, hotkeys, and the live API calls. Expect to fix things on first run.

### Is it really hidden from screen sharing?

Unknown. The window sets `NSWindow.sharingType = .none`. There are public reports that ScreenCaptureKit-based capture (what current browsers, Zoom and Teams use) ignores that flag on macOS 15 and later. This could not be measured here.

Settings has a **Test hiding** button that measures it on your machine: it paints the overlay a test colour, captures the display through ScreenCaptureKit with hiding off (control) and on, and reports whether the overlay showed up. Run it, and also check once with a real test call, before trusting it.

## Build and run

Requires macOS 14+, Go, and the Xcode command line tools.

```sh
make build          # build/bin/MeetingAssistant.app
make run            # build and open
make test           # Go tests + frontend tests (frontend needs Node)
```

Run it from the `.app` bundle: macOS only shows the permission prompts for a bundle with usage descriptions.

`make build` does not code-sign. Where signing is allowed, sign the bundle so macOS keeps the granted permissions across rebuilds:

```sh
CODESIGN_IDENTITY=- make build      # ad-hoc
```

## First run

1. Open Settings (the gear), pick a provider, paste an API key, press **Load list** and choose a model.
2. Press the status dot to start listening. macOS asks for Speech Recognition, Microphone, and Screen & System Audio Recording. After granting screen recording, quit and reopen.
3. Press **Help**, or type a question.

| Hotkey | Action |
| --- | --- |
| `cmd+shift+return` | Help (shows the overlay if hidden) |
| `cmd+shift+backslash` | Show / hide the overlay |

Both can be changed in Settings. With no Dock icon, the show/hide hotkey is the only way to bring a hidden overlay back, so the app refuses to hide if that hotkey failed to register.

## Things to know

- **Models**: the Gemini default is `gemini-3.8-flash`. Anthropic and OpenAI have no default model; choose one from the list the API returns.
- **API keys** are stored in `~/Library/Application Support/MeetingAssistant/settings.json` with owner-only permissions, not in the Keychain.
- **Transcription** uses Apple's Speech framework. It runs on-device when the language supports that; otherwise audio goes to Apple. The tooltip on the status dot says which.
- **The transcript is kept in memory only.** The screenshot, recent transcript and context files are sent to the provider you chose. Gemini requests set `store: false`.
- **Use headphones**, or the microphone will pick up the other participants and they appear twice.
- **Answers are text for you to read out.** The app does not speak into the meeting.
- macOS shows its own screen-recording indicator while meeting audio is captured.
- Windows and Linux compile to stubs only.
- Recording or transcribing other people can require their consent, depending on where you and they are.

## Layout

```
main.go, app.go        Wails setup and the methods the UI calls
internal/llm           streaming clients: Gemini, Anthropic, OpenAI-compatible
internal/native        macOS: overlay window, screen capture, audio + speech, hotkeys
internal/transcript    rolling in-memory transcript
internal/docs          context files -> text
internal/prompt        builds the system prompt and user message
internal/stealth       measures whether the overlay appears in a capture
internal/config        settings file
frontend/              HTML/CSS/JS, no build step
frontend_test/         Node tests for the frontend
```
