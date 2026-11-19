# Sourced by validate-nginx.sh and validate-haproxy.sh, which each pin the releases of their proxy in one file, one
# image a line: <proxy>-images beside this script, and nginx-otel-images for nginx's images tagged -otel, which carry
# the tracing module. The battery reads the same files with the same rule (pinnedImage in
# conformance/targets_test.go), so a release added to a file is validated and run alike.

# pinned_images <proxy> <title> fills images with the lines of <proxy>-images, or stops the validator that sourced it
# with a message that names <title>'s image form. An empty file would otherwise check nothing and pass. The global
# images is emptied first, so a validator that reads two files copies it after each call.
pinned_images() {
  local proxy=$1 title=$2 image
  local pin repository tag=''
  pin="$(dirname "$0")/$proxy-images"
  # A file named <proxy>-otel-images pins the <proxy> images tagged -otel, and every other file images without it, so
  # a plain entry never runs on an image with the tracing module, nor a tracing entry on one without.
  repository=${proxy%-otel}
  if [[ $repository != "$proxy" ]]; then
    tag=-otel
  fi
  images=()
  while IFS= read -r image || [[ -n $image ]]; do
    images+=("$image")
  done < <(cat "$pin" 2>/dev/null)
  for image in "${images[@]}"; do
    [[ $image =~ ^[^[:space:]@]+:[0-9]+\.[0-9]+\.[0-9]+(-otel)?@sha256:[0-9a-f]{64}$ && ${BASH_REMATCH[1]} == "$tag" ]] || images=()
  done
  if ((${#images[@]} == 0)); then
    echo "$(basename "$0"): $pin must hold one $title image a line, tagged with its version and pinned by digest ($repository:<major>.<minor>.<patch>$tag@sha256:<digest>)" >&2
    exit 1
  fi
}
