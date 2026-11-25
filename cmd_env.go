package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

// --- path queries -----------------------------------------------------------

// formulaNameForPath resolves a name for --prefix / --cellar without requiring installation.
func formulaNameForPath(n string) (string, bool) {
	if r := resolveInstalledFormula(n); r != "" {
		return r, true
	}
	s := stripCoreTap(n)
	if isThirdPartyTapName(s) {
		return baseName(s), true
	}
	if f := lookupFormula(s); f != nil {
		return f.Name, true
	}
	if !formulaAPI.Available() {
		return s, true
	}
	return "", false
}

func cmdPrefix(args []string) {
	o := parseArgs(args, nil, nil)
	if o.Has("unbrewed") {
		odie("--unbrewed is not supported by mise-brew")
	}
	if len(o.Pos) == 0 {
		if o.onlyCask() {
			fmt.Println(caskroom())
			return
		}
		fmt.Println(brewPrefix())
		return
	}
	failed := false
	for _, n := range o.Pos {
		if o.onlyCask() {
			if t := resolveInstalledCask(n); t != "" {
				fmt.Println(filepath.Join(caskroom(), t, currentCaskVersion(t)))
				continue
			}
			onoe("Cask '%s' is not installed.", n)
			failed = true
			continue
		}
		name, ok := formulaNameForPath(n)
		if !ok {
			onoe("No available formula with the name \"%s\".", n)
			failed = true
			continue
		}
		if o.Has("installed") && !isFormulaInstalled(name) {
			failed = true
			continue
		}
		fmt.Println(filepath.Join(optDir(), name))
	}
	if failed {
		exit(1)
	}
}

func cmdCellar(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(cellar())
		return
	}
	for _, n := range o.Pos {
		name, ok := formulaNameForPath(n)
		if !ok {
			odie("No available formula with the name \"%s\".", n)
		}
		fmt.Println(filepath.Join(cellar(), name))
	}
}

func cmdCaskroom(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(caskroom())
		return
	}
	for _, n := range o.Pos {
		t := stripCoreTap(n)
		if c := lookupCask(t); c != nil {
			t = c.Token
		}
		fmt.Println(filepath.Join(caskroom(), t))
	}
}

func cmdCache(args []string) {
	o := parseArgs(args, nil, nil)
	if tag, ok := o.Val("bottle-tag"); ok {
		os.Setenv("MISEBREW_BOTTLE_TAG", tag)
	}
	if len(o.Pos) == 0 {
		fmt.Println(brewCacheDir())
		return
	}
	for _, n := range o.Pos {
		if !o.onlyCask() {
			if f := lookupFormula(n); f != nil {
				if o.Has("build-from-source") || o.Has("HEAD") {
					fmt.Println(filepath.Join(miseBrewCacheDir(), "downloads", f.Name+"--"+f.Versions.Stable+filepath.Ext(f.URLs.Stable.URL)))
				} else {
					fmt.Println(bottleCachePath(f))
				}
				continue
			}
		}
		if c := lookupCask(n); c != nil && !o.onlyFormula() {
			fmt.Println(caskCachePath(c))
			continue
		}
		odie("No available formula or cask with the name \"%s\".", n)
	}
}

func cmdRepository(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(brewRepository())
		return
	}
	for _, t := range o.Pos {
		parts := strings.SplitN(strings.ToLower(t), "/", 2)
		if len(parts) != 2 {
			odie("Invalid tap name: %s", t)
		}
		repo := strings.TrimPrefix(parts[1], "homebrew-")
		fmt.Println(filepath.Join(brewRepository(), "Library", "Taps", parts[0], "homebrew-"+repo))
	}
}

// homebrewVersion reports the Homebrew version mise emulates (from receipts).
func homebrewVersion() string {
	if v := os.Getenv("MISEBREW_HOMEBREW_VERSION"); v != "" {
		return v
	}
	best := ""
	for _, n := range listDirNames(optDir(), false) {
		r := readReceipt(filepath.Join(optDir(), n))
		if r == nil || r.HomebrewVersion == "" {
			continue
		}
		v := strings.TrimSpace(strings.TrimSuffix(r.HomebrewVersion, "(mise)"))
		if best == "" || compareVersions(v, best) > 0 {
			best = v
		}
		if best != "" {
			break
		}
	}
	if best == "" {
		return defaultHomebrewVersion
	}
	return best
}

