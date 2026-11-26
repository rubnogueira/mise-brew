package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

type pkgRef struct {
	kind string // "formula" | "cask"
	name string // canonical name / token (may be tap-qualified for third-party taps)
	f    *homebrew.Formula
	c    *homebrew.Cask
}

func (r pkgRef) key() string {
	if r.kind == "cask" {
		return "brew-cask:" + r.name
	}
	return "brew:" + r.name
}

// resolvePackage resolves a user-supplied name for installing.
func resolvePackage(name string, o Opts) pkgRef {
	if !validPackageArg(name) {
		ui.Onoe("No available formula or cask with the name \"%s\".", name)
		ui.Exit(1)
	}
	n := homebrew.StripCoreTap(name)
	lower := strings.ToLower(n)
	if strings.HasPrefix(name, "homebrew/cask/") || strings.HasPrefix(name, "homebrew/homebrew-cask/") {
		o = o.withFlag("cask", "") // only for this name, not the rest of the command line
	}
	if homebrew.IsThirdPartyTapName(n) {
		if !o.onlyCask() {
			if f := homebrew.LookupTapFormula(lower); f != nil {
				return pkgRef{kind: "formula", name: f.FullName, f: f}
			}
		}
		if !o.onlyFormula() {
			if c := homebrew.LookupTapCask(lower); c != nil {
				return pkgRef{kind: "cask", name: c.FullToken, c: c}
			}
		}
		kind := "formula"
		if o.onlyCask() {
			kind = "cask"
		}
		return pkgRef{kind: kind, name: n}
	}
	if strings.HasSuffix(lower, ".rb") || strings.Contains(lower, "://") {
		ui.Odie("installing from a formula file or URL is not supported by mise-brew: %s", name)
	}
	homebrew.EnsureAPI()
	if !o.onlyCask() {
		if f := homebrew.LookupFormula(lower); f != nil {
			return pkgRef{kind: "formula", name: f.Name, f: f}
		}
	}
	if !o.onlyFormula() {
		if c := homebrew.LookupCask(lower); c != nil {
			return pkgRef{kind: "cask", name: c.Token, c: c}
		}
	}
	what := "formula or cask"
	if o.onlyFormula() {
		what = "formula"
	} else if o.onlyCask() {
		what = "cask"
	}
	ui.Onoe("No available %s with the name \"%s\".", what, name)
	if sim := similarNames(lower, o); len(sim) > 0 {
		ui.Ohai("Searching for similarly named formulae and casks...")
		ui.PutsColumns(sim)
		fmt.Printf("\nTo install %s, run:\n  brew install %s\n", sim[0], sim[0])
	}
	ui.Exit(1)
	return pkgRef{}
}

