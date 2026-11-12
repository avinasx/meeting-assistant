#!/bin/sh
# Builds build/bin/MeetingAssistant.app.
#
# The app must run from this bundle: macOS only shows the microphone, speech
# and screen-recording prompts for a bundle whose Info.plist explains why.
#
# Signing is opt-in. By default this script does NOT run codesign, because
# some managed Macs forbid it. The Go linker already puts an ad-hoc signature
# on the executable, which is what Apple silicon needs to start it.
#
# Where you are allowed to sign, do so: macOS ties privacy permissions to the
# signature, so a properly signed bundle keeps them across rebuilds.
#
#   CODESIGN_IDENTITY=- scripts/build.sh                      # ad-hoc bundle signature
#   CODESIGN_IDENTITY="Developer ID Application: ..." scripts/build.sh
set -eu

cd "$(dirname "$0")/.."

if [ "$(uname -s)" != "Darwin" ]; then
	echo "build.sh: the app only builds on macOS" >&2
	exit 1
fi

APP="build/bin/MeetingAssistant.app"
BIN="$APP/Contents/MacOS/MeetingAssistant"
IDENTITY="${CODESIGN_IDENTITY:-}"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

# One deployment target for every C/Objective-C object and the link, so the
# binary runs on macOS 14 and later.
export MACOSX_DEPLOYMENT_TARGET=14.0
export CGO_ENABLED=1
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"

# "desktop,production" are the build tags the Wails CLI would pass.
go build -tags desktop,production -trimpath -ldflags "-s -w" -o "$BIN" .

cp build/darwin/Info.plist "$APP/Contents/Info.plist"

if [ -z "$IDENTITY" ]; then
	echo "Built $APP (not code-signed; set CODESIGN_IDENTITY to sign)"
	exit 0
fi

if [ "$IDENTITY" = "-" ]; then
	codesign --force --sign - --entitlements build/darwin/entitlements.plist "$APP"
else
	codesign --force --options runtime --timestamp --sign "$IDENTITY" \
		--entitlements build/darwin/entitlements.plist "$APP"
fi
codesign --verify --strict "$APP"

echo "Built and signed $APP"