func cmdVersion([]string) {
	fmt.Printf("Homebrew %s\n", homebrewVersion())
	fmt.Printf("mise-brew %s (packages managed by mise)\n", miseBrewVersion)
}

// --- shellenv / env ---------------------------------------------------------

func cmdShellenv(args []string) {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	}
	if shell == "" {
		shell = filepath.Base(os.Getenv("SHELL"))
		if ppid := os.Getppid(); ppid > 1 {
			if out, err := exec.Command("ps", "-p", fmt.Sprint(ppid), "-o", "comm=").Output(); err == nil {
				if s := strings.TrimPrefix(filepath.Base(strings.TrimSpace(string(out))), "-"); s != "" {
					shell = s
				}
			}
		}
	}
	p, c, r := brewPrefix(), cellar(), brewRepository()
	pathHas := func(dir string) bool {
		for _, e := range filepath.SplitList(os.Getenv("PATH")) {
			if e == dir {
				return true
			}
		}
		return false
	}
	addPath := !(pathHas(p+"/bin") && pathHas(p+"/sbin"))
	switch shell {
	case "fish", "-fish":
		fmt.Printf("set --global --export HOMEBREW_PREFIX \"%s\";\n", p)
		fmt.Printf("set --global --export HOMEBREW_CELLAR \"%s\";\n", c)
		fmt.Printf("set --global --export HOMEBREW_REPOSITORY \"%s\";\n", r)
		if addPath {
			fmt.Printf("fish_add_path --global --move --path \"%s/bin\" \"%s/sbin\";\n", p, p)
		}
		fmt.Printf("if test -n \"$MANPATH[1]\"; set --global --export MANPATH '' $MANPATH; end;\n")
		fmt.Printf("if not contains \"%s/share/info\" $INFOPATH; set --global --export INFOPATH \"%s/share/info\" $INFOPATH; end;\n", p, p)
	case "csh", "-csh", "tcsh", "-tcsh":
		fmt.Printf("setenv HOMEBREW_PREFIX %s;\n", p)
		fmt.Printf("setenv HOMEBREW_CELLAR %s;\n", c)
		fmt.Printf("setenv HOMEBREW_REPOSITORY %s;\n", r)
		if addPath {
			fmt.Printf("setenv PATH %s/bin:%s/sbin:$PATH;\n", p, p)
		}
		fmt.Printf("test ${?MANPATH} -eq 1 && setenv MANPATH :${MANPATH};\n")
		fmt.Printf("setenv INFOPATH %s/share/info`test ${?INFOPATH} -eq 1 && echo :${INFOPATH}`;\n", p)
	case "pwsh", "-pwsh", "pwsh-preview", "-pwsh-preview":
		fmt.Printf("[System.Environment]::SetEnvironmentVariable('HOMEBREW_PREFIX','%s',[System.EnvironmentVariableTarget]::Process)\n", p)
		fmt.Printf("[System.Environment]::SetEnvironmentVariable('HOMEBREW_CELLAR','%s',[System.EnvironmentVariableTarget]::Process)\n", c)
		fmt.Printf("[System.Environment]::SetEnvironmentVariable('HOMEBREW_REPOSITORY','%s',[System.EnvironmentVariableTarget]::Process)\n", r)
		if addPath {
			fmt.Printf("[System.Environment]::SetEnvironmentVariable('PATH',$('%s/bin:%s/sbin:'+$ENV:PATH),[System.EnvironmentVariableTarget]::Process)\n", p, p)
		}
		fmt.Printf("[System.Environment]::SetEnvironmentVariable('MANPATH',$('%s/share/man'+$(if(${ENV:MANPATH}){':'+${ENV:MANPATH}})+':'),[System.EnvironmentVariableTarget]::Process)\n", p)
		fmt.Printf("[System.Environment]::SetEnvironmentVariable('INFOPATH',$('%s/share/info'+$(if(${ENV:INFOPATH}){':'+${ENV:INFOPATH}})),[System.EnvironmentVariableTarget]::Process)\n", p)
	default: // bash, zsh, sh, dash, ksh
		fmt.Printf("export HOMEBREW_PREFIX=\"%s\";\n", p)
		fmt.Printf("export HOMEBREW_CELLAR=\"%s\";\n", c)
		fmt.Printf("export HOMEBREW_REPOSITORY=\"%s\";\n", r)
		if strings.TrimPrefix(shell, "-") == "zsh" {
			fmt.Printf("fpath[1,0]=\"%s/share/zsh/site-functions\";\n", p)
		}
		if addPath {
			fmt.Printf("export PATH=\"%s/bin:%s/sbin${PATH+:$PATH}\";\n", p, p)
		}
		fmt.Printf("[ -z \"${MANPATH-}\" ] || export MANPATH=\":${MANPATH#:}\";\n")
		fmt.Printf("export INFOPATH=\"%s/share/info:${INFOPATH:-}\";\n", p)
	}
}

