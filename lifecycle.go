package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// --- shared helpers ---------------------------------------------------------

func envSet(k string) bool {
	v := strings.ToLower(os.Getenv(k))
	return v != "" && v != "0" && v != "false" && v != "no"
}

func envHint(lines ...string) {
	if envSet("HOMEBREW_NO_ENV_HINTS") || quiet {
		return
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	fmt.Println("Hide these hints with `HOMEBREW_NO_ENV_HINTS=1` (see `man brew`).")
}

func rejectUnsupportedInstallFlags(o Opts) {
	for _, f := range []string{"build-from-source", "HEAD", "interactive", "debug-symbols", "git", "include-test", "build-bottle", "bottle-arch", "cc", "adopt", "skip-post-install"} {
		if o.Has(f) {
			odie("--%s is not supported: mise installs Homebrew bottles and runs their post-install steps itself.", f)
		}
	}
	for _, f := range []string{"ignore-dependencies", "language", "no-binaries", "skip-cask-deps", "keep-tmp"} {
		if o.Has(f) {
			opoo("--%s is not supported by mise and was ignored.", f)
		}
	}
}

// caskOptions maps --appdir / HOMEBREW_CASK_OPTS onto mise's environment.
func caskOptions(o Opts) (env []string, quarantine bool) {
	var envOpts Opts
	if v := os.Getenv("HOMEBREW_CASK_OPTS"); v != "" {
		envOpts = parseArgs(strings.Fields(v), nil, nil)
	} else {
		envOpts = Opts{flags: map[string]string{}}
	}
	get := func(k string) (string, bool) {
		if v, ok := o.Val(k); ok {
			return v, true
		}
		return envOpts.Val(k)
	}
	if v, ok := get("appdir"); ok && v != "" {
		env = append(env, "MISE_BREW_CASK_OPT_APPDIR="+expandUserPath(v))
	}
	for _, k := range []string{"keyboard-layoutdir", "colorpickerdir", "prefpanedir", "qlplugindir", "mdimporterdir", "dictionarydir", "fontdir", "servicedir", "input-methoddir", "internet-plugindir", "audio-unit-plugindir", "vst-plugindir", "vst3-plugindir", "screen-saverdir"} {
		if _, ok := get(k); ok {
			opoo("--%s is not supported by mise (only --appdir is); using the default location.", k)
		}
	}
	quarantine = (o.Has("quarantine") || envOpts.Has("quarantine")) && !o.Has("no-quarantine")
	return
}

// formulaKey is the mise package key for an installed or named formula.
func formulaKey(name string) string {
	if tap := installedTapOf(name); tap != "" {
		return "brew:" + tap + "/" + name
	}
	return "brew:" + name
}

func snapshotKegs() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, n := range installedFormulae() {
		out[n] = map[string]bool{}
		for _, v := range kegVersions(n) {
			out[n][v] = true
		}
	}
	return out
}

