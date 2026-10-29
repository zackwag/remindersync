#!/usr/bin/env bash
# Builds ReminderSync.app: a universal (arm64 and x86_64) remindersync binary
# in an app bundle, signed with the hardened runtime.
#
# Signed with a real identity, the app's designated requirement is its bundle
# ID and team rather than the build's hash, so the Reminders permission
# survives upgrades. Ad-hoc signing (the default) works, but every rebuild
# asks again.
#
# Usage:
#   scripts/build-app.sh                                          # ad-hoc, version 0.0.0
#   scripts/build-app.sh -v 1.2.3 -s "Developer ID Application"   # release
#   scripts/build-app.sh -o /tmp/out                              # output directory (default dist)
#
# Requires: Go, Xcode command line tools (clang, lipo, codesign)

set -euo pipefail

version=0.0.0
identity=-
out=dist
while getopts "v:s:o:" opt; do
  case "$opt" in
    v) version="$OPTARG" ;;
    s) identity="$OPTARG" ;;
    o) out="$OPTARG" ;;
    *) exit 2 ;;
  esac
done

root="$(cd "$(dirname "$0")/.." && pwd)"
app="$out/ReminderSync.app"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=14.0
for arch in arm64 amd64; do
  clang_arch="$arch"
  if [ "$arch" = amd64 ]; then
    clang_arch=x86_64
  fi
  (cd "$root" && GOARCH="$arch" CC="clang -arch $clang_arch" \
    go build -trimpath -ldflags "-s -w" -o "$work/remindersync-$arch" ./cmd/remindersync)
done

rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
lipo -create -output "$app/Contents/MacOS/remindersync" "$work/remindersync-arm64" "$work/remindersync-amd64"
sed "s/__VERSION__/$version/g" "$root/packaging/Info.plist" > "$app/Contents/Info.plist"

sign=(--force --options runtime --entitlements "$root/packaging/remindersync.entitlements" --sign "$identity")
if [ "$identity" != - ]; then
  # Notarization needs a secure timestamp, which only a real identity gets.
  sign+=(--timestamp)
fi
codesign "${sign[@]}" "$app"
codesign --verify --strict --verbose=2 "$app"
lipo -info "$app/Contents/MacOS/remindersync"
codesign -dr - "$app"
