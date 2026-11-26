package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/rubnogueira/mise-brew/internal/brewfile"
	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

func brewfilePath(o Opts) string {
	if f, ok := o.Val("file"); ok && f != "" {
		return f
	}
	if o.Has("global") {
		if f := os.Getenv("HOMEBREW_BUNDLE_FILE_GLOBAL"); f != "" {
			return f
		}
		if x := os.Getenv("XDG_CONFIG_HOME"); x != "" && paths.FileExists(filepath.Join(x, "homebrew", "Brewfile")) {
			return filepath.Join(x, "homebrew", "Brewfile")
		}
		return filepath.Join(paths.HomeDir(), ".Brewfile")
	}
	if f := os.Getenv("HOMEBREW_BUNDLE_FILE"); f != "" {
		return f
	}
	return "Brewfile"
}

// readBrewfile parses the Brewfile. incomplete reports that some of it was
// skipped, so it must not be used to decide what to remove.
func readBrewfile(o Opts) (entries []brewfile.BrewfileEntry, incomplete bool) {
	p := brewfilePath(o)
	if p != "-" && !paths.FileExists(p) {
		ui.Odie("No Brewfile found: %s", p)
	}
	rubyFailed := false
	if ruby := brewfile.FindRuby(); ruby != "" && os.Getenv("MISEBREW_BUNDLE_NO_RUBY") == "" {
		entries, incomplete, err := brewfile.RubyBrewfile(ruby, p)
		if err == nil {
			return entries, incomplete
		}
		ui.Opoo("%v; falling back to the built-in Brewfile parser", err)
		// Ruby could not evaluate it, so the fallback parse is a best effort:
		// never let a destructive cleanup trust it.
		rubyFailed = true
	}
	if p == "-" {
		entries, incomplete := brewfile.ParseBrewfileFallback(bytes.NewReader(brewfile.StdinBrewfile()), "stdin")
		return entries, incomplete || rubyFailed
	}
	f, err := os.Open(p)
	if err != nil {
		ui.Odie("No Brewfile found: %s", p)
	}
	defer f.Close()
	entries, incomplete = brewfile.ParseBrewfileFallback(f, p)
	return entries, incomplete || rubyFailed
}

// refuseIncomplete stops a cleanup that would act on a partially parsed Brewfile.
func refuseIncomplete(what string) {
	ui.Odie("Refusing to run `brew bundle %s`: parts of the Brewfile could not be evaluated (see the warnings above), so packages it declares could be removed.\nInstall Ruby >= 2.6 (or set MISEBREW_RUBY) so the Brewfile can be evaluated in full, or simplify it.", what)
}

func editorCLI(e brewfile.BrewfileEntry) string {
	ed := e.OptString("editor")
	if ed == "" {
		ed = os.Getenv("HOMEBREW_BUNDLE_VSCODE_EDITOR")
	}
	if ed != "" {
		return ed
	}
	for _, c := range []string{"code", "codium", "cursor", "code-insiders"} {
		if _, err := exec.LookPath(c); err == nil {
			return c
		}
	}
	return "code"
}

func vscodeExtensions(cli string) map[string]bool {
	out := map[string]bool{}
	b, err := exec.Command(cli, "--list-extensions").Output()
	if err != nil {
		return out
	}
	for _, l := range strings.Fields(string(b)) {
		out[strings.ToLower(l)] = true
	}
	return out
}