// changedFormulae lists formulae that gained a keg since the snapshot.
func changedFormulae(before map[string]map[string]bool) []string {
	var out []string
	for _, n := range installedFormulae() {
		for _, v := range kegVersions(n) {
			if !before[n][v] {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func confirm(prompt string) bool {
	fmt.Printf("%s [Y/y/yes/N/n/no] ", prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

func quarantineCask(token string) {
	rec := readCaskRecord(token, currentCaskVersion(token))
	if rec == nil {
		return
	}
	val := fmt.Sprintf("0181;%x;mise-brew;", time.Now().Unix())
	for _, app := range pathsOf(rec.Apps) {
		_ = exec.Command("xattr", "-w", "com.apple.quarantine", val, app).Run()
	}
}

// --- install ----------------------------------------------------------------

func cmdInstall(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula or cask argument.")
	}
	rejectUnsupportedInstallFlags(o)
	env, quarantine := caskOptions(o)
	asDep := o.Has("as-dependency")
	record := recordEnabled() && !o.Has("no-record") && !asDep
	dry := o.Has("dry-run")
	var files []string
	if record {
		files = configFiles()
	}
	var useKeys, applyKeys []string
	var refs []pkgRef
	var requested []string // formulae named on the command line
	for _, name := range o.Pos {
		ref := resolvePackage(name, o)
		if ref.kind == "formula" && !o.Has("only-dependencies") {
			requested = append(requested, baseName(ref.name))
		}
		if o.Has("only-dependencies") && ref.f != nil {
			for _, d := range recursiveDeps(ref.f, depFilter{}) {
				if !isFormulaInstalled(baseName(d)) {
					applyKeys = append(applyKeys, "brew:"+d)
				}
			}
			continue
		}
		if o.Has("require-sha") && ref.c != nil {
			if raw := rawCaskMap(ref.c.Token); raw != nil && fmt.Sprint(raw["sha256"]) == "no_check" {
				odie("Cask '%s' does not have a sha256 checksum defined and was not installed.\nThis happens when the cask uses `sha256 :no_check` and --require-sha is set.", ref.name)
			}
		}
		declared := record && isDeclared(ref.key(), files)
		if (declared || !record) && !o.Has("force") && upToDate(ref) {
			if ref.kind == "cask" {
				opoo("Not upgrading %s, the latest version is already installed", ref.name)
			} else {
				v := currentVersion(baseName(ref.name))
				opoo("%s %s is already installed and up-to-date.\nTo reinstall %s, run:\n  brew reinstall %s", ref.name, v, v, ref.name)
			}
			continue
		}
		if record && !declared {
			useKeys = append(useKeys, ref.key())
		} else {
			applyKeys = append(applyKeys, ref.key())
		}
		refs = append(refs, ref)
	}
	// mise does not mark an already-installed dependency as installed on
	// request when it is named later; do it like Homebrew so autoremove keeps it.
	markRequested := func() {
		if asDep {
			return
		}
		for _, n := range requested {
			if isFormulaInstalled(n) {
				markOnRequest(n)
			}
		}
	}
	if len(useKeys)+len(applyKeys) == 0 {
		if !dry {
			markRequested()
		}
		return
	}
	run := func(dryRun bool, r *miseRun) {
		if len(useKeys) > 0 {
			a := append([]string{"bootstrap", "packages", "use"}, scopeArgs()...)
			a = append(a, "-y")
			if dryRun {
				a = append(a, "-n")
			}
			if code := runMiseBrewStyle(r, append(a, useKeys...)...); code != 0 {
				exit(code)
			}
		}
		if len(applyKeys) > 0 {
			a := []string{"bootstrap", "packages", "apply", "-y"}
			if dryRun {
				a = append(a, "-n")
			}
			if code := runMiseBrewStyle(r, append(a, applyKeys...)...); code != 0 {
				exit(code)
			}
		}
	}
	if dry {
		run(true, &miseRun{extraEnv: env})
		return
	}
	if o.Has("ask") {
		run(true, &miseRun{extraEnv: env})
		if !confirm("Do you want to proceed with the installation?") {
			exit(1)
		}
	}
	before := snapshotKegs()
	r := &miseRun{extraEnv: env}
	run(false, r)
	changed := changedFormulae(before)
	func() {
		defer lockPrefix()()
		for _, n := range changed {
			v := currentVersion(n)
			if o.Has("skip-link") {
				if contains(requested, n) { // dependencies stay linked, as in Homebrew
					unlinkKeg(n, v, false)
				}
			} else if o.Has("overwrite") && !isKegLinked(n) {
				if f := lookupFormula(n); f == nil || !f.KegOnly {
					_, _, _ = linkKeg(n, v, true, false)
				}
			}
		}
	}()
	if asDep {
		for _, ref := range refs {
			if ref.kind == "formula" && isFormulaInstalled(baseName(ref.name)) {
				setOnRequest(baseName(ref.name), false)
			}
		}
	}
	markRequested()
	for _, ref := range refs {
		if ref.kind == "cask" && quarantine {
			quarantineCask(baseName(ref.name))
		}
		if txt := caveatsFor(ref); txt != "" && !quiet {
			ohai("Caveats")
			fmt.Println(txt)
		}
	}
	installCleanup(changed)
	if o.Has("display-times") {
		r.printTimes()
	}
}

// --- reinstall / postinstall ------------------------------------------------

func cmdReinstall(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula or cask argument.")
	}
	rejectUnsupportedInstallFlags(o)
	env, quarantine := caskOptions(o)
	for _, name := range o.Pos {
		if !validPackageArg(name) {
			odie("No available formula or cask with the name \"%s\".", name)
		}
		rack := ""
		if !o.onlyCask() {
			restoreReinstallBackup(baseName(stripCoreTap(name)))
			rack = resolveInstalledFormula(name)
		}
		if rack != "" {
			reinstallFormula(rack, o.Has("dry-run"), env)
			continue
		}
		if !o.onlyFormula() {
			if t := resolveInstalledCask(name); t != "" {
				reinstallCask(t, name, o.Has("zap"), env, quarantine)
				continue
			}
		}
		opoo("%s is not installed; installing it", name)
		cmdInstall(append(passthroughFlags(o), name))
	}
}

// reinstallBackup is where reinstall/postinstall keep the current keg while
// mise pours it again: Cellar/.<rack>.mise-brew-reinstall/<version>.
func reinstallBackup(rack string) string {
	return filepath.Join(cellar(), "."+rack+".mise-brew-reinstall")
}

// restoreReinstallBackup puts back kegs left aside by an interrupted
// reinstall (for instance mise-brew was killed) and drops stale backups of
// kegs that were reinstalled.
func restoreReinstallBackup(rack string) {
	if !validName(rack) {
		return
	}
	backup := reinstallBackup(rack)
	if !isDir(backup) {
		return
	}
	defer lockPrefix()()
	for _, v := range listDirNames(backup, true) {
		keg := kegPath(rack, v)
		if isDir(keg) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(keg), 0o755); err != nil {
			onoe("could not restore %s from %s: %v", keg, backup, err)
			return
		}
		if err := os.Rename(filepath.Join(backup, v), keg); err != nil {
			onoe("could not restore %s from %s: %v", keg, backup, err)
			return
		}
		if !isDir(filepath.Join(optDir(), rack)) {
			_ = setOptLink(rack, v)
		}
		opoo("Restored %s from an interrupted reinstall.", keg)
	}
	_ = os.RemoveAll(backup)
}

// holdSignals keeps SIGINT/SIGTERM/SIGHUP from killing mise-brew while a keg
// is moved aside. mise, in the same process group, still gets the terminal's
// signals, so an interrupted mise exits non-zero and the keg is restored.
// Signals are caught rather than ignored so the mise child does not inherit
// an ignored disposition.
func holdSignals() (release func()) {
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-ch:
				odebug("received %v; finishing the reinstall before exiting", s)
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

func reinstallFormula(rack string, dry bool, env []string) {
	if !validName(rack) {
		odie("No such keg: %s", cellar()+"/"+rack)
	}
	v := currentVersion(rack)
	keg := kegPath(rack, v)
	rackDir := filepath.Join(cellar(), rack)
	if dry {
		fmt.Printf("Would reinstall %s %s\n", rack, v)
		return
	}
	if isPinned(rack) {
		odie("%s is pinned. You must unpin it to reinstall.", rack)
	}
	if v == "" || !validName(v) {
		odie("No such keg: %s", keg)
	}
	wasLinked := isKegLinked(rack)
	onReq := true
	if r := readReceipt(keg); r != nil {
		onReq = r.InstalledOnRequest
	}
	others := map[string]bool{}
	for _, x := range kegVersions(rack) {
		if x != v {
			others[x] = true
		}
	}
	// Only the current keg is moved aside; other installed versions stay.
	backup := reinstallBackup(rack)
	saved := filepath.Join(backup, v)
	restoreReinstallBackup(rack)
	if fileExists(backup) {
		odie("%s is left from an interrupted reinstall; move its kegs back to %s or remove it.", backup, rackDir)
	}
	defer holdSignals()()
	unlock := lockPrefix()
	unlinkKeg(rack, v, false)
	if err := os.MkdirAll(backup, 0o755); err != nil {
		unlock()
		odie("could not move %s aside: %v", keg, err)
	}
	if err := os.Rename(keg, saved); err != nil {
		_ = os.Remove(backup)
		if wasLinked {
			_, _, _ = linkKeg(rack, v, false, false)
		}
		unlock()
		odie("could not move %s aside: %v", keg, err)
	}
	if readLinkVersion(filepath.Join(optDir(), rack)) == v {
		_ = os.Remove(filepath.Join(optDir(), rack))
	}
	_ = os.Remove(filepath.Join(linkedDir(), rack))
	_ = os.Remove(rackDir) // only when no other version is installed
	unlock()
	ohai("Reinstalling %s", rack)
	code := runMiseBrewStyle(&miseRun{extraEnv: env}, "bootstrap", "packages", "apply", "-y", formulaKeyFromBackup(rack, backup, v))
	defer lockPrefix()()
	poured := false
	for _, x := range kegVersions(rack) {
		if !others[x] {
			poured = true
		}
	}
	if code == 0 && poured {
		_ = os.RemoveAll(backup)
		setOnRequest(rack, onReq) // mise writes a fresh receipt; keep how it was installed
		return
	}
	if code == 0 {
		code = 1
	}
	// Put the old keg back. Whatever mise left at the same path is partial.
	_ = os.RemoveAll(keg)
	if err := os.MkdirAll(rackDir, 0o755); err == nil {
		err = os.Rename(saved, keg)
	}
	if !isDir(keg) {
		onoe("reinstall of %s failed and %s could not be restored; it was left in %s", rack, keg, saved)
		exit(code)
	}
	_ = os.Remove(backup)
	if err := setOptLink(rack, v); err != nil {
		onoe("reinstall of %s failed; restored %s but could not update %s: %v", rack, keg, filepath.Join(optDir(), rack), err)
		exit(code)
	}
	if wasLinked {
		// mise may have linked part of a new keg before failing.
		for _, l := range brokenSymlinks() {
			if resolvesIntoAny(l, []string{rackDir, filepath.Join(optDir(), rack)}) {
				_ = os.Remove(l)
			}
		}
		_, conflicts, err := linkKeg(rack, v, false, false)
		if err == nil && len(conflicts) > 0 {
			err = fmt.Errorf("%s already exists", conflicts[0].dst)
		}
		if err != nil {
			onoe("reinstall of %s failed; restored %s but could not relink it: %v\nTo relink it, run:\n  brew link %s", rack, keg, err, rack)
			exit(code)
		}
	}
	onoe("reinstall of %s failed; restored %s", rack, keg)
	exit(code)
}

// formulaKeyFromBackup builds the key while the keg (and its receipt) is moved aside.
func formulaKeyFromBackup(rack, backup, v string) string {
	if r := readReceipt(filepath.Join(backup, v)); r != nil {
		if t := strings.ToLower(r.Source.Tap); t != "" && t != "homebrew/core" {
			return "brew:" + t + "/" + rack
		}
	}
	return "brew:" + rack
}

// caskKey is the mise key for an installed cask: tap-qualified when the cask
// is declared (or was named) as owner/tap/token.
func caskKey(token, typed string) string {
	if full := tapQualifiedDeclared("brew-cask", token); len(full) > 0 {
		return "brew-cask:" + full[0]
	}
	if n := strings.ToLower(stripCoreTap(typed)); isThirdPartyTapName(n) && baseName(n) == token {
		return "brew-cask:" + n
	}
	return "brew-cask:" + token
}

// reinstallCask uninstalls the cask natively and has mise install it again.
// The Caskroom entry is kept as a hard-linked copy and put back if mise
// fails, so the cask stays known to brew; artifacts already removed from
// their targets (apps, binaries) are not restored.
func reinstallCask(token, typed string, zap bool, env []string, quarantine bool) {
	if !validName(token) {
		odie("Cask '%s' is not installed.", token)
	}
	ohai("Reinstalling Cask %s", token)
	key := caskKey(token, typed)
	room := filepath.Join(caskroom(), token)
	backup := filepath.Join(caskroom(), "."+token+".mise-brew-reinstall")
	defer holdSignals()()
	_ = os.RemoveAll(backup)
	if err := linkTree(room, backup); err != nil {
		_ = os.RemoveAll(backup)
		opoo("could not back up %s (%v); it cannot be restored if the reinstall fails", room, err)
	}
	defer os.RemoveAll(backup)
	// restore puts the Caskroom entry back when nothing was (re)installed.
	restore := func() bool {
		if !isDir(backup) || isCaskInstalled(token) {
			return false
		}
		_ = os.RemoveAll(room)
		return os.Rename(backup, room) == nil
	}
	if err := nativeUninstallCask(token, zap, false); err != nil {
		restore()
		odie("%v", err)
	}
	code := runMiseBrewStyle(&miseRun{extraEnv: env}, "bootstrap", "packages", "apply", "-y", key)
	if code != 0 || !isCaskInstalled(token) {
		if code == 0 {
			code = 1
		}
		if restore() {
			onoe("reinstall of %s failed; restored %s. Its artifacts were removed; run `brew reinstall --cask %s` again once the problem is fixed.", token, room, token)
		}
		exit(code)
	}
	if quarantine {
		quarantineCask(token)
	}
}

// linkTree recreates src at dst with hard links (and copies of symlinks), so
// dst survives src being deleted without copying file data.
func linkTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		default:
			return os.Link(p, target)
		}
	})
}

