package homebrew

import (
	"strings"
)

// SafeVersion reports whether a version (which tap Ruby files control) can be
// embedded in a single path component.
func SafeVersion(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\\x00")
}
