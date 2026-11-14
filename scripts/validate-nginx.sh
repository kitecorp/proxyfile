#!/usr/bin/env bash
# Runs nginx's own check, nginx -t, on generated files with each nginx release proxyfile writes for. The tests prove
# that a file is the one written by hand; only nginx proves that it loads.
#
# nginx -t checks the syntax and then opens every file the configuration names. An nginx.conf this version writes names
# the njs module, which the image carries, and proxyfile.js, which render writes beside it and nginx reads from the
# configuration's folder, so the folder of the configuration is mounted and holds both.
set -euo pipefail

source "$(dirname "$0")/pinned-images.sh"
pinned_images nginx nginx

status=0
for file in "$@"; do
  dir=$(cd "$(dirname "$file")" && pwd)
  name=$(basename "$file")
  for image in "${images[@]}"; do
    # The image's entrypoint runs its start-up scripts before nginx and prints their lines; nginx alone prints only
    # its check.
    if out=$(docker run --rm --entrypoint nginx --volume "${dir}:/config:ro" "$image" -t -c "/config/$name" 2>&1) &&
      grep -qF "configuration file /config/$name test is successful" <<<"$out"; then
      echo "valid: $file on $image"
    else
      printf '%s\n' "$out"
      echo "refused: $file on $image"
      status=1
    fi
  done
done
exit "$status"
