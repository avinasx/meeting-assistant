// Package native wraps the operating-system facilities the assistant needs:
// the overlay window, screen capture, audio capture with speech recognition,
// global hotkeys and document text extraction.
//
// The real implementation is macOS-only (native_darwin.go). Other platforms
// get stubs that report ErrUnsupported so the rest of the program compiles.
package native

import (
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported is returned by every operation on platforms without a
// native implementation.
var ErrUnsupported = errors.New("not supported on this operating system (macOS 14+ required)")

// Source identifies where audio came from.
type Source int

const (
	SourceMic    Source = 0 // the user's microphone
	SourceSystem Source = 1 // system audio: the other participants
)

// Permission is the state of a privacy permission.
type Permission int

const (
	PermissionUnknown Permission = 0 // not asked yet (or, for screen recording, not granted)
	PermissionGranted Permission = 1
	PermissionDenied  Permission = 2
)

func (p Permission) String() string {
	switch p {
	case PermissionGranted:
		return "granted"
	case PermissionDenied:
		return "denied"
	}
	return "unknown"
}

// Permissions is the state of everything the assistant needs access to.
type Permissions struct {
	Screen     Permission
	Microphone Permission
	Speech     Permission
}

// Handlers receives events from the native layer. Callbacks arrive on
// arbitrary threads (the hotkey one on the UI thread) and must not block.
type Handlers struct {
	// Transcript reports recognized speech. Updates for the same lineID
	// replace the earlier text; final marks the line as settled.
	Transcript func(source Source, lineID uint64, text string, final bool)
	// Hotkey reports that the global hotkey registered under id was pressed.
	Hotkey func(id int)
	// Error reports a problem in background audio capture or recognition.
	Error func(source Source, message string)
	// CaptureStopped reports that capture of a source ended unexpectedly.
	CaptureStopped func(source Source)
}

// ListenResult describes what StartListening managed to start.
type ListenResult struct {
	Mic      bool
	System   bool
	OnDevice bool // speech is recognized on this machine, not on Apple's servers
}

// Rect is a rectangle expressed in fractions (0..1) of a display, with the
// origin at the top-left corner.
type Rect struct{ X, Y, W, H float64 }

// Hotkey is a parsed key combination.
type Hotkey struct {
	KeyCode   uint32
	Modifiers uint32
}

// Carbon modifier masks.
const (
	modCmd     = 0x0100
	modShift   = 0x0200
	modOption  = 0x0800
	modControl = 0x1000
)

// keyCodes maps key names to macOS virtual key codes (ANSI layout).
var keyCodes = map[string]uint32{
	"a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05, "z": 0x06, "x": 0x07,
	"c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C, "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10,
	"t": 0x11, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "=": 0x18,
	"9": 0x19, "7": 0x1A, "-": 0x1B, "8": 0x1C, "0": 0x1D, "]": 0x1E, "o": 0x1F, "u": 0x20,
	"[": 0x21, "i": 0x22, "p": 0x23, "l": 0x25, "j": 0x26, "'": 0x27, "k": 0x28, ";": 0x29,
	"\\": 0x2A, ",": 0x2B, "/": 0x2C, "n": 0x2D, "m": 0x2E, ".": 0x2F, "`": 0x32,
	"return": 0x24, "enter": 0x24, "tab": 0x30, "space": 0x31,
	"backslash": 0x2A, "slash": 0x2C, "comma": 0x2B, "period": 0x2F, "semicolon": 0x29,
	"quote": 0x27, "minus": 0x1B, "equal": 0x18, "backtick": 0x32,
	"left": 0x7B, "right": 0x7C, "down": 0x7D, "up": 0x7E,
	"f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60, "f6": 0x61,
	"f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6D, "f11": 0x67, "f12": 0x6F,
}

// ParseHotkey parses a combination such as "cmd+shift+return". At least one
// of cmd, ctrl or option is required: a global hotkey without them would
// swallow ordinary typing in every application.
func ParseHotkey(spec string) (Hotkey, error) {
	var hk Hotkey
	key := ""
	for _, part := range strings.Split(strings.ToLower(strings.TrimSpace(spec)), "+") {
		part = strings.TrimSpace(part)
		switch part {
		case "":
			return hk, fmt.Errorf("invalid hotkey %q", spec)
		case "cmd", "command", "⌘":
			hk.Modifiers |= modCmd
		case "shift", "⇧":
			hk.Modifiers |= modShift
		case "opt", "option", "alt", "⌥":
			hk.Modifiers |= modOption
		case "ctrl", "control", "⌃":
			hk.Modifiers |= modControl
		default:
			if key != "" {
				return hk, fmt.Errorf("invalid hotkey %q: more than one key", spec)
			}
			key = part
		}
	}
	code, ok := keyCodes[key]
	if !ok {
		if key == "" {
			return hk, fmt.Errorf("invalid hotkey %q: no key given", spec)
		}
		return hk, fmt.Errorf("invalid hotkey %q: unknown key %q", spec, key)
	}
	if hk.Modifiers&(modCmd|modOption|modControl) == 0 {
		return hk, fmt.Errorf("invalid hotkey %q: needs cmd, ctrl or option", spec)
	}
	hk.KeyCode = code
	return hk, nil
}