// --- config / doctor --------------------------------------------------------

func firstLine(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return "N/A"
	}
	s := strings.TrimSpace(string(out))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

func cmdConfig([]string) {
	miseVer := strings.TrimSpace(func() string { b, _ := miseOutput("--version"); return string(b) }())
	fmt.Printf("HOMEBREW_VERSION: %s (mise-brew %s)\n", homebrewVersion(), miseBrewVersion)
	fmt.Printf("ORIGIN: mise bootstrap packages (%s)\n", miseBin())
	fmt.Printf("HEAD: (none, packages are managed by mise)\n")
	if b, err := os.ReadFile(filepath.Join(apiDir(), "formula.last-modified")); err == nil {
		fmt.Printf("Core tap JSON: %s\n", strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile(filepath.Join(apiDir(), "cask.last-modified")); err == nil {
		fmt.Printf("Core cask tap JSON: %s\n", strings.TrimSpace(string(b)))
	}
	fmt.Printf("HOMEBREW_PREFIX: %s\n", brewPrefix())
	fmt.Printf("HOMEBREW_CELLAR: %s\n", cellar())
	fmt.Printf("HOMEBREW_CASKROOM: %s\n", caskroom())
	fmt.Printf("HOMEBREW_CACHE: %s\n", brewCacheDir())
	fmt.Printf("HOMEBREW_REPOSITORY: %s\n", brewRepository())
	fmt.Printf("HOMEBREW_MAKE_JOBS: %d\n", runtime.NumCPU())
	fmt.Printf("HOMEBREW_NO_ANALYTICS: set\n")
	fmt.Printf("mise: %s\n", miseVer)
	fmt.Printf("Homebrew Ruby: N/A (mise-brew is a Go binary)\n")
	fmt.Printf("Curl: %s\n", firstLine("curl", "--version"))
	fmt.Printf("Git: %s\n", firstLine("git", "--version"))
	var hb []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOMEBREW_") || strings.HasPrefix(e, "MISEBREW_") {
			k, v, _ := strings.Cut(e, "=")
			if strings.Contains(k, "TOKEN") || strings.Contains(k, "PASSWORD") {
				v = "set"
			}
			hb = append(hb, k+": "+v)
		}
	}
	sort.Strings(hb)
	for _, l := range hb {
		fmt.Println(l)
	}
	fmt.Printf("mise config target: %s\n", strings.Join(append([]string{"bootstrap packages use"}, scopeArgs()...), " "))
	cpu := fmt.Sprintf("%d-core 64-bit %s", runtime.NumCPU(), runtime.GOARCH)
	if runtime.GOOS == "darwin" {
		if b := firstLine("sysctl", "-n", "machdep.cpu.brand_string"); b != "N/A" {
			cpu += " (" + b + ")"
		}
	}
	fmt.Printf("CPU: %s\n", cpu)
	if runtime.GOOS == "darwin" {
		fmt.Printf("Clang: %s\n", firstLine("clang", "--version"))
		fmt.Printf("macOS: %s-%s\n", firstLine("sw_vers", "-productVersion"), runtime.GOARCH)
		clt := "N/A"
		if out, err := exec.Command("pkgutil", "--pkg-info=com.apple.pkg.CLTools_Executables").Output(); err == nil {
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, "version: ") {
					clt = strings.TrimPrefix(l, "version: ")
				}
			}
		}
		fmt.Printf("CLT: %s\n", clt)
		fmt.Printf("Xcode: %s\n", strings.TrimPrefix(firstLine("xcodebuild", "-version"), "Xcode "))
		fmt.Printf("Rosetta 2: %v\n", fileExists("/Library/Apple/usr/share/rosetta/rosetta"))
	} else {
		fmt.Printf("Kernel: %s\n", firstLine("uname", "-mors"))
		fmt.Printf("glibc: %s\n", firstLine("ldd", "--version"))
	}
}

type doctorCheck struct {
	name string
	run  func() string // returns "" when fine
}

