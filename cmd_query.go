package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- list -------------------------------------------------------------------

func cmdList(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) > 0 {
		listNamed(o)
		return
	}
	showF, showC := !o.onlyCask(), !o.onlyFormula()
	var formulae []string
	if showF {
		formulae = installedFormulae()
		formulae = filterFormulae(formulae, o)
	}
	var casks []string
	if showC && !o.Has("pinned", "installed-on-request", "installed-as-dependency", "poured-from-bottle", "built-from-source") {
		casks = installedCasks()
	}
	if o.Has("full-name") {
		for i, n := range formulae {
			if r := readReceipt(kegPath(n, currentVersion(n))); r != nil && r.Source.Tap != "" && r.Source.Tap != "homebrew/core" {
				formulae[i] = r.Source.Tap + "/" + n
			}
		}
	}
	if o.Has("versions") {
		for _, n := range formulae {
			vs := kegVersions(baseName(n))
			if o.Has("multiple") && len(vs) < 2 {
				continue
			}
			fmt.Println(n + " " + strings.Join(vs, " "))
		}
		for _, t := range casks {
			vs := caskVersions(t)
			if o.Has("multiple") && len(vs) < 2 {
				continue
			}
			fmt.Println(t + " " + strings.Join(vs, " "))
		}
		return
	}
	if o.Has("l", "t", "r") {
		lsArgs := "-1"
		if o.Has("l") {
			lsArgs = "-l"
		}
		if o.Has("t") {
			lsArgs += "t"
		}
		if o.Has("r") {
			lsArgs += "r"
		}
		if showF && len(formulae) > 0 {
			if showC && len(casks) > 0 {
				ohai("Formulae")
			}
			runLs(lsArgs, cellar(), formulae)
		}
		if showC && len(casks) > 0 {
			if showF && len(formulae) > 0 {
				fmt.Println()
				ohai("Casks")
			}
			runLs(lsArgs, caskroom(), casks)
		}
		return
	}
	one := o.Has("1") || !stdoutTTY
	print := func(items []string) {
		if one {
			for _, it := range items {
				fmt.Println(it)
			}
		} else {
			putsColumns(items)
		}
	}
	headers := stdoutTTY && !o.Has("1") && len(formulae) > 0 && len(casks) > 0
	if headers {
		ohai("Formulae")
	}
	print(formulae)
	if headers {
		fmt.Println()
		ohai("Casks")
	}
	print(casks)
}

func runLs(flags, dir string, names []string) {
	cmd := exec.Command("ls", append([]string{flags}, names...)...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	_ = cmd.Run()
}

func filterFormulae(names []string, o Opts) []string {
	if !o.Has("pinned", "installed-on-request", "installed-as-dependency", "poured-from-bottle", "built-from-source") {
		return names
	}
	var declared map[string]bool
	if o.Has("installed-on-request", "installed-as-dependency") {
		declared = declaredSet("brew")
	}
	var out []string
	for _, n := range names {
		r := readReceipt(kegPath(n, currentVersion(n)))
		onReq := declared[n] || (r != nil && r.InstalledOnRequest)
		ok := true
		if o.Has("pinned") && !isPinned(n) {
			ok = false
		}
		if o.Has("installed-on-request") && !onReq {
			ok = false
		}
		if o.Has("installed-as-dependency") && onReq {
			ok = false
		}
		if o.Has("poured-from-bottle") && (r == nil || !r.PouredFromBottle) {
			ok = false
		}
		if o.Has("built-from-source") && (r == nil || r.PouredFromBottle) {
			ok = false
		}
		if ok {
			out = append(out, n)
		}
	}
	return out
}

func listNamed(o Opts) {
	failed := false
	for _, n := range o.Pos {
		var rack, token string
		if !o.onlyCask() {
			rack = resolveInstalledFormula(n)
		}
		if rack == "" && !o.onlyFormula() {
			token = resolveInstalledCask(n)
		}
		switch {
		case rack != "":
			if o.Has("versions") {
				vs := kegVersions(rack)
				if o.Has("multiple") && len(vs) < 2 {
					continue
				}
				fmt.Println(rack + " " + strings.Join(vs, " "))
				continue
			}
			if o.Has("pinned") {
				if isPinned(rack) {
					fmt.Println(rack)
				}
				continue
			}
			listKegFiles(kegPath(rack, currentVersion(rack)), verbose)
		case token != "":
			if o.Has("versions") {
				fmt.Println(token + " " + strings.Join(caskVersions(token), " "))
				continue
			}
			listCaskFiles(token)
		default:
			failed = true
			if !o.Has("versions") {
				if o.onlyCask() {
					onoe("Cask '%s' is not installed.", n)
				} else {
					onoe("No such keg: %s", filepath.Join(cellar(), n))
				}
			}
		}
	}
	if failed {
		exit(1)
	}
}

// Homebrew's FORMULA_META_FILES plus mise/Homebrew bookkeeping files.
var kegMetafiles = regexp.MustCompile(`(?i)^(INSTALL_RECEIPT\.json|sbom\.spdx\.json|\.brew|AUTHORS|CHANGELOG|ChangeLog|CHANGES|COPYING|COPYRIGHT|CONTRIBUTORS|HISTORY|LICEN[CS]E|NEWS|NOTICE|README|TODO|THANKS|CREDITS|NOTES|RELEASE_NOTES)(\.[^/]*)?$`)

// listKegFiles is a port of Homebrew's PrettyListing (cmd/list.rb).
func listKegFiles(keg string, all bool) {
	if all {
		_ = filepath.WalkDir(keg, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				fmt.Println(p)
			}
			return nil
		})
		return
	}
	entries, _ := os.ReadDir(keg)
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	for _, e := range entries {
		p := filepath.Join(keg, e.Name())
		switch e.Name() {
		case "bin", "sbin":
			_ = filepath.WalkDir(p, func(q string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					fmt.Println(q)
				}
				return nil
			})
		case "lib":
			printPrettyDir(p, func(q string) bool {
				ext := filepath.Ext(q)
				fi, err := os.Lstat(q)
				return (ext == ".dylib" || ext == ".pc") && err == nil && fi.Mode()&os.ModeSymlink == 0
			})
		case ".brew":
		default:
			fi, err := os.Lstat(p)
			if err != nil {
				continue
			}
			switch {
			case fi.Mode()&os.ModeSymlink != 0 && isDir(p):
				t, _ := os.Readlink(p)
				fmt.Printf("%s -> %s\n", p, t)
			case fi.IsDir():
				printPrettyDir(p, nil)
			case !kegMetafiles.MatchString(e.Name()):
				fmt.Println(p)
			}
		}
	}
}

func printPrettyDir(root string, show func(string) bool) {
	var dirs, remaining []string
	other := ""
	entries, _ := os.ReadDir(root)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		switch {
		case e.IsDir():
			dirs = append(dirs, p)
		case show != nil && show(p):
			fmt.Println(p)
			other = "other "
		case e.Name() != ".DS_Store":
			remaining = append(remaining, p)
		}
	}
	for _, d := range dirs {
		var files []string
		_ = filepath.WalkDir(d, func(q string, de fs.DirEntry, err error) error {
			if err == nil && !de.IsDir() {
				files = append(files, q)
			}
			return nil
		})
		printRemaining(files, d, "")
	}
	printRemaining(remaining, root, other)
}

func printRemaining(files []string, root, other string) {
	switch {
	case len(files) == 1:
		fmt.Println(files[0])
	case len(files) > 1:
		fmt.Printf("%s/ (%d %sfiles)\n", root, len(files), other)
	}
}

