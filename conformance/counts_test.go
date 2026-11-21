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
// those of each group, and those in all, the rows of each scenario on each file of Envoy's entry
// (scenarioCountProblems), and the rows sent as their scenario starts (overlapCountProblems). The counts used to be
// kept by hand, and went stale each time a row was added.
// It also returns a line when the page names a file a group runs no row on, when two entries of one group run
// different rows, and when an entry belongs to no group or to two, as the page gives one count for each group.
func pageCountProblems(ts []target, byEntry map[string][]fileKeys) []string {
	text, err := os.ReadFile(countsPage)
	if err != nil {
		return []string{fmt.Sprintf("reading the counts of rows: %v", err)}
	}
	var envoy []fileKeys
	if i := slices.IndexFunc(ts, func(tg target) bool { return tg.name == "envoy" }); i >= 0 {
		envoy = byEntry[ts[i].label]
	}
	scenarioLines := slices.Concat(scenarioCountProblems(string(text), envoy), overlapCountProblems(string(text)))
	passage := countsPassage.FindStringSubmatch(strings.Join(strings.Fields(string(text)), " "))
	if passage == nil {
		return append([]string{fmt.Sprintf("%s holds no passage from %q to %q, which gives the rows each entry runs", pageName,
			"The scenarios run in this order: ", "in all")}, scenarioLines...)
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
	return append(appendDiffering(lines, passage[3], all, "the rows in all"), scenarioLines...)
}

// The table of the scenarios starts with its header and runs to the first line that is no row of it. Each row names a
// scenario in its first cell and ends its last with the rows the scenario runs on each file of Envoy's entry, the one
// entry that runs every file: "In Envoy's entry: " and a count on each file, such as "150 rows on `full`, `portable`
// and `nginx-file`" or "3 on `nginx-limits`", which may say after a count why a file runs fewer rows but holds no other
// number, so a count the battery cannot read in this form fails the run rather than go unchecked.
var (
	scenarioTableHead = "| Scenario | Keys | What its rows check |\n|---|---|---|\n"
	scenarioCounts    = regexp.MustCompile("In Envoy's entry: (.*)\\.$")
	countOnFiles      = regexp.MustCompile("([0-9][0-9,]*) (?:rows? )?on (`[^`]+`(?:(?:, | and )`[^`]+`)*)")
)

// scenarioCountProblems returns a line for each count of rows the table of the scenarios in text gives that differs
// from what the scenario runs on that file of Envoy's entry, whose rendered files are envoy, with the page's number and
// the counted one. It also returns a line for a scenario with no row in the table or with two, a row that names no
// scenario or holds other than three cells, a last cell that does not end with the rows in Envoy's entry or holds a number it does
// not read as a count, a file a count names twice or that the scenario runs no row on, and a file the scenario runs
// rows on that no count names.
func scenarioCountProblems(text string, envoy []fileKeys) []string {
	start := strings.Index(text, scenarioTableHead)
	if start < 0 {
		return []string{fmt.Sprintf("%s holds no table of the scenarios, a header %q", pageName, strings.SplitN(scenarioTableHead, "\n", 2)[0])}
	}
	byName := map[string][]string{}
	var lines []string
	for _, row := range strings.Split(text[start+len(scenarioTableHead):], "\n") {
		if !strings.HasPrefix(row, "|") {
			break
		}
		cell := cells(row)
		switch {
		case len(cell) != 3:
			lines = append(lines, fmt.Sprintf("the row %q of the table of the scenarios in %s holds %d cells, where it must hold three: a | inside a cell is written \\|",
				cell[0], pageName, len(cell)))
		case byName[cell[0]] != nil:
			// A second row of a name would be read in place of the first, so a stale count in either goes unread.
			lines = append(lines, fmt.Sprintf("the table of the scenarios in %s has two rows of the scenario %q", pageName, cell[0]))
		default:
			byName[cell[0]] = cell
		}
	}
	for _, s := range scenarios {
		cell, ok := byName[s.name]
		if !ok {
			lines = append(lines, fmt.Sprintf("the table of the scenarios in %s has no row of the scenario %q", pageName, s.name))
			continue
		}
		delete(byName, s.name)
		counted := map[string]int{}
		for _, f := range envoy {
			for _, e := range s.exchanges {
				if f.runs(s, e) {
					counted[f.label]++
				}
			}
		}
		what := fmt.Sprintf("the rows of the scenario %q", s.name)
		clause := scenarioCounts.FindStringSubmatch(cell[2])
		if clause == nil || strings.Count(cell[2], "In Envoy's entry: ") != 1 {
			lines = append(lines, fmt.Sprintf("%s's row of the scenario %q does not end with one sentence of its rows on each file of Envoy's entry, which starts %q",
				pageName, s.name, "In Envoy's entry: "))
			continue
		}
		pairs := countOnFiles.FindAllStringSubmatch(clause[1], -1)
		if found := len(countNumber.FindAllString(backquoted.ReplaceAllString(clause[1], ""), -1)); found != len(pairs) {
			lines = append(lines, fmt.Sprintf("%s holds %d numbers in %s on each file of Envoy's entry, %q, and the battery reads %d of them as counts",
				pageName, found, what, clause[1], len(pairs)))
		}
		given := map[string]bool{}
		for _, pair := range pairs {
			for _, label := range backquoted.FindAllString(pair[2], -1) {
				label = strings.Trim(label, "`")
				switch _, runs := counted[label]; {
				case given[label]:
					lines = append(lines, fmt.Sprintf("%s names `%s` twice in %s in Envoy's entry", pageName, label, what))
				case !runs:
					lines = append(lines, fmt.Sprintf("%s gives %s as %s on `%s` in Envoy's entry, a file it runs no row on", pageName, pair[1], what, label))
				default:
					lines = appendDiffering(lines, pair[1], counted[label], fmt.Sprintf("%s on `%s` in Envoy's entry", what, label))
				}
				given[label] = true
			}
		}
		for _, label := range slices.Sorted(maps.Keys(counted)) {
			if !given[label] {
				lines = append(lines, fmt.Sprintf("%s gives no count of %s on `%s` in Envoy's entry, and the battery counts %s",
					pageName, what, label, commas(counted[label])))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		lines = append(lines, fmt.Sprintf("the table of the scenarios in %s has a row %q, which names no scenario", pageName, name))
	}
	return lines
}

// The rows sent as their scenario starts (exchange.overlap) are given in one sentence of the page, read with its white
// space made single spaces: those of every scenario but tracing, which wait for a proxy's timer or its close, and
// those of the tracing scenario, each a count in digits before these words.
const (
	overlapOthers  = "rows that wait for a proxy's timer or its close"
	overlapTracing = "rows of the tracing scenario"
)

// overlapCountProblems returns a line when text, the page, does not give each count of the rows sent as their scenario
// starts once, or gives another number than the scenarios hold: the rows of the tracing scenario and those of the
// others. They are rows of the scenarios, whatever file runs them.
func overlapCountProblems(text string) []string {
	flat := strings.Join(strings.Fields(text), " ")
	var others, traced int
	for _, s := range scenarios {
		for _, e := range s.exchanges {
			switch {
			case !e.overlap:
			case s.name == tracing.name:
				traced++
			default:
				others++
			}
		}
	}
	var lines []string
	for _, c := range []struct {
		words   string
		counted int
	}{{overlapOthers, others}, {overlapTracing, traced}} {
		found := regexp.MustCompile("([0-9][0-9,]*) "+regexp.QuoteMeta(c.words)).FindAllStringSubmatch(flat, -1)
		if len(found) != 1 {
			lines = append(lines, fmt.Sprintf("%s gives %d times a count in digits of the %s, where it must give it once, and the battery counts %s",
				pageName, len(found), c.words, commas(c.counted)))
			continue
		}
		lines = appendDiffering(lines, found[0][1], c.counted, "the "+c.words)
	}
	return lines
}

// cells returns the cells of a row of a Markdown table, split at each | no backslash escapes, as GitHub splits it: a |
// inside a code span that is not escaped ends the cell there, and GitHub drops what comes after the third cell.
func cells(row string) []string {
	var found []string
	var cell strings.Builder
	for i := 1; i < len(strings.TrimSuffix(row, "|")); i++ {
		if row[i] == '|' && row[i-1] != '\\' {
			found = append(found, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteByte(row[i])
	}
	return append(found, strings.TrimSpace(cell.String()))
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
