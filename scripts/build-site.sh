#!/usr/bin/env bash
# Builds the documentation site into the folder named, from the README and the pages under docs/ (site/hugo.toml says
# how), with the Hugo release pinned in hugo-image beside this script. The repository is mounted read-only, so a build
# changes nothing in it. Links to files the site does not publish lead to them on GitHub at the commit checked out.
# scripts/check-site.sh checks what it built.
set -euo pipefail
if (($# != 1)); then
  echo "usage: build-site.sh <output folder>" >&2
  exit 2
fi
# An empty file would otherwise reach docker run and read as Hugo failing to build.
pin="$(dirname "$0")/hugo-image"
image=$(cat "$pin" 2>/dev/null) || true
if [[ ! $image =~ ^[^[:space:]@]+:v[0-9]+\.[0-9]+\.[0-9]+@sha256:[0-9a-f]{64}$ ]]; then
  echo "build-site.sh: $pin must hold the Hugo image tagged with its version and pinned by digest (<image>:v<major>.<minor>.<patch>@sha256:<digest>)" >&2
  exit 1
fi
root=$(cd "$(dirname "$0")/.." && pwd)
# Hugo reads {{< and {{% as the start of its own shortcode anywhere in a page, in a code block too, and the first text
# of a page as front matter when it starts with -, +, { or #+: it refuses a line that is not its delimiter and drops
# the text up to the closing one. GitHub shows each as written. The pages are built as they are, so a page that holds
# one is refused here, before Hugo runs, with the line that holds it.
found=$(cd "$root" && {
  grep -n -H -F -e '{{<' -e '{{%' README.md docs/*.md | sed 's/^/shortcode:/' || true
  awk 'FNR == 1 { looking = 1 } looking && /[^[:space:]]/ { looking = 0; if (/^[[:space:]]*([-+{]|#\+)/) print "front matter:" FILENAME ":" FNR ":" $0 }' README.md docs/*.md
}) || true
if [[ -n $found ]]; then
  while IFS= read -r line; do
    kind=${line%%:*} rest=${line#*:}
    file=${rest%%:*} rest=${rest#*:}
    if [[ $kind == shortcode ]]; then
      reading="the start of a shortcode in {{< or {{%, even in a code block"
    else
      reading="the start of the page as front matter"
    fi
    echo "build-site.sh: $file line ${rest%%:*} holds \"${rest#*:}\": Hugo would read $reading, where GitHub shows the text as written" >&2
  done <<<"$found"
  exit 1
fi
# The folder must hold this build alone, so no file of an earlier build is checked or published with it, and it is never
# emptied here, so naming the wrong folder cannot delete anything.
mkdir -p "$1"
if [[ -n $(ls -A "$1") ]]; then
  echo "build-site.sh: $1 is not empty; name an empty or new folder" >&2
  exit 1
fi
out=$(cd "$1" && pwd)
commit=$(git -C "$root" rev-parse HEAD)
# Hugo runs as the caller's user, so the files it writes belong to the caller on a Linux host too, with its cache in the
# container, which that user can write. The image's entrypoint is skipped: it runs npm install when the folder holds a
# package.json. A warning, such as a page Hugo found no template for, fails the build.
docker run --rm --user "$(id -u):$(id -g)" --entrypoint hugo --workdir /project \
  --volume "${root}:/project:ro" --volume "${out}:/site" \
  --env HUGO_PARAMS_COMMIT="$commit" --env HUGO_CACHEDIR=/tmp/hugo-cache \
  "$image" build --config site/hugo.toml --destination /site --panicOnWarning