func cmdPostinstall(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	for _, n := range o.Pos {
		if validPackageArg(n) {
			restoreReinstallBackup(baseName(stripCoreTap(n)))
		}
	}
	for _, n := range requireInstalled(o.Pos) {
		ohai("Postinstalling %s", n)
		// mise runs a formula's post-install steps as part of pouring it.
		reinstallFormula(n, false, nil)
	}
}

// markOnRequest records that the user asked for name, unless its receipt
// already says so (or there is no receipt to edit).
func markOnRequest(name string) {
	r := readReceipt(kegPath(name, currentVersion(name)))
	if r == nil || (r.InstalledOnRequest && !r.InstalledAsDependency) {
		return
	}
	setOnRequest(name, true)
}

func setOnRequest(name string, v bool) {
	p := filepath.Join(kegPath(name, currentVersion(name)), "INSTALL_RECEIPT.json")
	editReceipt(p, map[string]any{"installed_on_request": v, "installed_as_dependency": !v})
}

// --- uninstall / autoremove -------------------------------------------------

func cmdUninstall(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 installed formula or cask argument.")
	}
	dry := o.Has("dry-run")
	force := o.Has("force")
	type item struct{ kind, name string }
	var items []item
	failed := false
	for _, n := range o.Pos {
		if !validPackageArg(n) {
			onoe("No such keg: %s", cellar()+"/"+n)
			failed = true
			continue
		}
		if !o.onlyCask() {
			if r := resolveInstalledFormula(n); r != "" {
				items = append(items, item{"formula", r})
				continue
			}
		}
		if !o.onlyFormula() {
			if t := resolveInstalledCask(n); t != "" {
				items = append(items, item{"cask", t})
				continue
			}
		}
		key := "brew:" + stripCoreTap(n)
		if o.onlyCask() {
			key = "brew-cask:" + stripCoreTap(n)
		}
		if decls := findDeclarations(key, configFiles()); len(decls) > 0 {
			if dry {
				fmt.Printf("Would remove %s from mise config\n", key)
			} else {
				undeclare(key)
			}
			continue
		}
		if o.onlyCask() || (force && lookupCask(n) != nil) {
			if force {
				continue // brew: --force uninstalls "even if cask is not installed"
			}
			onoe("Cask '%s' is not installed.", n)
		} else {
			onoe("No such keg: %s", filepath.Join(cellar(), n))
		}
		failed = true
	}

	if !o.Has("ignore-dependencies") && !envSet("HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK_ON_UNINSTALL") {
		deps := installedRuntimeDeps()
		removing := map[string]bool{}
		for _, it := range items {
			removing[it.kind+":"+it.name] = true
		}
		// Installed casks with depends_on formula: count as dependents too.
		caskUsers := map[string][]string{}
		for _, t := range installedCasks() {
			if removing["cask:"+t] {
				continue
			}
			if c := lookupCask(t); c != nil {
				for _, d := range c.DependsOn.Formula {
					caskUsers[baseName(d)] = append(caskUsers[baseName(d)], t)
				}
			}
		}
		for _, it := range items {
			if it.kind != "formula" {
				continue
			}
			var users []string
			for _, d := range installedDependents(it.name, deps) {
				if !removing["formula:"+d] {
					users = append(users, d)
				}
			}
			users = append(users, caskUsers[it.name]...)
			if len(users) > 0 {
				onoe("Refusing to uninstall %s\nbecause it is required by %s, which %s currently installed.\nYou can override this and force removal with:\n  brew uninstall --ignore-dependencies %s",
					kegPath(it.name, currentVersion(it.name)), strings.Join(users, ", "), map[bool]string{true: "is", false: "are"}[len(users) == 1], it.name)
				exit(1)
			}
		}
	}

	removedFormula := false
	for _, it := range items {
		switch it.kind {
		case "formula":
			versions := kegVersions(it.name)
			if !force {
				versions = []string{currentVersion(it.name)}
			}
			if dry {
				for _, v := range versions {
					fmt.Printf("Would uninstall %s %s\n", kegPath(it.name, v), kegSummary(kegPath(it.name, v)))
				}
				continue
			}
			restores := []func(){undeclare("brew:" + it.name)}
			for _, full := range tapQualifiedDeclared("brew", it.name) {
				restores = append(restores, undeclare("brew:"+full))
			}
			func() {
				defer lockPrefix()()
				if err := removeKegs(it.name, versions); err != nil {
					if isFormulaInstalled(it.name) {
						runRestores(restores)
					}
					odie("%v", err)
				}
			}()
			removedFormula = true
			if rest := kegVersions(it.name); len(rest) > 0 {
				fmt.Printf("%s %s %s still installed.\nTo remove all versions, run:\n  brew uninstall --force %s\n",
					it.name, strings.Join(rest, ", "), map[bool]string{true: "is", false: "are"}[len(rest) == 1], it.name)
			}
		case "cask":
			uninstallCask(it.name, o.Has("zap"), dry)
		}
	}
	autoOn := !envSet("HOMEBREW_NO_AUTOREMOVE")
	if removedFormula && autoOn && !dry {
		autoremove(false, true)
	}
	if failed {
		exit(1)
	}
}

