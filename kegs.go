package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// validName reports whether name is safe to use as a single path component
// for a formula rack, cask token or tap segment: no separators, no "." or
// "..", no leading dot or dash. Every user-supplied name must pass this
// before it is joined onto the Cellar, Caskroom or prefix.
func validName(name string) bool {
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

// validVersion reports whether v is safe as a single keg or cask version path
// component. Versions are looser than names (cask versions contain commas), but
// must never be "." or ".." or contain a separator.
func validVersion(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\x00")
}

// validTapName reports whether every "/"-separated segment of name is a
// validName (so "owner/tap/formula" and "formula" pass, "../x" does not).
func validTapName(name string) bool {
	for _, seg := range strings.Split(name, "/") {
		if !validName(seg) {
			return false
		}
	}
	return true
}

func readReceipt(kegPath string) *Receipt {
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

func listDirNames(dir string, wantDir bool) []string {
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

// installedFormulae lists rack names in the Cellar that have at least one keg.
func installedFormulae() []string {
	var out []string
	for _, n := range listDirNames(cellar(), true) {
		if len(kegVersions(n)) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func kegVersions(name string) []string {
	if !validName(name) {
		return nil
	}
	v := listDirNames(filepath.Join(cellar(), name), true)
	sort.Slice(v, func(i, j int) bool { return compareVersions(v[i], v[j]) < 0 })
	return v
}

func isFormulaInstalled(name string) bool { return len(kegVersions(name)) > 0 }

func readLinkVersion(link string) string {
	t, err := os.Readlink(link)
	if err != nil {
		return ""
	}
	if v := filepath.Base(t); validVersion(v) {
		return v
	}
	return ""
}

// currentVersion returns the keg the opt link points at, else the newest keg.
func currentVersion(name string) string {
	vs := kegVersions(name)
	if len(vs) == 0 {
		return ""
	}
	if v := readLinkVersion(filepath.Join(optDir(), name)); v != "" {
		for _, x := range vs {
			if x == v {
				return v
			}
		}
	}
	return vs[len(vs)-1]
}

func kegPath(name, version string) string { return filepath.Join(cellar(), name, version) }

// linkedVersion returns the linked keg version. Older mise releases linked kegs
// without writing var/homebrew/linked, so fall back to looking for prefix symlinks.
func linkedVersion(name string) string {
	if v := readLinkVersion(filepath.Join(linkedDir(), name)); v != "" {
		return v
	}
	if v := currentVersion(name); v != "" && kegHasLinks(name, v) {
		return v
	}
	return ""
}

func pinnedVersion(name string) string {
	return readLinkVersion(filepath.Join(pinnedDir(), name))
}

func isPinned(name string) bool {
	return validName(name) && fileExists(filepath.Join(pinnedDir(), name))
}

func installedCasks() []string {
	var out []string
	for _, t := range listDirNames(caskroom(), true) {
		if len(caskVersions(t)) > 0 {
			out = append(out, t)
		}
	}
	return out
}

// caskVersions lists real version directories (mise adds a `latest` symlink).
func caskVersions(token string) []string {
	if !validName(token) {
		return nil
	}
	dir := filepath.Join(caskroom(), token)
	var out []string
	for _, v := range listDirNames(dir, true) {
		if fi, err := os.Lstat(filepath.Join(dir, v)); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			out = append(out, v)
		}
	}
	return out
}

// currentCaskVersion follows mise's `latest` link, else the newest directory.
func currentCaskVersion(token string) string {
	dir := filepath.Join(caskroom(), token)
	vs := caskVersions(token)
	if t, err := os.Readlink(filepath.Join(dir, "latest")); err == nil {
		if v := filepath.Base(filepath.Clean(t)); contains(vs, v) {
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

func isCaskInstalled(token string) bool { return len(caskVersions(token)) > 0 }

// resolveInstalledFormula maps an alias/old name/tap-qualified name to an installed rack name.
func resolveInstalledFormula(name string) string {
	n := stripCoreTap(name)
	if !validTapName(n) {
		return ""
	}
	if isThirdPartyTapName(n) {
		n = n[strings.LastIndex(n, "/")+1:]
	}
	if !validName(n) {
		return ""
	}
	if isFormulaInstalled(n) {
		return n
	}
	if f := lookupFormula(n); f != nil && isFormulaInstalled(f.Name) {
		return f.Name
	}
	return ""
}

func resolveInstalledCask(token string) string {
	t := stripCoreTap(token)
	if !validTapName(t) {
		return ""
	}
	if isThirdPartyTapName(t) {
		t = t[strings.LastIndex(t, "/")+1:]
	}
	if !validName(t) {
		return ""
	}
	if isCaskInstalled(t) {
		return t
	}
	if c := lookupCask(t); c != nil && isCaskInstalled(c.Token) {
		return c.Token
	}
	return ""
}

// diskUsage counts files and bytes under dir (symlinks counted as files, not followed).
func diskUsage(dir string) (int, int64) {
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

func kegSummary(dir string) string {
	n, s := diskUsage(dir)
	return "(" + plural(n, "file") + ", " + humanSize(s) + ")"
}

// --- dependents -------------------------------------------------------------

// installedRuntimeDeps maps installed rack -> runtime dependency names (from receipts).
func installedRuntimeDeps() map[string][]string {
	out := map[string][]string{}
	for _, n := range installedFormulae() {
		r := readReceipt(kegPath(n, currentVersion(n)))
		var deps []string
		if r != nil {
			for _, d := range r.RuntimeDependencies {
				deps = append(deps, baseName(d.FullName))
			}
		}
		out[n] = deps
	}
	return out
}

func baseName(full string) string {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[i+1:]
	}
	return full
}

// installedDependents returns installed formulae whose runtime deps include name.
func installedDependents(name string, deps map[string][]string) []string {
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
