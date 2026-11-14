#!/usr/bin/env bash
# Runs Envoy's own validation on generated files, with the Envoy release proxyfile writes for. The tests prove that a
# tree is the one written by hand; only Envoy proves that the file loads.
#
# Envoy's validate mode reads the files a tls key names and refuses a missing one, one that is not PEM and a key that
# does not match its certificate. So every file is validated with a throwaway certificate, its key and a CA bundle
# mounted at /etc/gateway/tls, the folder examples/tls names. It reads the token keys auth.jwt.keys names too, and
# refuses a missing file and one that is not a JSON Web Key Set with a key, and it reads the CA bundle auth.jwt.ca
# names. So every file is also validated with /etc/gateway/jwt, the folder examples/jwt and examples/full name: a key
# set holding the throwaway key's public half, and a copy of the throwaway certificate as the CA bundle. Both folders
# are made for this run by throwaway-tls.sh and removed after it, and no key is ever committed.
set -euo pipefail
# The Envoy release proxyfile writes for, pinned by digest; the conformance battery reads the same file. An empty file
# would otherwise reach docker run and read as Envoy refusing every file.
pin="$(dirname "$0")/envoy-image"
image=$(cat "$pin" 2>/dev/null) || true
if [[ $image != *@sha256:* ]]; then
  echo "validate-envoy.sh: $pin must hold the Envoy image pinned by digest (<image>@sha256:<digest>)" >&2
  exit 1
fi
files=$(mktemp -d)
trap 'rm -rf "$files"' EXIT
"$(dirname "$0")/throwaway-tls.sh" "$files"
tls="$files/tls"
jwt="$files/jwt"
status=0
for file in "$@"; do
  dir=$(cd "$(dirname "$file")" && pwd)
  name=$(basename "$file")
  if out=$(docker run --rm --volume "${dir}:/config:ro" --volume "${tls}:/etc/gateway/tls:ro" \
    --volume "${jwt}:/etc/gateway/jwt:ro" "$image" \
    --mode validate -c "/config/$name" 2>&1) &&
    grep -qF "configuration '/config/$name' OK" <<<"$out"; then
    echo "valid: $file"
  else
    printf '%s\n' "$out"
    echo "refused: $file"
    status=1
  fi
done
exit "$status"
