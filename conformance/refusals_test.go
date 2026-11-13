package conformance

import "github.com/kitecorp/proxyfile/v2/internal/format"

// refusedKeys returns the keys of s that declared refuses, in the scenario's key order, or nil when it refuses none.
func refusedKeys(declared format.Target, s scenario) []string {
	var keys []string
	for _, key := range s.keys {
		if declared.Keys[key].Level == format.Refused {
			keys = append(keys, key)
		}
	}
	return keys
}

// expectedRefusals returns the refusal render must print for each key of s that declared refuses, in the scenario's
// key order, or nil when it refuses none of them. The full example writes every key a scenario names, so a target
// that refuses one cannot render it, and the scenario's check is then that the user is told why.
func expectedRefusals(declared format.Target, s scenario) []string {
	var refusals []string
	for _, key := range refusedKeys(declared, s) {
		refusals = append(refusals, format.RefusedMessage(key, declared))
	}
	return refusals
}
