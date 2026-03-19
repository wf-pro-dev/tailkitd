package config

import (
	"fmt"
	"sort"
	"strings"
)

// validateAllowList checks that every entry in allow is a member of valid.
// On failure it returns an error naming the section, the unknown value, and
// the full sorted list of valid values — so the user knows exactly what to fix.
//
// This is the single validation function used by every integration parser.
// All allow lists in tailkitd configs are closed enums validated here at startup.
func validateAllowList(section string, allow []string, valid map[string]bool) error {
	for _, op := range allow {
		if !valid[op] {
			return fmt.Errorf("[%s] allow: unknown value %q; valid values are: %s",
				section, op, joinKeys(valid))
		}
	}
	return nil
}

// joinKeys returns the keys of m as a sorted, comma-separated string.
// Used in validation error messages to list valid values.
func joinKeys(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
