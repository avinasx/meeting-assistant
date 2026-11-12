// speech_darwin.m — captures microphone and system audio and transcribes both
// with the Speech framework, reporting text to Go as it is recognized.
//
// Threading: each MATranscriber owns a serial queue. Audio buffers are
// delivered on that queue, the housekeeping timer fires on it and recognizer
// callbacks hop onto it, so a transcriber's state is only touched there.

#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <Foundation/Foundation.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <Speech/Speech.h>
#import <stdatomic.h>

#include <stdlib.h>
#include <string.h>

#include "bridge.h"
#include "_cgo_export.h"

// A pause this long after the last change in the hypothesis ends an utterance.
static const CFTimeInterval kSilenceGap = 1.4;
// Utterances are cut at this length even without a pause (server-side
// recognition refuses audio longer than about a minute).
static const CFTimeInterval kMaxSegment = 55.0;

static _Atomic uint64_t gNextLineID = 1;

static uint64_t maNextLineID(void) {
    return atomic_fetch_add(&gNextLineID, 1);
}

static void maEmit(int source, uint64_t lineID, NSString *text, BOOL final) {
    const char *utf8 = text.UTF8String;
    if (utf8 == NULL) {
        return;
    }
    goTranscript(source, lineID, (char *)utf8, final ? 1 : 0);
}

static void maReport(int source, NSString *message) {
    const char *utf8 = message.UTF8String;
    if (utf8 != NULL) {
        goNativeError(source, (char *)utf8);
    }
}

// The recognizer ends a task with one of these when it simply heard nothing
// (1110 "No speech detected") or wants the request repeated (203 "Retry").
// Neither means recognition is broken.
static BOOL maIsSilenceError(NSError *error) {
    return [error.domain isEqualToString:@"kAFAssistantErrorDomain"] && (error.code == 1110 || error.code == 203);
}

// Copies the PCM audio of a sample buffer into an AVAudioPCMBuffer, which is
// the form the recognizer accepts for any sample rate and channel layout.
static AVAudioPCMBuffer *maPCMBuffer(CMSampleBufferRef sample) {
    if (sample == NULL || !CMSampleBufferDataIsReady(sample)) {
        return nil;
    }
    CMFormatDescriptionRef description = CMSampleBufferGetFormatDescription(sample);
    if (description == NULL) {
        return nil;
    }
    const AudioStreamBasicDescription *asbd = CMAudioFormatDescriptionGetStreamBasicDescription(description);
    if (asbd == NULL || asbd->mFormatID != kAudioFormatLinearPCM) {
        return nil;
    }
    CMItemCount frames = CMSampleBufferGetNumSamples(sample);
    if (frames <= 0) {
        return nil;
    }
    AVAudioFormat *format = [[AVAudioFormat alloc] initWithStreamDescription:asbd];
    if (format == nil) {
        return nil;
    }
    AVAudioPCMBuffer *buffer = [[AVAudioPCMBuffer alloc] initWithPCMFormat:format
                                                             frameCapacity:(AVAudioFrameCount)frames];
    if (buffer == nil) {
        return nil;
    }
    buffer.frameLength = (AVAudioFrameCount)frames;
    OSStatus status = CMSampleBufferCopyPCMDataIntoAudioBufferList(sample, 0, (int32_t)frames,
                                                                   buffer.mutableAudioBufferList);
    return status == noErr ? buffer : nil;
}

// ---- one utterance --------------------------------------------------------

@interface MASegment : NSObject
@property(nonatomic) uint64_t lineID;
@property(nonatomic, copy) NSString *text;
@property(nonatomic) CFAbsoluteTime started;
@property(nonatomic) CFAbsoluteTime lastChange;
@property(nonatomic) BOOL ended; // no more audio will be appended
@property(nonatomic) BOOL done;  // the recognizer has finished with it
@property(nonatomic, strong) SFSpeechAudioBufferRecognitionRequest *request;
@property(nonatomic, strong) SFSpeechRecognitionTask *task;
@end

@implementation MASegment
@end

// ---- transcriber: audio buffers in, transcript lines out -------------------