func listCaskFiles(token string) {
	c := lookupCask(token)
	if c != nil {
		printed := false
		for _, a := range caskArtifacts(c) {
			if a.target != "" {
				if !printed {
					printed = true
				}
				ohai(a.kind)
				fmt.Println(a.target)
			}
		}
		if printed {
			return
		}
	}
	listKegFiles(filepath.Join(caskroom(), token, currentCaskVersion(token)), true)
}

type caskArtifact struct{ kind, name, target string }

var artifactSingular = map[string]string{"Apps": "App", "Binaries": "Binary", "Pkgs": "Pkg", "Fonts": "Font", "Suites": "Suite", "Manpages": "Manpage"}

func caskArtifacts(c *Cask) []caskArtifact {
	var out []caskArtifact
	appdir := caskAppdir()
	// resolve expands ~, $HOME, $APPDIR and $HOMEBREW_PREFIX in a target and
	// roots a relative one in dir, as Homebrew does when it installs.
	resolve := func(dir, target string) string {
		t := expandCaskPath(target)
		if filepath.IsAbs(t) || strings.HasPrefix(t, "~") {
			return t
		}
		return filepath.Join(dir, t)
	}
	for _, raw := range c.Artifacts {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for k, v := range m {
			var list []any
			if json.Unmarshal(v, &list) != nil || len(list) == 0 {
				continue
			}
			first, _ := list[0].(string)
			target := filepath.Base(first)
			if len(list) > 1 {
				if opts, ok := list[1].(map[string]any); ok {
					if t, ok := opts["target"].(string); ok && t != "" {
						target = t
					}
				}
			}
			switch k {
			case "app":
				out = append(out, caskArtifact{kind: "Apps", name: first, target: resolve(appdir, target)})
			case "binary":
				out = append(out, caskArtifact{kind: "Binaries", name: first, target: expandCaveats(resolve(filepath.Join(brewPrefix(), "bin"), target))})
			case "pkg":
				out = append(out, caskArtifact{kind: "Pkgs", name: first})
			case "font":
				out = append(out, caskArtifact{kind: "Fonts", name: first, target: resolve(expandCaskPath("~/Library/Fonts"), filepath.Base(first))})
			case "suite":
				out = append(out, caskArtifact{kind: "Suites", name: first, target: resolve(appdir, target)})
			case "manpage":
				out = append(out, caskArtifact{kind: "Manpages", name: first})
			}
		}
	}
	return out
}

// --- info -------------------------------------------------------------------

func cmdInfo(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	keepVariations = o.Has("variations")
	if o.Has("analytics") {
		forceAnalytics = true
		if len(o.Pos) == 0 {
			globalAnalytics(o)
			return
		}
	}
	if o.Has("sizes") {
		infoSizes(o)
		return
	}
	if jv, ok := o.Val("json"); ok {
		infoJSON(o, jv)
		return
	}
	if len(o.Pos) == 0 {
		if o.Has("installed") {
			for _, n := range installedFormulae() {
				o.Pos = append(o.Pos, n)
			}
		} else {
			n, files, size := 0, 0, int64(0)
			for _, r := range installedFormulae() {
				for _, v := range kegVersions(r) {
					n++
					f, s := diskUsage(kegPath(r, v))
					files += f
					size += s
				}
			}
			fmt.Printf("%s, %s, %s\n", plural(n, "keg"), plural(files, "file"), humanSize(size))
			return
		}
	}
	if !formulaAPI.Available() && !caskAPI.Available() {
		opoo("mise has not downloaded the Homebrew API; showing installed data only. Run `brew update`.")
	}
	failed := false
	for i, n := range o.Pos {
		if i > 0 {
			fmt.Println()
		}
		if o.Has("github") {
			url := ""
			if f := lookupFormula(n); f != nil && !o.onlyCask() {
				url = formulaSourceURL(f)
			} else if c := lookupCask(n); c != nil && !o.onlyFormula() {
				url = caskSourceURL(c)
			}
			if url != "" {
				openURL(url)
				continue
			}
		}
		if !o.onlyCask() {
			if f := lookupFormula(n); f != nil {
				infoFormula(f)
				continue
			}
			if r := resolveInstalledFormula(n); r != "" {
				infoInstalledOnly(r)
				continue
			}
		}
		if !o.onlyFormula() {
			if c := lookupCask(n); c != nil {
				infoCask(c)
				continue
			}
		}
		what := "formula or cask"
		if o.onlyFormula() {
			what = "formula"
		} else if o.onlyCask() {
			what = "cask"
		}
		onoe("No available %s with the name \"%s\".", what, n)
		failed = true
	}
	if failed {
		exit(1)
	}
}

func infoFormula(f *Formula) {
	installed := isFormulaInstalled(f.Name)
	head := f.Name
	if f.Tap != "" && f.Tap != "homebrew/core" {
		head = f.FullName
	}
	if installed {
		head += " " + check()
	}
	spec := "stable " + f.Versions.Stable
	if f.Versions.Bottle {
		spec += " (bottled)"
	}
	if f.Versions.Head != "" {
		spec += ", HEAD"
	}
	if f.KegOnly {
		spec += ", keg-only"
	}
	ohai("%s: %s", head, spec)
	if f.Desc != "" {
		fmt.Println(f.Desc)
	}
	fmt.Println(f.Homepage)
	if f.Deprecated {
		reason := ""
		if f.DeprecationReason != nil {
			reason = " because it " + strings.ReplaceAll(*f.DeprecationReason, "_", " ")
		}
		fmt.Printf("Deprecated%s!\n", reason)
	}
	if f.Disabled {
		reason := ""
		if f.DisableReason != nil {
			reason = " because it " + strings.ReplaceAll(*f.DisableReason, "_", " ")
		}
		fmt.Printf("Disabled%s!\n", reason)
	}
	if len(f.ConflictsWith) > 0 {
		fmt.Println("Conflicts with:")
		for i, c := range f.ConflictsWith {
			reason := ""
			if i < len(f.ConflictsWithReasons) && f.ConflictsWithReasons[i] != "" {
				reason = " (because " + f.ConflictsWithReasons[i] + ")"
			}
			fmt.Printf("  %s%s\n", c, reason)
		}
	}
	printInstalledKegs(f.Name)
	if u := formulaSourceURL(f); u != "" {
		fmt.Printf("From: %s\n", u)
	}
	if f.License != "" {
		fmt.Printf("License: %s\n", f.License)
	}
	mark := func(ds []string) string {
		var out []string
		for _, d := range ds {
			if depInstalled(d) {
				out = append(out, d+" "+check())
			} else {
				out = append(out, d+" "+cross())
			}
		}
		return strings.Join(out, ", ")
	}
	if len(f.BuildDependencies)+len(f.Dependencies)+len(f.RecommendedDependencies)+len(f.OptionalDependencies)+len(f.TestDependencies) > 0 {
		ohai("Dependencies")
		if len(f.BuildDependencies) > 0 {
			fmt.Println("Build: " + mark(f.BuildDependencies))
		}
		if len(f.Dependencies) > 0 {
			fmt.Println("Required: " + mark(f.Dependencies))
		}
		if len(f.RecommendedDependencies) > 0 {
			fmt.Println("Recommended: " + mark(f.RecommendedDependencies))
		}
		if len(f.OptionalDependencies) > 0 {
			fmt.Println("Optional: " + mark(f.OptionalDependencies))
		}
		if len(f.TestDependencies) > 0 {
			fmt.Println("Test: " + mark(f.TestDependencies))
		}
	}
	if len(f.Requirements) > 0 {
		ohai("Requirements")
		var build, req []string
		for _, r := range f.Requirements {
			txt, ok := requirementText(r.Name, r.Version)
			mark := cross()
			if ok {
				mark = check()
			}
			if contains(r.Contexts, "build") {
				build = append(build, txt+" "+mark)
			} else {
				req = append(req, txt+" "+mark)
			}
		}
		if len(build) > 0 {
			fmt.Println("Build: " + strings.Join(build, ", "))
		}
		if len(req) > 0 {
			fmt.Println("Required: " + strings.Join(req, ", "))
		}
	}
	if f.Versions.Head != "" {
		ohai("Options")
		fmt.Println("--HEAD\n\tInstall HEAD version")
	}
	if txt := caveatsFor(pkgRef{kind: "formula", name: f.Name, f: f}); txt != "" {
		ohai("Caveats")
		fmt.Println(txt)
	}
	printAnalytics("formula", f.Name)
}

