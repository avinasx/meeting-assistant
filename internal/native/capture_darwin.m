// capture_darwin.m — single-frame screen capture and document text extraction.

#import <Cocoa/Cocoa.h>
#import <PDFKit/PDFKit.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>

#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "bridge.h"

static char *maCopyString(NSString *s) {
    const char *utf8 = s.UTF8String;
    return strdup(utf8 != NULL ? utf8 : "");
}

static NSString *maDescribe(NSError *error, NSString *fallback) {
    if (error == nil) {
        return fallback;
    }
    return error.localizedDescription.length > 0 ? error.localizedDescription : fallback;
}

unsigned char *ma_capture_screen(uint32_t displayID, int excludeSelf, int maxDim,
                                 double quality, size_t *outLen, char **err) {
    *outLen = 0;
    *err = NULL;

    if (@available(macOS 14.0, *)) {
        dispatch_semaphore_t done = dispatch_semaphore_create(0);
        __block NSData *jpeg = nil;
        __block NSString *failure = nil;

        [SCShareableContent
            getShareableContentExcludingDesktopWindows:NO
                                   onScreenWindowsOnly:YES
                                     completionHandler:^(SCShareableContent *content, NSError *error) {
            if (content == nil) {
                failure = maDescribe(error, @"screen recording is not permitted");
                dispatch_semaphore_signal(done);
                return;
            }

            SCDisplay *display = nil;
            for (SCDisplay *candidate in content.displays) {
                if (candidate.displayID == displayID) {
                    display = candidate;
                    break;
                }
            }
            if (display == nil) {
                display = content.displays.firstObject;
            }
            if (display == nil) {
                failure = @"no display available to capture";
                dispatch_semaphore_signal(done);
                return;
            }

            NSMutableArray<SCWindow *> *excluded = [NSMutableArray array];
            if (excludeSelf) {
                pid_t me = getpid();
                for (SCWindow *window in content.windows) {
                    if (window.owningApplication.processID == me) {
                        [excluded addObject:window];
                    }
                }
            }

            SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:excluded];

            double scale = filter.pointPixelScale > 0 ? filter.pointPixelScale : 1.0;
            double width = display.width * scale;
            double height = display.height * scale;
            double longest = MAX(width, height);
            if (maxDim > 0 && longest > maxDim) {
                double k = (double)maxDim / longest;
                width *= k;
                height *= k;
            }

            SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
            config.width = (size_t)MAX(1.0, round(width));
            config.height = (size_t)MAX(1.0, round(height));
            config.showsCursor = NO;

            [SCScreenshotManager captureImageWithFilter:filter
                                          configuration:config
                                      completionHandler:^(CGImageRef image, NSError *captureError) {
                if (image == NULL) {
                    failure = maDescribe(captureError, @"screen capture failed");
                } else {
                    NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithCGImage:image];
                    jpeg = [rep representationUsingType:NSBitmapImageFileTypeJPEG
                                             properties:@{NSImageCompressionFactor : @(quality)}];
                    if (jpeg.length == 0) {
                        failure = @"could not encode the screenshot";
                    }
                }
                dispatch_semaphore_signal(done);
            }];
        }];

        if (dispatch_semaphore_wait(done, dispatch_time(DISPATCH_TIME_NOW, 15 * NSEC_PER_SEC)) != 0) {
            *err = maCopyString(@"screen capture timed out");
            return NULL;
        }
        if (failure != nil || jpeg.length == 0) {
            *err = maCopyString(failure ?: @"screen capture failed");
            return NULL;
        }

        unsigned char *bytes = malloc(jpeg.length);
        if (bytes == NULL) {
            *err = maCopyString(@"out of memory");
            return NULL;
        }
        memcpy(bytes, jpeg.bytes, jpeg.length);
        *outLen = jpeg.length;
        return bytes;
    }

    *err = maCopyString(@"screen capture needs macOS 14 or newer");
    return NULL;
}

char *ma_extract_text(const char *path, char **err) {
    *err = NULL;
    @autoreleasepool {
        NSURL *url = [NSURL fileURLWithPath:@(path)];
        NSString *ext = url.pathExtension.lowercaseString;
        NSString *text = nil;

        @try {
            if ([ext isEqualToString:@"pdf"]) {
                PDFDocument *doc = [[PDFDocument alloc] initWithURL:url];
                if (doc == nil) {
                    *err = maCopyString(@"cannot open PDF");
                    return NULL;
                }
                if (doc.isLocked) {
                    *err = maCopyString(@"PDF is password protected");
                    return NULL;
                }
                text = doc.string;
            } else {
                NSError *error = nil;
                NSAttributedString *contents = [[NSAttributedString alloc] initWithURL:url
                                                                                options:@{}
                                                                     documentAttributes:nil
                                                                                  error:&error];
                if (contents == nil) {
                    *err = maCopyString(maDescribe(error, @"cannot read document"));
                    return NULL;
                }
                text = contents.string;
            }
        } @catch (NSException *exception) {
            *err = maCopyString(exception.reason ?: @"cannot read document");
            return NULL;
        }

        return maCopyString(text ?: @"");
    }
}