func runShell(script string) error {
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// --- commands ---------------------------------------------------------------

func cmdBundle(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	sub := "install"
	pos := o.Pos
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	if o.Has("install") && sub != "install" {
		bundleInstall(o)
	}
	switch sub {
	case "install", "upgrade":
		if sub == "upgrade" {
			o.flags["upgrade"] = ""
		}
		bundleInstall(o)
	case "check":
		if !bundleCheck(o, true) {
			ui.Exit(1)
		}
	case "list":
		bundleList(o)
	case "dump":
		bundleDump(o)
	case "cleanup":
		bundleCleanup(o)
	case "exec", "sh", "env":
		bundleExec(o, sub, pos)
	case "add":
		bundleEdit(o, pos, true)
	case "remove":
		bundleEdit(o, pos, false)
	case "edit":
		openInEditor(brewfilePath(o))
	default:
		ui.Odie("Unknown `brew bundle` subcommand: %s", sub)
	}
}

func bundleNoUpgrade(o Opts) bool {
	if o.Has("upgrade") {
		return false
	}
	return o.Has("no-upgrade") || envSet("HOMEBREW_BUNDLE_NO_UPGRADE")
}

func bundleInstall(o Opts) {
	entries, incomplete := readBrewfile(o)
	if incomplete && o.Has("cleanup") && !o.Has("dry-run") {
		refuseIncomplete("install --cleanup")
	}
	dry := o.Has("dry-run")
	files := mise.ConfigFiles()
	taps := homebrew.ConfiguredTaps()
	for _, e := range entries {
		if e.Kind != "tap" {
			continue
		}
		t := strings.ToLower(e.Name)
		if _, ok := taps[t]; ok || strings.HasPrefix(t, "homebrew/") {
			continue
		}
		a := append([]string{"bootstrap", "packages", "brew", "tap"}, mise.TapScopeArgs()...)
		if dry {
			a = append(a, "-n")
		}
		a = append(a, t)
		if e.Extra != "" {
			a = append(a, e.Extra)
		}
		if code := mise.RunMise(a...); code != 0 {
			ui.Exit(code)
		}
	}
	if !dry { // conflicting formulae are unlinked first, as brew bundle does
		func() {
			defer homebrew.LockPrefix()()
			for _, e := range entries {
				for _, c := range e.OptStrings("conflicts_with") {
					if r := homebrew.ResolveInstalledFormula(c); r != "" && homebrew.IsKegLinked(r) {
						fmt.Printf("Unlinking %s (conflicts with %s)\n", r, e.Name)
						homebrew.UnlinkKeg(r, homebrew.LinkedVersion(r), false)
					}
				}
			}
		}()
	}
	// Group packages by the cask appdir they need (mise reads it from the env).
	type group struct {
		env        []string
		use, apply []string
	}
	groups := map[string]*group{}
	globalAppdir := ""
	if v, ok := brewfile.BundleCaskArgs["appdir"]; ok {
		globalAppdir = homebrew.ExpandUserPath(fmt.Sprint(v))
	}
	var tools, upgrades []string
	for _, e := range entries {
		if e.Kind == "brew" {
			if a := e.OptStrings("args"); len(a) > 0 {
				ui.Opoo("brew \"%s\": args %v need a source build and were ignored (mise installs bottles).", e.Name, a)
			}
		}
		k := brewfile.PackageKey(e)
		if k == "" {
			if tk := brewfile.ToolKey(e); tk != "" {
				tools = append(tools, tk)
			}
			continue
		}
		appdir := globalAppdir
		if m, ok := e.Options["args"].(map[string]any); ok && e.Kind == "cask" {
			if v, ok := m["appdir"]; ok {
				appdir = homebrew.ExpandUserPath(fmt.Sprint(v))
			}
		}
		g := groups[appdir]
		if g == nil {
			g = &group{}
			if appdir != "" {
				g.env = []string{"MISE_BREW_CASK_OPT_APPDIR=" + appdir}
			}
			groups[appdir] = g
		}
		if !bundleNoUpgrade(o) {
			if r := homebrew.ResolveInstalledFormula(e.Name); e.Kind == "brew" && r != "" {
				if len(outdatedFormulae([]string{r}, false)) > 0 && !homebrew.IsPinned(r) {
					upgrades = append(upgrades, formulaKey(r))
				}
			} else if t := homebrew.ResolveInstalledCask(e.Name); e.Kind == "cask" && t != "" {
				gr := greedy{}
				if b, _ := e.OptBool("greedy"); b {
					gr = greedy{true, true}
				}
				if len(outdatedCasks([]string{t}, gr)) > 0 {
					upgrades = append(upgrades, "brew-cask:"+t)
				}
			}
		}
		if !mise.RecordEnabled() || mise.IsDeclared(k, files) {
			g.apply = append(g.apply, k)
		} else {
			g.use = append(g.use, k)
		}
	}
	before, beforeCasks := snapshotKegs(), snapshotCasks()
	r := &miseRun{}
	runKeys := func(env []string, a []string, keys []string) {
		if len(keys) == 0 {
			return
		}
		if dry {
			a = append(a, "-n")
		}
		r.extraEnv = env
		if code := runMiseBrewStyle(r, append(a, keys...)...); code != 0 {
			ui.Exit(code)
		}
	}
	var dirs []string
	for d := range groups {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		g := groups[d]
		use := append([]string{"bootstrap", "packages", "use"}, mise.ScopeArgs()...)
		runKeys(g.env, append(use, "-y"), g.use)
		runKeys(g.env, []string{"bootstrap", "packages", "apply", "-y"}, g.apply)
	}
	runKeys(nil, []string{"bootstrap", "packages", "upgrade", "-y"}, upgrades)
	if len(tools) > 0 {
		a := append([]string{"use"}, mise.ScopeArgs()...)
		if dry {
			a = append(a, "--dry-run")
		}
		if code := mise.RunMise(append(a, tools...)...); code != 0 {
			ui.Exit(code)
		}
	}
	// Editor extensions and whalebrew images are installed by their own CLIs.
	for _, e := range entries {
		switch e.Kind {
		case "vscode":
			cli := editorCLI(e)
			if vscodeExtensions(cli)[strings.ToLower(e.Name)] {
				continue
			}
			fmt.Printf("Installing %s extension %s\n", cli, e.Name)
			if !dry {
				cmd := exec.Command(cli, "--install-extension", e.Name)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					ui.Onoe("Installing %s has failed!", e.Name)
					ui.Exit(1)
				}
			}
		case "whalebrew":
			if _, err := exec.LookPath("whalebrew"); err != nil {
				ui.Opoo("Skipping whalebrew \"%s\": whalebrew is not installed", e.Name)
				continue
			}
			if !dry {
				cmd := exec.Command("whalebrew", "install", e.Name)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				_ = cmd.Run()
			}
		}
	}
	if dry {
		return
	}
	changed := map[string]bool{}
	for _, n := range changedFormulae(before) {
		changed[n] = true
	}
	newCasks := changedCasks(beforeCasks)
	failures := 0
	for _, e := range entries {
		if e.Kind == "cask" {
			// As in Homebrew, only casks this run installed or upgraded.
			if pi := e.OptString("postinstall"); pi != "" && newCasks[homebrew.ResolveInstalledCask(e.Name)] {
				if err := runShell(pi); err != nil {
					ui.Onoe("postinstall for %s failed: %v", e.Name, err)
					failures++
				}
			}
			continue
		}
		if e.Kind != "brew" {
			continue
		}
		n := homebrew.ResolveInstalledFormula(e.Name)
		if n == "" {
			continue
		}
		if l, overwrite, ok := e.LinkOption(); ok {
			if !bundleLink(n, l, overwrite) {
				failures++
			}
		}
		if s := findService(n); s != nil {
			switch rs := e.RestartService(); {
			case rs == "always" || (rs == "changed" && changed[n]):
				if s.state().Loaded {
					serviceStop(s, false)
				}
				serviceStart(s, "", "")
			case e.StartService() && !s.state().Running:
				serviceStart(s, "", "")
			}
		}
		if pi := e.OptString("postinstall"); pi != "" && changed[n] {
			if err := runShell(pi); err != nil {
				ui.Onoe("postinstall for %s failed: %v", n, err)
				failures++
			}
		}
	}
	installCleanup(changedFormulae(before))
	if failures > 0 {
		ui.Onoe("`brew bundle` failed! %d Brewfile %s failed to install", failures, map[bool]string{true: "dependency", false: "dependencies"}[failures == 1])
		ui.Exit(1)
	}
	if !ui.Quiet {
		n := 0
		for _, e := range entries {
			if e.Kind != "tap" {
				n++
			}
		}
		ui.Oh1("`brew bundle` complete! %d Brewfile %s now installed.", n, map[bool]string{true: "dependency", false: "dependencies"}[n == 1])
	}
	if o.Has("cleanup") {
		o.flags["force"] = ""
		bundleCleanup(o)
	}
}