@interface MATranscriber : NSObject
@property(nonatomic, readonly) dispatch_queue_t queue;
@property(nonatomic, readonly) BOOL onDevice;
- (instancetype)initWithSource:(int)source locale:(NSString *)locale error:(NSString **)error;
- (void)start;
- (void)stop;
- (void)appendSample:(CMSampleBufferRef)sample; // call on self.queue
@end

@implementation MATranscriber {
    int _source;
    SFSpeechRecognizer *_recognizer;
    MASegment *_current;
    dispatch_source_t _timer;
    BOOL _running;
    int _failures; // consecutive segments that ended in an error without text
}

- (instancetype)initWithSource:(int)source locale:(NSString *)locale error:(NSString **)error {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    _source = source;
    _recognizer = [[SFSpeechRecognizer alloc] initWithLocale:[NSLocale localeWithLocaleIdentifier:locale]];
    if (_recognizer == nil) {
        *error = [NSString stringWithFormat:@"speech recognition does not support the language \"%@\"", locale];
        return nil;
    }
    if (!_recognizer.isAvailable) {
        *error = [NSString stringWithFormat:@"speech recognition for \"%@\" is currently unavailable", locale];
        return nil;
    }
    _onDevice = _recognizer.supportsOnDeviceRecognition;
    const char *label = source == MA_SOURCE_MIC ? "meeting-assistant.transcribe.mic" : "meeting-assistant.transcribe.system";
    _queue = dispatch_queue_create(label, DISPATCH_QUEUE_SERIAL);
    return self;
}

- (void)start {
    dispatch_async(_queue, ^{
        if (self->_running) {
            return;
        }
        self->_running = YES;
        self->_failures = 0;
        [self beginSegment];

        self->_timer = dispatch_source_create(DISPATCH_SOURCE_TYPE_TIMER, 0, 0, self->_queue);
        dispatch_source_set_timer(self->_timer, dispatch_time(DISPATCH_TIME_NOW, 250 * NSEC_PER_MSEC),
                                  250 * NSEC_PER_MSEC, 50 * NSEC_PER_MSEC);
        __weak MATranscriber *weakSelf = self;
        dispatch_source_set_event_handler(self->_timer, ^{
            [weakSelf tick];
        });
        dispatch_resume(self->_timer);
    });
}

- (void)stop {
    dispatch_sync(_queue, ^{
        if (!self->_running) {
            return;
        }
        self->_running = NO;
        if (self->_timer != nil) {
            dispatch_source_cancel(self->_timer);
            self->_timer = nil;
        }
        MASegment *segment = self->_current;
        self->_current = nil;
        if (segment != nil && !segment.done) {
            segment.ended = YES;
            // Let the recognizer deliver its final text for what was said.
            [segment.request endAudio];
        }
    });
}

// All methods below run on _queue.

- (void)beginSegment {
    SFSpeechAudioBufferRecognitionRequest *request = [[SFSpeechAudioBufferRecognitionRequest alloc] init];
    request.shouldReportPartialResults = YES;
    request.taskHint = SFSpeechRecognitionTaskHintDictation;
    request.requiresOnDeviceRecognition = _onDevice;
    if (@available(macOS 13.0, *)) {
        request.addsPunctuation = YES;
    }

    MASegment *segment = [[MASegment alloc] init];
    segment.lineID = maNextLineID();
    segment.text = @"";
    segment.started = CFAbsoluteTimeGetCurrent();
    segment.lastChange = segment.started;
    segment.request = request;
    _current = segment;

    __weak MATranscriber *weakSelf = self;
    dispatch_queue_t queue = _queue;
    segment.task = [_recognizer recognitionTaskWithRequest:request
                                             resultHandler:^(SFSpeechRecognitionResult *result, NSError *error) {
        NSString *text = result.bestTranscription.formattedString;
        BOOL isFinal = result.isFinal;
        dispatch_async(queue, ^{
            [weakSelf segment:segment didProduce:text final:isFinal error:error];
        });
    }];
}

