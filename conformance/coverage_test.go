package conformance

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/kitecorp/proxyfile/v2/internal/format"
	"github.com/kitecorp/proxyfile/v2/internal/render"
)

// fileKeys is a file an entry renders, by its label, with every core key a render of it reads (writtenKeys).
type fileKeys struct {
	label string
	keys  []string
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
			byEntry[tg.label] = append(byEntry[tg.label], fileKeys{label: f.label, keys: keys})
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

// runsOnAny reports whether e runs on one of files.
func runsOnAny(e exchange, files []fileKeys) bool {
	return slices.ContainsFunc(files, func(f fileKeys) bool { return runsOn(e, f.keys) })
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
			if runsOnAny(e, files) {
				for _, key := range e.keys {
					seen[key] = true
				}
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// coverage returns a line for each key a row exercises on an Envoy entry's files and on none of another entry's,
// when that entry's target does not refuse the key: the entry's files must give its proxy each key Envoy is checked
// on, or its target must say it refuses the key. An Envoy entry is held to the same, so an Envoy entry that runs fewer
// files is held to the keys of the others.
func coverage(ts []target, byEntry map[string][]fileKeys) []string {
	envoy := exercised(filesOf("envoy", ts, byEntry))
	var lines []string
	for _, tg := range ts {
		keys := exercised(byEntry[tg.label])
		for _, key := range envoy {
			if !slices.Contains(keys, key) && !refusesAny(tg.name, []string{key}) {
				lines = append(lines, fmt.Sprintf("%s %s: no row exercises it, where Envoy's do, and its target does not refuse it", tg.label, key))
			}
		}
	}
	return lines
}

// rowCoverage returns a line for each row that runs on an Envoy entry's file and on none of another entry's files,
// when that entry's target refuses none of the row's keys. coverage alone would pass an entry whose files split a
// row's keys between them, each key exercised by other rows, while the row itself runs on none of them. An Envoy entry
// is held to the same.
func rowCoverage(ts []target, byEntry map[string][]fileKeys) []string {
	envoy := filesOf("envoy", ts, byEntry)
	var lines []string
	for _, tg := range ts {
		for _, s := range scenarios {
			for _, e := range s.exchanges {
				if runsOnAny(e, envoy) && !runsOnAny(e, byEntry[tg.label]) && !refusesAny(tg.name, e.keys) {
					lines = append(lines, fmt.Sprintf("%s: row %q of %q runs on none of its files, and its target refuses none of %q",
						tg.label, e.name, s.name, e.keys))
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
// whose literal no proxy was measured on.
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
				case !runsOnAny(e, byLabel[label]):
					lines = append(lines, fmt.Sprintf("row %q of %q has a literal on %q, a file it does not run on", e.name, s.name, label))
				}
			}
			for _, name := range slices.Sorted(maps.Keys(e.by)) {
				declared, _ := format.TargetNamed(name)
				if difference := e.by[name].difference; declared.Keys[difference].Level != format.Partial {
					lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal for %q, which %s does not declare partial",
						e.name, s.name, name, difference, name))
				}
				if !runsOnAny(e, filesOf(name, ts, byEntry)) {
					lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal, but runs on none of the files of the %s entries",
						e.name, s.name, name, name))
				}
				for _, label := range e.by[name].files {
					if !runsOnAny(e, labelled(filesOf(name, ts, byEntry), label)) {
						lines = append(lines, fmt.Sprintf("row %q of %q has %s's own literal on %q, no file of the %s entries the row runs on",
							e.name, s.name, name, label, name))
					}
				}
			}
			if !runsOnAny(e, envoy) {
				lines = append(lines, fmt.Sprintf("row %q of %q runs on no file of an Envoy entry: none writes all of %q", e.name, s.name, e.keys))
			}
		}
	}
	return lines
}