// bundleLink applies a Brewfile `link:` option; false when linking failed.
func bundleLink(n string, link, overwrite bool) bool {
	defer homebrew.LockPrefix()()
	if link && !homebrew.IsKegLinked(n) {
		_, conflicts, err := homebrew.LinkKeg(n, homebrew.CurrentVersion(n), overwrite, false)
		if err != nil {
			ui.Onoe("Linking %s failed: %v", n, err)
			return false
		}
		if len(conflicts) > 0 {
			c := conflicts[0]
			ui.Onoe("Could not symlink %s\nTarget %s\nalready exists. Use `link: :overwrite` in the Brewfile to overwrite it.", c.Rel, c.Dst)
			return false
		}
	} else if !link && homebrew.IsKegLinked(n) {
		homebrew.UnlinkKeg(n, homebrew.LinkedVersion(n), false)
	}
	return true
}

// snapshotCasks records the installed version directories of every cask.
func snapshotCasks() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, t := range homebrew.InstalledCasks() {
		out[t] = map[string]bool{}
		for _, v := range homebrew.CaskVersions(t) {
			out[t][v] = true
		}
	}
	return out
}

// changedCasks lists casks that gained a version since the snapshot.
func changedCasks(before map[string]map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, t := range homebrew.InstalledCasks() {
		for _, v := range homebrew.CaskVersions(t) {
			if !before[t][v] {
				out[t] = true
				break
			}
		}
	}
	return out
}