- (void)segment:(MASegment *)segment didProduce:(NSString *)text final:(BOOL)isFinal error:(NSError *)error {
    if (segment.done) {
        return;
    }

    if (text.length > 0) {
        NSString *previous = segment.text;
        // The on-device recognizer sometimes discards its hypothesis after a
        // pause and starts over within the same task. Keep what was said.
        if (!segment.ended && previous.length >= 16 && text.length * 2 < previous.length &&
            ![previous hasPrefix:text]) {
            maEmit(_source, segment.lineID, previous, YES);
            segment.lineID = maNextLineID();
            previous = @"";
        }
        if (![text isEqualToString:previous]) {
            segment.text = text;
            segment.lastChange = CFAbsoluteTimeGetCurrent();
        }
        if (!isFinal) {
            maEmit(_source, segment.lineID, text, NO);
        }
        _failures = 0;
    }

    if (!isFinal && error == nil) {
        return;
    }

    // The task is over, successfully or not.
    segment.done = YES;
    segment.task = nil;
    segment.request = nil;
    NSString *finalText = text.length > 0 ? text : segment.text;
    if (finalText.length > 0) {
        maEmit(_source, segment.lineID, finalText, YES);
    }

    if (segment != _current || !_running) {
        return; // a segment we already rotated away from
    }

    // The live segment ended on its own. A clean finish or a "nothing was
    // said" timeout just needs a fresh segment; for real errors back off so
    // a broken recognizer is not restarted in a tight loop.
    BOOL shortLived = CFAbsoluteTimeGetCurrent() - segment.started < 0.5;
    if (error == nil && !shortLived) {
        [self beginSegment];
        return;
    }
    CFTimeInterval delay = 0.3;
    if (error != nil && finalText.length == 0 && !maIsSilenceError(error)) {
        _failures++;
        delay = MIN(0.25 * (double)(1 << MIN(_failures, 5)), 6.0);
        if (_failures == 4) {
            maReport(_source, [NSString stringWithFormat:@"speech recognition keeps failing: %@",
                                                         error.localizedDescription ?: @"unknown error"]);
        }
    }
    __weak MATranscriber *weakSelf = self;
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(delay * NSEC_PER_SEC)), _queue, ^{
        [weakSelf restartAfter:segment];
    });
}

- (void)restartAfter:(MASegment *)segment {
    if (_running && _current == segment) {
        [self beginSegment];
    }
}

- (void)tick {
    MASegment *segment = _current;
    if (!_running || segment == nil || segment.done) {
        return;
    }
    CFAbsoluteTime now = CFAbsoluteTimeGetCurrent();
    BOOL paused = segment.text.length > 0 && now - segment.lastChange > kSilenceGap;
    BOOL tooLong = now - segment.started > kMaxSegment;
    if (paused || tooLong) {
        // Close this utterance and immediately open the next so no audio is
        // lost; the closed one reports its final text asynchronously.
        segment.ended = YES;
        [segment.request endAudio];
        [self beginSegment];
    }
}

- (void)appendSample:(CMSampleBufferRef)sample {
    MASegment *segment = _current;
    if (!_running || segment == nil || segment.done || segment.ended) {
        return;
    }
    AVAudioPCMBuffer *buffer = maPCMBuffer(sample);
    if (buffer != nil) {
        [segment.request appendAudioPCMBuffer:buffer];
    }
}

@end

// ---- microphone -----------------------------------------------------------

@interface MAMicCapture : NSObject <AVCaptureAudioDataOutputSampleBufferDelegate>
@end

@implementation MAMicCapture {
    AVCaptureSession *_session;
    MATranscriber *_transcriber;
}

