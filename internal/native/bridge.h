// bridge.h — C interface between Go (cgo) and the macOS implementation.
//
// Conventions:
//   * Functions returning `char *` return a malloc'd UTF-8 string the caller
//     must free, or NULL.
//   * `char **err` out-parameters receive a malloc'd message on failure.
//   * None of these functions may be called on the main thread unless noted;
//     several of them block while waiting for the system.

#ifndef MA_BRIDGE_H
#define MA_BRIDGE_H

#include <stddef.h>
#include <stdint.h>

// Audio sources / transcript speakers.
enum { MA_SOURCE_MIC = 0, MA_SOURCE_SYSTEM = 1 };

// Permission states.
enum { MA_PERM_UNKNOWN = 0, MA_PERM_GRANTED = 1, MA_PERM_DENIED = 2 };

// Bits returned by ma_listen_start.
enum { MA_LISTEN_MIC = 1, MA_LISTEN_SYSTEM = 2, MA_LISTEN_ON_DEVICE = 4 };

// ---- overlay window -------------------------------------------------------

// Turns the app's window into a floating overlay: above full-screen apps, on
// every Space, no Dock icon, and (if stealth) excluded from screen capture.
void ma_overlay_configure(int stealth);
void ma_overlay_set_stealth(int stealth);
void ma_overlay_set_visible(int visible);
// Display the overlay is on (falls back to the main display).
uint32_t ma_overlay_display_id(void);
// Overlay frame as fractions of its display, origin top-left. Returns 0 if
// the window cannot be found.
int ma_overlay_frame(double *x, double *y, double *w, double *h);

// ---- permissions ----------------------------------------------------------

// 1 when running from an .app bundle whose Info.plist carries the usage
// descriptions macOS requires before it will show a permission prompt.
int ma_has_usage_descriptions(void);
int ma_screen_permission(void);          // MA_PERM_GRANTED or MA_PERM_UNKNOWN
void ma_request_screen_permission(void); // shows the system prompt once
int ma_mic_permission(void);
int ma_request_mic_permission(void);     // blocks until the user answers
int ma_speech_permission(void);
int ma_request_speech_permission(void);  // blocks until the user answers
// Opens the given Privacy & Security pane: "screen", "microphone", "speech".
void ma_open_privacy_settings(const char *pane);

// ---- screen capture -------------------------------------------------------

// Captures one display as JPEG. excludeSelf removes this app's own windows
// from the image. maxDim bounds the longer side in pixels (0 = native).
// Returns malloc'd bytes (length in *outLen) or NULL with *err set.
unsigned char *ma_capture_screen(uint32_t displayID, int excludeSelf, int maxDim,
                                 double quality, size_t *outLen, char **err);

// ---- audio capture + speech recognition ----------------------------------

// Starts capturing and transcribing. Returns a bitmask of MA_LISTEN_* bits
// for what is running; *err describes anything that failed to start.
int ma_listen_start(const char *locale, int wantMic, int wantSystem, char **err);
void ma_listen_stop(void);
// Newline-separated locale identifiers the recognizer supports.
char *ma_speech_locales(void);

// ---- global hotkeys -------------------------------------------------------

// Registers a system-wide hotkey (Carbon key code + modifier mask). Returns 0
// on success or the OSStatus error. Re-registering an id replaces it.
int ma_hotkey_register(int id, uint32_t keyCode, uint32_t modifiers);
void ma_hotkey_unregister(int id);

// ---- document text extraction --------------------------------------------

// Extracts plain text from PDF, DOCX, DOC, RTF or ODT.
char *ma_extract_text(const char *path, char **err);

#endif
