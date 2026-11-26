package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Homebrew's Version comparison (Library/Homebrew/version.rb), reimplemented:
// a version is scanned into tokens; pre-release tokens (alpha < beta < pre < rc)
// sort before a release, patch/post tokens after it, and missing tokens compare
// as zero. A PkgVersion adds a `_N` revision compared after the version.

type tokKind int

const (
	tokNull tokKind = iota
	tokAlpha
	tokBeta
	tokPre
	tokRC
	tokNumeric
	tokPatch
	tokPost
	tokString
)

type vtoken struct {
	kind tokKind
	num  uint64 // numeric value, or the number following alpha/beta/rc/...
	str  string
}

var versionTokenRe = regexp.MustCompile(`(?i)(alpha|beta|pre|rc|patch|post|a|b|p)(\d*)|\d+|[a-z]+`)

func tokenize(v string) []vtoken {
	var out []vtoken
	for _, m := range versionTokenRe.FindAllStringSubmatch(v, -1) {
		whole := m[0]
		if m[1] != "" {
			// Single letters only count as pre-release markers when followed by digits
			// (1.0a1, 1.0b2, 1.0p1); otherwise they are plain strings (1.0a).
			word := strings.ToLower(m[1])
			if len(word) == 1 && m[2] == "" {
				out = append(out, vtoken{kind: tokString, str: strings.ToLower(whole)})
				continue
			}
			n, _ := strconv.ParseUint(m[2], 10, 64)
			k := map[string]tokKind{"alpha": tokAlpha, "a": tokAlpha, "beta": tokBeta, "b": tokBeta, "pre": tokPre, "rc": tokRC, "patch": tokPatch, "p": tokPatch, "post": tokPost}[word]
			out = append(out, vtoken{kind: k, num: n})
			continue
		}
		if n, err := strconv.ParseUint(whole, 10, 64); err == nil {
			out = append(out, vtoken{kind: tokNumeric, num: n})
			continue
		}
		out = append(out, vtoken{kind: tokString, str: strings.ToLower(whole)})
	}
	return out
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// rank orders token kinds relative to a release (numeric/null).
func rank(k tokKind) int {
	switch k {
	case tokAlpha:
		return -4
	case tokBeta:
		return -3
	case tokPre:
		return -2
	case tokRC:
		return -1
	case tokNull, tokNumeric:
		return 0
	case tokPatch:
		return 1
	case tokPost:
		return 2
	}
	return 0
}

func cmpToken(a, b vtoken) int {
	if a.kind == tokNull && b.kind == tokNull {
		return 0
	}
	if a.kind == tokString && b.kind == tokString {
		return strings.Compare(a.str, b.str)
	}
	if a.kind == tokString || b.kind == tokString {
		// Numbers outrank words; a word outranks nothing.
		if a.kind == tokString {
			if b.kind == tokNull {
				return 1
			}
			return -1
		}
		if a.kind == tokNull {
			return -1
		}
		return 1
	}
	if a.kind == tokNumeric && b.kind == tokNumeric {
		return cmpUint(a.num, b.num)
	}
	if a.kind == tokNull && b.kind == tokNumeric {
		return cmpUint(0, b.num)
	}
	if a.kind == tokNumeric && b.kind == tokNull {
		return cmpUint(a.num, 0)
	}
	if ra, rb := rank(a.kind), rank(b.kind); ra != rb {
		return map[bool]int{true: -1, false: 1}[ra < rb]
	}
	return cmpUint(a.num, b.num)
}

// compareVersionStrings compares two versions without revisions.
func compareVersionStrings(a, b string) int {
	if a == b {
		return 0
	}
	if a == "HEAD" || strings.HasPrefix(a, "HEAD-") {
		if b == "HEAD" || strings.HasPrefix(b, "HEAD-") {
			return 0
		}
		return 1
	}
	if b == "HEAD" || strings.HasPrefix(b, "HEAD-") {
		return -1
	}
	ta, tb := tokenize(a), tokenize(b)
	for i := 0; i < len(ta) || i < len(tb); i++ {
		x, y := vtoken{}, vtoken{}
		if i < len(ta) {
			x = ta[i]
		}
		if i < len(tb) {
			y = tb[i]
		}
		if c := cmpToken(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// SplitRevision splits "1.2.3_4" into ("1.2.3", 4).
func SplitRevision(v string) (string, int) {
	if i := strings.LastIndex(v, "_"); i > 0 {
		if n, err := strconv.Atoi(v[i+1:]); err == nil {
			return v[:i], n
		}
	}
	return v, 0
}

// CompareVersions compares Homebrew pkg_versions ("version[_revision]").
func CompareVersions(a, b string) int {
	va, ra := SplitRevision(a)
	vb, rb := SplitRevision(b)
	if c := compareVersionStrings(va, vb); c != 0 {
		return c
	}
	switch {
	case ra < rb:
		return -1
	case ra > rb:
		return 1
	}
	return 0
}
