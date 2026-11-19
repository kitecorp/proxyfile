package conformance

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// fileKeys is a file an entry renders, by its label, with every core key a render of it reads (writtenKeys), the
// scenarios it is held to (file.scenarios), and the main rows of the entry's target (target.mainRows).
type fileKeys struct {
	label     string
	keys      []string
	scenarios []string
	mainRows  map[string][]string
}

// runs reports whether row e of scenario s runs on f: a row of a scenario f is held to, or of any when it names none,
// whose keys f writes (runsOn), and that is a main row of each of its keys the target holds to main rows alone (held).
func (f fileKeys) runs(s scenario, e exchange) bool {
	return (f.scenarios == nil || slices.Contains(f.scenarios, s.name)) && runsOn(e, f.keys) && held(f.mainRows, e)
}

// held reports whether a target with mainRows runs e: for each key of e that mainRows names, e is one of its main rows.
// A target that writes a key with its own feature is held to the key's main behaviour, not to every answer the Envoy
// output gives with it, so a row that holds such a key and is not a main row of it runs on none of the target's files,
// and rowCoverage does not ask the target for it.
func held(mainRows map[string][]string, e exchange) bool {
	for _, key := range e.keys {
		if rows, ok := mainRows[key]; ok && !slices.Contains(rows, e.name) {
			return false
		}
	}
	return true
}

// rendered returns, by each entry's label, the files of the entry that render.Read accepts for its target, in the
// entry's order. A file render.Read refuses runs no row, so it is left out.
func rendered(ts []target) (map[string][]fileKeys, error) {
	byEntry := map[string][]fileKeys{}
	for _, tg := range ts {
		for _, f := range tg.files {
			if _, problems := render.Read(render.Request{Proxyfile: f.proxyfile, Target: tg.name}); problems != nil {
				continue
			}
			keys, problem := writtenKeys(f.proxyfile)
			if problem != "" {
				return nil, errors.New(problem)
			}
			byEntry[tg.label] = append(byEntry[tg.label], fileKeys{label: f.label, keys: keys, scenarios: f.scenarios, mainRows: tg.mainRows})
		}
	}
	return byEntry, nil
}

// filesOf returns the rendered files of every entry of the target named name. Envoy's are where every row's literals
// were measured, and another target's where its own literals were.
func filesOf(name string, ts []target, byEntry map[string][]fileKeys) []fileKeys {
	var files []fileKeys
	for _, tg := range ts {
		if tg.name == name {
			files = append(files, byEntry[tg.label]...)
		}
	}
	return files
}

// runsOnAny reports whether row e of scenario s runs on one of files.
func runsOnAny(s scenario, e exchange, files []fileKeys) bool {
	return slices.ContainsFunc(files, func(f fileKeys) bool { return f.runs(s, e) })
}

// labelled returns the files of files whose label is label.
func labelled(files []fileKeys, label string) []fileKeys {
	var found []fileKeys
	for _, f := range files {
		if f.label == label {
			found = append(found, f)
		}
	}
	return found
}

// refusesAny reports whether the target named name declares one of keys refused.
func refusesAny(name string, keys []string) bool {
	declared, _ := format.TargetNamed(name)
	return slices.ContainsFunc(keys, func(key string) bool { return declared.Keys[key].Level == format.Refused })
}

