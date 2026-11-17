#!/usr/bin/env bash
# Checks the site scripts/build-site.sh built into the folder named, before it is published. It fails when a page has
# no built page (docs/<name>.md is built as docs/<name>.html and the README as index.html, the first page), when a
# built page's credit is not the README's "Made by" section, when a page's Mermaid diagram is not drawn, when a page
# lacks its description, canonical address or Open Graph names, when a page runs a script the theme did not write or
# asks another host for something its own Markdown does not name, or when a link of a built page leads nowhere: a
# link that stays on the site must lead to a file the build wrote, and with a fragment to an id that file holds, and
# a link that leaves the site must be a well-formed http, https or mailto address. Links that leave are not fetched,
# so the check needs no network and another site being down never fails it.
set -euo pipefail
if (($# != 1)) || [[ ! -d $1 ]]; then
  echo "usage: check-site.sh <folder of the built site>" >&2
  exit 2
fi
site=$(cd "$1" && pwd)
root=$(cd "$(dirname "$0")/.." && pwd)
status=0
fail() {
  printf '%s\n' "$*"
  status=1
}

listed=0
while IFS= read -r page; do
  listed=$((listed + 1))
  [[ -f $site/${page%.md}.html ]] || fail "missing page: $page has no ${page%.md}.html"
done < <(git -C "$root" ls-files 'docs/*.md')
# With no page listed the loop checks nothing, which would read as every page being built.
((listed > 0)) || fail "missing page: git lists no page under docs/"
[[ -f $site/index.html ]] || fail "missing page: README.md has no index.html"

pages=()
while IFS= read -r page; do
  pages+=("$page")
done < <(cd "$site" && find . -type f -name '*.html' | sed 's|^\./||' | LC_ALL=C sort)
# The first page holds the README's own "Made by" section and the footer of every other page renders it, so the
# credit is the paragraph under that heading on the first page: it links each address the README's section links, and
# every other page's footer is that paragraph byte for byte (the page reader below counts it).
credit_links=()
while IFS= read -r link; do
  credit_links+=("$link")
done < <(sed -n '/^## Made by$/,/^## /p' "$root/README.md" | grep -o -E '\]\(https?://[^)]+\)' | sed -E 's/^\]\(//; s/\)$//')
((${#credit_links[@]} > 0)) || fail "no credit: README.md has no ## Made by section that links an address"
section=$(awk '/^<h2 id="made-by">/ { on = 1; next } on && /^<(h[1-6]|\/main)[[:space:]>]/ { exit } on { print } on && /<\/p>$/ { exit }' "$site/index.html" 2>/dev/null) || true
paragraph=""
if [[ $section == "<p>"*"</p>" ]]; then
  paragraph=${section#<p>}
  paragraph=${paragraph%</p>}
else
  fail "no credit: index.html holds no paragraph under its Made by heading"
fi
for link in ${credit_links[@]+"${credit_links[@]}"}; do
  [[ -z $paragraph || $paragraph == *"href=\"$link\""* ]] || fail "no credit: the Made by paragraph of index.html does not link $link"
done

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$site" && find . -type f | sed 's|^\./||') >"$work/files"
: >"$work/ids"
: >"$work/links"
# An empty list would expand to nothing under bash 3.2's set -u, and grep given no file reads its standard input.
if ((${#pages[@]} > 0)); then
  # A page whose source holds Mermaid blocks shows each as a diagram: as many containers as blocks, and the theme's
  # starter, which asks for the drawing script the site serves. A block is counted wherever its fence starts, in a
  # list or a quote too, with backticks or tildes, as GitHub draws it there. Hugo writes a block no hook draws as code
  # of the language mermaid, so no page may hold that, and a page with no diagram does not load the starter.
  fence='^[ >]*(```+|~~~+)[[:space:]]*mermaid([[:space:]]|$)'
  drawing=$(git -C "$root" grep -l -E "$fence" -- README.md 'docs/*.md' | sed -E 's/^README\.md$/index.md/; s/\.md$/.html/') || true
  for page in "${pages[@]}"; do
    blocks=0
    if grep -q -F -x "$page" <<<"$drawing"; then
      source=${page%.html}.md
      [[ $source == index.md ]] && source=README.md
      blocks=$(grep -c -E "$fence" "$root/$source")
    fi
    drawn=$(grep -o -F '<pre class="mermaid">' "$site/$page" | wc -l | tr -d ' ') || true
    ((drawn == blocks)) || fail "diagram not drawn: $page holds $drawn diagram containers for $blocks mermaid blocks"
    loads=$(grep -c -E '<script src="[^"]*mermaid/draw\.js"' "$site/$page") || true
    if ((blocks > 0 && loads == 0)); then fail "diagram not drawn: $page does not load mermaid/draw.js"; fi
    if ((blocks > 0)) && [[ ! -f $site/mermaid/mermaid.min.js ]]; then fail "diagram not drawn: no mermaid/mermaid.min.js for $page"; fi
    if ((blocks == 0 && loads > 0)); then fail "diagram script: $page loads it and holds no diagram"; fi
    if grep -q -F 'class="language-mermaid"' "$site/$page"; then fail "diagram not drawn: $page shows a diagram's source as code"; fi
  done
  # Search engines and link previews: every page names one canonical address, its own (the site's address from
  # site/hugo.toml with the page's path under it, the first page as the address alone, so it has one address), a
  # description and the Open Graph title, description, type and address; the sitemap lists each canonical address
  # once, and robots.txt names the sitemap.
  base=$(sed -n "s/^baseURL = '\(.*\)'\$/\1/p" "$root/site/hugo.toml")
  [[ $base == https://*/ ]] || fail "page head: site/hugo.toml has no line baseURL = 'https://<host>/<path>/'"
  : >"$work/canonical"
  for page in "${pages[@]}"; do
    want=$base
    [[ $page == index.html ]] || want=$base$page
    printf '%s\n' "$want" >>"$work/canonical"
    named=$(grep -o -E '<link rel="canonical" href="[^"]*">' "$site/$page" | sed -E 's/.*href="([^"]*)">/\1/' | tr '\n' ' ') || true
    [[ $named == "$want " ]] || fail "page head: $page names the canonical address(es) '$named', want one, $want"
    grep -q -F "<meta property=\"og:url\" content=\"$want\">" "$site/$page" || fail "page head: $page has no og:url $want"
    for meta in 'name="description"' 'property="og:title"' 'property="og:description"' 'property="og:type"'; do
      grep -q -E "<meta $meta content=\"[^\"]*[^\"[:space:]][^\"]*\">" "$site/$page" || fail "page head: $page has no $meta with text"
    done
  done
  # A page of four sections and subsections or more starts with the list of them, under its title.
  for page in "${pages[@]}"; do
    sections=$(grep -o -E '<h[23] id="' "$site/$page" | wc -l | tr -d ' ') || true
    ((sections < 4)) || awk 'last ~ /<\/h1>$/ && $0 == "<nav id=\"TableOfContents\">" { found = 1 } { last = $0 } END { exit !found }' "$site/$page" ||
      fail "sections: $page has $sections sections and does not start with the list of them"
  done
  if [[ -f $site/sitemap.xml ]]; then
    grep -o -E '<loc>[^<]*</loc>' "$site/sitemap.xml" | sed -E 's|</?loc>||g' | LC_ALL=C sort >"$work/listed" || true
    LC_ALL=C sort "$work/canonical" >"$work/wanted"
    while IFS= read -r address; do fail "sitemap: sitemap.xml lists $address more than once"; done < <(uniq -d "$work/listed")
    while IFS= read -r address; do fail "sitemap: sitemap.xml does not list $address"; done < <(LC_ALL=C sort -u "$work/listed" | comm -13 - "$work/wanted")
    while IFS= read -r address; do fail "sitemap: sitemap.xml lists $address, which is no page's address"; done < <(LC_ALL=C sort -u "$work/listed" | comm -23 - "$work/wanted")
  else
    fail "sitemap: no sitemap.xml"
  fi
  grep -q -F -x "Sitemap: ${base}sitemap.xml" "$site/robots.txt" 2>/dev/null || fail "robots: robots.txt does not name ${base}sitemap.xml"
  # Every tag of every page, read as a browser reads it: outside comments and outside the text of a script or style
  # element, with each attribute in double quotes, single quotes or none and its name in any case. The reader writes
  # each id and each link to the files the next program reads, marking as a load what the browser fetches by itself
  # (src, srcset, poster, and the href of a stylesheet or of an SVG image). It refuses a script the theme did not
  # write (a script element or an on... attribute), an element that brings in another page or program and a base
  # element, which moves every relative link. Outside comments, every page but the first holds the footer that is the
  # Made by paragraph, and no page says that paragraph twice.
  if ! (cd "$site" && LC_ALL=C CREDIT=$paragraph IDS=$work/ids LINKS=$work/links awk '
    # The tag that starts s, the text after a "<": its name in tname, its attributes in aname and avalue (na of them),
    # and the position of the ">" that ends it, or 0 when s ends first, as it does at a "<" inside a quoted value.
    function tag(s,    n, p, c, rest, q, e) {
      n = length(s)
      match(s, /^[A-Za-z][A-Za-z0-9:-]*/)
      tname = tolower(substr(s, 1, RLENGTH))
      p = RLENGTH + 1
      na = 0
      while (p <= n) {
        c = substr(s, p, 1)
        if (c == ">") return p
        if (c ~ /[[:space:]]/ || c == "/") { p++; continue }
        rest = substr(s, p)
        match(rest, /^[^[:space:]\/>=]+/)
        if (RLENGTH < 1) RLENGTH = 1
        aname[++na] = tolower(substr(rest, 1, RLENGTH))
        avalue[na] = ""
        p += RLENGTH
        while (p <= n && substr(s, p, 1) ~ /[[:space:]]/) p++
        if (substr(s, p, 1) != "=") continue
        p++
        while (p <= n && substr(s, p, 1) ~ /[[:space:]]/) p++
        q = substr(s, p, 1)
        if (q == "\"" || q == "\047") {
          e = index(substr(s, p + 1), q)
          if (e == 0) return 0
          avalue[na] = substr(s, p + 1, e - 1)
          p += e + 1
        } else {
          rest = substr(s, p)
          match(rest, /^[^[:space:]>]*/)
          avalue[na] = substr(rest, 1, RLENGTH)
          p += RLENGTH
        }
      }
      return 0
    }
    function count(s, t,    n, at) {
      n = 0
      while ((at = index(s, t)) > 0) { n++; s = substr(s, at + length(t)) }
      return n
    }
    function shown(s) {
      gsub(/[[:space:]]+/, " ", s)
      return "<" (length(s) > 80 ? substr(s, 1, 77) "..." : s) ">"
    }
    function refuse(problem) {
      print problem
      status = 1
    }
    function scan(    up, theme, n, parts, i, s, stop, k, text, raw, rawtext, rel, kind, value, said) {
      up = page
      gsub(/[^\/]/, "", up)
      gsub(/\//, "../", up)
      theme = "script src=\"" up "mermaid/draw.js\" defer"
      n = split(doc, parts, /</)
      text = parts[1]
      rawtext = ""
      for (i = 2; i <= n; i++) {
        s = parts[i]
        # The text of a script or style element is not markup: it runs to the element end tag.
        if (rawtext != "" && tolower(substr(s, 1, length(rawtext) + 1)) != "/" rawtext) { text = text "<" s; continue }
        rawtext = ""
        if (substr(s, 1, 3) == "!--") {
          s = substr(s, 4)
          while (index(s, "-->") == 0 && i < n) s = s "<" parts[++i]
          if ((stop = index(s, "-->")) == 0) break
          text = text substr(s, stop + 3)
          continue
        }
        if (s !~ /^[A-Za-z]/) { text = text "<" s; continue }
        while ((stop = tag(s)) == 0 && i < n) s = s "<" parts[++i]
        text = text "<" s
        if (stop == 0) continue
        raw = substr(s, 1, stop - 1)
        if (tname == "script") {
          if (raw != theme || stop != length(s) || tolower(substr(parts[i + 1], 1, 8)) != "/script>")
            refuse("script: " page " holds " shown(raw) ", which the theme did not write")
          rawtext = "script"
        }
        if (tname == "style") rawtext = "style"
        if (tname ~ /^(iframe|frame|object|embed|applet)$/) refuse("embed: " page " holds " shown(raw) ", which brings in another page or program")
        if (tname == "base") refuse("base: " page " holds " shown(raw) ", which moves every relative link")
        rel = ""
        for (k = 1; k <= na; k++) if (aname[k] == "rel") rel = tolower(avalue[k])
        for (k = 1; k <= na; k++) {
          # The browser drops tabs and line ends from an address.
          value = avalue[k]
          gsub(/[\t\n\r]/, "", value)
          if (aname[k] ~ /^on/) refuse("script: " page " holds " shown(raw) ", whose " aname[k] " attribute is a script")
          else if (aname[k] == "id") print page "\t" value > ids
          else if (aname[k] ~ /^(href|src|srcset|poster|xlink:href)$/) {
            kind = "loads"
            if (aname[k] == "href" && tname !~ /^(image|use|feimage)$/ && !(tname == "link" && rel != "canonical")) kind = "goes"
            print page "\t" aname[k] "\t" value "\t" kind > links
          }
        }
      }
      if (credit == "") return
      said = count(text, credit)
      if (page == "index.html" && said == 0) refuse("no credit: index.html says the Made by paragraph only inside a comment")
      if (page != "index.html" && count(text, "<footer>" credit "</footer>") == 0)
        refuse("no credit: " page " has no footer that is the Made by paragraph of index.html")
      if (said > 1) refuse("credit twice: " page " says it " said " times")
    }
    BEGIN { credit = ENVIRON["CREDIT"]; ids = ENVIRON["IDS"]; links = ENVIRON["LINKS"] }
    FNR == 1 { if (page != "") scan(); page = FILENAME; doc = "" }
    { doc = doc $0 "\n" }
    END { if (page != "") scan(); exit status }
  ' "${pages[@]}"); then
    status=1
  fi
fi

# Every file the build wrote, then every id and every link the page reader found, read by one awk program.
LC_ALL=C ROOT=$root awk -F '\t' -v status="$status" '
  # The value of an attribute as the browser reads it, for the entities the generator writes in one.
  function unescape(s) {
    gsub(/&#34;|&quot;/, "\"", s); gsub(/&#39;/, "\047", s); gsub(/&lt;/, "<", s); gsub(/&gt;/, ">", s)
    gsub(/&amp;/, "\\&", s)
    return s
  }
  # A fragment percent-decoded, as the browser compares it with an id.
  function decode(s,    out, i, c) {
    out = ""
    for (i = 1; i <= length(s); i++) {
      c = substr(s, i, 1)
      if (c == "%" && i + 2 <= length(s) && (substr(s, i + 1, 2) in hex)) { out = out hex[substr(s, i + 1, 2)]; i += 2 }
      else out = out c
    }
    return out
  }
  # The path of a relative link from the folder of a page, with . and .. segments resolved, or "" when it climbs out
  # of the site.
  function resolve(page, path,    n, parts, i, depth, stack, out) {
    sub(/[^\/]*$/, "", page)
    n = split(page path, parts, "/")
    depth = 0
    for (i = 1; i <= n; i++) {
      if (parts[i] == "..") { if (depth == 0) return ""; depth-- }
      else if (parts[i] != "." && parts[i] != "") stack[++depth] = parts[i]
    }
    out = ""
    for (i = 1; i <= depth; i++) out = out (i > 1 ? "/" : "") stack[i]
    if (out == "" || path ~ /\/$/ || (out in folder)) out = out (out == "" ? "" : "/") "index.html"
    return out
  }
  # The Markdown a page is built from: the README for the first page, docs/<name>.md for docs/<name>.html.
  function source(page) {
    if (page == "index.html") return "README.md"
    sub(/\.html$/, ".md", page)
    return page
  }
  # Whether the Markdown of a page holds the address, as written or with each & written &amp; as HTML writes it.
  function named(page, address,    file, line, text, amp) {
    file = source(page)
    if (!(file in markdown)) {
      text = ""
      while ((getline line < (ENVIRON["ROOT"] "/" file)) > 0) text = text line "\n"
      close(ENVIRON["ROOT"] "/" file)
      markdown[file] = text
    }
    amp = address
    gsub(/&/, "\\&amp;", amp)
    return index(markdown[file], address) > 0 || index(markdown[file], amp) > 0
  }
  function check(page, link, kind,    target, fragment, at) {
    if (link ~ /^[A-Za-z][A-Za-z0-9+.-]*:/ || link ~ /^\/\//) {
      leaving++
      # Another host may answer anything, so the browser fetches from it only what the page itself names.
      if (kind == "loads" && !named(page, link)) {
        printf "outside address: %s: %s is fetched from another host, and %s does not name it\n", page, link, source(page)
        status = 1
        return
      }
      if (link ~ /^\/\//) link = "https:" link
      if (link ~ /^https?:\/\/[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*(:[0-9]+)?([\/?#][^[:space:]<>"]*)?$/) return
      if (link ~ /^mailto:[^[:space:]@<>"]+@[^[:space:]@<>"]+$/) return
      fault(page, link, "is not a well-formed http, https or mailto address")
      return
    }
    staying++
    if (link ~ /^\//) { fault(page, link, "starts at the root of the host, so it works only where the site is served at /"); return }
    at = index(link, "#")
    fragment = at ? substr(link, at + 1) : ""
    target = at ? substr(link, 1, at - 1) : link
    sub(/\?.*/, "", target)
    if (target ~ /(^|\/)index\.html$/) { fault(page, link, "names index.html; a link to the folder gives the page one address"); return }
    if (target == "") target = page
    else if ((target = resolve(page, target)) == "") { fault(page, link, "climbs out of the site"); return }
    if (!(target in built)) { fault(page, link, "leads to no built file"); return }
    if (fragment != "" && !((target "\t" decode(fragment)) in id)) fault(page, link, "names no id of " target)
  }
  function fault(page, link, problem) {
    printf "broken link: %s: %s %s\n", page, link, problem
    status = 1
  }
  BEGIN {
    split("0 1 2 3 4 5 6 7 8 9 A B C D E F", digits, " ")
    for (i = 1; i <= 16; i++) for (j = 1; j <= 16; j++) {
      c = sprintf("%c", (i - 1) * 16 + j - 1)
      hex[digits[i] digits[j]] = c; hex[tolower(digits[i] digits[j])] = c
    }
  }
  FILENAME ~ /files$/ {
    built[$0] = 1
    path = $0
    while (sub(/\/[^\/]*$/, "", path)) folder[path] = 1
    next
  }
  FILENAME ~ /ids$/ { id[$1 "\t" unescape($2)] = 1; next }
  {
    value = unescape($3)
    if ($2 != "srcset") { check($1, value, $4); next }
    # A srcset lists candidates separated by commas, each a URL and an optional width or density.
    n = split(value, candidates, ",")
    for (k = 1; k <= n; k++) {
      candidate = candidates[k]
      sub(/^[[:space:]]+/, "", candidate)
      sub(/[[:space:]].*$/, "", candidate)
      if (candidate != "") check($1, candidate, $4)
    }
  }
  END {
    pages = 0
    for (file in built) if (file ~ /\.html$/) pages++
    printf "checked %d built pages: %d links on the site, %d leaving it\n", pages, staying, leaving
    exit status
  }
' "$work/files" "$work/ids" "$work/links" || status=1
if ((status == 0)); then
  echo "site ok: $site"
else
  echo "site refused: $site"
fi
exit "$status"
