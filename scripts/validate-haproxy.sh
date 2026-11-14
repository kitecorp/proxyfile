#!/usr/bin/env bash
# Runs HAProxy's own check, haproxy -c, on generated files with each HAProxy release proxyfile writes for. The tests
# prove that a file is the one written by hand; only HAProxy proves that it loads, on each version the file is written
# for, with no warning: -dW makes a warning fail the check as an error does.
#
# HAProxy's check reads the certificate, the key and the clients CA a tls key names, and refuses a missing one. So
# every file is checked with a throwaway certificate, its key and a CA bundle mounted at /etc/gateway/tls, the folder
# examples/tls names, made for this run by throwaway-tls.sh and removed after it; no key is ever committed. The file
# loads proxyfile.lua, which render writes beside it, from its own folder, so the folder of the configuration is
# mounted and holds both; the check needs no network.
set -euo pipefail

source "$(dirname "$0")/pinned-images.sh"
pinned_images haproxy HAProxy

files=$(mktemp -d)
trap 'rm -rf "$files"' EXIT
"$(dirname "$0")/throwaway-tls.sh" "$files"
tls="$files/tls"

status=0
for file in "$@"; do
  dir=$(cd "$(dirname "$file")" && pwd)
  name=$(basename "$file")
  for image in "${images[@]}"; do
    tag=${image%%@*}
    if out=$(docker run --rm --network none --volume "${dir}:/config:ro" --volume "${tls}:/etc/gateway/tls:ro" \
      "$image" haproxy -c -V -dW -f "/config/$name" 2>&1) &&
      grep -qF "Configuration file is valid" <<<"$out"; then
      echo "valid: $file on $tag"
    else
      printf '%s\n' "$out"
      echo "refused: $file on $tag"
      status=1
    fi
  done
done
exit "$status"