func bundleCheck(o Opts, report bool) bool {
	entries, _ := readBrewfile(o)
	taps := homebrew.ConfiguredTaps()
	var missing []string
	var masState map[string]bool
	exts := map[string]map[string]bool{}
	checkUpgrade := !bundleNoUpgrade(o)
	for _, e := range entries {
		switch e.Kind {
		case "tap":
			t := strings.ToLower(e.Name)
			if _, ok := taps[t]; !ok && !strings.HasPrefix(t, "homebrew/") {
				missing = append(missing, fmt.Sprintf("Tap %s needs to be tapped.", e.Name))
			}
		case "brew":
			r := homebrew.ResolveInstalledFormula(e.Name)
			if r == "" {
				missing = append(missing, fmt.Sprintf("Formula %s needs to be installed.", e.Name))
			} else if checkUpgrade && len(outdatedFormulae([]string{r}, false)) > 0 && !homebrew.IsPinned(r) {
				missing = append(missing, fmt.Sprintf("Formula %s needs to be upgraded.", e.Name))
			}
		case "cask":
			t := homebrew.ResolveInstalledCask(e.Name)
			if t == "" {
				missing = append(missing, fmt.Sprintf("Cask %s needs to be installed.", e.Name))
			} else if checkUpgrade && len(outdatedCasks([]string{t}, greedy{})) > 0 {
				missing = append(missing, fmt.Sprintf("Cask %s needs to be upgraded.", e.Name))
			}
		case "mas":
			if masState == nil {
				masState = map[string]bool{}
				for _, p := range mise.PackagesStatus()["mas"].Packages {
					masState[p.Package] = p.State == "installed"
				}
			}
			if !masState[e.ID] {
				missing = append(missing, fmt.Sprintf("App %s needs to be installed or updated.", e.Name))
			}
		case "vscode":
			cli := editorCLI(e)
			if exts[cli] == nil {
				exts[cli] = vscodeExtensions(cli)
			}
			if !exts[cli][strings.ToLower(e.Name)] {
				missing = append(missing, fmt.Sprintf("VSCode Extension %s needs to be installed.", e.Name))
			}
		default:
			if k := brewfile.ToolKey(e); k != "" {
				if _, err := mise.MiseOutput("where", k); err != nil {
					missing = append(missing, fmt.Sprintf("%s package %s needs to be installed.", e.Kind, e.Name))
				}
			}
		}
	}
	if !report {
		return len(missing) == 0
	}
	if len(missing) == 0 {
		fmt.Println("The Brewfile's dependencies are satisfied.")
		return true
	}
	fmt.Println("brew bundle can't satisfy your Brewfile's dependencies.")
	if !ui.Verbose && len(missing) > 1 {
		missing = missing[:1]
	}
	for _, m := range missing {
		fmt.Println("→ " + m)
	}
	fmt.Println("Satisfy missing dependencies with `brew bundle install`.")
	return false
}

var bundleKindFlags = map[string]string{"formula": "brew", "formulae": "brew", "brews": "brew", "cask": "cask", "casks": "cask", "tap": "tap", "taps": "tap", "mas": "mas", "vscode": "vscode", "go": "go", "cargo": "cargo", "npm": "npm", "uv": "uv", "flatpak": "flatpak", "whalebrew": "whalebrew"}

func bundleList(o Opts) {
	entries, _ := readBrewfile(o)
	kinds := map[string]bool{}
	for flag, kind := range bundleKindFlags {
		if o.Has(flag) {
			kinds[kind] = true
		}
	}
	if len(kinds) == 0 && !o.Has("all") {
		kinds["brew"] = true
	}
	for _, e := range entries {
		if o.Has("all") || kinds[e.Kind] {
			fmt.Println(e.Name)
		}
	}
}

