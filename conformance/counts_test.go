package conformance

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// countsPage is the page that says how many rows each entry runs, from this package's folder, and pageName its name
// from the repository's root, which a failure names.
const (
	countsPage = "../docs/conformance.md"
	pageName   = "docs/conformance.md"
)

// countedGroup is entries the page gives one count of rows for, named there by words such as "each HAProxy entry", so
// every entry of a group must run the same rows on the same files.
type countedGroup struct {
	words string
	holds func(target) bool
}

// otelImage reports whether tg runs one of nginx's images tagged -otel, the images pinnedImages reads from
// scripts/nginx-otel-images.
func otelImage(tg target) bool { return strings.Contains(tg.image, "-otel@") }

// countedGroups are the groups the page counts rows for, in its order; each entry belongs to one.
var countedGroups = []countedGroup{
	{words: "Envoy's entry", holds: func(tg target) bool { return tg.name == "envoy" }},
	{words: "each nginx entry on the plain image", holds: func(tg target) bool { return tg.name == "nginx" && !otelImage(tg) }},
	{words: "each nginx entry on the image tagged `-otel`", holds: func(tg target) bool { return tg.name == "nginx" && otelImage(tg) }},
	{words: "each HAProxy entry", holds: func(tg target) bool { return tg.name == "haproxy" }},
}

// The passage that gives the counts, read with its white space made single spaces: the rows of each group on each
// file, a clause for each group in "; ", then "That is" each group's rows and the rows in all. A count is a number of
// one or more digits, written with commas, and the passage holds no other number, so one the battery cannot read in
// these forms fails the run rather than go unchecked.
var (
	countsPassage = regexp.MustCompile("The scenarios run in this order: (.*?)\\. That is (.*?), ([0-9][0-9,]*) in all\\b")
	countOnFile   = regexp.MustCompile("([0-9][0-9,]*) (?:rows )?on `([^`]+)`")
	countNumber   = regexp.MustCompile("[0-9][0-9,]*")
	backquoted    = regexp.MustCompile("`[^`]*`")
)

// rowsOnFiles returns, by file label, how many rows run on each of files that runs one (fileKeys.runs).
func rowsOnFiles(files []fileKeys) map[string]int {
	counts := map[string]int{}
	for _, f := range files {
		for _, s := range scenarios {
			for _, e := range s.exchanges {
				if f.runs(s, e) {
					counts[f.label]++
				}
			}
		}
	}
	return counts
}

// pageCountProblems returns a line for each count of rows docs/conformance.md gives that differs from what the
// entries run, with the page's number and the counted one: the rows of each group of entries on each of its files,
// those of each group, and those in all. The counts used to be kept by hand, and went stale each time a row was added.
// It also returns a line when the page names a file a group runs no row on, when two entries of one group run
// different rows, and when an entry belongs to no group or to two, as the page gives one count for each group.
func pageCountProblems(ts []target, byEntry map[string][]fileKeys) []string {
	text, err := os.ReadFile(countsPage)
	if err != nil {
		return []string{fmt.Sprintf("reading the counts of rows: %v", err)}
	}
	passage := countsPassage.FindStringSubmatch(strings.Join(strings.Fields(string(text)), " "))
	if passage == nil {
		return []string{fmt.Sprintf("%s holds no passage from %q to %q, which gives the rows each entry runs", pageName,
			"The scenarios run in this order: ", "in all")}
	}
	breakdown, totals := passage[1], passage[2]
	var lines []string
	for _, part := range []struct {
		text string
		read int
	}{{breakdown, len(countOnFile.FindAllString(breakdown, -1))}, {totals, len(countedGroups)}} {
		if found := len(countNumber.FindAllString(backquoted.ReplaceAllString(part.text, ""), -1)); found != part.read {
			lines = append(lines, fmt.Sprintf("%s holds %d numbers in %q, and the battery reads %d of them as counts", pageName, found, part.text, part.read))
		}
	}
	for _, tg := range ts {
		groups := 0
		for _, g := range countedGroups {
			if g.holds(tg) {
				groups++
			}
		}
		if groups != 1 {
			lines = append(lines, fmt.Sprintf("entry %s belongs to %d of the groups %s counts rows for, where it must belong to one", tg.label, groups, pageName))
		}
	}
	clauses := strings.Split(breakdown, "; ")
	all := 0
	for _, g := range countedGroups {
		var first string
		var counted map[string]int
		for _, tg := range ts {
			if !g.holds(tg) {
				continue
			}
			runs := rowsOnFiles(byEntry[tg.label])
			for _, n := range runs {
				all += n
			}
			switch {
			case counted == nil:
				first, counted = tg.label, runs
			case !maps.Equal(counted, runs):
				lines = append(lines, fmt.Sprintf("%s and %s run different rows on their files, %v and %v, where %s gives one count for %s",
					first, tg.label, counted, runs, pageName, g.words))
			}
		}
		i := slices.IndexFunc(clauses, func(c string) bool { return strings.HasPrefix(strings.TrimPrefix(c, "and "), "in "+g.words+" ") })
		var clause string
		if i < 0 {
			lines = append(lines, fmt.Sprintf("%s gives no count of the rows on each file in %s", pageName, g.words))
		} else {
			clause = clauses[i]
		}
		given := map[string]bool{}
		for _, m := range countOnFile.FindAllStringSubmatch(clause, -1) {
			given[m[2]] = true
			lines = appendDiffering(lines, m[1], counted[m[2]], fmt.Sprintf("the rows on `%s` in %s", m[2], g.words))
		}
		for _, label := range slices.Sorted(maps.Keys(counted)) {
			if !given[label] {
				lines = append(lines, fmt.Sprintf("%s gives no count of the rows on `%s` in %s, and the battery counts %s", pageName, label, g.words, commas(counted[label])))
			}
		}
		total := 0
		for _, n := range counted {
			total += n
		}
		m := regexp.MustCompile("([0-9][0-9,]*) (?:rows )?in " + regexp.QuoteMeta(g.words) + "(?:,| and |$)").FindStringSubmatch(totals)
		if m == nil {
			lines = append(lines, fmt.Sprintf("%s gives no count of the rows in %s after \"That is\", and the battery counts %s", pageName, g.words, commas(total)))
			continue
		}
		lines = appendDiffering(lines, m[1], total, "the rows in "+g.words)
	}
	return appendDiffering(lines, passage[3], all, "the rows in all")
}

// appendDiffering appends a line to lines when page, a count as the page writes it, is not counted, the count of what,
// and returns lines.
func appendDiffering(lines []string, page string, counted int, what string) []string {
	if n, err := strconv.Atoi(strings.ReplaceAll(page, ",", "")); err != nil || n != counted {
		return append(lines, fmt.Sprintf("%s gives %s as %s, and the battery counts %s", pageName, page, what, commas(counted)))
	}
	return lines
}

// commas writes n as the page writes a count, with a comma between each group of three digits: 11,073.
func commas(n int) string {
	digits := strconv.Itoa(n)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}