// formulaSourceURL is the GitHub URL of a formula's Ruby source ("" if unknown).
func formulaSourceURL(f *Formula) string {
	if f.RubySourcePath == "" {
		return ""
	}
	repo := "Homebrew/homebrew-core"
	if !formulaFromCore(f) {
		o, r := tapRepo(f.Tap)
		if o == "" {
			return ""
		}
		repo = o + "/" + r
	}
	return "https://github.com/" + repo + "/blob/HEAD/" + f.RubySourcePath
}

// caskSourceURL is the GitHub URL of a cask's Ruby source ("" if unknown).
func caskSourceURL(c *Cask) string {
	if c.RubySourcePath == "" {
		return ""
	}
	repo := "Homebrew/homebrew-cask"
	if !caskFromCore(c) {
		o, r := tapRepo(c.Tap)
		if o == "" {
			return ""
		}
		repo = o + "/" + r
	}
	return "https://github.com/" + repo + "/blob/HEAD/" + c.RubySourcePath
}

// showAnalytics follows Homebrew: shown in interactive `brew info` unless disabled.
var forceAnalytics bool

func printAnalytics(kind, name string) {
	if envSet("HOMEBREW_NO_ANALYTICS") && !forceAnalytics {
		return
	}
	if !forceAnalytics && (!stdoutTTY || envSet("HOMEBREW_NO_GITHUB_API")) {
		return
	}
	body, err := httpGetTimeout(fmt.Sprintf("https://formulae.brew.sh/api/%s/%s.json", kind, name), 4*time.Second)
	if err != nil {
		odebug("analytics: %v", err)
		return
	}
	var m struct {
		Analytics map[string]map[string]map[string]int `json:"analytics"`
	}
	if json.Unmarshal(body, &m) != nil || len(m.Analytics) == 0 {
		return
	}
	ohai("Analytics")
	for _, cat := range []string{"install", "install_on_request", "build_error"} {
		data, ok := m.Analytics[cat]
		if !ok {
			continue
		}
		var parts []string
		for _, period := range []string{"30d", "90d", "365d"} {
			if d, ok := data[period]; ok {
				n := 0
				for k, v := range d {
					if k == name || strings.HasPrefix(k, name+" ") || kind == "cask" {
						n += v
					}
				}
				parts = append(parts, fmt.Sprintf("%s (%s days)", commas(n), strings.TrimSuffix(period, "d")))
			}
		}
		fmt.Printf("%s: %s\n", strings.ReplaceAll(cat, "_", "-"), strings.Join(parts, ", "))
	}
}

func commas(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func httpGetTimeout(url string, d time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: d}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// globalAnalytics prints `brew info --analytics` without arguments.
func globalAnalytics(o Opts) {
	days, _ := o.Val("days")
	if days == "" {
		days = "30"
	}
	cat, _ := o.Val("category")
	if cat == "" {
		cat = "install"
		if o.onlyCask() {
			cat = "cask-install"
		}
	}
	body, err := httpGetTimeout(fmt.Sprintf("https://formulae.brew.sh/api/analytics/%s/%sd.json", cat, days), 15*time.Second)
	if err != nil {
		odie("could not fetch analytics: %v", err)
	}
	var m struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		odie("could not parse analytics: %v", err)
	}
	for i, it := range m.Items {
		if i >= 50 {
			break
		}
		name := fmt.Sprint(it["formula"])
		if v, ok := it["cask"]; ok {
			name = fmt.Sprint(v)
		}
		fmt.Printf("%2d | %-40s | %10v | %s\n", i+1, name, it["count"], it["percent"])
	}
}

func printInstalledKegs(name string) {
	vs := kegVersions(name)
	if len(vs) == 0 {
		fmt.Println("Not installed")
		return
	}
	if len(vs) == 1 {
		fmt.Println("Installed")
	} else {
		fmt.Printf("Installed (%d versions)\n", len(vs))
	}
	linked := linkedVersion(name)
	for _, v := range vs {
		keg := kegPath(name, v)
		star := ""
		if v == linked {
			star = " *"
		}
		fmt.Printf("%s %s%s\n", keg, kegSummary(keg), star)
		if r := readReceipt(keg); r != nil {
			how := "Poured from bottle"
			if !r.PouredFromBottle {
				how = "Built from source"
			}
			via := ""
			if r.LoadedFromAPI {
				via = " using the formulae.brew.sh API"
			}
			when := ""
			if r.Time > 0 {
				when = time.Unix(r.Time, 0).Format(" on 2006-01-02 at 15:04:05")
			}
			fmt.Printf("  %s%s%s\n", how, via, when)
		}
	}
}

func infoInstalledOnly(rack string) {
	v := currentVersion(rack)
	ohai("%s %s: %s", rack, check(), v)
	printInstalledKegs(rack)
	if r := readReceipt(kegPath(rack, v)); r != nil {
		if r.Source.Tap != "" {
			fmt.Printf("From: %s\n", r.Source.Tap)
		}
		if len(r.RuntimeDependencies) > 0 {
			ohai("Dependencies")
			var ds []string
			for _, d := range r.RuntimeDependencies {
				ds = append(ds, baseName(d.FullName))
			}
			fmt.Println("Required: " + strings.Join(ds, ", "))
		}
	}
}

func infoCask(c *Cask) {
	installed := isCaskInstalled(c.Token)
	head := c.Token
	if installed {
		head += " " + check()
	}
	ver := c.Version
	if c.AutoUpdates != nil && *c.AutoUpdates {
		ver += " (auto_updates)"
	}
	ohai("%s: %s", head, ver)
	fmt.Println(c.Homepage)
	if installed {
		fmt.Println("Installed")
		for _, v := range caskVersions(c.Token) {
			p := filepath.Join(caskroom(), c.Token, v)
			fi, _ := os.Stat(p)
			when := ""
			if fi != nil {
				when = fi.ModTime().Format(" (2006-01-02 15:04)")
			}
			fmt.Printf("%s%s\n", p, when)
		}
	} else {
		fmt.Println("Not installed")
	}
	if u := caskSourceURL(c); u != "" {
		fmt.Printf("From: %s\n", u)
	}
	if len(c.Name) > 0 {
		ohai("Name")
		fmt.Println(strings.Join(c.Name, "\n"))
	}
	if d := c.DescString(); d != "" {
		ohai("Description")
		fmt.Println(d)
	}
	if len(c.DependsOn.Formula)+len(c.DependsOn.Cask) > 0 {
		ohai("Dependencies")
		var ds []string
		for _, d := range c.DependsOn.Formula {
			m := cross()
			if depInstalled(d) {
				m = check()
			}
			ds = append(ds, d+" "+m)
		}
		for _, d := range c.DependsOn.Cask {
			m := cross()
			if isCaskInstalled(d) {
				m = check()
			}
			ds = append(ds, d+" (cask) "+m)
		}
		fmt.Println(strings.Join(ds, ", "))
	}
	if arts := caskArtifacts(c); len(arts) > 0 {
		ohai("Artifacts")
		for _, a := range arts {
			fmt.Printf("%s (%s)\n", a.name, artifactSingular[a.kind])
		}
	}
	if c.Caveats != nil && *c.Caveats != "" {
		ohai("Caveats")
		fmt.Println(strings.TrimRight(expandCaveats(*c.Caveats), "\n"))
	}
	printAnalytics("cask", c.Token)
}

