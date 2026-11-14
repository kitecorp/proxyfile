# Sourced by validate-nginx.sh and validate-haproxy.sh, which each pin the releases of their proxy in one file, one
# image a line: <proxy>-images beside this script. The battery reads the same files with the same rule (pinnedImage in
# conformance/targets_test.go), so a release added to the file is validated and run alike.

# pinned_images <proxy> <title> fills images with the lines of <proxy>-images, or stops the validator that sourced it
# with a message that names <title>'s image form. An empty file would otherwise check nothing and pass.
pinned_images() {
  local proxy=$1 title=$2 image
  local pin
  pin="$(dirname "$0")/$proxy-images"
  images=()
  while IFS= read -r image || [[ -n $image ]]; do
    images+=("$image")
  done < <(cat "$pin" 2>/dev/null)
  for image in "${images[@]}"; do
    [[ $image =~ ^[^[:space:]@]+:[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$ ]] || images=()
  done
  if ((${#images[@]} == 0)); then
    echo "$(basename "$0"): $pin must hold one $title image a line, tagged with its version and pinned by digest ($proxy:<major>.<minor>.<patch>@sha256:<digest>)" >&2
    exit 1
  fi
}
