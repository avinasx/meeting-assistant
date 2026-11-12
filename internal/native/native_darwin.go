//go:build darwin

package native

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework Carbon -framework AVFoundation
#cgo LDFLAGS: -framework CoreMedia -framework CoreGraphics -framework PDFKit
#cgo LDFLAGS: -framework ScreenCaptureKit -framework Speech
// Wails' macOS code uses UTType but leaves linking the framework to its CLI.
// Linking it here lets a plain `go build -tags desktop,production` work.
#cgo LDFLAGS: -framework UniformTypeIdentifiers

#include <stdlib.h>
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

// Supported reports whether this build has a native implementation.
const Supported = true

var (
	handlersMu sync.RWMutex
	handlers   Handlers
)

// SetHandlers installs the callbacks for native events. Call it once, before
// starting to listen or registering hotkeys.
func SetHandlers(h Handlers) {
	handlersMu.Lock()
	handlers = h
	handlersMu.Unlock()
}

func currentHandlers() Handlers {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers
}

//export goTranscript
func goTranscript(source C.int, lineID C.uint64_t, text *C.char, final C.int) {
	if fn := currentHandlers().Transcript; fn != nil {
		fn(Source(source), uint64(lineID), C.GoString(text), final != 0)
	}
}

//export goNativeError
func goNativeError(source C.int, message *C.char) {
	if fn := currentHandlers().Error; fn != nil {
		fn(Source(source), C.GoString(message))
	}
}

//export goCaptureStopped
func goCaptureStopped(source C.int) {
	if fn := currentHandlers().CaptureStopped; fn != nil {
		fn(Source(source))
	}
}

//export goHotkey
func goHotkey(id C.int) {
	if fn := currentHandlers().Hotkey; fn != nil {
		fn(int(id))
	}
}

func boolInt(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// takeString converts a malloc'd C string to Go and frees it.
func takeString(s *C.char) string {
	if s == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(s))
	return C.GoString(s)
}

// ConfigureOverlay turns the app window into the floating overlay. With
// stealth the window is excluded from screen capture and screen sharing.
func ConfigureOverlay(stealth bool) { C.ma_overlay_configure(boolInt(stealth)) }

// SetStealth switches exclusion from screen capture on or off.
func SetStealth(stealth bool) { C.ma_overlay_set_stealth(boolInt(stealth)) }

// SetVisible shows or hides the overlay without activating the app.
func SetVisible(visible bool) { C.ma_overlay_set_visible(boolInt(visible)) }

// OverlayDisplay returns the ID of the display the overlay is on.
func OverlayDisplay() uint32 { return uint32(C.ma_overlay_display_id()) }

// OverlayFrame returns where the overlay sits on its display.
func OverlayFrame() (Rect, bool) {
	var x, y, w, h C.double
	if C.ma_overlay_frame(&x, &y, &w, &h) == 0 {
		return Rect{}, false
	}
	return Rect{X: float64(x), Y: float64(y), W: float64(w), H: float64(h)}, true
}

// CanRequestPermissions reports whether the process runs from an app bundle
// with the usage descriptions macOS needs to show permission prompts.
func CanRequestPermissions() bool { return C.ma_has_usage_descriptions() != 0 }

// CheckPermissions returns the current permission states without prompting.
func CheckPermissions() Permissions {
	return Permissions{
		Screen:     Permission(C.ma_screen_permission()),
		Microphone: Permission(C.ma_mic_permission()),
		Speech:     Permission(C.ma_speech_permission()),
	}
}

// RequestScreenPermission shows the system's screen-recording prompt (macOS
// shows it only once; afterwards the user must use System Settings).
func RequestScreenPermission() { C.ma_request_screen_permission() }

// RequestMicrophonePermission prompts if needed and waits for the answer.
func RequestMicrophonePermission() Permission { return Permission(C.ma_request_mic_permission()) }

// RequestSpeechPermission prompts if needed and waits for the answer.
func RequestSpeechPermission() Permission { return Permission(C.ma_request_speech_permission()) }

// OpenPrivacySettings opens a Privacy & Security pane: "screen",
// "microphone" or "speech".
func OpenPrivacySettings(pane string) {
	cs := C.CString(pane)
	defer C.free(unsafe.Pointer(cs))
	C.ma_open_privacy_settings(cs)
}

// CaptureScreen returns a JPEG of the given display. With excludeSelf the
// app's own windows are left out. maxDim bounds the longer side in pixels.
func CaptureScreen(displayID uint32, excludeSelf bool, maxDim int, quality float64) ([]byte, error) {
	var length C.size_t
	var cerr *C.char
	data := C.ma_capture_screen(C.uint32_t(displayID), boolInt(excludeSelf), C.int(maxDim), C.double(quality), &length, &cerr)
	if data == nil {
		msg := takeString(cerr)
		if msg == "" {
			msg = "screen capture failed"
		}
		return nil, errors.New(msg)
	}
	defer C.free(unsafe.Pointer(data))
	return C.GoBytes(unsafe.Pointer(data), C.int(length)), nil
}

// StartListening starts capturing and transcribing the requested sources,
// replacing any capture already running. The result says what is running;
// the error describes whatever failed, and both can be set at once.
func StartListening(locale string, mic, system bool) (ListenResult, error) {
	cs := C.CString(locale)
	defer C.free(unsafe.Pointer(cs))
	var cerr *C.char
	bits := int(C.ma_listen_start(cs, boolInt(mic), boolInt(system), &cerr))
	res := ListenResult{
		Mic:      bits&int(C.MA_LISTEN_MIC) != 0,
		System:   bits&int(C.MA_LISTEN_SYSTEM) != 0,
		OnDevice: bits&int(C.MA_LISTEN_ON_DEVICE) != 0,
	}
	if msg := takeString(cerr); msg != "" {
		return res, errors.New(msg)
	}
	return res, nil
}

// StopListening stops all audio capture and transcription.
func StopListening() { C.ma_listen_stop() }

// SpeechLocales lists the languages the speech recognizer supports.
func SpeechLocales() []string {
	raw := takeString(C.ma_speech_locales())
	if raw == "" {
		return nil
	}
	return strings.Split(raw, "\n")
}

// RegisterHotkey registers a system-wide hotkey under id (0..7), replacing
// any hotkey previously registered under the same id.
func RegisterHotkey(id int, hk Hotkey) error {
	status := int(C.ma_hotkey_register(C.int(id), C.uint32_t(hk.KeyCode), C.uint32_t(hk.Modifiers)))
	switch status {
	case 0:
		return nil
	case -9878: // eventHotKeyExistsErr
		return errors.New("that key combination is already taken by another application")
	}
	return errors.New("the system refused to register the hotkey (error " + strconv.Itoa(status) + ")")
}

// UnregisterHotkey removes the hotkey registered under id.
func UnregisterHotkey(id int) { C.ma_hotkey_unregister(C.int(id)) }

// ExtractText returns the plain text of a PDF, DOCX, DOC, RTF or ODT file.
func ExtractText(path string) (string, error) {
	cs := C.CString(path)
	defer C.free(unsafe.Pointer(cs))
	var cerr *C.char
	text := C.ma_extract_text(cs, &cerr)
	if text == nil {
		msg := takeString(cerr)
		if msg == "" {
			msg = "cannot read document"
		}
		return "", errors.New(msg)
	}
	return takeString(text), nil
}
