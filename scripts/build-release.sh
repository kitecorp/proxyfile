#!/usr/bin/env bash
# Builds the release files into dist/: one program per platform, the schema, and their checksums.
set -euo pipefail
version=${1:?usage: scripts/build-release.sh <tag, such as v1.0.0>}
root=$(cd "$(dirname "$0")/.." && pwd)
rm -rf "$root/dist"
mkdir -p "$root/dist"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
  os=${target%/*}
  arch=${target#*/}
  # CGO off, so the Linux programs need no C library and the image can hold the program alone.
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go -C "$root" build -trimpath -ldflags "-s -w -X main.version=$version" \
    -o "dist/proxyfile-$os-$arch" ./cmd/proxyfile
done
cp "$root/schema/proxyfile.schema.json" "$root/dist/"
(cd "$root/dist" && shasum -a 256 proxyfile-darwin-amd64 proxyfile-darwin-arm64 proxyfile-linux-amd64 \
  proxyfile-linux-arm64 proxyfile.schema.json > checksums.txt)
