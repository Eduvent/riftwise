#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
case "${1:-help}" in
  help|--help|-h) echo 'Usage: bash spikes/macos-overlay/check.sh {test|build|help} (macOS arm64, Xcode SDK; never launches GUI)'; exit 0 ;;
  test|build) mode="$1" ;;
  *) echo 'Expected test, build or help' >&2; exit 2 ;;
esac
if [ "$#" -ne 1 ]; then echo 'Expected exactly one argument' >&2; exit 2; fi
mkdir -p .build/modules
compiler=/Applications/Xcode.app/Contents/Developer/Toolchains/XcodeDefault.xctoolchain/usr/bin/swiftc
sdk=/Applications/Xcode.app/Contents/Developer/Platforms/MacOSX.platform/Developer/SDKs/MacOSX.sdk
flags=(-sdk "$sdk" -target arm64-apple-macos14.0 -module-cache-path .build/modules)
if [ "$mode" = test ]; then
  "$compiler" "${flags[@]}" Policy.swift Transport.swift Tests.swift -o .build/policy-tests
  .build/policy-tests
else
  "$compiler" "${flags[@]}" Policy.swift Transport.swift Overlay.swift -o .build/overlay
  echo 'Built spikes/macos-overlay/.build/overlay (not launched)'
fi
