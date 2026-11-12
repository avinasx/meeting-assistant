//go:build !darwin

package native

// Supported reports whether this build has a native implementation.
const Supported = false

func SetHandlers(Handlers) {}

func ConfigureOverlay(bool) {}
func SetStealth(bool)       {}
func SetVisible(bool)       {}

func OverlayDisplay() uint32      { return 0 }
func OverlayFrame() (Rect, bool)  { return Rect{}, false }
func CanRequestPermissions() bool { return false }

func CheckPermissions() Permissions           { return Permissions{} }
func RequestScreenPermission()                {}
func RequestMicrophonePermission() Permission { return PermissionDenied }
func RequestSpeechPermission() Permission     { return PermissionDenied }
func OpenPrivacySettings(string)              {}

func CaptureScreen(uint32, bool, int, float64) ([]byte, error) { return nil, ErrUnsupported }

func StartListening(string, bool, bool) (ListenResult, error) { return ListenResult{}, ErrUnsupported }
func StopListening()                                          {}
func SpeechLocales() []string                                 { return nil }

func RegisterHotkey(int, Hotkey) error { return ErrUnsupported }
func UnregisterHotkey(int)             {}

func ExtractText(string) (string, error) { return "", ErrUnsupported }
