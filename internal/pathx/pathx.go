// Package pathx gives paths the same identity the Binaly server gives them, so the client and
// the server agree on when two spellings name the same file.
//
// The rules (they must match the server exactly; testdata/pathkeys.json pins them):
//  1. `\` becomes `/`.
//  2. Runs of `/` collapse to one, except a leading `//` (a network share).
//  3. A trailing `/` is dropped.
//  4. Drive-letter and network-share paths are lowercased; anything else keeps its case.
//
// A key is for comparison only. Paths are always sent and stored as spelled.
package pathx

import (
	"regexp"
	"strings"
)

var (
	driveRe = regexp.MustCompile(`^[A-Za-z]:`)
	slashes = regexp.MustCompile(`/{2,}`)
)

// Key returns the identity key for a path.
func Key(p string) string {
	s := strings.ReplaceAll(p, `\`, "/")
	unc := strings.HasPrefix(s, "//")
	s = slashes.ReplaceAllString(s, "/")
	if unc {
		s = "/" + s
	}
	if len(s) > 1 && strings.HasSuffix(s, "/") {
		s = s[:len(s)-1]
	}
	if driveRe.MatchString(s) || strings.HasPrefix(s, "//") {
		return strings.ToLower(s)
	}
	return s
}

// IsUnder reports whether path is prefix itself or lies inside it.
func IsUnder(prefix, path string) bool {
	if prefix == "" {
		return true
	}
	k, pk := Key(prefix), Key(path)
	return pk == k || strings.HasPrefix(pk, k+"/")
}