func infoJSON(o Opts, version string) {
	if version == "" {
		version = "v1"
	}
	if version != "v1" && version != "v2" {
		odie("Invalid JSON version: %s", version)
	}
	if version == "v1" && o.onlyCask() {
		odie("Invalid usage: Cannot specify `--cask` when using `--json=v1`!")
	}
	// Formulae are resolved up front so tap formulae are reported from their
	// tap, not from a homebrew/core formula with the same name.
	type fref struct {
		name string
		f    *Formula
	}
	var frefs []fref
	var fnames, cnames []string
	// Casks are resolved up front too; c is nil when only the API record is needed.
	type cref struct {
		token string
		c     *Cask
	}
	var crefs []cref
	switch {
	case len(o.Pos) > 0:
		for _, n := range o.Pos {
			if !o.onlyCask() {
				if f := lookupFormula(n); f != nil {
					frefs = append(frefs, fref{f.Name, f})
					continue
				}
				if r := resolveInstalledFormula(n); r != "" {
					frefs = append(frefs, fref{r, nil})
					continue
				}
			}
			if !o.onlyFormula() && version == "v2" {
				if c := lookupCask(n); c != nil {
					crefs = append(crefs, cref{c.Token, c})
					continue
				}
			}
			odie("No available formula with the name \"%s\".", n)
		}
	case o.Has("installed"):
		if !o.onlyCask() {
			fnames = installedFormulae()
		}
		if !o.onlyFormula() && version == "v2" {
			cnames = installedCasks()
		}
	case o.Has("eval-all", "all"):
		if !o.onlyCask() {
			fnames = formulaAPI.Names()
		}
		if !o.onlyFormula() && version == "v2" {
			cnames = caskAPI.Names()
		}
	default:
		odie("Invalid usage: This command's option requires a formula argument")
	}
	for _, n := range fnames {
		var f *Formula
		switch {
		case installedTapOf(n) != "":
			f = lookupFormula(installedTapOf(n) + "/" + n)
		case isFormulaInstalled(n):
			f = lookupFormula(n)
		default:
			// --eval-all: only the API record is needed (formulaMapFor of a
			// bare core formula reads it); skip decoding every formula.
			f = &Formula{Name: n}
		}
		frefs = append(frefs, fref{n, f})
	}
	formulae := []any{}
	for _, r := range frefs {
		formulae = append(formulae, formulaJSON(r.name, r.f))
	}
	if version == "v1" {
		printJSON(formulae)
		return
	}
	for _, t := range cnames {
		var c *Cask
		if isCaskInstalled(t) {
			// A cask installed from a tap is reported from that tap.
			if full := tapQualifiedDeclared("brew-cask", t); len(full) > 0 {
				c = lookupCask(full[0])
			}
		}
		crefs = append(crefs, cref{t, c})
	}
	casks := []any{}
	for _, r := range crefs {
		casks = append(casks, caskJSON(r.token, r.c))
	}
	printJSON(map[string]any{"formulae": formulae, "casks": casks})
}

// formulaJSON returns the JSON record of the installed rack name; f is the
// resolved formula (nil when only the keg is known).
func formulaJSON(name string, f *Formula) map[string]any {
	var m map[string]any
	if f != nil {
		m = formulaMapFor(f)
	}
	if m == nil {
		// Installed from a tap mise has no API data for: synthesize from the receipt.
		v := currentVersion(name)
		m = map[string]any{
			"name": name, "full_name": name, "tap": nil, "oldnames": []any{}, "aliases": []any{},
			"versioned_formulae": []any{}, "desc": nil, "license": nil, "homepage": nil,
			"versions": map[string]any{"stable": v, "head": nil, "bottle": true}, "revision": 0,
			"keg_only": false, "dependencies": []any{}, "build_dependencies": []any{},
			"caveats": nil, "deprecated": false, "disabled": false,
		}
		if r := readReceipt(kegPath(name, v)); r != nil && r.Source.Tap != "" {
			m["tap"] = r.Source.Tap
			m["full_name"] = r.Source.Tap + "/" + name
		}
	}
	var inst []any
	for _, v := range kegVersions(name) {
		e := map[string]any{"version": v, "used_options": []any{}, "built_as_bottle": true, "poured_from_bottle": true,
			"time": nil, "runtime_dependencies": []any{}, "installed_as_dependency": false, "installed_on_request": true}
		if r := readReceipt(kegPath(name, v)); r != nil {
			deps := []any{}
			for _, d := range r.RuntimeDependencies {
				_, rev := splitRevision(d.PkgVersion)
				deps = append(deps, map[string]any{"full_name": d.FullName, "version": d.Version, "revision": rev, "pkg_version": d.PkgVersion, "declared_directly": d.Declared})
			}
			e["built_as_bottle"] = r.BuiltAsBottle
			e["poured_from_bottle"] = r.PouredFromBottle
			e["time"] = r.Time
			e["runtime_dependencies"] = deps
			e["installed_as_dependency"] = r.InstalledAsDependency
			e["installed_on_request"] = r.InstalledOnRequest
			e["used_options"] = nonNil(r.UsedOptions)
		}
		inst = append(inst, e)
	}
	if inst == nil {
		inst = []any{}
	}
	m["installed"] = inst
	if lv := linkedVersion(name); lv != "" {
		m["linked_keg"] = lv
	} else {
		m["linked_keg"] = nil
	}
	m["pinned"] = isPinned(name)
	outdated := false
	if f != nil && len(inst) > 0 {
		outdated = kegOutdated(name, f)
	}
	m["outdated"] = outdated
	return m
}

func nonNil(xs []string) []any {
	out := []any{}
	for _, x := range xs {
		out = append(out, x)
	}
	return out
}

// caskJSON returns the JSON record of cask token; c is the resolved cask
// (nil to use the homebrew/cask API record).
func caskJSON(token string, c *Cask) map[string]any {
	var m map[string]any
	if c != nil && !caskFromCore(c) {
		m = caskMapFor(c)
	} else {
		m = rawCaskMap(token)
	}
	if m == nil {
		m = map[string]any{"token": token, "full_token": token, "name": []any{token}, "version": nil}
	}
	if isCaskInstalled(token) {
		v := currentCaskVersion(token)
		m["installed"] = v
		if fi, err := os.Stat(filepath.Join(caskroom(), token, v)); err == nil {
			m["installed_time"] = fi.ModTime().Unix()
		}
		if c == nil {
			c = lookupCask(token)
		}
		if c != nil {
			m["outdated"] = c.Version != "latest" && !contains(caskVersions(token), c.Version)
		}
	} else {
		m["installed"] = nil
	}
	return m
}

// caskMapFor builds a JSON record for a cask parsed from a third-party tap,
// shaped like the API's: lists are [] rather than null.
func caskMapFor(c *Cask) map[string]any {
	b, err := json.Marshal(c)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	if c.FullToken == "" && c.Tap != "" {
		m["full_token"] = c.Tap + "/" + c.Token
	}
	for _, k := range []string{"old_tokens", "name", "artifacts"} {
		if m[k] == nil {
			m[k] = []any{}
		}
	}
	return m
}