- (BOOL)startWithTranscriber:(MATranscriber *)transcriber error:(NSString **)error {
    AVCaptureDevice *device = [AVCaptureDevice defaultDeviceWithMediaType:AVMediaTypeAudio];
    if (device == nil) {
        *error = @"no microphone found";
        return NO;
    }
    NSError *inputError = nil;
    AVCaptureDeviceInput *input = [AVCaptureDeviceInput deviceInputWithDevice:device error:&inputError];
    if (input == nil) {
        *error = inputError.localizedDescription ?: @"cannot open the microphone";
        return NO;
    }
    AVCaptureSession *session = [[AVCaptureSession alloc] init];
    AVCaptureAudioDataOutput *output = [[AVCaptureAudioDataOutput alloc] init];
    if (![session canAddInput:input] || ![session canAddOutput:output]) {
        *error = @"cannot configure microphone capture";
        return NO;
    }
    [session addInput:input];
    [session addOutput:output];
    [output setSampleBufferDelegate:self queue:transcriber.queue];

    _transcriber = transcriber;
    _session = session;
    [session startRunning];
    if (!session.isRunning) {
        _session = nil;
        *error = @"microphone capture did not start";
        return NO;
    }
    return YES;
}

- (void)stop {
    [_session stopRunning];
    _session = nil;
}

- (void)captureOutput:(AVCaptureOutput *)output
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
           fromConnection:(AVCaptureConnection *)connection {
    [_transcriber appendSample:sampleBuffer];
}

@end

// ---- system audio (what the other participants say) ------------------------

@interface MASystemCapture : NSObject <SCStreamOutput, SCStreamDelegate>
@end

@implementation MASystemCapture {
    SCStream *_stream;
    MATranscriber *_transcriber;
    dispatch_queue_t _videoQueue;
    BOOL _stopping;
}

- (BOOL)startWithTranscriber:(MATranscriber *)transcriber error:(NSString **)error {
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    __block NSString *failure = nil;
    __block SCStream *started = nil;
    _transcriber = transcriber;
    _videoQueue = dispatch_queue_create("meeting-assistant.capture.video", DISPATCH_QUEUE_SERIAL);

    [SCShareableContent
        getShareableContentExcludingDesktopWindows:NO
                               onScreenWindowsOnly:NO
                                 completionHandler:^(SCShareableContent *content, NSError *contentError) {
        SCDisplay *display = content.displays.firstObject;
        if (display == nil) {
            failure = contentError.localizedDescription ?: @"screen recording is not permitted";
            dispatch_semaphore_signal(done);
            return;
        }

        // System audio is captured through a display stream. The video side
        // is unavoidable, so keep it as small and slow as allowed.
        SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
        SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
        config.capturesAudio = YES;
        config.excludesCurrentProcessAudio = YES;
        config.sampleRate = 48000;
        config.channelCount = 1;
        config.width = 2;
        config.height = 2;
        config.minimumFrameInterval = CMTimeMake(1, 1);
        config.queueDepth = 3;
        config.showsCursor = NO;

        SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:self];
        NSError *outputError = nil;
        if (![stream addStreamOutput:self
                                type:SCStreamOutputTypeAudio
                  sampleHandlerQueue:transcriber.queue
                               error:&outputError]) {
            failure = outputError.localizedDescription ?: @"cannot capture system audio";
            dispatch_semaphore_signal(done);
            return;
        }
        // Frames are discarded; registering an output avoids a log message
        // per dropped frame.
        [stream addStreamOutput:self type:SCStreamOutputTypeScreen sampleHandlerQueue:self->_videoQueue error:nil];

        [stream startCaptureWithCompletionHandler:^(NSError *startError) {
            if (startError != nil) {
                failure = startError.localizedDescription ?: @"cannot start system audio capture";
            } else {
                started = stream;
            }
            dispatch_semaphore_signal(done);
        }];
    }];

    if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 15 * NSEC_PER_SEC)) != 0) {
        *error = @"system audio capture timed out while starting";
        return NO;
    }
    if (started == nil) {
        *error = failure ?: @"cannot start system audio capture";
        return NO;
    }
    _stream = started;
    return YES;
}

- (void)stop {
    _stopping = YES;
    SCStream *stream = _stream;
    _stream = nil;
    if (stream == nil) {
        return;
    }
    dispatch_semaphore_t done = dispatch_semaphore_create(0);
    [stream stopCaptureWithCompletionHandler:^(NSError *error) {
        dispatch_semaphore_signal(done);
    }];
    dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
}

- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
                   ofType:(SCStreamOutputType)type {
    if (type == SCStreamOutputTypeAudio) {
        [_transcriber appendSample:sampleBuffer];
    }
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    if (_stopping) {
        return;
    }
    maReport(MA_SOURCE_SYSTEM, [NSString stringWithFormat:@"system audio capture stopped: %@",
                                                          error.localizedDescription ?: @"unknown reason"]);
    goCaptureStopped(MA_SOURCE_SYSTEM);
}

@end

// ---- C entry points ---------------------------------------------------------

static NSObject *gLock;
static MATranscriber *gMicTranscriber;
static MATranscriber *gSystemTranscriber;
static MAMicCapture *gMic;
static MASystemCapture *gSystem;

static NSObject *maLock(void) {
    static dispatch_once_t once;
    dispatch_once(&once, ^{
        gLock = [[NSObject alloc] init];
    });
    return gLock;
}

static void maStopLocked(void) {
    [gMic stop];
    [gSystem stop];
    [gMicTranscriber stop];
    [gSystemTranscriber stop];
    gMic = nil;
    gSystem = nil;
    gMicTranscriber = nil;
    gSystemTranscriber = nil;
}

int ma_listen_start(const char *locale, int wantMic, int wantSystem, char **err) {
    *err = NULL;
    NSString *localeID = locale != NULL && strlen(locale) > 0 ? @(locale) : @"en-US";
    NSMutableArray<NSString *> *problems = [NSMutableArray array];
    int result = 0;

    @synchronized(maLock()) {
        maStopLocked();
        @try {
            if (wantMic) {
                NSString *problem = nil;
                MATranscriber *transcriber = [[MATranscriber alloc] initWithSource:MA_SOURCE_MIC
                                                                           locale:localeID
                                                                            error:&problem];
                MAMicCapture *mic = [[MAMicCapture alloc] init];
                if (transcriber != nil && [mic startWithTranscriber:transcriber error:&problem]) {
                    [transcriber start];
                    gMicTranscriber = transcriber;
                    gMic = mic;
                    result |= MA_LISTEN_MIC;
                    if (transcriber.onDevice) {
                        result |= MA_LISTEN_ON_DEVICE;
                    }
                } else {
                    [problems addObject:[@"Microphone: " stringByAppendingString:problem ?: @"failed to start"]];
                }
            }
            if (wantSystem) {
                NSString *problem = nil;
                MATranscriber *transcriber = [[MATranscriber alloc] initWithSource:MA_SOURCE_SYSTEM
                                                                           locale:localeID
                                                                            error:&problem];
                MASystemCapture *system = [[MASystemCapture alloc] init];
                if (transcriber != nil && [system startWithTranscriber:transcriber error:&problem]) {
                    [transcriber start];
                    gSystemTranscriber = transcriber;
                    gSystem = system;
                    result |= MA_LISTEN_SYSTEM;
                    if (transcriber.onDevice) {
                        result |= MA_LISTEN_ON_DEVICE;
                    }
                } else {
                    [problems addObject:[@"Meeting audio: " stringByAppendingString:problem ?: @"failed to start"]];
                }
            }
        } @catch (NSException *exception) {
            [problems addObject:exception.reason ?: @"unexpected error while starting audio capture"];
        }
    }

    if (problems.count > 0) {
        *err = strdup([[problems componentsJoinedByString:@"\n"] UTF8String] ?: "failed to start");
    }
    return result;
}

void ma_listen_stop(void) {
    @synchronized(maLock()) {
        maStopLocked();
    }
}

char *ma_speech_locales(void) {
    NSMutableArray<NSString *> *identifiers = [NSMutableArray array];
    for (NSLocale *locale in [SFSpeechRecognizer supportedLocales]) {
        // Speech uses "en_US"; present the BCP-47 form "en-US".
        [identifiers addObject:[locale.localeIdentifier stringByReplacingOccurrencesOfString:@"_" withString:@"-"]];
    }
    [identifiers sortUsingSelector:@selector(compare:)];
    return strdup([[identifiers componentsJoinedByString:@"\n"] UTF8String] ?: "");
}
