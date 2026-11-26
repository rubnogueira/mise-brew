package homebrew

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
	"github.com/rubnogueira/mise-brew/internal/version"
)

type RuntimeDep struct {
	FullName   string `json:"full_name"`
	Version    string `json:"version"`
	PkgVersion string `json:"pkg_version"`
	Declared   bool   `json:"declared_directly"`
}

type Receipt struct {
	HomebrewVersion       string       `json:"homebrew_version"`
	UsedOptions           []string     `json:"used_options"`
	UnusedOptions         []string     `json:"unused_options"`
	BuiltAsBottle         bool         `json:"built_as_bottle"`
	PouredFromBottle      bool         `json:"poured_from_bottle"`
	LoadedFromAPI         bool         `json:"loaded_from_api"`
	InstalledAsDependency bool         `json:"installed_as_dependency"`
	InstalledOnRequest    bool         `json:"installed_on_request"`
	Time                  int64        `json:"time"`
	RuntimeDependencies   []RuntimeDep `json:"runtime_dependencies"`
	Aliases               []string     `json:"aliases"`
	Source                struct {
		Tap      string `json:"tap"`
		Spec     string `json:"spec"`
		Versions struct {
			Stable        string `json:"stable"`
			VersionScheme int    `json:"version_scheme"`
		} `json:"versions"`
	} `json:"source"`
	Arch string `json:"arch"`
}

// ValidName reports whether name is safe to use as a single path component
// for a formula rack, cask token or tap segment: no separators, no "." or
// "..", no leading dot or dash. Every user-supplied name must pass this
// before it is joined onto the Cellar, Caskroom or prefix.
func ValidName(name string) bool {
	if name == "" || len(name) > 255 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && strings.ContainsRune("@+_.-", r):
		default:
			return false
		}
	}
	return true
}

// ValidVersion reports whether v is safe as a single keg or cask version path
// component. Versions are looser than names (cask versions contain commas), but
// must never be "." or ".." or contain a separator.
func ValidVersion(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\x00")
}

// validTapName reports whether every "/"-separated segment of name is a
// validName (so "owner/tap/formula" and "formula" pass, "../x" does not).
func validTapName(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if !ValidName(seg) {
			return false
		}
	}
	return true
}

func ReadReceipt(kegPath string) *Receipt {
	data, err := os.ReadFile(filepath.Join(kegPath, "INSTALL_RECEIPT.json"))
	if err != nil {
		return nil
	}
	var r Receipt
	if json.Unmarshal(data, &r) != nil {
		return nil
	}
	return &r
}

