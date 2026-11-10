#!/usr/bin/env bash
# Runs Envoy's own validation on generated files, with the Envoy release proxyfile writes for. The tests prove that a
# tree is the one written by hand; only Envoy proves that the file loads.
set -euo pipefail
image='envoyproxy/envoy:distroless-v1.40.2@sha256:09ca6f2140a61ad8df11779a4a30d52b48433d444466328494f9188c9c35eded'
status=0
for file in "$@"; do
  dir=$(cd "$(dirname "$file")" && pwd)
  name=$(basename "$file")
  if out=$(docker run --rm --volume "${dir}:/config:ro" "$image" --mode validate -c "/config/$name" 2>&1) &&
    grep -qF "configuration '/config/$name' OK" <<<"$out"; then
    echo "valid: $file"
  else
    printf '%s\n' "$out"
    echo "refused: $file"
    status=1
  fi
done
exit "$status"