// tapQualifiedDeclared finds declared names like owner/tap/<name> for a manager.
func tapQualifiedDeclared(manager, name string) []string {
	var out []string
	for _, p := range packagesStatus()[manager].Packages {
		if strings.Contains(p.Package, "/") && baseName(p.Package) == name {
			out = append(out, p.Package)
		}
	}
	return out
}

// uninstallCask undeclares the cask and removes it the way Homebrew does. mise
// prunes casks only as a group, so removal is done natively from the API's
// uninstall/zap stanzas and the files mise recorded in .mise-cask.toml.
func uninstallCask(token string, zap, dry bool) {
	if dry {
		if err := nativeUninstallCask(token, zap, true); err != nil {
			odie("%v", err)
		}
		return
	}
	restores := []func(){undeclare("brew-cask:" + token)}
	for _, full := range tapQualifiedDeclared("brew-cask", token) {
		restores = append(restores, undeclare("brew-cask:"+full))
	}
	err := nativeUninstallCask(token, zap, false)
	if err != nil {
		if isCaskInstalled(token) {
			runRestores(restores)
		}
		odie("%v", err)
	}
}

// runRestores undoes undeclare calls, newest first.
func runRestores(restores []func()) {
	for i := len(restores) - 1; i >= 0; i-- {
		restores[i]()
	}
}