func infoSizes(o Opts) {
	names := o.Pos
	var rows [][2]string
	var total int64
	add := func(label, dir string) {
		_, size := diskUsage(dir)
		total += size
		rows = append(rows, [2]string{label, humanSize(size)})
	}
	if len(names) == 0 {
		if !o.onlyCask() {
			for _, n := range installedFormulae() {
				add(n, filepath.Join(cellar(), n))
			}
		}
		if !o.onlyFormula() {
			for _, t := range installedCasks() {
				add(t, filepath.Join(caskroom(), t))
			}
		}
	}
	for _, n := range names {
		if r := resolveInstalledFormula(n); r != "" && !o.onlyCask() {
			add(r, filepath.Join(cellar(), r))
		} else if t := resolveInstalledCask(n); t != "" {
			add(t, filepath.Join(caskroom(), t))
		} else {
			odie("No such keg: %s", filepath.Join(cellar(), n))
		}
	}
	w := 4
	for _, r := range rows {
		if len(r[0]) > w {
			w = len(r[0])
		}
	}
	for _, r := range rows {
		fmt.Printf("%s %s\n", padRight(r[0], w), r[1])
	}
	if len(rows) > 1 {
		fmt.Printf("%s %s\n", padRight("Total", w), humanSize(total))
	}
}

// --- search / desc ----------------------------------------------------------

func compileQuery(q string) func(string) bool {
	if len(q) > 1 && strings.HasPrefix(q, "/") && strings.HasSuffix(q, "/") {
		re, err := regexp.Compile("(?i)" + q[1:len(q)-1])
		if err != nil {
			odie("Invalid regex: %v", err)
		}
		return re.MatchString
	}
	lq := strings.ToLower(q)
	simp := simplify(lq)
	return func(s string) bool {
		ls := strings.ToLower(s)
		return strings.Contains(ls, lq) || (simp != "" && strings.Contains(simplify(ls), simp))
	}
}

func simplify(s string) string {
	return strings.NewReplacer("-", "", "_", "", "@", "", ".", "", " ", "").Replace(s)
}

type descEntry struct{ name, desc string }

func allFormulaDescs() []descEntry {
	var out []descEntry
	for _, raw := range formulaAPI.All() {
		var m struct {
			Name string `json:"name"`
			Desc string `json:"desc"`
		}
		if json.Unmarshal(raw, &m) == nil {
			out = append(out, descEntry{m.Name, m.Desc})
		}
	}
	return out
}

func allCaskDescs() []descEntry {
	var out []descEntry
	for _, raw := range caskAPI.All() {
		var m struct {
			Token string   `json:"token"`
			Name  []string `json:"name"`
			Desc  *string  `json:"desc"`
		}
		if json.Unmarshal(raw, &m) == nil {
			d := ""
			if m.Desc != nil {
				d = *m.Desc
			}
			if len(m.Name) > 0 {
				d = "(" + strings.Join(m.Name, ", ") + ") " + d
			}
			out = append(out, descEntry{m.Token, d})
		}
	}
	return out
}

func cmdSearch(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	ensureAPI()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 text or regex argument.")
	}
	for _, site := range []string{"alpine", "repology", "macports", "fink", "opensuse", "fedora", "archlinux", "debian", "ubuntu"} {
		if o.Has(site) {
			openURL(searchSiteURL(site, strings.Join(o.Pos, " ")))
			return
		}
	}
	if o.Has("pull-request") {
		searchPullRequests(strings.Join(o.Pos, " "), o)
		return
	}
	match := compileQuery(strings.Join(o.Pos, " "))
	if o.Has("desc") {
		var groups [][]descEntry
		if !o.onlyCask() {
			groups = append(groups, filterDescs(allFormulaDescs(), match, true))
		}
		if !o.onlyFormula() {
			groups = append(groups, filterDescs(allCaskDescs(), match, true))
		}
		printDescGroups(groups, o)
		return
	}
	var formulae, casks []string
	if !o.onlyCask() {
		for _, n := range formulaAPI.Names() {
			if match(n) {
				if stdoutTTY && isFormulaInstalled(n) {
					n += " " + check()
				}
				formulae = append(formulae, n)
			}
		}
	}
	if !o.onlyFormula() {
		for _, n := range caskAPI.Names() {
			if match(n) {
				if stdoutTTY && isCaskInstalled(n) {
					n += " " + check()
				}
				casks = append(casks, n)
			}
		}
	}
	// Third-party taps (names are fully qualified, as Homebrew prints them).
	tf, tc := tapNames()
	if !o.onlyCask() {
		for _, n := range tf {
			if match(baseName(n)) {
				formulae = append(formulae, n)
			}
		}
	}
	if !o.onlyFormula() {
		for _, n := range tc {
			if match(baseName(n)) {
				casks = append(casks, n)
			}
		}
	}
	if len(formulae)+len(casks) == 0 {
		odie("No formulae or casks found for %q.", strings.Join(o.Pos, " "))
	}
	both := !o.onlyFormula() && !o.onlyCask()
	if len(formulae) > 0 {
		if both {
			ohai("Formulae")
		}
		putsColumns(formulae)
	}
	if len(casks) > 0 {
		if both {
			if len(formulae) > 0 {
				fmt.Println()
			}
			ohai("Casks")
		}
		putsColumns(casks)
	}
}

func searchPullRequests(q string, o Opts) {
	repos := []string{"Homebrew/homebrew-core", "Homebrew/homebrew-cask"}
	if o.onlyFormula() {
		repos = repos[:1]
	} else if o.onlyCask() {
		repos = repos[1:]
	}
	state := ""
	switch {
	case o.Has("open") && !o.Has("closed"):
		state = "+is:open"
	case o.Has("closed") && !o.Has("open"):
		state = "+is:closed"
	}
	found := false
	for _, repo := range repos {
		url := fmt.Sprintf("https://api.github.com/search/issues?q=%s+repo:%s+is:pr%s&per_page=30", strings.ReplaceAll(q, " ", "+"), repo, state)
		body, err := httpGet(url, "application/vnd.github+json")
		if err != nil {
			odie("GitHub search failed: %v", err)
		}
		var res struct {
			Items []struct {
				Title   string `json:"title"`
				HTMLURL string `json:"html_url"`
				State   string `json:"state"`
			} `json:"items"`
		}
		_ = json.Unmarshal(body, &res)
		for _, it := range res.Items {
			found = true
			st := ""
			if it.State == "closed" {
				st = " (CLOSED)"
			}
			fmt.Printf("%s%s (%s)\n", it.Title, st, it.HTMLURL)
		}
	}
	if !found {
		odie("No pull requests found for %q.", q)
	}
}

func searchSiteURL(site, q string) string {
	switch site {
	case "alpine":
		return "https://pkgs.alpinelinux.org/packages?name=" + q
	case "repology":
		return "https://repology.org/projects/?search=" + q
	case "macports":
		return "https://ports.macports.org/search/?q=" + q
	case "fink":
		return "https://pdb.finkproject.org/pdb/browse.php?summary=" + q
	case "opensuse":
		return "https://software.opensuse.org/search?q=" + q
	case "fedora":
		return "https://packages.fedoraproject.org/search?query=" + q
	case "archlinux":
		return "https://archlinux.org/packages/?q=" + q
	case "debian":
		return "https://packages.debian.org/search?keywords=" + q
	default:
		return "https://packages.ubuntu.com/search?keywords=" + q
	}
}

func filterDescs(all []descEntry, match func(string) bool, includeDesc bool) []descEntry {
	var out []descEntry
	for _, e := range all {
		if match(e.name) || (includeDesc && match(e.desc)) {
			out = append(out, e)
		}
	}
	return out
}

func printDescGroups(groups [][]descEntry, o Opts) {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total == 0 {
		odie("No formulae or casks found for %q.", strings.Join(o.Pos, " "))
	}
	both := len(groups) == 2 && len(groups[0]) > 0 && len(groups[1]) > 0
	for i, g := range groups {
		if len(g) == 0 {
			continue
		}
		if both {
			if i == 0 {
				ohai("Formulae")
			} else {
				fmt.Println()
				ohai("Casks")
			}
		}
		for _, e := range g {
			fmt.Printf("%s: %s\n", bold(e.name), e.desc)
		}
	}
}

