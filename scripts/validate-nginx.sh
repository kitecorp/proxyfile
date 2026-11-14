#!/usr/bin/env bash
# Runs nginx's own check, nginx -t, on generated files with each nginx release proxyfile writes for. The tests prove
# that a file is the one written by hand; only nginx proves that it loads.
#
# nginx -t checks the syntax and then opens every file the configuration names. An nginx.conf this version writes names
# the njs module, which the image carries, and proxyfile.js, which render writes beside it and nginx reads from the
# configuration's folder, so the folder of the configuration is mounted and holds both.
set -euo pipefail

refuse_pin() {
  echo "validate-nginx.sh: $pin must hold one nginx image a line, tagged with its version and pinned by digest (nginx:<major>.<minor>.<patch>@sha256:<digest>)" >&2
  exit 1
}

# The nginx releases proxyfile writes for, one a line, each pinned by digest; the conformance battery runs an entry for
# each of them. An empty file would otherwise check nothing and pass.
pin="$(dirname "$0")/nginx-images"
images=()
while IFS= read -r image || [[ -n $image ]]; do
  images+=("$image")
done < <(cat "$pin" 2>/dev/null)
if ((${#images[@]} == 0)); then
  refuse_pin
fi
# The battery's entries (conformance/targets_nginx_test.go) hold the same rule.
for image in "${images[@]}"; do
  [[ $image =~ ^[^[:space:]@]+:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$ ]] || refuse_pin
done

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