func bundleDump(o Opts) {
	var b strings.Builder
	describe := o.Has("describe")
	only := map[string]bool{}
	for flag, kind := range bundleKindFlags {
		if o.Has(flag) {
			only[kind] = true
		}
	}
	want := func(kind string) bool {
		if o.Has("no-"+kind) || (kind == "brew" && o.Has("no-brew")) {
			return false
		}
		return len(only) == 0 || only[kind]
	}
	comment := func(d string) {
		if describe && d != "" {
			fmt.Fprintf(&b, "# %s\n", d)
		}
	}
	if want("tap") {
		taps := homebrew.ConfiguredTaps()
		var names []string
		for t := range taps {
			names = append(names, t)
		}
		sort.Strings(names)
		for _, t := range names {
			if u := taps[t]; u != "" && u != defaultTapURL(t) && u != defaultTapURL(t)+".git" {
				fmt.Fprintf(&b, "tap %q, %q\n", t, u)
			} else {
				fmt.Fprintf(&b, "tap %q\n", t)
			}
		}
	}
	if want("brew") {
		for _, n := range mise.SortedKeys(declaredFullNames("brew")) {
			if f := homebrew.LookupFormula(n); f != nil {
				comment(f.Desc)
			}
			line := fmt.Sprintf("brew %q", n)
			if r := homebrew.ResolveInstalledFormula(n); r != "" {
				if !o.Has("no-restart") {
					if s := findService(r); s != nil && s.state().Running {
						line += ", restart_service: :changed"
					}
				}
				if f := homebrew.LookupFormula(r); f != nil && !f.KegOnly && !homebrew.IsKegLinked(r) {
					line += ", link: false"
				} else if f != nil && f.KegOnly && homebrew.IsKegLinked(r) {
					line += ", link: true"
				}
			}
			b.WriteString(line + "\n")
		}
	}
	if want("cask") {
		for _, n := range mise.SortedKeys(declaredFullNames("brew-cask")) {
			if c := homebrew.LookupCask(n); c != nil {
				comment(c.DescString())
			}
			fmt.Fprintf(&b, "cask %q\n", n)
		}
	}
	if want("mas") {
		names := masNames()
		for _, id := range mise.SortedKeys(declaredSet("mas")) {
			n := names[id]
			if n == "" {
				n = id
			}
			fmt.Fprintf(&b, "mas %q, id: %s\n", n, id)
		}
	}
	if want("vscode") {
		for _, e := range mise.SortedKeys(vscodeExtensions(editorCLI(brewfile.BrewfileEntry{}))) {
			fmt.Fprintf(&b, "vscode %q\n", e)
		}
	}
	for _, kind := range []string{"go", "cargo", "npm", "uv"} {
		if !want(kind) {
			continue
		}
		for _, t := range globalMiseTools(map[string]string{"go": "go", "cargo": "cargo", "npm": "npm", "uv": "pipx"}[kind]) {
			fmt.Fprintf(&b, "%s %q\n", kind, t)
		}
	}
	if want("flatpak") {
		for _, n := range mise.SortedKeys(declaredSet("flatpak")) {
			fmt.Fprintf(&b, "flatpak %q\n", n)
		}
	}
	path := brewfilePath(o)
	if path == "-" {
		fmt.Print(b.String())
		return
	}
	if paths.FileExists(path) && !o.Has("force") {
		ui.Odie("%s already exists. Use `--force` to overwrite.", path)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		ui.Odie("%v", err)
	}
}

// declaredFullNames is declaredSet with tap-qualified names (owner/tap/name)
// restored, so a dump reinstalls from the same tap.
func declaredFullNames(manager string) map[string]bool {
	names := declaredSet(manager)
	for _, p := range mise.PackagesStatus()[manager].Packages {
		if strings.Contains(p.Package, "/") && (p.DesiredState == "" || p.DesiredState == "present") {
			delete(names, homebrew.BaseName(p.Package))
			names[p.Package] = true
		}
	}
	return names
}

// masNames maps App Store IDs to names via `mas list`.
func masNames() map[string]string {
	out := map[string]string{}
	b, err := exec.Command("mas", "list").Output()
	if err != nil {
		return out
	}
	re := regexp.MustCompile(`^\s*(\d+)\s+(.+?)\s+\(`)
	for _, l := range strings.Split(string(b), "\n") {
		if m := re.FindStringSubmatch(l); m != nil {
			out[m[1]] = m[2]
		}
	}
	return out
}

// globalMiseTools lists tools of a backend in the global mise config.
func globalMiseTools(backend string) []string {
	b, err := mise.MiseOutput("ls", "--global", "--json")
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	var out []string
	for k := range m {
		if strings.HasPrefix(k, backend+":") {
			out = append(out, strings.TrimPrefix(k, backend+":"))
		}
	}
	sort.Strings(out)
	return out
}

// tapOfFullName returns owner/tap of a tap-qualified owner/tap/name.
func tapOfFullName(name string) string {
	if strings.Count(name, "/") != 2 {
		return ""
	}
	return strings.ToLower(name[:strings.LastIndex(name, "/")])
}

// miseGlobalConfigDir is mise's global config directory.
func miseGlobalConfigDir() string {
	if d := os.Getenv("MISE_CONFIG_DIR"); d != "" {
		return d
	}
	x := os.Getenv("XDG_CONFIG_HOME")
	if x == "" {
		x = filepath.Join(paths.HomeDir(), ".config")
	}
	return filepath.Join(x, "mise")
}