func doctorChecks() []doctorCheck {
	return []doctorCheck{
		{"check_mise", func() string {
			if _, err := miseOutput("--version"); err != nil {
				return fmt.Sprintf("mise is not working:\n  %v", err)
			}
			return ""
		}},
		{"check_prefix_writable", func() string {
			if !isDir(brewPrefix()) {
				return fmt.Sprintf("The Homebrew prefix %s does not exist.\nRun `brew install <formula>` (mise creates it) or set MISEBREW_PREFIX.", brewPrefix())
			}
			if syscall.Access(brewPrefix(), 2) != nil {
				return fmt.Sprintf("%s is not writable.\nYou should change the ownership of %s to your user:\n  sudo chown -R $(whoami) %s", brewPrefix(), brewPrefix(), brewPrefix())
			}
			return ""
		}},
		{"check_api_freshness", func() string {
			if !formulaAPI.Available() {
				return "mise has no copy of the Homebrew API yet.\nRun `brew update`."
			}
			if age := time.Since(formulaAPI.ModTime()); age > 7*24*time.Hour {
				return fmt.Sprintf("mise's copy of the Homebrew API is %d days old.\nRun `brew update`.", int(age.Hours()/24))
			}
			return ""
		}},
		{"check_brew_on_path", func() string {
			self, err := os.Executable()
			if err != nil {
				return ""
			}
			self, _ = filepath.EvalSymlinks(self)
			if p, err := exec.LookPath("brew"); err == nil {
				if rp, _ := filepath.EvalSymlinks(p); rp != self {
					return fmt.Sprintf("The first `brew` on your PATH is not mise-brew:\n  %s\nmise-brew is %s", p, self)
				}
			}
			return ""
		}},
		{"check_user_path", func() string {
			bin := filepath.Join(brewPrefix(), "bin")
			var idx, usr = -1, -1
			for i, e := range filepath.SplitList(os.Getenv("PATH")) {
				if e == bin && idx < 0 {
					idx = i
				}
				if e == "/usr/bin" && usr < 0 {
					usr = i
				}
			}
			if idx < 0 {
				return fmt.Sprintf("Homebrew's \"bin\" was not found in your PATH.\nConsider setting your PATH for example like so:\n  echo 'eval \"$(%s/bin/brew shellenv)\"' >> ~/.zprofile", brewPrefix())
			}
			if usr >= 0 && usr < idx {
				return fmt.Sprintf("/usr/bin occurs before %s in your PATH.\nThis means that system-provided programs will be used instead of those\nprovided by Homebrew.", bin)
			}
			return ""
		}},
		{"check_for_installed_developer_tools", func() string {
			if runtime.GOOS != "darwin" {
				return ""
			}
			if err := exec.Command("xcode-select", "-p").Run(); err != nil {
				return "No developer tools installed.\nInstall the Command Line Tools:\n  xcode-select --install"
			}
			return ""
		}},
		{"check_git", func() string {
			if _, err := exec.LookPath("git"); err != nil {
				return "Git could not be found in your PATH.\nHomebrew uses Git for several internal functions and some formulae use Git\ncheckouts instead of stable tarballs."
			}
			return ""
		}},
		{"check_broken_symlinks", func() string {
			if broken := brokenSymlinks(); len(broken) > 0 {
				sort.Strings(broken)
				return fmt.Sprintf("Broken symlinks were found. Remove them with `brew cleanup`:\n  %s", strings.Join(limit(broken, 20), "\n  "))
			}
			return ""
		}},
		{"check_for_unlinked_but_not_keg_only", func() string {
			var unlinked []string
			for _, n := range installedFormulae() {
				if !isKegLinked(n) {
					if f := lookupFormula(n); f == nil || !f.KegOnly {
						unlinked = append(unlinked, n)
					}
				}
			}
			if len(unlinked) > 0 {
				return fmt.Sprintf("You have unlinked kegs in your Cellar.\nLeaving kegs unlinked can lead to build-trouble and cause formulae that depend on\nthose kegs to fail to run properly once built. Run `brew link` on these:\n  %s", strings.Join(unlinked, "\n  "))
			}
			return ""
		}},
		{"check_missing_opt_links", func() string {
			var noOpt []string
			for _, n := range installedFormulae() {
				if !isDir(filepath.Join(optDir(), n)) {
					noOpt = append(noOpt, n)
				}
			}
			if len(noOpt) > 0 {
				return fmt.Sprintf("Some installed formulae are missing their opt link:\n  %s\nRun `brew reinstall` on them.", strings.Join(noOpt, "\n  "))
			}
			return ""
		}},
		{"check_missing_deps", func() string {
			var missing []string
			for _, n := range installedFormulae() {
				if r := readReceipt(kegPath(n, currentVersion(n))); r != nil {
					for _, d := range r.RuntimeDependencies {
						if b := baseName(d.FullName); !isFormulaInstalled(b) {
							missing = append(missing, n+": "+b)
						}
					}
				}
			}
			if len(missing) > 0 {
				return fmt.Sprintf("Some installed formulae are missing dependencies:\n  %s\nRun `brew install` for each one.", strings.Join(missing, "\n  "))
			}
			return ""
		}},
		{"check_deprecated_disabled", func() string {
			var dep, dis []string
			for _, n := range installedFormulae() {
				if f := lookupFormula(n); f != nil && f.Name == n {
					if f.Disabled {
						dis = append(dis, n)
					} else if f.Deprecated {
						dep = append(dep, n)
					}
				}
			}
			var parts []string
			if len(dep) > 0 {
				parts = append(parts, fmt.Sprintf("Some installed formulae are deprecated or disabled.\nYou should find replacements for the following formulae:\n  %s", strings.Join(append(dep, dis...), "\n  ")))
			}
			return strings.Join(parts, "\n")
		}},
		{"check_undeclared_formulae", func() string {
			if cands, _ := pruneCandidates("brew", false); len(cands) > 0 {
				return fmt.Sprintf("These formulae are installed but no mise config declares or needs them;\n`brew autoremove --mise` (mise bootstrap packages prune) would remove them:\n  %s\nKeep one with `brew install <formula>`.", strings.Join(cands, "\n  "))
			}
			return ""
		}},
		{"check_declared_packages", func() string {
			var notOK []string
			for m, st := range packagesStatus() {
				if m != "brew" && m != "brew-cask" {
					continue
				}
				for _, p := range st.Packages {
					if (p.DesiredState == "" || p.DesiredState == "present") && p.State != "installed" {
						notOK = append(notOK, fmt.Sprintf("%s:%s (%s)", m, p.Package, p.State))
					}
				}
			}
			if len(notOK) > 0 {
				sort.Strings(notOK)
				return fmt.Sprintf("Some declared packages are not installed:\n  %s\nRun `mise bootstrap packages apply`.", strings.Join(notOK, "\n  "))
			}
			return ""
		}},
	}
}

