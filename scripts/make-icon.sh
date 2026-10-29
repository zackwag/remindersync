#!/usr/bin/env bash
# Regenerates packaging/AppIcon.icns from packaging/make-icon.swift.
#
# Usage: scripts/make-icon.sh
#
# Requires: Xcode command line tools (swift, sips, iconutil)

set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

swift "$root/packaging/make-icon.swift" "$work/icon.png"
set="$work/AppIcon.iconset"
mkdir "$set"
for px in 16 32 128 256 512; do
  sips -z "$px" "$px" "$work/icon.png" --out "$set/icon_${px}x${px}.png" >/dev/null
  sips -z $((px * 2)) $((px * 2)) "$work/icon.png" --out "$set/icon_${px}x${px}@2x.png" >/dev/null
done
iconutil -c icns "$set" -o "$root/packaging/AppIcon.icns"
cp "$work/icon.png" "$root/packaging/AppIcon.png"
echo "Wrote packaging/AppIcon.icns and packaging/AppIcon.png"