func ListDirNames(dir string, wantDir bool) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if wantDir {
			fi, err := os.Stat(filepath.Join(dir, n))
			if err != nil || !fi.IsDir() {
				continue
			}
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// InstalledFormulae lists rack names in the Cellar that have at least one keg.
func InstalledFormulae() []string {
	var out []string
	for _, n := range ListDirNames(paths.Cellar(), true) {
		if len(KegVersions(n)) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func KegVersions(name string) []string {
	if !ValidName(name) {
		return nil
	}
	v := ListDirNames(filepath.Join(paths.Cellar(), name), true)
	sort.Slice(v, func(i, j int) bool { return version.CompareVersions(v[i], v[j]) < 0 })
	return v
}

func IsFormulaInstalled(name string) bool { return len(KegVersions(name)) > 0 }

func ReadLinkVersion(link string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if v := filepath.Base(t); ValidVersion(v) {
		return v
	}
	return ""
}

// CurrentVersion returns the keg the opt link points at, else the newest keg.
func CurrentVersion(name string) string {
	vs := KegVersions(name)
	if len(vs) == 0 {
		return ""
	}
	if v := ReadLinkVersion(filepath.Join(paths.OptDir(), name)); v != "" {
		for _, x := range vs {
			if x == v {
				return v
			}
		}
	}
	return vs[len(vs)-1]
}

func KegPath(name, version string) string { return filepath.Join(paths.Cellar(), name, version) }

// LinkedVersion returns the linked keg version. Older mise releases linked kegs
// without writing var/homebrew/linked, so fall back to looking for prefix symlinks.
func LinkedVersion(name string) string {
	if v := ReadLinkVersion(filepath.Join(paths.LinkedDir(), name)); v != "" {
		return v
	}
	if v := CurrentVersion(name); v != "" && kegHasLinks(name, v) {
		return v
	}
	return ""
}

func PinnedVersion(name string) string {
	return ReadLinkVersion(filepath.Join(paths.PinnedDir(), name))
}

func IsPinned(name string) bool {
	return ValidName(name) && paths.FileExists(filepath.Join(paths.PinnedDir(), name))
}

func InstalledCasks() []string {
	var out []string
	for _, t := range ListDirNames(paths.Caskroom(), true) {
		if len(CaskVersions(t)) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// CaskVersions lists real version directories (mise adds a `latest` symlink).
func CaskVersions(token string) []string {
	if !ValidName(token) {
		return nil
	}
	dir := filepath.Join(paths.Caskroom(), token)
	var out []string
	for _, v := range ListDirNames(dir, true) {
		if fi, err := os.Lstat(filepath.Join(dir, v)); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			out = append(out, v)
		}
	}
	return out
}

// CurrentCaskVersion follows mise's `latest` link, else the newest directory.
func CurrentCaskVersion(token string) string {
	dir := filepath.Join(paths.Caskroom(), token)
	vs := CaskVersions(token)
	if t, err := os.Readlink(filepath.Join(dir, "latest")); err == nil {
		if v := filepath.Base(filepath.Clean(t)); slices.Contains(vs, v) {
			return v
		}
	}
	var best string
	var bestT int64
	for _, v := range vs {
		fi, err := os.Stat(filepath.Join(dir, v))
		if err != nil {
			continue
		}
		if t := fi.ModTime().UnixNano(); best == "" || t > bestT {
			best, bestT = v, t
		}
	}
	return best
}

func IsCaskInstalled(token string) bool { return len(CaskVersions(token)) > 0 }

// ResolveInstalledFormula maps an alias/old name/tap-qualified name to an installed rack name.
func ResolveInstalledFormula(name string) string {
	n := StripCoreTap(name)
	if !validTapName(n) {
		return ""
	}
	if IsThirdPartyTapName(n) {
		n = n[strings.LastIndex(n, "/")+1:]
	}
	if !ValidName(n) {
		return ""
	}
	if IsFormulaInstalled(n) {
		return n
	}
	if f := LookupFormula(n); f != nil && IsFormulaInstalled(f.Name) {
		return f.Name
	}
	return ""
}

func ResolveInstalledCask(token string) string {
	t := StripCoreTap(token)
	if !validTapName(t) {
		return ""
	}
	if IsThirdPartyTapName(t) {
		t = t[strings.LastIndex(t, "/")+1:]
	}
	if !ValidName(t) {
		return ""
	}
	if IsCaskInstalled(t) {
		return t
	}
	if c := LookupCask(t); c != nil && IsCaskInstalled(c.Token) {
		return c.Token
	}
	return ""
}

// DiskUsage counts files and bytes under dir (symlinks counted as files, not followed).
func DiskUsage(dir string) (int, int64) {
	files := 0
	var size int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		files++
		if info, err := d.Info(); err == nil && d.Type().IsRegular() {
			size += info.Size()
		}
		return nil
	})
	return files, size
}

func KegSummary(dir string) string {
	n, s := DiskUsage(dir)
	return "(" + ui.Plural(n, "file") + ", " + ui.HumanSize(s) + ")"
}

// --- dependents -------------------------------------------------------------

// InstalledRuntimeDeps maps installed rack -> runtime dependency names (from receipts).
func InstalledRuntimeDeps() map[string][]string {
	out := map[string][]string{}
	for _, n := range InstalledFormulae() {
		r := ReadReceipt(KegPath(n, CurrentVersion(n)))
		var deps []string
		if r != nil {
			for _, d := range r.RuntimeDependencies {
				deps = append(deps, BaseName(d.FullName))
			}
		}
		out[n] = deps
	}
	return out
}

func BaseName(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}

// InstalledDependents returns installed formulae whose runtime deps include name.
func InstalledDependents(name string, deps map[string][]string) []string {
	var out []string
	for k, ds := range deps {
		if k == name {
			continue
		}
		for _, d := range ds {
			if d == name {
				out = append(out, k)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