func pathWithin(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// isGlobalMiseConfig reports whether f is mise's global config (or the file
// MISEBREW_CONFIG points mise-brew at), as opposed to a project's config.
func isGlobalMiseConfig(f string) bool {
	cands := []string{f}
	if r, err := filepath.EvalSymlinks(f); err == nil {
		cands = append(cands, r)
	}
	for _, c := range cands {
		if v := os.Getenv("MISEBREW_CONFIG"); v != "" && v != "global" && v != "local" && filepath.Clean(c) == filepath.Clean(v) {
			return true
		}
		if v := os.Getenv("MISE_GLOBAL_CONFIG_FILE"); v != "" && filepath.Clean(c) == filepath.Clean(v) {
			return true
		}
		dir := miseGlobalConfigDir()
		if pathWithin(c, dir) {
			return true
		}
		if r, err := filepath.EvalSymlinks(dir); err == nil && pathWithin(c, r) {
			return true
		}
	}
	return false
}

// projectDeclarations lists non-global config files declaring any of keys.
// `brew bundle cleanup` leaves those packages alone: uninstalling would
// undeclare them from a project's config as well.
func projectDeclarations(keys, files []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range keys {
		for _, d := range mise.FindDeclarations(k, files) {
			if !seen[d.File] && !isGlobalMiseConfig(d.File) {
				seen[d.File] = true
				out = append(out, d.File)
			}
		}
	}
	return out
}

// skipProjectDeclared drops (with a warning) items declared in a project config.
func skipProjectDeclared(items []string, keys func(string) []string, files []string) []string {
	var out []string
	for _, n := range items {
		if where := projectDeclarations(keys(n), files); len(where) > 0 {
			for i := range where {
				where[i] = paths.Tildify(where[i])
			}
			ui.Opoo("Not uninstalling %s: it is declared in %s; remove it there first.", n, strings.Join(where, ", "))
			continue
		}
		out = append(out, n)
	}
	return out
}

// tapsToUntap mirrors Homebrew's taps_to_untap: current taps not named by a
// tap entry, by a tap-qualified brew/cask entry (owner/tap/name), or by the
// receipt of a kept, installed formula.
func tapsToUntap(entries []brewfile.BrewfileEntry, keepF map[string]bool, current map[string]string) []string {
	keep := map[string]bool{"homebrew/core": true}
	for _, e := range entries {
		switch e.Kind {
		case "tap":
			keep[strings.ToLower(e.Name)] = true
		case "brew":
			if t := tapOfFullName(homebrew.StripCoreTap(e.Name)); t != "" {
				keep[t] = true
			}
		case "cask":
			full := e.OptString("full_name")
			if full == "" {
				full = e.Name
			}
			if t := tapOfFullName(homebrew.StripCoreTap(full)); t != "" {
				keep[t] = true
			}
		}
	}
	for n := range keepF {
		if v := homebrew.CurrentVersion(n); v != "" {
			if r := homebrew.ReadReceipt(homebrew.KegPath(n, v)); r != nil && r.Source.Tap != "" {
				keep[strings.ToLower(r.Source.Tap)] = true
			}
		}
	}
	var out []string
	for t := range current {
		if !keep[strings.ToLower(t)] {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func bundleCleanup(o Opts) {
	entries, incomplete := readBrewfile(o)
	if incomplete {
		if o.Has("force") {
			refuseIncomplete("cleanup --force")
		}
		ui.Opoo("Parts of the Brewfile could not be evaluated; this list may include packages it declares.")
	}
	keepF, keepC, keepExt := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case "brew":
			n := homebrew.BaseName(homebrew.StripCoreTap(e.Name))
			if f := homebrew.LookupFormula(n); f != nil {
				n = f.Name
			}
			keepF[n] = true
			if r := homebrew.ResolveInstalledFormula(e.Name); r != "" {
				keepF[r] = true
			}
		case "cask":
			keepC[homebrew.BaseName(homebrew.StripCoreTap(e.Name))] = true
			// Renamed casks stay installed under their old token, as check sees them.
			if t := homebrew.ResolveInstalledCask(e.Name); t != "" {
				keepC[t] = true
			}
		case "vscode":
			keepExt[strings.ToLower(e.Name)] = true
		}
	}
	deps := homebrew.InstalledRuntimeDeps()
	var walk func(string)
	walk = func(n string) {
		for _, d := range deps[n] {
			if !keepF[d] {
				keepF[d] = true
				walk(d)
			}
		}
	}
	for n := range keepF {
		walk(n)
	}
	for _, c := range homebrew.InstalledCasks() {
		if keepC[c] {
			if cc := homebrew.LookupCask(c); cc != nil {
				for _, d := range cc.DependsOn.Formula {
					keepF[d] = true
					walk(d)
				}
			}
		}
	}
	var rmF, rmC, rmExt []string
	for _, n := range homebrew.InstalledFormulae() {
		if !keepF[n] {
			rmF = append(rmF, n)
		}
	}
	for _, c := range homebrew.InstalledCasks() {
		if !keepC[c] {
			rmC = append(rmC, c)
		}
	}
	rmT := tapsToUntap(entries, keepF, homebrew.ConfiguredTapURLs())
	if len(rmF)+len(rmC) > 0 {
		files := mise.ConfigFiles()
		rmF = skipProjectDeclared(rmF, func(n string) []string { return []string{"brew:" + n, formulaKey(n)} }, files)
		rmC = skipProjectDeclared(rmC, func(t string) []string { return []string{"brew-cask:" + t} }, files)
	}
	if len(keepExt) > 0 || o.Has("vscode") {
		for e := range vscodeExtensions(editorCLI(brewfile.BrewfileEntry{})) {
			if !keepExt[e] {
				rmExt = append(rmExt, e)
			}
		}
	}
	sort.Strings(rmT)
	sort.Strings(rmExt)
	if len(rmF)+len(rmC)+len(rmT)+len(rmExt) == 0 {
		return
	}
	if !o.Has("force") {
		for _, grp := range []struct {
			what  string
			items []string
		}{{"casks", rmC}, {"formulae", rmF}, {"taps", rmT}, {"VSCode extensions", rmExt}} {
			if len(grp.items) > 0 {
				fmt.Printf("Would uninstall %s:\n", grp.what)
				ui.PutsColumns(grp.items)
			}
		}
		fmt.Println("Run `brew bundle cleanup --force` to make these changes.")
		return
	}
	if len(rmC) > 0 {
		a := []string{"--cask"}
		if o.Has("zap") {
			a = append(a, "--zap")
		}
		cmdUninstall(append(a, rmC...))
	}
	if len(rmF) > 0 {
		cmdUninstall(append([]string{"--formula", "--ignore-dependencies"}, rmF...))
	}
	if len(rmT) > 0 {
		cmdUntap(rmT)
	}
	for _, e := range rmExt {
		cli := editorCLI(brewfile.BrewfileEntry{})
		_ = exec.Command(cli, "--uninstall-extension", e).Run()
		fmt.Printf("Uninstalled %s extension %s\n", cli, e)
	}
}

func bundleExec(o Opts, sub string, cmdArgs []string) {
	if o.Has("check") && !bundleCheck(o, false) {
		bundleCheck(o, true)
		ui.Exit(1)
	}
	entries, _ := readBrewfile(o)
	var formulae []string
	for _, e := range entries {
		if e.Kind == "brew" {
			formulae = append(formulae, homebrew.BaseName(e.Name))
		}
	}
	envPairs := buildEnv(formulae, false)
	if sub == "env" {
		for _, kv := range envPairs {
			fmt.Printf("export %s=%q\n", kv[0], kv[1])
		}
		return
	}
	for i, kv := range envPairs {
		if kv[0] == "PATH" {
			envPairs[i][1] = kv[1] + ":" + os.Getenv("PATH")
		}
	}
	env := mergeEnv(os.Environ(), envPairs)
	if o.Has("no-secrets") {
		var clean []string
		for _, e := range env {
			k, _, _ := strings.Cut(e, "=")
			uk := strings.ToUpper(k)
			if strings.Contains(uk, "TOKEN") || strings.Contains(uk, "SECRET") || strings.Contains(uk, "PASSWORD") || strings.HasSuffix(uk, "_KEY") {
				continue
			}
			clean = append(clean, e)
		}
		env = clean
	}
	if sub == "sh" {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		cmdArgs = []string{sh}
	}
	if len(cmdArgs) == 0 {
		ui.Odie("Invalid usage: `brew bundle exec` needs a command to run.")
	}
	path, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		ui.Odie("%v", err)
	}
	if o.Has("services") {
		runWithServices(formulae, path, cmdArgs, env)
	}
	if err := syscall.Exec(path, cmdArgs, env); err != nil {
		ui.Odie("%v", err)
	}
}

// runWithServices runs a command with the Brewfile's services temporarily
// started (`brew bundle exec --services`). Only services that weren't already
// running or registered are started, and exactly those are stopped afterwards,
// keeping any registered service file, as Homebrew's `keep: true`.
func runWithServices(formulae []string, path string, cmdArgs, env []string) {
	type temp struct {
		s           *service
		createdFile bool
	}
	var started []temp
	// The child gets Ctrl-C itself; the parent must survive it (and a stray
	// SIGTERM) to stop the services.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	// Deferred so the services are stopped even when starting one fails.
	defer func() {
		for _, t := range started {
			serviceStop(t.s, true)
			if t.createdFile {
				_ = os.Remove(t.s.installedPath())
				_ = runQuiet("systemctl", systemctlArgs("daemon-reload")...)
			}
		}
	}()
	for _, f := range formulae {
		s := findService(f)
		if s == nil {
			continue
		}
		if st := s.state(); st.Running || st.Loaded || paths.FileExists(s.installedPath()) {
			continue
		}
		serviceRun(s)
		// serviceRun registers the unit on Linux; remove it again afterwards.
		started = append(started, temp{s, runtime.GOOS != "darwin" && paths.FileExists(s.installedPath())})
	}
	cmd := exec.Command(path, cmdArgs[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		ui.Onoe("%v", err)
		ui.Exit(127)
	}
	// Forward SIGTERM sent to brew alone (kill, timeout(1), a supervisor) to
	// the child; remember the last signal so brew exits like it was killed by it
	// once the services are stopped.
	var got os.Signal
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	var err error
	for done := false; !done; {
		select {
		case sig := <-sigs:
			got = sig
			// A terminal Ctrl-C already reached the child through the
			// process group; sending it twice makes many tools force-quit.
			if sig != os.Interrupt {
				_ = cmd.Process.Signal(sig)
			}
		case err = <-waited:
			done = true
		}
	}
	if s, ok := got.(syscall.Signal); ok && got != nil {
		ui.Exit(128 + int(s))
	}
	ui.Exit(exitCode(err))
}

// bundleEntryRe matches entries of the given kinds and names anywhere in a
// line, like Homebrew's remover: `brew "x"`, `brew("x", ...)`, `... if OS.mac?`.
func bundleEntryRe(kinds, names []string) *regexp.Regexp {
	q := func(xs []string) string {
		var out []string
		for _, x := range xs {
			out = append(out, regexp.QuoteMeta(x))
		}
		return strings.Join(out, "|")
	}
	return regexp.MustCompile(`\b(` + q(kinds) + `)\s*\(?\s*["'](` + q(names) + `)["']`)
}

// editBrewfileLines adds (or removes) entries of kinds named names. Removing
// drops every matching line plus the continuation lines of multi-line entries.
func editBrewfileLines(lines []string, kinds, names []string, add bool) []string {
	if add {
		for _, n := range names {
			re := bundleEntryRe(kinds[:1], []string{n})
			found := false
			for _, l := range lines {
				if re.MatchString(brewfile.StripRubyComment(l)) {
					found = true
					break
				}
			}
			if !found {
				lines = append(lines, fmt.Sprintf("%s %q", kinds[0], n))
			}
		}
		return lines
	}
	re := bundleEntryRe(kinds, names)
	var kept []string
	for i := 0; i < len(lines); i++ {
		code := brewfile.StripRubyComment(lines[i])
		if !re.MatchString(code) {
			kept = append(kept, lines[i])
			continue
		}
		for brewfile.ContinuesLine(code) && i+1 < len(lines) {
			i++
			code = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(code), `\`)) + " " + strings.TrimSpace(brewfile.StripRubyComment(lines[i]))
		}
	}
	return kept
}

func bundleEdit(o Opts, names []string, add bool) {
	if len(names) == 0 {
		ui.Odie("Invalid usage: `brew bundle %s` needs at least one name.", map[bool]string{true: "add", false: "remove"}[add])
	}
	var flags, kinds []string
	for f := range bundleKindFlags {
		flags = append(flags, f)
	}
	sort.Strings(flags)
	seen := map[string]bool{}
	for _, flag := range flags {
		if k := bundleKindFlags[flag]; o.Has(flag) && !seen[k] {
			seen[k] = true
			kinds = append(kinds, k)
		}
	}
	if len(kinds) == 0 {
		if add {
			kinds = []string{"brew"}
		} else {
			// Without a type flag, remove matching entries of every kind.
			kinds = []string{"tap", "brew", "cask", "mas", "vscode", "cursor", "antigravity", "whalebrew", "go", "cargo", "npm", "uv", "flatpak"}
		}
	}
	path := brewfilePath(o)
	data, err := os.ReadFile(path)
	if err != nil && (!add || !os.IsNotExist(err)) {
		ui.Odie("No Brewfile found: %s", path)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	lines = editBrewfileLines(lines, kinds, names, add)
	out := strings.Join(lines, "\n")
	if out != "" {
		out += "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		ui.Odie("%v", err)
	}
}