func cmdDoctor(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	checks := doctorChecks()
	if o.Has("list-checks") {
		for _, c := range checks {
			fmt.Println(c.name)
		}
		return
	}
	selected := checks
	if len(o.Pos) > 0 {
		selected = nil
		for _, c := range checks {
			if contains(o.Pos, c.name) {
				selected = append(selected, c)
			}
		}
	}
	var warnings []string
	for _, c := range selected {
		start := time.Now()
		if w := c.run(); w != "" {
			warnings = append(warnings, w)
		}
		if o.Has("audit-debug") {
			fmt.Fprintf(os.Stderr, "%s: %.3fs\n", c.name, time.Since(start).Seconds())
		}
	}
	if len(warnings) == 0 {
		fmt.Println("Your system is ready to brew.")
		return
	}
	fmt.Fprintln(os.Stderr, "Please note that these warnings are just used to help the Homebrew maintainers\nwith debugging if you file an issue. If everything you use Homebrew for is\nworking fine: please don't worry or file an issue; just ignore this. Thanks!")
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr)
		opoo("%s", w)
	}
	exit(1)
}

func limit(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], fmt.Sprintf("... and %d more", len(xs)-n))
	}
	return xs
}

func cmdAnalytics(args []string) {
	if len(args) > 0 && args[0] == "on" {
		opoo("mise-brew never sends analytics; leaving them disabled.")
		return
	}
	if len(args) == 0 || args[0] == "state" {
		fmt.Println("InfluxDB analytics are disabled.")
		fmt.Println("Google Analytics were destroyed.")
	}
}

func cmdDeveloper(args []string) {
	if len(args) > 0 && args[0] == "on" {
		opoo("developer mode is not available in mise-brew.")
		return
	}
	if len(args) == 0 || args[0] == "state" {
		fmt.Println("Developer mode is disabled.")
	}
}

func cmdCompletions(args []string) {
	if len(args) == 0 || args[0] == "state" {
		fmt.Println("Completions are not linked.")
		return
	}
	opoo("mise-brew does not ship completions; `brew completions %s` did nothing.", args[0])
}