func cmdDesc(args []string) {
	o := parseArgs(args, nil, map[string]string{"s": "search", "n": "name", "d": "description"})
	o.applyGlobal()
	ensureAPI()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula, cask or text argument.")
	}
	if o.Has("search", "name", "description") {
		match := compileQuery(strings.Join(o.Pos, " "))
		f := func(all []descEntry) []descEntry {
			var out []descEntry
			for _, e := range all {
				ok := false
				switch {
				case o.Has("name"):
					ok = match(e.name)
				case o.Has("description"):
					ok = match(e.desc)
				default:
					ok = match(e.name) || match(e.desc)
				}
				if ok {
					out = append(out, e)
				}
			}
			return out
		}
		var groups [][]descEntry
		if !o.onlyCask() {
			groups = append(groups, f(allFormulaDescs()))
		}
		if !o.onlyFormula() {
			groups = append(groups, f(allCaskDescs()))
		}
		printDescGroups(groups, o)
		return
	}
	for _, n := range o.Pos {
		if !o.onlyCask() {
			if f := lookupFormula(n); f != nil {
				d := f.Desc
				if d == "" {
					d = "(no description)"
				}
				fmt.Printf("%s: %s\n", bold(f.Name), d)
				continue
			}
		}
		if !o.onlyFormula() {
			if c := lookupCask(n); c != nil {
				d := c.DescString()
				if len(c.Name) > 0 {
					d = "(" + strings.Join(c.Name, ", ") + ") " + d
				}
				fmt.Printf("%s: %s\n", bold(c.Token), d)
				continue
			}
		}
		odie("No available formula or cask with the name \"%s\".", n)
	}
}

// --- deps / uses ------------------------------------------------------------

type depFilter struct {
	build, test, optional, skipRecommended bool
}

func depFilterFrom(o Opts) depFilter {
	return depFilter{
		build:           o.Has("include-build"),
		test:            o.Has("include-test"),
		optional:        o.Has("include-optional"),
		skipRecommended: o.Has("skip-recommended"),
	}
}

// directDeps lists the direct dependencies of a formula. name may be
// tap-qualified; dependencies on third-party tap formulae keep their
// user/repo/name form (core ones are bare names).
func directDeps(name string, flt depFilter) []string {
	f := lookupFormula(name)
	if f == nil {
		// Unknown to the API and taps: fall back to the install receipt.
		var out []string
		rack := baseName(name)
		if r := readReceipt(kegPath(rack, currentVersion(rack))); r != nil {
			for _, d := range r.RuntimeDependencies {
				if d.Declared {
					out = append(out, stripCoreTap(d.FullName))
				}
			}
		}
		return out
	}
	seen := map[string]bool{}
	var out []string
	add := func(ds ...string) {
		for _, d := range ds {
			d = tapDepName(f, d)
			if !seen[d] {
				seen[d] = true
				out = append(out, d)
			}
		}
	}
	if flt.build {
		add(f.BuildDependencies...)
	}
	add(f.Dependencies...)
	if !flt.skipRecommended {
		add(f.RecommendedDependencies...)
	}
	if flt.optional {
		add(f.OptionalDependencies...)
	}
	if flt.test {
		add(f.TestDependencies...)
	}
	for i, e := range usesFromMacos(f) {
		d, typ := e[0], e[1]
		if !macosDepApplies(f, i) {
			continue
		}
		if typ == "" || (typ == "build" && flt.build) || (typ == "test" && flt.test) {
			add(d)
		}
	}
	return out
}

// tapDepName normalises a dependency name declared by f: homebrew/core
// prefixes are dropped, and a bare name in a third-party tap formula that is
// not a core formula but exists in the same tap is qualified with that tap.
func tapDepName(f *Formula, d string) string {
	d = stripCoreTap(d)
	if formulaFromCore(f) || strings.Contains(d, "/") || f.Tap == "" {
		return d
	}
	if formulaAPI.Available() {
		if _, ok := formulaAPI.Raw(d); ok {
			return d
		}
	}
	if ti := loadTapIndex(f.Tap); ti != nil {
		if _, ok := ti.Formulae[d]; ok {
			return f.Tap + "/" + d
		}
	}
	return d
}

// depInstalled reports whether a (possibly tap-qualified) dependency has a keg.
func depInstalled(d string) bool { return isFormulaInstalled(baseName(d)) }

// sameDep reports whether declared dependency x refers to dep.
func sameDep(f *Formula, x, dep string) bool { return x == dep || tapDepName(f, x) == dep }

func containsDep(f *Formula, xs []string, dep string) bool {
	for _, x := range xs {
		if sameDep(f, x, dep) {
			return true
		}
	}
	return false
}

// recursiveDeps returns deps in topological order (dependencies before dependents).
func recursiveDeps(f *Formula, flt depFilter) []string {
	var order []string
	root := qualifiedFormulaName(f)
	seen := map[string]bool{root: true}
	var visit func(string)
	visit = func(n string) {
		for _, d := range directDeps(n, flt) {
			if seen[d] {
				continue
			}
			seen[d] = true
			visit(d)
			order = append(order, d)
		}
	}
	visit(root)
	return order
}

func depsOf(name string, flt depFilter, direct bool) []string {
	if direct {
		return directDeps(name, flt)
	}
	f := lookupFormula(name)
	if f == nil {
		f = &Formula{Name: name}
	}
	return recursiveDeps(f, flt)
}

// depType classifies how formula `parent` depends on `dep` (for --annotate).
func depType(parent, dep string) string {
	if strings.HasPrefix(parent, "cask:") {
		return ""
	}
	f := lookupFormula(parent)
	if f == nil {
		return ""
	}
	switch {
	case containsDep(f, f.BuildDependencies, dep):
		return "build"
	case containsDep(f, f.TestDependencies, dep):
		return "test"
	case containsDep(f, f.OptionalDependencies, dep):
		return "optional"
	case containsDep(f, f.RecommendedDependencies, dep):
		return "recommended"
	}
	for i, e := range usesFromMacos(f) {
		if e[0] == dep && macosDepApplies(f, i) {
			if e[1] != "" {
				return e[1]
			}
			return "implicit"
		}
	}
	return ""
}

func requirementsOf(name string) []string {
	f := lookupFormula(name)
	if f == nil {
		return nil
	}
	var out []string
	for _, r := range f.Requirements {
		out = append(out, ":"+r.Name)
	}
	return out
}

