#!/bin/sh
# Builds the rcas command for every supported platform, and the WebAssembly (WASI) module,
# into packages/go/dist/, and writes their SHA-256 checksums.
#
#   sh packages/go/scripts/build-all.sh            version taken from version.go
#   VERSION=1.2.3 sh packages/go/scripts/build-all.sh
set -eu

cd "$(dirname "$0")/.."
version=${VERSION:-$(sed -n 's/^const Version = "\(.*\)"$/\1/p' version.go)}
if [ -z "$version" ]; then
  echo "build-all: cannot read the version from version.go; set VERSION" >&2
  exit 1
fi
out=dist
mkdir -p "$out"

artefacts=""
build() { # os, architecture, file name
  echo "building $3"
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "$out/$3" ./cmd/rcas
  artefacts="$artefacts $3"
}

build linux amd64 rcas-linux-amd64
build linux arm64 rcas-linux-arm64
build darwin amd64 rcas-darwin-amd64
build darwin arm64 rcas-darwin-arm64
build windows amd64 rcas-windows-amd64.exe
build windows arm64 rcas-windows-arm64.exe
build wasip1 wasm rcas.wasm

cd "$out"
if command -v sha256sum >/dev/null 2>&1; then
  # shellcheck disable=SC2086
  sha256sum $artefacts > SHA256SUMS
else
  # shellcheck disable=SC2086
  shasum -a 256 $artefacts > SHA256SUMS
fi
echo "rule-cascade $version: 7 artefacts and SHA256SUMS in packages/go/$out"