// autoremoveCandidates mirrors Homebrew: formulae installed as dependencies
// (not declared in mise config, not installed on request) that nothing needs.
func autoremoveCandidates() []string {
	// Without mise's view of what is declared, every formula would look unneeded.
	if err := packagesStatusErr(); err != nil {
		odebug("not autoremoving: `mise bootstrap packages status` failed: %v", err)
		return nil
	}
	declared := declaredSet("brew")
	deps := installedRuntimeDeps()
	// Also count current API dependencies: a newer version may need a formula
	// that the installed keg's receipt predates (it is kept for the upgrade).
	for n := range deps {
		if f := lookupFormula(n); f != nil && f.Name == n {
			for _, d := range directDeps(n, depFilter{}) {
				deps[n] = append(deps[n], baseName(d))
			}
			deps[n] = dedupe(deps[n])
		}
	}
	onRequest := func(n string) bool {
		if declared[n] {
			return true
		}
		r := readReceipt(kegPath(n, currentVersion(n)))
		return r == nil || r.InstalledOnRequest
	}
	needed := map[string]bool{}
	var walk func(string)
	walk = func(n string) {
		for _, d := range deps[n] {
			if !needed[d] {
				needed[d] = true
				walk(d)
			}
		}
	}
	for n := range deps {
		if onRequest(n) {
			needed[n] = true
			walk(n)
		}
	}
	for _, t := range installedCasks() {
		if c := lookupCask(t); c != nil {
			for _, d := range c.DependsOn.Formula {
				needed[baseName(d)] = true
				walk(baseName(d))
			}
		}
	}
	var out []string
	for n := range deps {
		if !needed[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func autoremove(dry, silentIfNone bool) {
	if err := packagesStatusErr(); err != nil && !silentIfNone {
		opoo("Not autoremoving anything: could not read mise's declared packages (%v).", err)
	}
	cands := autoremoveCandidates()
	if len(cands) == 0 {
		return
	}
	if dry {
		ohai("Would autoremove %s:", plural(len(cands), "unneeded formula"))
		fmt.Println(strings.Join(cands, "\n"))
		return
	}
	ohai("Autoremoving %s:", plural(len(cands), "unneeded formula"))
	fmt.Println(strings.Join(cands, "\n"))
	defer lockPrefix()()
	for _, n := range cands {
		if err := removeKegs(n, kegVersions(n)); err != nil {
			onoe("%v", err)
		}
	}
}

func cmdAutoremove(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if o.Has("mise") {
		a := []string{"bootstrap", "packages", "prune", "-m", "brew"}
		if o.Has("dry-run") {
			a = append(a, "--dry-run")
		} else {
			a = append(a, "-y")
		}
		if code := runMise(a...); code != 0 {
			exit(code)
		}
		return
	}
	autoremove(o.Has("dry-run"), false)
}

// --- upgrade / update / outdated -------------------------------------------

type greedy struct{ latest, autoUpdates bool }

func greedyFrom(o Opts) greedy {
	g := greedy{
		latest:      o.Has("greedy", "greedy-latest") || envSet("HOMEBREW_UPGRADE_GREEDY"),
		autoUpdates: o.Has("greedy", "greedy-auto-updates") || envSet("HOMEBREW_UPGRADE_GREEDY"),
	}
	return g
}

func greedyCasks() map[string]bool {
	out := map[string]bool{}
	for _, t := range strings.Fields(os.Getenv("HOMEBREW_UPGRADE_GREEDY_CASKS")) {
		out[t] = true
	}
	return out
}

func outdatedCasks(names []string, g greedy) []outdatedItem {
	if !caskAPI.Available() {
		return nil
	}
	if names == nil {
		names = installedCasks()
	}
	always := greedyCasks()
	var out []outdatedItem
	for _, t := range names {
		c := lookupCask(t)
		if c == nil {
			continue
		}
		vs := caskVersions(t)
		if len(vs) == 0 || contains(vs, c.Version) {
			continue
		}
		if c.Version == "latest" && !g.latest && !always[t] {
			continue
		}
		if c.AutoUpdates != nil && *c.AutoUpdates && !g.autoUpdates && !always[t] {
			continue
		}
		out = append(out, outdatedItem{Name: t, InstalledVersions: []string{currentCaskVersion(t)}, CurrentVersion: c.Version})
	}
	return out
}

func cmdUpgrade(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	rejectUnsupportedInstallFlags(o)
	env, quarantine := caskOptions(o)
	ensureAPI()
	dry := o.Has("dry-run")
	g := greedyFrom(o)
	var formulae, casks []outdatedItem
	var pinned, namedPinned []string
	// Homebrew fails when a pinned formula is named explicitly.
	finish := func() {
		if len(namedPinned) > 0 {
			exit(1)
		}
	}
	if len(o.Pos) > 0 {
		for _, n := range o.Pos {
			if !validPackageArg(n) {
				onoe("%s not installed", n)
				exit(1)
			}
			if !o.onlyCask() {
				if r := resolveInstalledFormula(n); r != "" {
					items := outdatedFormulae([]string{r}, false)
					switch {
					case len(items) == 0:
						opoo("%s %s already installed", r, currentVersion(r))
					case isPinned(r):
						namedPinned = append(namedPinned, fmt.Sprintf("%s %s", r, strings.Join(items[0].InstalledVersions, ", ")))
					default:
						formulae = append(formulae, items...)
					}
					continue
				}
			}
			if !o.onlyFormula() {
				if t := resolveInstalledCask(n); t != "" {
					items := outdatedCasks([]string{t}, greedy{true, true})
					if len(items) == 0 {
						opoo("Not upgrading %s, the latest version is already installed", t)
					}
					casks = append(casks, items...)
					continue
				}
			}
			onoe("%s not installed", n)
			exit(1)
		}
	} else {
		if !o.onlyCask() {
			for _, it := range outdatedFormulae(nil, false) {
				if it.Pinned {
					pinned = append(pinned, fmt.Sprintf("%s %s", it.Name, strings.Join(it.InstalledVersions, ", ")))
					continue
				}
				formulae = append(formulae, it)
			}
		}
		if !o.onlyFormula() {
			casks = outdatedCasks(nil, g)
		}
	}
	// Dependents of upgraded formulae that are outdated too (Homebrew's
	// installed-dependents check).
	var dependents []outdatedItem
	if len(formulae) > 0 && !envSet("HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK") {
		chosen := map[string]bool{}
		for _, it := range formulae {
			chosen[it.Name] = true
		}
		deps := installedRuntimeDeps()
		queue := append([]outdatedItem(nil), formulae...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, d := range installedDependents(cur.Name, deps) {
				if chosen[d] || isPinned(d) {
					continue
				}
				if items := outdatedFormulae([]string{d}, false); len(items) > 0 {
					chosen[d] = true
					dependents = append(dependents, items...)
					queue = append(queue, items...)
				}
			}
		}
	}
	if len(pinned) > 0 && !quiet {
		ohai("Not upgrading %s:", plural(len(pinned), "pinned package"))
		fmt.Println(strings.Join(pinned, "\n"))
	}
	if len(namedPinned) > 0 {
		onoe("Not upgrading %s:", plural(len(namedPinned), "pinned package"))
		fmt.Println(strings.Join(namedPinned, ", "))
	}
	all := append(append([]outdatedItem(nil), formulae...), casks...)
	if len(all) == 0 && len(dependents) == 0 {
		finish()
		return
	}
	verb := "Upgrading"
	if dry {
		verb = "Would upgrade"
	}
	if len(all) > 0 && !quiet {
		ohai("%s %s:", verb, plural(len(all), "outdated package"))
		for _, it := range all {
			fmt.Printf("%s %s -> %s\n", it.Name, strings.Join(it.InstalledVersions, ", "), it.CurrentVersion)
		}
	}
	if len(dependents) > 0 && !quiet {
		ohai("%s %s of upgraded formulae:", verb, plural(len(dependents), "dependent"))
		for _, it := range dependents {
			fmt.Printf("%s %s -> %s\n", it.Name, strings.Join(it.InstalledVersions, ", "), it.CurrentVersion)
		}
	}
	if dry {
		finish()
		return
	}
	if o.Has("ask") && !confirm("Do you want to proceed with the upgrade?") {
		exit(1)
	}
	var keys []string
	for _, it := range append(formulae, dependents...) {
		keys = append(keys, formulaKey(it.Name))
	}
	for _, it := range casks {
		keys = append(keys, "brew-cask:"+it.Name)
	}
	before := snapshotKegs()
	r := &miseRun{extraEnv: env}
	code := runMiseBrewStyle(r, append([]string{"bootstrap", "packages", "upgrade", "-y"}, keys...)...)
	if quarantine {
		for _, it := range casks {
			quarantineCask(it.Name)
		}
	}
	installCleanup(changedFormulae(before))
	if o.Has("display-times") {
		r.printTimes()
	}
	if code != 0 {
		exit(code)
	}
	finish()
}

func cmdUpdate(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	before := formulaAPI.ModTime()
	for _, m := range []string{"brew", "brew-cask"} {
		out, err := miseCombined("bootstrap", "packages", "upgrade", "--dry-run", "-m", m)
		if err != nil && (verbose || debug) {
			fmt.Fprint(os.Stderr, string(out))
		}
	}
	tapRefresh = true
	for tap := range configuredTaps() {
		if loadTapIndex(tap) != nil && verbose {
			fmt.Printf("Updated tap %s\n", tap)
		}
	}
	tapRefresh = false
	if quiet {
		return
	}
	if formulaAPI.ModTime().After(before) {
		fmt.Println("Updated Homebrew API data from formulae.brew.sh (via mise).")
	} else {
		fmt.Println("Already up-to-date.")
	}
	formulaCache, caskCache = map[string]*Formula{}, map[string]*Cask{}
	formulaAPI, caskAPI = &apiSource{kind: "formula"}, &apiSource{kind: "cask"}
	f, c := outdatedFormulae(nil, false), outdatedCasks(nil, greedy{})
	for _, grp := range []struct {
		title string
		items []outdatedItem
	}{{"Outdated Formulae", f}, {"Outdated Casks", c}} {
		if len(grp.items) == 0 {
			continue
		}
		ohai("%s", grp.title)
		var names []string
		for _, x := range grp.items {
			names = append(names, x.Name)
		}
		putsColumns(names)
	}
	if n := len(f) + len(c); n > 0 {
		them := map[bool]string{true: "it", false: "them"}[n == 1]
		fmt.Printf("\nYou have %s installed.\nYou can upgrade %s with brew upgrade\nor list %s with brew outdated.\n", outdatedSummary(len(f), len(c)), them, them)
	}
}

func cmdOutdated(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	ensureAPI()
	g := greedyFrom(o)
	var fnames, cnames []string
	if len(o.Pos) > 0 {
		fnames, cnames = []string{}, []string{}
		for _, n := range o.Pos {
			if !validPackageArg(n) {
				odie("No such keg: %s", cellar()+"/"+n)
			}
			if r := resolveInstalledFormula(n); r != "" && !o.onlyCask() {
				fnames = append(fnames, r)
			} else if t := resolveInstalledCask(n); t != "" && !o.onlyFormula() {
				cnames = append(cnames, t)
			} else {
				odie("No such keg: %s", filepath.Join(cellar(), n))
			}
		}
		g = greedy{true, true}
	}
	var f, c []outdatedItem
	if !o.onlyCask() {
		f = outdatedFormulae(fnames, false)
	}
	if !o.onlyFormula() {
		c = outdatedCasks(cnames, g)
	}
	// Homebrew: naming packages makes outdated fail when any of them is outdated.
	finish := func() {
		if len(o.Pos) > 0 && len(f)+len(c) > 0 {
			exit(1)
		}
	}
	if v, ok := o.Val("json"); ok {
		if f == nil {
			f = []outdatedItem{}
		}
		switch v {
		case "", "v2": // bare --json means v2 for outdated
		case "v1":
			printJSON(f)
			finish()
			return
		default:
			odie("Invalid usage: invalid JSON version: %s", v)
		}
		type caskItem struct {
			Name              string   `json:"name"`
			InstalledVersions []string `json:"installed_versions"`
			CurrentVersion    string   `json:"current_version"`
		}
		cs := []caskItem{}
		for _, x := range c {
			cs = append(cs, caskItem{x.Name, x.InstalledVersions, x.CurrentVersion})
		}
		printJSON(map[string]any{"formulae": f, "casks": cs})
		finish()
		return
	}
	detailed := (stdoutTTY || verbose) && !quiet
	for _, list := range [][]outdatedItem{f, c} {
		for _, it := range list {
			if !detailed {
				fmt.Println(it.Name)
				continue
			}
			line := fmt.Sprintf("%s (%s) < %s", it.Name, strings.Join(it.InstalledVersions, ", "), it.CurrentVersion)
			if it.Pinned {
				line += fmt.Sprintf(" [pinned at %s]", *it.PinnedVersion)
			}
			fmt.Println(line)
		}
	}
	finish()
}

// --- cleanup ----------------------------------------------------------------

type cleanupOpts struct {
	dry, scrub, prefixOnly bool
	pruneDays              int // -1 = default (HOMEBREW_CLEANUP_MAX_AGE_DAYS or 120)
	names                  []string
	quietNone              bool
}

func cmdCleanup(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	c := cleanupOpts{dry: o.Has("dry-run"), scrub: o.Has("scrub"), prefixOnly: o.Has("prune-prefix"), pruneDays: -1}
	if v, ok := o.Val("prune"); ok {
		if v == "all" {
			c.pruneDays = 0
		} else if n, err := strconv.Atoi(v); err == nil {
			c.pruneDays = n
		} else {
			odie("--prune= expects a number of days or \"all\"")
		}
	}
	for _, n := range o.Pos {
		if !validPackageArg(n) {
			opoo("No installed formula or cask with the name \"%s\".", n)
		} else if r := resolveInstalledFormula(n); r != "" {
			c.names = append(c.names, r)
		} else if t := resolveInstalledCask(n); t != "" {
			c.names = append(c.names, "cask:"+t)
		} else {
			opoo("No installed formula or cask with the name \"%s\".", n)
		}
	}
	if len(o.Pos) > 0 && len(c.names) == 0 {
		return // never fall back to cleaning up everything
	}
	doCleanup(c)
	if len(o.Pos) == 0 && !c.dry {
		touchCleaned()
	}
}

func doCleanup(c cleanupOpts) int64 {
	defer lockPrefix()()
	var freed int64
	verb := "Removing"
	if c.dry {
		verb = "Would remove"
	}
	remove := func(p string, size int64, summary string) {
		if !cleanupAllowed(p) {
			opoo("Refusing to remove %s: not a keg, cask version or cache file.", p)
			return
		}
		fmt.Printf("%s: %s%s\n", verb, p, summary)
		freed += size
		if !c.dry {
			_ = os.RemoveAll(p)
		}
	}
	maxAge := 120
	if v, err := strconv.Atoi(os.Getenv("HOMEBREW_CLEANUP_MAX_AGE_DAYS")); err == nil {
		maxAge = v
	}
	if c.pruneDays >= 0 {
		maxAge = c.pruneDays
	}
	old := func(t time.Time) bool { return time.Since(t) > time.Duration(maxAge)*24*time.Hour }

	if !c.prefixOnly {
		formulae, casks := installedFormulae(), installedCasks()
		if len(c.names) > 0 {
			formulae, casks = nil, nil
			for _, n := range c.names {
				if strings.HasPrefix(n, "cask:") {
					casks = append(casks, strings.TrimPrefix(n, "cask:"))
				} else {
					formulae = append(formulae, n)
				}
			}
		}
		formulae, casks = filterValidNames(formulae), filterValidNames(casks)
		// Old kegs.
		for _, n := range formulae {
			if isPinned(n) {
				continue
			}
			cur := currentVersion(n)
			for _, v := range kegVersions(n) {
				if v == cur {
					continue
				}
				keg := kegPath(n, v)
				files, size := diskUsage(keg)
				if !c.dry {
					unlinkKeg(n, v, false)
				}
				remove(keg, size, fmt.Sprintf("... (%s, %s)", plural(files, "file"), humanSize(size)))
			}
		}
		// Old cask versions (mise keeps a `latest` symlink to the current one).
		for _, t := range casks {
			cur := currentCaskVersion(t)
			for _, v := range caskVersions(t) {
				if v == cur {
					continue
				}
				p := filepath.Join(caskroom(), t, v)
				files, size := diskUsage(p)
				remove(p, size, fmt.Sprintf("... (%s, %s)", plural(files, "file"), humanSize(size)))
			}
		}
		// Downloads: mise's bottles and mise-brew's cask/source downloads.
		current := map[string]string{}
		for _, n := range installedFormulae() {
			current[n] = currentVersion(n)
		}
		for _, dir := range []string{bottlesDir(), filepath.Join(miseBrewCacheDir(), "downloads")} {
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				info, err := e.Info()
				if err != nil {
					continue
				}
				name, ver := splitBottleName(e.Name(), current)
				stale := c.scrub || maxAge == 0 || name == "" || ver != current[name] || old(info.ModTime())
				if len(c.names) > 0 && !contains(formulae, name) {
					stale = false
				}
				if stale {
					remove(filepath.Join(dir, e.Name()), info.Size(), fmt.Sprintf(" (%s)", humanSize(info.Size())))
				}
			}
		}
		if len(c.names) == 0 {
			// Interrupted mise extractions and empty racks.
			for _, rack := range listDirNames(cellar(), true) {
				rd := filepath.Join(cellar(), rack)
				entries, _ := os.ReadDir(rd)
				for _, e := range entries {
					if strings.HasPrefix(e.Name(), ".mise-extract-") {
						if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
							remove(filepath.Join(rd, e.Name()), 0, "")
						}
					}
				}
				if len(kegVersions(rack)) == 0 {
					if entries, _ := os.ReadDir(rd); len(entries) == 0 && !c.dry {
						_ = os.Remove(rd)
					}
				}
			}
			// Logs.
			logs := logsDir()
			if entries, err := os.ReadDir(logs); err == nil {
				for _, e := range entries {
					if info, err := e.Info(); err == nil && (old(info.ModTime()) || c.scrub) {
						remove(filepath.Join(logs, e.Name()), 0, "")
					}
				}
			}
		}
	}
	if len(c.names) == 0 {
		broken := brokenSymlinks()
		if len(broken) > 0 {
			if c.dry {
				fmt.Printf("Would prune %s from %s\n", plural(len(broken), "symbolic link"), brewPrefix())
			} else {
				for _, b := range broken {
					_ = os.Remove(b)
				}
				fmt.Printf("Pruned %s from %s\n", plural(len(broken), "symbolic link"), brewPrefix())
			}
		}
	}
	if freed > 0 {
		if c.dry {
			ohai("This operation would free approximately %s of disk space.", humanSize(freed))
		} else {
			ohai("This operation has freed approximately %s of disk space.", humanSize(freed))
		}
	}
	return freed
}

// logsDir is where Homebrew (and so `brew cleanup`) keeps build logs.
func logsDir() string { return filepath.Join(homeDir(), "Library", "Logs", "Homebrew") }

func filterValidNames(names []string) []string {
	var out []string
	for _, n := range names {
		if validName(n) {
			out = append(out, n)
		} else {
			odebug("cleanup: skipping invalid name %q", n)
		}
	}
	return out
}

// plainEntry is a directory entry name that cannot escape its parent.
func plainEntry(s string) bool {
	return s != "" && !strings.HasPrefix(s, ".") && !strings.ContainsAny(s, `/\`)
}

// cleanupAllowed reports whether p is something doCleanup may delete:
// <Cellar>/<rack>/<version> (or an interrupted .mise-extract-* there),
// <Caskroom>/<token>/<version>, or a file directly in the bottle, download
// or log directories.
func cleanupAllowed(p string) bool {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return false
	}
	parent, base := filepath.Dir(p), filepath.Base(p)
	grand, mid := filepath.Dir(parent), filepath.Base(parent)
	switch {
	case grand == cellar() && validName(mid):
		return plainEntry(base) || (strings.HasPrefix(base, ".mise-extract-") && plainEntry(strings.TrimPrefix(base, ".mise-extract-")))
	case grand == caskroom() && validName(mid):
		return plainEntry(base)
	case parent == bottlesDir(), parent == filepath.Join(miseBrewCacheDir(), "downloads"), parent == logsDir():
		return base != "." && base != ".." && !strings.ContainsAny(base, `/\`)
	}
	return false
}

func cleanedStamp() string { return filepath.Join(miseBrewCacheDir(), ".cleaned") }

func touchCleaned() {
	_ = os.MkdirAll(miseBrewCacheDir(), 0o755)
	_ = os.WriteFile(cleanedStamp(), nil, 0o644)
	now := time.Now()
	_ = os.Chtimes(cleanedStamp(), now, now)
}

// installCleanup is Homebrew's post-install cleanup: old versions of the
// formulae just installed, plus a full cleanup every 30 days.
func installCleanup(changed []string) {
	if envSet("HOMEBREW_NO_INSTALL_CLEANUP") || len(changed) == 0 {
		return
	}
	days := 30
	if v, err := strconv.Atoi(os.Getenv("HOMEBREW_CLEANUP_PERIODIC_FULL_DAYS")); err == nil {
		days = v
	}
	fi, err := os.Stat(cleanedStamp())
	if err != nil || time.Since(fi.ModTime()) > time.Duration(days)*24*time.Hour {
		if err == nil {
			ohai("`brew cleanup` has not been run in the last %d days, running now...", days)
			envHint("Disable this behaviour by setting `HOMEBREW_NO_INSTALL_CLEANUP=1`.")
			doCleanup(cleanupOpts{pruneDays: -1})
		}
		touchCleaned()
		return
	}
	var stale []string
	for _, n := range changed {
		cur := currentVersion(n)
		for _, v := range kegVersions(n) {
			if v != cur && !isPinned(n) {
				stale = append(stale, n)
				break
			}
		}
	}
	for _, n := range stale {
		ohai("Running `brew cleanup %s`...", n)
		envHint("Disable this behaviour by setting `HOMEBREW_NO_INSTALL_CLEANUP=1`.")
		doCleanup(cleanupOpts{pruneDays: -1, names: []string{n}})
	}
}