func cmdDeps(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if osv, ok := o.Val("os"); ok || o.Has("arch") {
		arch, _ := o.Val("arch")
		if arch == "" {
			arch = map[string]string{"arm64": "arm64", "amd64": "x86_64"}[runtime.GOARCH]
		}
		switch {
		case osv == "linux":
			os.Setenv("MISEBREW_BOTTLE_TAG", arch+"_linux")
		case osv != "" && osv != "all":
			t := strings.TrimPrefix(osv, "macos-")
			if m := macosMajorFor(t); m > 0 {
				t = macosCodename(m)
				os.Setenv("MISEBREW_MACOS_VERSION", strconv.Itoa(m))
			}
			if arch == "arm64" {
				t = "arm64_" + t
			}
			os.Setenv("MISEBREW_BOTTLE_TAG", t)
		}
	}
	flt := depFilterFrom(o)
	direct := o.Has("direct")
	topo := o.Has("topological")
	names := o.Pos
	forEach := o.Has("for-each")
	if len(names) == 0 {
		switch {
		case o.Has("installed"):
			names = installedFormulae()
			forEach = true
		case o.Has("eval-all"):
			names = formulaAPI.Names()
			forEach = true
		default:
			odie("Invalid usage: This command requires at least 1 formula argument.")
		}
	}
	// Formulae are keyed by name (tap-qualified for third-party taps), casks
	// by "cask:" + token (likewise). Formulae win unless --cask is given, so
	// the (slow) cask lookup only runs for names that are not formulae.
	var canon []string
	for _, n := range names {
		if !o.onlyCask() {
			if f := lookupFormula(n); f != nil {
				canon = append(canon, qualifiedFormulaName(f))
				continue
			}
			if r := resolveInstalledFormula(n); r != "" {
				canon = append(canon, r)
				continue
			}
		}
		if !o.onlyFormula() {
			if c := lookupCask(n); c != nil {
				canon = append(canon, "cask:"+qualifiedCaskToken(c))
				continue
			}
		}
		if o.onlyCask() {
			odie("No available cask with the name \"%s\".", n)
		}
		odie("No available formula with the name \"%s\".", n)
	}
	// caskDeps returns the formula dependencies of a "cask:" key, and its
	// cask dependencies as "cask:" keys.
	caskDeps := func(n string) []string {
		c := lookupCask(strings.TrimPrefix(n, "cask:"))
		if c == nil {
			return nil
		}
		ds := append([]string{}, c.DependsOn.Formula...)
		for _, d := range c.DependsOn.Cask {
			ds = append(ds, "cask:"+d)
		}
		return ds
	}
	// Installed formulae use their installed runtime dependencies unless
	// --declared (or build/test deps, which receipts don't record) is asked for.
	useInstalled := o.Has("installed") && !o.Has("declared") && !flt.build && !flt.test
	label := func(d string) string {
		if o.Has("full-name") {
			if f := lookupFormula(d); f != nil && f.FullName != "" {
				return f.FullName
			}
		}
		return d
	}
	get := func(n string) []string {
		var ds []string
		switch {
		case strings.HasPrefix(n, "cask:"):
			if c := lookupCask(strings.TrimPrefix(n, "cask:")); c != nil {
				for _, d := range c.DependsOn.Formula {
					ds = append(ds, d)
					if !direct {
						ds = append(ds, depsOf(d, flt, false)...)
					}
				}
			}
			ds = dedupe(ds)
		case useInstalled && depInstalled(n):
			rack := baseName(n)
			if r := readReceipt(kegPath(rack, currentVersion(rack))); r != nil {
				for _, d := range r.RuntimeDependencies {
					if !direct || d.Declared {
						ds = append(ds, stripCoreTap(d.FullName))
					}
				}
			}
		default:
			ds = depsOf(n, flt, direct)
		}
		if o.Has("installed") {
			var keep []string
			for _, d := range ds {
				if depInstalled(d) {
					keep = append(keep, d)
				}
			}
			ds = keep
		}
		if o.Has("missing") {
			var keep []string
			for _, d := range ds {
				if !depInstalled(d) {
					keep = append(keep, d)
				}
			}
			ds = keep
		}
		if o.Has("include-requirements") && !strings.HasPrefix(n, "cask:") {
			ds = append(requirementsOf(n), ds...)
		}
		if !topo {
			sort.Strings(ds)
		}
		return ds
	}
	annotate := func(parent, d string) string {
		s := label(d)
		if o.Has("annotate") {
			if t := depType(parent, d); t != "" {
				s += " [" + t + "]"
			}
		}
		return s
	}
	if o.Has("graph") || o.Has("dot") {
		var b strings.Builder
		b.WriteString("digraph {\n")
		seen := map[string]bool{}
		var walk func(string)
		walk = func(n string) {
			if seen[n] {
				return
			}
			seen[n] = true
			var ds []string
			if strings.HasPrefix(n, "cask:") {
				ds = caskDeps(n)
			} else {
				ds = directDeps(n, flt)
			}
			for _, d := range ds {
				style := ""
				if t := depType(n, d); t == "build" || t == "test" || t == "optional" {
					style = " [style=dashed]"
				}
				fmt.Fprintf(&b, "  %s -> %s%s\n", dotQuote(strings.TrimPrefix(n, "cask:")), dotQuote(strings.TrimPrefix(d, "cask:")), style)
				if !direct {
					walk(d)
				}
			}
		}
		for _, n := range canon {
			walk(n)
		}
		b.WriteString("}\n")
		if o.Has("dot") || !stdoutTTY {
			fmt.Print(b.String())
			return
		}
		html := "<!DOCTYPE html><html><body><script src=\"https://cdn.jsdelivr.net/npm/viz.js@2.1.2/viz.js\"></script><script src=\"https://cdn.jsdelivr.net/npm/viz.js@2.1.2/full.render.js\"></script><div id=g></div><script>new Viz().renderSVGElement(" + jsString(b.String()) + ").then(e=>document.getElementById('g').appendChild(e))</script></body></html>"
		p := filepath.Join(miseBrewCacheDir(), "deps-graph.html")
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(html), 0o644); err != nil {
			odie("%v", err)
		}
		openURL("file://" + p)
		return
	}
	if o.Has("tree") {
		for i, n := range canon {
			if i > 0 {
				fmt.Println()
			}
			fmt.Println(label(strings.TrimPrefix(n, "cask:")))
			pruned := map[string]bool{}
			printTree(n, flt, "", map[string]bool{}, o.Has("installed"), o.Has("prune"), pruned, annotate, o.Has("include-requirements"))
		}
		return
	}
	if forEach {
		for _, n := range canon {
			var ds []string
			for _, d := range get(n) {
				ds = append(ds, annotate(n, d))
			}
			fmt.Printf("%s: %s\n", label(strings.TrimPrefix(n, "cask:")), strings.Join(ds, " "))
		}
		return
	}
	var result []string
	if o.Has("union") || len(canon) == 1 {
		set := map[string]bool{}
		for _, n := range canon {
			for _, d := range get(n) {
				if !set[d] {
					set[d] = true
					result = append(result, d)
				}
			}
		}
		if !topo {
			sort.Strings(result)
		}
	} else {
		count := map[string]int{}
		for _, n := range canon {
			for _, d := range get(n) {
				count[d]++
			}
		}
		for _, d := range get(canon[0]) {
			if count[d] == len(canon) {
				result = append(result, d)
			}
		}
	}
	for _, d := range result {
		if strings.HasPrefix(d, ":") {
			fmt.Println(d)
			continue
		}
		fmt.Println(annotate(canon[0], d))
	}
}

// dotQuote renders s as a Graphviz quoted ID.
func dotQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// jsString renders s as a JavaScript string literal safe inside <script>:
// <, > and & are escaped as \u003c etc., as are U+2028/U+2029.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func printTree(n string, flt depFilter, indent string, path map[string]bool, installedOnly, prune bool, pruned map[string]bool, annotate func(string, string) string, reqs bool) {
	var ds []string
	if strings.HasPrefix(n, "cask:") {
		if c := lookupCask(strings.TrimPrefix(n, "cask:")); c != nil {
			ds = append(ds, c.DependsOn.Formula...)
		}
	} else {
		ds = directDeps(n, flt)
	}
	if installedOnly {
		var keep []string
		for _, d := range ds {
			if depInstalled(d) {
				keep = append(keep, d)
			}
		}
		ds = keep
	}
	sort.Strings(ds)
	if reqs && !strings.HasPrefix(n, "cask:") {
		ds = append(requirementsOf(n), ds...)
	}
	for i, d := range ds {
		last := i == len(ds)-1
		branch, next := "├── ", "│   "
		if last {
			branch, next = "└── ", "    "
		}
		if strings.HasPrefix(d, ":") {
			fmt.Println(indent + branch + d)
			continue
		}
		line := annotate(n, d)
		if prune && pruned[d] {
			fmt.Println(indent + branch + line + " (pruned)")
			continue
		}
		fmt.Println(indent + branch + line)
		if path[d] {
			continue
		}
		pruned[d] = true
		path[d] = true
		printTree(d, flt, indent+next, path, installedOnly, prune, pruned, annotate, reqs)
		delete(path, d)
	}
}

