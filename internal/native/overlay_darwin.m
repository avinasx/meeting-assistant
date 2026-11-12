// overlay_darwin.m — window behaviour, permissions and global hotkeys.

#import <AVFoundation/AVFoundation.h>
#import <Carbon/Carbon.h>
#import <Cocoa/Cocoa.h>
#import <Speech/Speech.h>

#include "bridge.h"
#include "_cgo_export.h"

static void maOnMain(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_async(dispatch_get_main_queue(), block);
    }
}

static void maOnMainSync(dispatch_block_t block) {
    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}

// The app has exactly one real window, created by Wails. Must run on main.
static NSWindow *maMainWindow(void) {
    for (NSWindow *w in [NSApp windows]) {
        if ([NSStringFromClass([w class]) isEqualToString:@"WailsWindow"]) {
            return w;
        }
    }
    return [NSApp mainWindow] ?: [[NSApp windows] firstObject];
}

static void maApplyStealth(NSWindow *w, int stealth) {
    w.sharingType = stealth ? NSWindowSharingNone : NSWindowSharingReadOnly;
}

void ma_overlay_configure(int stealth) {
    maOnMain(^{
        // Accessory apps have no Dock icon and no Cmd-Tab entry, and their
        // windows are allowed to float over other apps' full-screen Spaces.
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        NSWindow *w = maMainWindow();
        if (w == nil) {
            return;
        }
        maApplyStealth(w, stealth);
        w.opaque = NO;
        w.backgroundColor = [NSColor clearColor];
        w.hasShadow = NO;
        w.level = NSStatusWindowLevel;
        w.hidesOnDeactivate = NO;
        w.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                               NSWindowCollectionBehaviorFullScreenAuxiliary |
                               NSWindowCollectionBehaviorStationary |
                               NSWindowCollectionBehaviorIgnoresCycle;
        [w orderFrontRegardless];
    });
}

void ma_overlay_set_stealth(int stealth) {
    maOnMain(^{
        NSWindow *w = maMainWindow();
        if (w != nil) {
            maApplyStealth(w, stealth);
        }
    });
}

void ma_overlay_set_visible(int visible) {
    maOnMain(^{
        NSWindow *w = maMainWindow();
        if (w == nil) {
            return;
        }
        if (visible) {
            // Show without activating the app, so the meeting keeps focus.
            [w orderFrontRegardless];
        } else {
            [w orderOut:nil];
        }
    });
}

uint32_t ma_overlay_display_id(void) {
    __block uint32_t displayID = 0;
    maOnMainSync(^{
        NSWindow *w = maMainWindow();
        NSScreen *screen = w.screen ?: [NSScreen mainScreen];
        NSNumber *number = screen.deviceDescription[@"NSScreenNumber"];
        displayID = number != nil ? number.unsignedIntValue : CGMainDisplayID();
    });
    return displayID;
}

int ma_overlay_frame(double *x, double *y, double *w, double *h) {
    __block int ok = 0;
    __block NSRect wf = NSZeroRect, sf = NSZeroRect;
    maOnMainSync(^{
        NSWindow *win = maMainWindow();
        NSScreen *screen = win.screen ?: [NSScreen mainScreen];
        if (win == nil || screen == nil) {
            return;
        }
        wf = win.frame;
        sf = screen.frame;
        ok = 1;
    });
    if (!ok || sf.size.width <= 0 || sf.size.height <= 0) {
        return 0;
    }
    // Cocoa's origin is bottom-left; images are top-left.
    *x = (wf.origin.x - sf.origin.x) / sf.size.width;
    *y = (NSMaxY(sf) - NSMaxY(wf)) / sf.size.height;
    *w = wf.size.width / sf.size.width;
    *h = wf.size.height / sf.size.height;
    return 1;
}

// ---- permissions ----------------------------------------------------------

int ma_has_usage_descriptions(void) {
    NSBundle *bundle = [NSBundle mainBundle];
    return [bundle objectForInfoDictionaryKey:@"NSMicrophoneUsageDescription"] != nil &&
           [bundle objectForInfoDictionaryKey:@"NSSpeechRecognitionUsageDescription"] != nil;
}

int ma_screen_permission(void) {
    return CGPreflightScreenCaptureAccess() ? MA_PERM_GRANTED : MA_PERM_UNKNOWN;
}

