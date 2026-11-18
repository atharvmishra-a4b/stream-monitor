#!/usr/bin/env bash
# Builds one self-contained file per platform into agent/dist (needs Go).
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p dist
build() { CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -ldflags "-s -w" -o "dist/$3" . && echo "built dist/$3"; }
build windows amd64 stream-agent-windows.exe
build darwin  arm64 stream-agent-mac-arm64
build darwin  amd64 stream-agent-mac-intel
build linux   amd64 stream-agent-linux