func similarNames(q string, o Opts) []string {
	var out []string
	if !o.onlyCask() {
		for _, n := range homebrew.FormulaAPI.Names() {
			if strings.Contains(n, q) {
				out = append(out, n)
			}
		}
	}
	if !o.onlyFormula() {
		for _, n := range homebrew.CaskAPI.Names() {
			if strings.Contains(n, q) {
				out = append(out, n)
			}
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func upToDate(ref pkgRef) bool {
	switch {
	case ref.f != nil:
		return homebrew.IsFormulaInstalled(ref.f.Name) && !homebrew.KegOutdated(ref.f.Name, ref.f)
	case ref.c != nil:
		return homebrew.IsCaskInstalled(ref.c.Token) && (ref.c.Version == "latest" || slices.Contains(homebrew.CaskVersions(ref.c.Token), ref.c.Version))
	case ref.kind == "formula":
		return homebrew.IsFormulaInstalled(homebrew.BaseName(ref.name))
	default:
		return homebrew.IsCaskInstalled(homebrew.BaseName(ref.name))
	}
}

func caveatsFor(ref pkgRef) string {
	var parts []string
	switch {
	case ref.f != nil:
		if ref.f.KegOnly {
			parts = append(parts, kegOnlyCaveat(ref.f))
		}
		if ref.f.Caveats != nil && *ref.f.Caveats != "" {
			parts = append(parts, strings.TrimRight(homebrew.ExpandCaveats(*ref.f.Caveats), "\n"))
		}
		if s := serviceCaveat(ref.f); s != "" {
			parts = append(parts, s)
		}
	case ref.c != nil:
		if ref.c.Caveats != nil && *ref.c.Caveats != "" {
			parts = append(parts, strings.TrimRight(homebrew.ExpandCaveats(*ref.c.Caveats), "\n"))
		}
	}
	return strings.Join(parts, "\n\n")
}

func kegOnlyCaveat(f *homebrew.Formula) string {
	reason := ""
	var r struct {
		Reason      string `json:"reason"`
		Explanation string `json:"explanation"`
	}
	if json.Unmarshal(f.KegOnlyReason, &r) == nil {
		reason = r.Explanation
		if reason == "" {
			switch r.Reason {
			case ":provided_by_macos":
				reason = "macOS already provides this software and installing another version in\nparallel can cause all kinds of trouble."
			case ":versioned_formula":
				reason = "this is an alternate version of another formula."
			case ":shadowed_by_macos":
				reason = "macOS provides similar software and installing this software in\nparallel can cause all kinds of trouble."
			default:
				reason = strings.TrimPrefix(r.Reason, ":")
			}
		}
	}
	opt := filepath.Join(paths.OptDir(), f.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "%s is keg-only, which means it was not symlinked into %s,\nbecause %s\n", f.Name, paths.BrewPrefix(), strings.TrimSuffix(reason, "."))
	if paths.IsDir(filepath.Join(opt, "bin")) {
		fmt.Fprintf(&b, "\nIf you need to have %s first in your PATH, run:\n  echo 'export PATH=\"%s/bin:$PATH\"' >> ~/.zshrc\n", f.Name, opt)
	}
	if paths.IsDir(filepath.Join(opt, "lib")) {
		fmt.Fprintf(&b, "\nFor compilers to find %s you may need to set:\n  export LDFLAGS=\"-L%s/lib\"\n", f.Name, opt)
		if paths.IsDir(filepath.Join(opt, "include")) {
			fmt.Fprintf(&b, "  export CPPFLAGS=\"-I%s/include\"\n", opt)
		}
	}
	if paths.IsDir(filepath.Join(opt, "lib", "pkgconfig")) {
		fmt.Fprintf(&b, "\nFor pkg-config to find %s you may need to set:\n  export PKG_CONFIG_PATH=\"%s/lib/pkgconfig\"\n", f.Name, opt)
	}
	return strings.TrimRight(b.String(), "\n")
}

func serviceCaveat(f *homebrew.Formula) string {
	if len(f.Service) == 0 || string(f.Service) == "null" {
		return ""
	}
	var svc struct {
		Run any `json:"run"`
	}
	_ = json.Unmarshal(f.Service, &svc)
	cmd := ""
	switch r := svc.Run.(type) {
	case []any:
		var ps []string
		for _, x := range r {
			if s, ok := x.(string); ok {
				ps = append(ps, homebrew.ExpandCaveats(s))
			}
		}
		cmd = strings.Join(ps, " ")
	case map[string]any:
		for _, k := range []string{"macos", "linux"} {
			if arr, ok := r[k].([]any); ok {
				var ps []string
				for _, x := range arr {
					if s, ok := x.(string); ok {
						ps = append(ps, homebrew.ExpandCaveats(s))
					}
				}
				cmd = strings.Join(ps, " ")
				break
			}
		}
	}
	s := fmt.Sprintf("To start %s now and restart at login:\n  brew services start %s", f.Name, f.Name)
	if cmd != "" {
		s += fmt.Sprintf("\nOr, if you don't want/need a background service you can just run:\n  %s", cmd)
	}
	return s
}

// --- reinstall --------------------------------------------------------------

func passthroughFlags(o Opts) []string {
	var out []string
	for _, k := range o.order {
		if len(k) == 1 {
			out = append(out, "-"+k)
		} else if v := o.flags[k]; v != "" {
			out = append(out, "--"+k+"="+v)
		} else {
			out = append(out, "--"+k)
		}
	}
	return out
}

// --- uninstall --------------------------------------------------------------

// --- upgrade / update / outdated -------------------------------------------

func outdatedSummary(f, c int) string {
	var parts []string
	if f > 0 {
		parts = append(parts, fmt.Sprintf("%d outdated %s", f, map[bool]string{true: "formula", false: "formulae"}[f == 1]))
	}
	if c > 0 {
		parts = append(parts, fmt.Sprintf("%d outdated %s", c, map[bool]string{true: "cask", false: "casks"}[c == 1]))
	}
	return strings.Join(parts, " and ")
}

type outdatedItem struct {
	Name              string   `json:"name"`
	InstalledVersions []string `json:"installed_versions"`
	CurrentVersion    string   `json:"current_version"`
	Pinned            bool     `json:"pinned"`
	PinnedVersion     *string  `json:"pinned_version"`
}

func outdatedFormulae(names []string, _ bool) []outdatedItem {
	if !homebrew.FormulaAPI.Available() {
		return nil
	}
	if names == nil {
		names = homebrew.InstalledFormulae()
	}
	var out []outdatedItem
	for _, n := range names {
		f := homebrew.LookupFormula(n)
		if f == nil || f.Name != n {
			continue
		}
		vs := homebrew.KegVersions(n)
		if len(vs) == 0 {
			continue
		}
		cur := f.PkgVersion()
		if !homebrew.KegOutdated(n, f) {
			continue
		}
		it := outdatedItem{Name: n, InstalledVersions: vs, CurrentVersion: cur, Pinned: homebrew.IsPinned(n)}
		if it.Pinned {
			pv := homebrew.PinnedVersion(n)
			it.PinnedVersion = &pv
		}
		out = append(out, it)
	}
	return out
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// --- autoremove / cleanup ---------------------------------------------------

// splitBottleName splits "<name>-<version>.tar.gz" using installed names to
// disambiguate names that contain dashes. Returns "" when no installed formula matches.
func splitBottleName(file string, installed map[string]string) (string, string) {
	base := file
	for _, ext := range []string{".tar.gz", ".bottle.tar.gz", ".tgz", ".json"} {
		base = strings.TrimSuffix(base, ext)
	}
	best := ""
	for n := range installed {
		if strings.HasPrefix(base, n+"-") && len(n) > len(best) {
			best = n
		}
	}
	if best == "" {
		return "", ""
	}
	return best, base[len(best)+1:]
}

// --- pin / link / tab -------------------------------------------------------

// validPackageArg reports whether a user-supplied formula/cask name (after
// removing homebrew/core|cask/) is a plain name or owner/tap/name whose every
// segment is safe to join onto the Cellar or Caskroom. ".", ".." and
// "../var" are rejected.
func validPackageArg(name string) bool {
	n := homebrew.StripCoreTap(name)
	if homebrew.IsThirdPartyTapName(n) {
		for _, part := range strings.Split(n, "/") {
			if !homebrew.ValidName(part) {
				return false
			}
		}
		return true
	}
	return homebrew.ValidName(n)
}

func requireInstalled(names []string) []string {
	if len(names) == 0 {
		ui.Odie("Invalid usage: This command requires at least 1 installed formula argument.")
	}
	var out []string
	for _, n := range names {
		if !validPackageArg(n) {
			ui.Odie("No such keg: %s", paths.Cellar()+"/"+n)
		}
		r := homebrew.ResolveInstalledFormula(n)
		if r == "" {
			ui.Odie("No such keg: %s", filepath.Join(paths.Cellar(), n))
		}
		out = append(out, r)
	}
	return out
}

func cmdPin(args []string) {
	defer homebrew.LockPrefix()()
	o := parseArgs(args, nil, nil)
	for _, n := range requireInstalled(o.Pos) {
		if homebrew.IsPinned(n) {
			ui.Opoo("%s already pinned", n)
			continue
		}
		if err := homebrew.SetRecordLink(paths.PinnedDir(), n, homebrew.CurrentVersion(n)); err != nil {
			ui.Odie("%v", err)
		}
	}
}

func cmdUnpin(args []string) {
	defer homebrew.LockPrefix()()
	o := parseArgs(args, nil, nil)
	for _, n := range requireInstalled(o.Pos) {
		if !homebrew.IsPinned(n) {
			ui.Opoo("%s not pinned", n)
			continue
		}
		_ = os.Remove(filepath.Join(paths.PinnedDir(), n))
	}
}

func cmdLink(args []string) {
	defer homebrew.LockPrefix()()
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	dry := o.Has("dry-run")
	for _, n := range requireInstalled(o.Pos) {
		v := homebrew.CurrentVersion(n)
		keg := homebrew.KegPath(n, v)
		if lv := homebrew.LinkedVersion(n); lv == v && !o.Has("overwrite") {
			ui.Opoo("Already linked: %s\nTo relink, run:\n  brew unlink %s && brew link %s", keg, n, n)
			continue
		}
		if f := homebrew.LookupFormula(n); f != nil && f.KegOnly && !o.Has("force") {
			ui.Opoo("Refusing to link keg-only %s\nIf you need to have this software first in your PATH instead consider running:\n  echo 'export PATH=\"%s/bin:$PATH\"' >> ~/.zshrc", n, filepath.Join(paths.OptDir(), n))
			continue
		}
		if dry {
			// Like Homebrew: list every file that would be linked, or with
			// --overwrite every existing file that would be deleted.
			if o.Has("overwrite") {
				fmt.Println("Would remove:")
			} else {
				fmt.Println("Would link:")
			}
			for _, l := range linkDryRun(n, v, o.Has("overwrite")) {
				fmt.Println(l)
			}
			continue
		}
		count, conflicts, err := homebrew.LinkKeg(n, v, o.Has("overwrite"), false)
		if err != nil {
			ui.Odie("%v", err)
		}
		if len(conflicts) > 0 {
			c := conflicts[0]
			fmt.Printf("Linking %s...\n", keg)
			if o.Has("overwrite") {
				// --overwrite only replaces files, symlinks and empty
				// directories; anything left is a real directory or a
				// directory link that points outside the Cellar.
				ui.Onoe("Could not symlink %s\nTarget %s\nis a directory (or a link to one outside the Cellar) that --overwrite will not replace.\nMove or remove it yourself, then run:\n  brew link %s", c.Rel, c.Dst, n)
				ui.Exit(1)
			}
			ui.Onoe("Could not symlink %s\nTarget %s\nalready exists. You may want to remove it:\n  rm '%s'\n\nTo force the link and overwrite all conflicting files:\n  brew link --overwrite %s\n\nTo list all files that would be deleted:\n  brew link --overwrite %s --dry-run", c.Rel, c.Dst, c.Dst, n, n)
			ui.Exit(1)
		}
		fmt.Printf("Linking %s... %s created.\n", keg, ui.Plural(count, "symlink"))
	}
}

// linkDryRun lists what `brew link --dry-run` prints: each prefix path that
// would be linked, or (overwrite) each existing path that would be replaced,
// shown as "dst -> target" for symlinks. Paths already linked to the keg are
// skipped, as linkKeg does.
func linkDryRun(name, version string, overwrite bool) []string {
	keg := homebrew.KegPath(name, version)
	roots := homebrew.KegRoots(name, version)
	var out []string
	for _, top := range homebrew.LinkableDirs {
		base := filepath.Join(keg, top)
		if !paths.IsDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(keg, p)
			if homebrew.SkipLinkFiles[rel] || d.Name() == ".DS_Store" {
				return nil
			}
			dst := filepath.Join(paths.BrewPrefix(), rel)
			fi, err := os.Lstat(dst)
			switch {
			case err != nil:
				if !overwrite {
					out = append(out, dst)
				}
			case fi.Mode()&os.ModeSymlink != 0 && homebrew.ResolvesIntoAny(dst, roots):
				// already linked
			case !overwrite:
				out = append(out, dst)
			case fi.Mode()&os.ModeSymlink != 0:
				t, _ := filepath.EvalSymlinks(dst)
				if t == "" {
					t, _ = os.Readlink(dst)
				}
				out = append(out, dst+" -> "+t)
			default:
				out = append(out, dst)
			}
			return nil
		})
	}
	return out
}

func cmdUnlink(args []string) {
	defer homebrew.LockPrefix()()
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	dry := o.Has("dry-run")
	for _, n := range requireInstalled(o.Pos) {
		v := homebrew.CurrentVersion(n)
		if lv := homebrew.LinkedVersion(n); lv != "" {
			v = lv
		}
		keg := homebrew.KegPath(n, v)
		count, paths := homebrew.UnlinkKeg(n, v, dry)
		if dry {
			fmt.Println("Would remove:")
			for _, p := range paths {
				fmt.Println(p)
			}
			continue
		}
		fmt.Printf("Unlinking %s... %s removed.\n", keg, ui.Plural(count, "symlink"))
	}
}

func cmdTab(args []string) {
	defer homebrew.LockPrefix()()
	o := parseArgs(args, nil, nil)
	var val *bool
	if o.Has("installed-on-request") {
		t := true
		val = &t
	}
	if o.Has("no-installed-on-request") {
		f := false
		val = &f
	}
	if val == nil {
		ui.Odie("Invalid usage: `brew tab` needs --installed-on-request or --no-installed-on-request")
	}
	for _, n := range requireInstalled(o.Pos) {
		setOnRequest(n, *val)
		state := "installed on request"
		if !*val {
			state = "not installed on request"
		}
		fmt.Printf("%s is now %s.\n", n, state)
	}
}

// editReceipt merges fields into an INSTALL_RECEIPT.json, keeping the rest intact.
func editReceipt(p string, fields map[string]any) {
	data, err := os.ReadFile(p)
	if err != nil {
		ui.Odie("%v", err)
	}
	var m map[string]any
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		ui.Odie("%s: %v", p, err)
	}
	for k, v := range fields {
		m[k] = v
	}
	out, _ := json.Marshal(m)
	if err := os.WriteFile(p, out, 0o644); err != nil {
		ui.Odie("%v", err)
	}
}

func sortStrings(xs []string) []string { sort.Strings(xs); return xs }