void ma_request_screen_permission(void) {
    CGRequestScreenCaptureAccess();
}

int ma_mic_permission(void) {
    switch ([AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio]) {
        case AVAuthorizationStatusAuthorized:
            return MA_PERM_GRANTED;
        case AVAuthorizationStatusDenied:
        case AVAuthorizationStatusRestricted:
            return MA_PERM_DENIED;
        default:
            return MA_PERM_UNKNOWN;
    }
}

int ma_request_mic_permission(void) {
    if (!ma_has_usage_descriptions()) {
        return MA_PERM_DENIED; // requesting without a usage string aborts the process
    }
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio
                             completionHandler:^(BOOL granted) {
                                 dispatch_semaphore_signal(done);
                             }];
    dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
    return ma_mic_permission();
}

int ma_speech_permission(void) {
    switch ([SFSpeechRecognizer authorizationStatus]) {
        case SFSpeechRecognizerAuthorizationStatusAuthorized:
            return MA_PERM_GRANTED;
        case SFSpeechRecognizerAuthorizationStatusDenied:
        case SFSpeechRecognizerAuthorizationStatusRestricted:
            return MA_PERM_DENIED;
        default:
            return MA_PERM_UNKNOWN;
    }
}

int ma_request_speech_permission(void) {
    if (!ma_has_usage_descriptions()) {
        return MA_PERM_DENIED;
    }
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    [SFSpeechRecognizer requestAuthorization:^(SFSpeechRecognizerAuthorizationStatus status) {
        dispatch_semaphore_signal(done);
    }];
    dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
    return ma_speech_permission();
}

void ma_open_privacy_settings(const char *pane) {
    NSString *name = pane != NULL ? @(pane) : @"";
    NSString *anchor = @"Privacy_ScreenCapture";
    if ([name isEqualToString:@"microphone"]) {
        anchor = @"Privacy_Microphone";
    } else if ([name isEqualToString:@"speech"]) {
        anchor = @"Privacy_SpeechRecognition";
    }
    NSString *link = [@"x-apple.systempreferences:com.apple.preference.security?" stringByAppendingString:anchor];
    maOnMain(^{
        [[NSWorkspace sharedWorkspace] openURL:[NSURL URLWithString:link]];
    });
}

// ---- global hotkeys (Carbon; needs no Accessibility permission) -----------

#define MA_MAX_HOTKEYS 8
static EventHotKeyRef gHotKeys[MA_MAX_HOTKEYS];
static BOOL gHotKeyHandlerInstalled = NO;

static OSStatus maHotKeyHandler(EventHandlerCallRef next, EventRef event, void *userData) {
    EventHotKeyID hotKey;
    OSStatus status = GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL,
                                        sizeof(hotKey), NULL, &hotKey);
    if (status == noErr) {
        goHotkey((int)hotKey.id);
    }
    return noErr;
}

int ma_hotkey_register(int id, uint32_t keyCode, uint32_t modifiers) {
    if (id < 0 || id >= MA_MAX_HOTKEYS) {
        return paramErr;
    }
    __block OSStatus status = noErr;
    maOnMainSync(^{
        if (!gHotKeyHandlerInstalled) {
            EventTypeSpec spec = {kEventClassKeyboard, kEventHotKeyPressed};
            status = InstallApplicationEventHandler(NewEventHandlerUPP(maHotKeyHandler), 1, &spec, NULL, NULL);
            if (status != noErr) {
                return;
            }
            gHotKeyHandlerInstalled = YES;
        }
        if (gHotKeys[id] != NULL) {
            UnregisterEventHotKey(gHotKeys[id]);
            gHotKeys[id] = NULL;
        }
        EventHotKeyID hotKeyID = {.signature = 'MAst', .id = (UInt32)id};
        EventHotKeyRef ref = NULL;
        status = RegisterEventHotKey(keyCode, modifiers, hotKeyID, GetApplicationEventTarget(), 0, &ref);
        if (status == noErr) {
            gHotKeys[id] = ref;
        }
    });
    return (int)status;
}

void ma_hotkey_unregister(int id) {
    if (id < 0 || id >= MA_MAX_HOTKEYS) {
        return;
    }
    maOnMainSync(^{
        if (gHotKeys[id] != NULL) {
            UnregisterEventHotKey(gHotKeys[id]);
            gHotKeys[id] = NULL;
        }
    });
}
