#!/usr/bin/env bash
# Runs nginx's own check, nginx -t, on generated files with each nginx release proxyfile writes for. The tests prove
# that a file is the one the generator writes; only nginx proves that it loads.
#
# nginx -t checks the syntax and then opens every file the configuration names. An nginx.conf this version writes names
# the njs module, which the image carries, and proxyfile.js, which render writes beside it and nginx reads from the
# configuration's folder, so the folder of the configuration is mounted and holds both. It also loads the certificate,
# key and clients CA a tls key names, and refuses a file that is missing, so every file is checked with the throwaway
# certificate, its key and a CA bundle mounted at /etc/gateway/tls, the folder examples/tls names, and the throwaway
# token files at /etc/gateway/jwt, as scripts/validate-envoy.sh mounts them. Both folders are made for this run by
# throwaway-tls.sh and removed after it, and no key is ever committed.
set -euo pipefail
# Each file named is checked, so a run that names none would check nothing and exit 0, which reads as every file
# loading.
if (($# == 0)); then
  echo "usage: validate-nginx.sh <nginx.conf>..." >&2
  exit 2
fi

source "$(dirname "$0")/pinned-images.sh"
pinned_images nginx nginx

files=$(mktemp -d)
trap 'rm -rf "$files"' EXIT
"$(dirname "$0")/throwaway-tls.sh" "$files"
tls="$files/tls"
jwt="$files/jwt"
status=0
for file in "$@"; do
  dir=$(cd "$(dirname "$file")" && pwd)
  name=$(basename "$file")
  for image in "${images[@]}"; do
    # The image's entrypoint runs its start-up scripts before nginx and prints their lines; nginx alone prints only
    # its check. nginx -t passes a file it loads with a warning, such as a server name two servers on one port hold,
    # where nginx ignores the second, so the file would not do what it says: a [warn] line refuses it too.
    if out=$(docker run --rm --entrypoint nginx --volume "${dir}:/config:ro" --volume "${tls}:/etc/gateway/tls:ro" \
      --volume "${jwt}:/etc/gateway/jwt:ro" "$image" -t -c "/config/$name" 2>&1) &&
      grep -qF "configuration file /config/$name test is successful" <<<"$out" && ! grep -qF '[warn]' <<<"$out"; then
      echo "valid: $file on $image"
    else
      printf '%s\n' "$out"
      echo "refused: $file on $image"
      status=1
    fi
  done
done
exit "$status"
