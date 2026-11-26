package homebrew

import (
	"strings"

	"github.com/rubnogueira/mise-brew/internal/version"
)

// KegOutdated decides whether installed kegs are older than the formula,
// honouring version_scheme the way Homebrew's Formula#outdated_kegs does.
func KegOutdated(name string, f *Formula) bool {
	vs := KegVersions(name)
	if len(vs) == 0 {
		return false
	}
	cur := f.PkgVersion()
	for _, v := range vs {
		if strings.HasPrefix(v, "HEAD") {
			return false // HEAD installs are only outdated with --fetch-HEAD
		}
		// mise records version_scheme 0 in every receipt, so an exact match
		// means up to date whatever the schemes say.
		if version.CompareVersions(v, cur) == 0 {
			return false
		}
		scheme := 0
		if r := ReadReceipt(KegPath(name, v)); r != nil {
			scheme = r.Source.Versions.VersionScheme
		}
		if scheme > f.VersionScheme {
			return false
		}
		if scheme == f.VersionScheme && version.CompareVersions(v, cur) >= 0 {
			return false
		}
	}
	return true
}