// exercised returns, sorted, every key a row exercises on files: the keys of each row that runs on one of them.
func exercised(files []fileKeys) []string {
	seen := map[string]bool{}
	for _, s := range scenarios {
		for _, e := range s.exchanges {
			if runsOnAny(s, e, files) {
				for _, key := range e.keys {
					seen[key] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// version is the entries of one target at one version, such as nginx 1.31's entry on the plain image and its entry on
// the image tagged -otel, with the files of them all that render, and the target's main rows.
type version struct {
	name, number string
	labels       []string
	files        []fileKeys
	mainRows     map[string][]string
}

// versions returns the entries of ts by target and version, in the order each version first appears, each with the
// rendered files of all its entries (byEntry). An image that loads only some files, such as nginx's -otel images, which
// carry the tracing module, runs them in an entry of its own, so a version is judged on what its entries run together;
// entries of two versions are never judged together, so no version passes on a row another version runs.
func versions(ts []target, byEntry map[string][]fileKeys) []version {
	var found []version
	for _, tg := range ts {
		if tg.version == "" {
			// An entry with no version would be judged with every other entry of its target that has none, which is a
			// mistake in the entries, so it panics, as a mistake in the scenario data does.
			panic(fmt.Sprintf("entry %q names no version, so the coverage checks cannot tell which entries to judge together", tg.label))
		}
		i := slices.IndexFunc(found, func(v version) bool { return v.name == tg.name && v.number == tg.version })
		if i < 0 {
			found = append(found, version{name: tg.name, number: tg.version, mainRows: tg.mainRows})
			i = len(found) - 1
		}
		found[i].labels = append(found[i].labels, tg.label)
		found[i].files = append(found[i].files, byEntry[tg.label]...)
	}
	return found
}

// coverage returns a line for each key a row exercises on an Envoy entry's files and on none of the files of another
// version's entries, when that target does not refuse the key: the files of a version's entries must give its proxy
// each key Envoy is checked on, or its target must say it refuses the key. Envoy's version is held to the same, so an
// Envoy entry that runs fewer files is held to the keys of the others.
func coverage(ts []target, byEntry map[string][]fileKeys) []string {
	envoy := exercised(filesOf("envoy", ts, byEntry))
	var lines []string
	for _, v := range versions(ts, byEntry) {
		keys := exercised(v.files)
		for _, key := range envoy {
			if !slices.Contains(keys, key) && !refusesAny(v.name, []string{key}) {
				lines = append(lines, fmt.Sprintf("%s %s: no row exercises it on a file of %s %s, where Envoy's do, and %s does not refuse it",
					strings.Join(v.labels, ", "), key, v.name, v.number, v.name))
			}
		}
	}
	return lines
}

// rowCoverage returns a line for each row that runs on an Envoy entry's file and on none of the files of another
// version's entries, when that target refuses none of the row's keys and holds the row (held). coverage alone would
// pass a version whose files split a row's keys between them, each key exercised by other rows, while the row itself
// runs on none of them. Envoy's version is held to the same.
func rowCoverage(ts []target, byEntry map[string][]fileKeys) []string {
	envoy := filesOf("envoy", ts, byEntry)
	var lines []string
	for _, v := range versions(ts, byEntry) {
		for _, s := range scenarios {
			for _, e := range s.exchanges {
				if held(v.mainRows, e) && runsOnAny(s, e, envoy) && !runsOnAny(s, e, v.files) && !refusesAny(v.name, e.keys) {
					lines = append(lines, fmt.Sprintf("%s: row %q of %q runs on no file of %s %s, and %s refuses none of %q",
						strings.Join(v.labels, ", "), e.name, s.name, v.name, v.number, v.name, e.keys))
				}
			}
		}
	}
	return lines
}

// rowProblems returns a line for each literal of a row that no run reads: one on a label no entry's file carries, one
// on a file the row does not run on, a target's own literal on a row that runs on none of the files of the target's
// entries, and one held on a file label that names no file of the target's entries the row runs on. It also returns a line for each target's own literal whose difference the target does not declare
// partial, a difference docs/portability.md would not show, and for each row that runs on no file of an Envoy entry,
// whose literal no proxy was measured on, and the lines of mainRowProblems.
func rowProblems(ts []target, byEntry map[string][]fileKeys) []string {
	labels := map[string]bool{}
	byLabel := map[string][]fileKeys{}
	for _, tg := range ts {
		for _, f := range tg.files {
			labels[f.label] = true
		}
		for _, f := range byEntry[tg.label] {
			byLabel[f.label] = append(byLabel[f.label], f)
		}
	}
	envoy := filesOf("envoy", ts, byEntry)
	var lines []string
	for _, s := range scenarios {
		for _, e := range s.exchanges {
			for _, label := range slices.Sorted(maps.Keys(e.on)) {
				switch {
				case !labels[label]:
					lines = append(lines, fmt.Sprintf("row %q of %q has a literal on %q, the label of no target's file", e.name, s.name, label))
				case !runsOnAny(s, e, byLabel[label]):
					lines = append(lines, fmt.Sprintf("row %q of %q has a literal on %q, a file it does not run on", e.name, s.name, label))
				}
			}
			for _, name := range slices.Sorted(maps.Keys(e.by)) {
				declared, _ := format.TargetNamed(name)
				if !runsOnAny(s, e, filesOf(name, ts, byEntry)) {
					lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal, but runs on none of the files of the %s entries",
						e.name, s.name, name, name))
				}
				for o := e.by[name]; ; o = *o.other {
					if declared.Keys[o.difference].Level != format.Partial {
						lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal for %q, which %s does not declare partial",
							e.name, s.name, name, o.difference, name))
					}
					for _, label := range o.files {
						if !runsOnAny(s, e, labelled(filesOf(name, ts, byEntry), label)) {
							lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal on %q, no file of the %s entries the row runs on",
								e.name, s.name, name, label, name))
						}
					}
					if o.other == nil {
						break
					}
					if o.files == nil {
						lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal on every file and another beside it, which no run reads",
							e.name, s.name, name))
					}
				}
			}
			if !runsOnAny(s, e, envoy) {
				lines = append(lines, fmt.Sprintf("row %q of %q runs on no file of an Envoy entry: none writes all of %q", e.name, s.name, e.keys))
			}
		}
	}
	return append(lines, mainRowProblems(ts, byEntry)...)
}

// mainRowProblems returns a line for each main row a target names (target.mainRows) that is no row of the scenarios,
// that does not hold the key it is named under, or that runs on no file of the target's entries: each would leave the
// key held by no row on that target while reading as held.
func mainRowProblems(ts []target, byEntry map[string][]fileKeys) []string {
	var lines []string
	judged := map[string]bool{}
	for _, tg := range ts {
		if judged[tg.name] {
			continue
		}
		judged[tg.name] = true
		files := filesOf(tg.name, ts, byEntry)
		for _, key := range slices.Sorted(maps.Keys(tg.mainRows)) {
			for _, name := range tg.mainRows[key] {
				s, e, found := rowNamed(name)
				switch {
				case !found:
					lines = append(lines, fmt.Sprintf("%s names %q a main row of %q, the name of no row", tg.name, name, key))
				case !slices.Contains(e.keys, key):
					lines = append(lines, fmt.Sprintf("%s names row %q of %q a main row of %q, a key it does not hold", tg.name, name, s.name, key))
				case !runsOnAny(s, e, files):
					lines = append(lines, fmt.Sprintf("%s names row %q of %q a main row of %q, but it runs on none of the files of the %s entries",
						tg.name, name, s.name, key, tg.name))
				}
			}
		}
	}
	return lines
}

// rowNamed returns the first row named name and its scenario, and false when no row has the name.
func rowNamed(name string) (scenario, exchange, bool) {
	for _, s := range scenarios {
		for _, e := range s.exchanges {
			if e.name == name {
				return s, e, true
			}
		}
	}
	return scenario{}, exchange{}, false
}