func cmdUses(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula argument.")
	}
	flt := depFilterFrom(o)
	var targets []string
	missing := false
	for _, n := range o.Pos {
		if f := lookupFormula(n); f != nil {
			targets = append(targets, qualifiedFormulaName(f))
		} else {
			// Like Homebrew: warn, then look for dependents of the bare name.
			opoo("No available formula with the name \"%s\".", n)
			missing = true
			targets = append(targets, stripCoreTap(n))
		}
	}
	var candidates []string
	switch {
	case o.onlyCask():
	case o.Has("installed"):
		candidates = installedFormulae()
	default:
		ensureAPI()
		candidates = formulaAPI.Names()
	}
	var out []string
	for _, c := range candidates {
		var ds []string
		if o.Has("recursive") {
			ds = depsOf(c, flt, false)
		} else {
			ds = directDeps(c, flt)
		}
		all := true
		for _, t := range targets {
			if !contains(ds, t) {
				all = false
				break
			}
		}
		if all {
			out = append(out, c)
		}
	}
	if !o.onlyFormula() && !o.Has("installed") {
		for _, raw := range caskAPI.All() {
			var m struct {
				Token     string `json:"token"`
				DependsOn struct {
					Formula []string `json:"formula"`
				} `json:"depends_on"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			all := true
			for _, t := range targets {
				if !contains(m.DependsOn.Formula, t) {
					all = false
				}
			}
			if all && len(m.DependsOn.Formula) > 0 {
				out = append(out, m.Token)
			}
		}
	} else if !o.onlyFormula() {
		for _, t := range installedCasks() {
			if c := lookupCask(t); c != nil {
				all := true
				for _, x := range targets {
					if !contains(c.DependsOn.Formula, x) {
						all = false
					}
				}
				if all && len(c.DependsOn.Formula) > 0 {
					out = append(out, t)
				}
			}
		}
	}
	if o.Has("missing") {
		var keep []string
		for _, n := range out {
			if !isFormulaInstalled(n) {
				keep = append(keep, n)
			}
		}
		out = keep
	}
	if len(out) == 0 {
		return
	}
	sort.Strings(out)
	putsColumns(out)
	if missing {
		odie("Missing formulae should not have dependents!")
	}
}

func cmdLeaves(args []string) {
	o := parseArgs(args, nil, map[string]string{"r": "installed-on-request", "p": "installed-as-dependency"})
	deps := installedRuntimeDeps()
	used := map[string]bool{}
	for _, ds := range deps {
		for _, d := range ds {
			used[d] = true
		}
	}
	var declared map[string]bool
	if o.Has("installed-on-request", "installed-as-dependency") {
		declared = declaredSet("brew")
	}
	var out []string
	for n := range deps {
		if used[n] {
			continue
		}
		if declared != nil {
			r := readReceipt(kegPath(n, currentVersion(n)))
			onReq := declared[n] || (r != nil && r.InstalledOnRequest)
			if o.Has("installed-on-request") && !onReq {
				continue
			}
			if o.Has("installed-as-dependency") && onReq {
				continue
			}
		}
		out = append(out, n)
	}
	sort.Strings(out)
	for _, n := range out {
		fmt.Println(n)
	}
}

func cmdMissing(args []string) {
	o := parseArgs(args, nil, nil)
	names := installedFormulae()
	if len(o.Pos) > 0 {
		names = requireInstalled(o.Pos)
	}
	// Like Homebrew, --hide pretends the listed formulae are not installed,
	// so dependencies on them are reported as missing.
	hide := map[string]bool{}
	if v, ok := o.Val("hide"); ok {
		for _, h := range strings.Split(v, ",") {
			hide[baseName(strings.TrimSpace(h))] = true
		}
	}
	found := false
	for _, n := range names {
		r := readReceipt(kegPath(n, currentVersion(n)))
		if r == nil {
			continue
		}
		var miss []string
		for _, d := range r.RuntimeDependencies {
			b := baseName(d.FullName)
			if hide[b] || !isFormulaInstalled(b) {
				miss = append(miss, b)
			}
		}
		if len(miss) > 0 {
			found = true
			sort.Strings(miss)
			if len(names) > 1 {
				fmt.Printf("%s: ", n)
			}
			fmt.Println(strings.Join(miss, " "))
		}
	}
	if found {
		exit(1)
	}
}

// --- misc lookups -----------------------------------------------------------

func openURL(url string) {
	browser := os.Getenv("HOMEBREW_BROWSER")
	if browser == "" {
		browser = os.Getenv("BROWSER")
	}
	if browser == "" {
		if runtime.GOOS == "darwin" {
			browser = "open"
		} else {
			browser = "xdg-open"
		}
	}
	fmt.Printf("Opening %s\n", url)
	cmd := exec.Command(browser, url)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		odie("could not open %s: %v", url, err)
	}
}

func cmdHome(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		openURL("https://brew.sh")
		return
	}
	for _, n := range o.Pos {
		if f := lookupFormula(n); f != nil && !o.onlyCask() {
			openURL(f.Homepage)
		} else if c := lookupCask(n); c != nil && !o.onlyFormula() {
			openURL(c.Homepage)
		} else {
			odie("No available formula or cask with the name \"%s\".", n)
		}
	}
}

func cmdDocs([]string) { openURL("https://docs.brew.sh") }

func refOr(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}

func cmdFormulae([]string) {
	ensureAPI()
	for _, n := range formulaAPI.Names() {
		fmt.Println(n)
	}
}

func cmdCasks([]string) {
	ensureAPI()
	for _, n := range caskAPI.Names() {
		fmt.Println(n)
	}
}

func cmdOptions(args []string) {
	o := parseArgs(args, nil, nil)
	names := o.Pos
	if o.Has("installed") {
		names = installedFormulae()
	} else if o.Has("eval-all") {
		names = formulaAPI.Names()
	}
	if c, ok := o.Val("command"); ok {
		c = canonical(c)
		if _, ok := commands[c]; !ok {
			odie("Unknown command: %s", c)
		}
		var opts []string
		for _, f := range commandSpecs[c] {
			opts = append(opts, "--"+f.long)
		}
		if o.Has("compact") {
			fmt.Println(strings.Join(opts, " "))
		} else {
			fmt.Println(strings.Join(opts, "\n"))
		}
		return
	}
	for i, n := range names {
		f := lookupFormula(n)
		if f == nil {
			odie("No available formula with the name \"%s\".", n)
		}
		// Bottles carry no build options; HEAD is the only one Homebrew lists.
		if f.Versions.Head == "" {
			continue
		}
		if len(names) > 1 && !o.Has("compact") {
			if i > 0 {
				fmt.Println()
			}
			fmt.Println(f.Name)
		}
		if o.Has("compact") {
			fmt.Println("--HEAD")
		} else {
			fmt.Println("--HEAD\n\tInstall HEAD version")
		}
	}
}

func cmdWhichFormula(args []string) {
	o := parseArgs(args, nil, nil)
	ensureAPI()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 command argument.")
	}
	want := map[string]bool{}
	for _, c := range o.Pos {
		want[c] = true
	}
	found := map[string][]string{}
	for _, raw := range formulaAPI.All() {
		var m struct {
			Name        string   `json:"name"`
			Executables []string `json:"executables"`
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for _, e := range m.Executables {
			if want[e] {
				found[e] = append(found[e], m.Name)
			}
		}
	}
	failed := false
	for _, c := range o.Pos {
		fs := found[c]
		if len(fs) == 0 {
			failed = true
			continue
		}
		sort.Strings(fs)
		if o.Has("explain") {
			fmt.Printf("The program '%s' is currently not installed. You can install it by typing:\n  brew install %s\n", c, fs[0])
		} else {
			for _, f := range fs {
				fmt.Println(f)
			}
		}
	}
	if failed {
		exit(1)
	}
}
