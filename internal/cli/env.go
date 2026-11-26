package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/rubnogueira/mise-brew/internal/buildinfo"
	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
	"github.com/rubnogueira/mise-brew/internal/version"
)

// --- path queries -----------------------------------------------------------

// formulaNameForPath resolves a name for --prefix / --cellar without requiring installation.
func formulaNameForPath(n string) (string, bool) {
	if r := homebrew.ResolveInstalledFormula(n); r != "" {
		return r, true
	}
	s := homebrew.StripCoreTap(n)
	if homebrew.IsThirdPartyTapName(s) {
		return homebrew.BaseName(s), true
	}
	if f := homebrew.LookupFormula(s); f != nil {
		return f.Name, true
	}
	if !homebrew.FormulaAPI.Available() {
		return s, true
	}
	return "", false
}

func cmdPrefix(args []string) {
	o := parseArgs(args, nil, nil)
	if o.Has("unbrewed") {
		ui.Odie("--unbrewed is not supported by mise-brew")
	}
	if len(o.Pos) == 0 {
		if o.onlyCask() {
			fmt.Println(paths.Caskroom())
			return
		}
		fmt.Println(paths.BrewPrefix())
		return
	}
	failed := false
	for _, n := range o.Pos {
		if o.onlyCask() {
			if t := homebrew.ResolveInstalledCask(n); t != "" {
				fmt.Println(filepath.Join(paths.Caskroom(), t, homebrew.CurrentCaskVersion(t)))
				continue
			}
			ui.Onoe("Cask '%s' is not installed.", n)
			failed = true
			continue
		}
		name, ok := formulaNameForPath(n)
		if !ok {
			ui.Onoe("No available formula with the name \"%s\".", n)
			failed = true
			continue
		}
		if o.Has("installed") && !homebrew.IsFormulaInstalled(name) {
			failed = true
			continue
		}
		fmt.Println(filepath.Join(paths.OptDir(), name))
	}
	if failed {
		ui.Exit(1)
	}
}

func cmdCellar(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(paths.Cellar())
		return
	}
	for _, n := range o.Pos {
		name, ok := formulaNameForPath(n)
		if !ok {
			ui.Odie("No available formula with the name \"%s\".", n)
		}
		fmt.Println(filepath.Join(paths.Cellar(), name))
	}
}

func cmdCaskroom(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(paths.Caskroom())
		return
	}
	for _, n := range o.Pos {
		t := homebrew.StripCoreTap(n)
		if c := homebrew.LookupCask(t); c != nil {
			t = c.Token
		}
		fmt.Println(filepath.Join(paths.Caskroom(), t))
	}
}

func cmdCache(args []string) {
	o := parseArgs(args, nil, nil)
	bottleTag, tagged := o.Val("bottle-tag")
	if tagged {
		if !homebrew.ValidName(bottleTag) {
			ui.Odie("Invalid bottle tag: %s", bottleTag)
		}
		os.Setenv("MISEBREW_BOTTLE_TAG", bottleTag)
	}
	if len(o.Pos) == 0 {
		fmt.Println(paths.BrewCacheDir())
		return
	}
	for _, n := range o.Pos {
		if !o.onlyCask() {
			if f := homebrew.LookupFormula(n); f != nil {
				if err := checkPathParts(f.Name, f.Versions.Stable); err != nil {
					ui.Odie("%v", err)
				}
				if o.Has("build-from-source") || o.Has("HEAD") {
					fmt.Println(filepath.Join(paths.MiseBrewCacheDir(), "downloads", f.Name+"--"+f.Versions.Stable+filepath.Ext(f.URLs.Stable.URL)))
				} else if tag, _, _, ok := bottleFileFor(f); ok && tagged {
					fmt.Println(bottleDownloadPath(f, tag))
				} else {
					fmt.Println(bottleCachePath(f))
				}
				continue
			}
		}
		if c := homebrew.LookupCask(n); c != nil && !o.onlyFormula() {
			if err := checkPathParts(c.Token, c.Version); err != nil {
				ui.Odie("%v", err)
			}
			fmt.Println(caskCachePath(c))
			continue
		}
		ui.Odie("No available formula or cask with the name \"%s\".", n)
	}
}

func cmdRepository(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(paths.BrewRepository())
		return
	}
	for _, t := range o.Pos {
		parts := strings.SplitN(strings.ToLower(t), "/", 2)
		if len(parts) != 2 {
			ui.Odie("Invalid tap name: %s", t)
		}
		repo := strings.TrimPrefix(parts[1], "homebrew-")
		fmt.Println(filepath.Join(paths.BrewRepository(), "Library", "Taps", parts[0], "homebrew-"+repo))
	}
}

// homebrewVersion reports the Homebrew version mise emulates (from receipts).
func homebrewVersion() string {
	if v := os.Getenv("MISEBREW_HOMEBREW_VERSION"); v != "" {
		return v
	}
	// Report the newest version any receipt claims (never below the default):
	// an old keg's receipt must not make `brew --version` fail min-version checks.
	best := buildinfo.DefaultHomebrewVersion
	for _, n := range homebrew.ListDirNames(paths.OptDir(), false) {
		r := homebrew.ReadReceipt(filepath.Join(paths.OptDir(), n))
		if r == nil || r.HomebrewVersion == "" {
			continue
		}
		v := strings.TrimSpace(strings.TrimSuffix(r.HomebrewVersion, "(mise)"))
		if v != "" && version.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

func cmdVersion([]string) {
	fmt.Printf("Homebrew %s\n", homebrewVersion())
	fmt.Printf("mise-brew %s (packages managed by mise)\n", buildinfo.Version)
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
	fmt.Print(shellenv(shell, os.Getenv("PATH"), paths.BrewPrefix(), paths.Cellar(), paths.BrewRepository()))
}

// shellenv ports Homebrew's Library/Homebrew/cmd/shellenv.sh. Like upstream it
// prints nothing once PATH already starts with the prefix's bin and sbin, so
// re-evaluating it does not duplicate INFOPATH or fpath entries; otherwise it
// always prepends them, even when path_helper moved them behind system dirs.
func shellenv(shell, path, p, c, r string) string {
	if strings.HasPrefix(path+":", p+"/bin:"+p+"/sbin:") {
		return ""
	}
	var b strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	switch shell {
	case "fish", "-fish":
		q := fishQuote
		line("set --global --export HOMEBREW_PREFIX %s;", q(p))
		line("set --global --export HOMEBREW_CELLAR %s;", q(c))
		line("set --global --export HOMEBREW_REPOSITORY %s;", q(r))
		line("fish_add_path --global --move --path %s %s;", q(p+"/bin"), q(p+"/sbin"))
		line(`if test -n "$MANPATH"; set --global --export MANPATH (string replace --regex '^:*(.*?):*$' ':$1' -- "$MANPATH"); end;`)
		line("if not set --query INFOPATH; set INFOPATH ''; end; set --global --export INFOPATH %s $INFOPATH;", q(p+"/share/info"))
	case "csh", "-csh", "tcsh", "-tcsh":
		line("setenv HOMEBREW_PREFIX %s;", cshWord(p))
		line("setenv HOMEBREW_CELLAR %s;", cshWord(c))
		line("setenv HOMEBREW_REPOSITORY %s;", cshWord(r))
		line("setenv PATH %s;", cshQuote(p+"/bin:"+p+"/sbin:", "$PATH"))
		line("test ${?MANPATH} -eq 1 && test -n \"${MANPATH}\" && setenv MANPATH :`printf '%%s' \"${MANPATH}\" | /usr/bin/sed -e 's/^:*//' -e 's/:*$//'`;")
		line("test ${?INFOPATH} -eq 1 || setenv INFOPATH '';")
		line("setenv INFOPATH %s;", cshQuote(p+"/share/info:", "${INFOPATH}"))
	case "pwsh", "-pwsh", "pwsh-preview", "-pwsh-preview":
		q := pwshQuote
		line("[System.Environment]::SetEnvironmentVariable('HOMEBREW_PREFIX',%s,[System.EnvironmentVariableTarget]::Process)", q(p))
		line("[System.Environment]::SetEnvironmentVariable('HOMEBREW_CELLAR',%s,[System.EnvironmentVariableTarget]::Process)", q(c))
		line("[System.Environment]::SetEnvironmentVariable('HOMEBREW_REPOSITORY',%s,[System.EnvironmentVariableTarget]::Process)", q(r))
		line("[System.Environment]::SetEnvironmentVariable('PATH',$(%s+$ENV:PATH),[System.EnvironmentVariableTarget]::Process)", q(p+"/bin:"+p+"/sbin:"))
		line("if (${ENV:MANPATH}) { [System.Environment]::SetEnvironmentVariable('MANPATH',(':'+${ENV:MANPATH}.Trim(':')),[System.EnvironmentVariableTarget]::Process) }")
		line("[System.Environment]::SetEnvironmentVariable('INFOPATH',(%s+${ENV:INFOPATH}),[System.EnvironmentVariableTarget]::Process)", q(p+"/share/info:"))
	default: // bash, zsh, sh, dash, ksh
		q := shDQ
		line(`export HOMEBREW_PREFIX="%s";`, q(p))
		line(`export HOMEBREW_CELLAR="%s";`, q(c))
		line(`export HOMEBREW_REPOSITORY="%s";`, q(r))
		if shell == "zsh" || shell == "-zsh" {
			line(`fpath[1,0]="%s/share/zsh/site-functions";`, q(p))
			line("export FPATH;")
		}
		line(`export PATH="%s/bin:%s/sbin${PATH+:$PATH}";`, q(p), q(p))
		line(`[ -z "${MANPATH-}" ] || { export MANPATH="${MANPATH%%"${MANPATH##*[!:]}"}"; export MANPATH=":${MANPATH#"${MANPATH%%%%[!:]*}"}"; };`)
		line(`export INFOPATH="%s/share/info:${INFOPATH:-}";`, q(p))
	}
	return b.String()
}

// shDQ escapes s for use inside a POSIX/zsh double-quoted string.
func shDQ(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\\"$`", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// fishQuote returns s as a fish double-quoted string.
func fishQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if strings.ContainsRune("\\\"$", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

// pwshQuote returns s as a PowerShell single-quoted (verbatim) string.
func pwshQuote(s string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, r := range s {
		b.WriteRune(r)
		if strings.ContainsRune("'‘’‚‛", r) {
			b.WriteRune(r) // PowerShell escapes (smart) single quotes by doubling
		}
	}
	b.WriteByte('\'')
	return b.String()
}

var cshPlain = regexp.MustCompile(`^[A-Za-z0-9/._+@%:,=-]*$`)

// cshQuote quotes the literal s, followed by the variable expansion v (may
// be empty), for csh/tcsh: in double quotes when nothing in s is special there
// (matching upstream's output), else s in single quotes, where only ', ! and
// newlines need escaping, followed by "v".
func cshQuote(s, v string) string {
	if cshPlain.MatchString(strings.ReplaceAll(s, " ", "")) {
		return `"` + s + v + `"`
	}
	r := strings.NewReplacer("'", `'\''`, "!", `\!`, "\n", "\\\n")
	out := "'" + r.Replace(s) + "'"
	if v != "" {
		out += `"` + v + `"`
	}
	return out
}

// cshWord is cshQuote, but leaves plain words unquoted as upstream does.
func cshWord(s string) string {
	if s != "" && cshPlain.MatchString(s) {
		return s
	}
	return cshQuote(s, "")
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
	miseVer := strings.TrimSpace(func() string { b, _ := mise.MiseOutput("--version"); return string(b) }())
	fmt.Printf("HOMEBREW_VERSION: %s (mise-brew %s)\n", homebrewVersion(), buildinfo.Version)
	fmt.Printf("ORIGIN: mise bootstrap packages (%s)\n", paths.MiseBin())
	fmt.Printf("HEAD: (none, packages are managed by mise)\n")
	if b, err := os.ReadFile(filepath.Join(paths.ApiDir(), "formula.last-modified")); err == nil {
		fmt.Printf("Core tap JSON: %s\n", strings.TrimSpace(string(b)))
	}
	if b, err := os.ReadFile(filepath.Join(paths.ApiDir(), "cask.last-modified")); err == nil {
		fmt.Printf("Core cask tap JSON: %s\n", strings.TrimSpace(string(b)))
	}
	fmt.Printf("HOMEBREW_PREFIX: %s\n", paths.BrewPrefix())
	fmt.Printf("HOMEBREW_CELLAR: %s\n", paths.Cellar())
	fmt.Printf("HOMEBREW_CASKROOM: %s\n", paths.Caskroom())
	fmt.Printf("HOMEBREW_CACHE: %s\n", paths.BrewCacheDir())
	fmt.Printf("HOMEBREW_REPOSITORY: %s\n", paths.BrewRepository())
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
			hb = append(hb, k+": "+configEnvValue(k, v))
		}
	}
	sort.Strings(hb)
	for _, l := range hb {
		fmt.Println(l)
	}
	fmt.Printf("mise config target: %s\n", strings.Join(append([]string{"bootstrap packages use"}, mise.ScopeArgs()...), " "))
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
		fmt.Printf("Rosetta 2: %v\n", paths.FileExists("/Library/Apple/usr/share/rosetta/rosetta"))
	} else {
		fmt.Printf("Kernel: %s\n", firstLine("uname", "-mors"))
		fmt.Printf("glibc: %s\n", firstLine("ldd", "--version"))
	}
}

var (
	secretEnvRe = regexp.MustCompile(`TOKEN|PASSWORD|SECRET|KEY|AUTH|CREDENTIAL`)
	urlUserRe   = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/?#\s]*@`)
)

// configEnvValue hides secrets from `brew config`: variables whose name looks
// like a credential print as "set", and user:pass@ is stripped from URLs.
func configEnvValue(k, v string) string {
	if secretEnvRe.MatchString(strings.ToUpper(k)) {
		return "set"
	}
	return urlUserRe.ReplaceAllString(v, "$1")
}

type doctorCheck struct {
	name string
	run  func() string // returns "" when fine
}

func doctorChecks() []doctorCheck {
	return []doctorCheck{
		{"check_mise", func() string {
			if _, err := mise.MiseOutput("--version"); err != nil {
				return fmt.Sprintf("mise is not working:\n  %v", err)
			}
			return ""
		}},
		{"check_prefix_writable", func() string {
			if !paths.IsDir(paths.BrewPrefix()) {
				return fmt.Sprintf("The Homebrew prefix %s does not exist.\nRun `brew install <formula>` (mise creates it) or set MISEBREW_PREFIX.", paths.BrewPrefix())
			}
			if syscall.Access(paths.BrewPrefix(), 2) != nil {
				return fmt.Sprintf("%s is not writable.\nYou should change the ownership of %s to your user:\n  sudo chown -R $(whoami) %s", paths.BrewPrefix(), paths.BrewPrefix(), paths.BrewPrefix())
			}
			return ""
		}},
		{"check_api_freshness", func() string {
			if !homebrew.FormulaAPI.Available() {
				return "mise has no copy of the Homebrew API yet.\nRun `brew update`."
			}
			if age := time.Since(homebrew.FormulaAPI.ModTime()); age > 7*24*time.Hour {
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
			bin := filepath.Join(paths.BrewPrefix(), "bin")
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
				return fmt.Sprintf("Homebrew's \"bin\" was not found in your PATH.\nConsider setting your PATH for example like so:\n  echo 'eval \"$(%s/bin/brew shellenv)\"' >> ~/.zprofile", paths.BrewPrefix())
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
			if broken := homebrew.BrokenSymlinks(); len(broken) > 0 {
				sort.Strings(broken)
				return fmt.Sprintf("Broken symlinks were found. Remove them with `brew cleanup`:\n  %s", strings.Join(limit(broken, 20), "\n  "))
			}
			return ""
		}},
		{"check_for_unlinked_but_not_keg_only", func() string {
			var unlinked []string
			for _, n := range homebrew.InstalledFormulae() {
				if !homebrew.IsKegLinked(n) {
					if f := homebrew.LookupFormula(n); f == nil || !f.KegOnly {
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
			for _, n := range homebrew.InstalledFormulae() {
				if !paths.IsDir(filepath.Join(paths.OptDir(), n)) {
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
			for _, n := range homebrew.InstalledFormulae() {
				if r := homebrew.ReadReceipt(homebrew.KegPath(n, homebrew.CurrentVersion(n))); r != nil {
					for _, d := range r.RuntimeDependencies {
						if b := homebrew.BaseName(d.FullName); !homebrew.IsFormulaInstalled(b) {
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
			for _, n := range homebrew.InstalledFormulae() {
				if f := homebrew.LookupFormula(n); f != nil && f.Name == n {
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
			for m, st := range mise.PackagesStatus() {
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
			if slices.Contains(o.Pos, c.name) {
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
		ui.Opoo("%s", w)
	}
	ui.Exit(1)
}

func limit(xs []string, n int) []string {
	if len(xs) > n {
		return append(xs[:n:n], fmt.Sprintf("... and %d more", len(xs)-n))
	}
	return xs
}

func cmdAnalytics(args []string) {
	if len(args) > 0 && args[0] == "on" {
		ui.Opoo("mise-brew never sends analytics; leaving them disabled.")
		return
	}
	if len(args) == 0 || args[0] == "state" {
		fmt.Println("InfluxDB analytics are disabled.")
		fmt.Println("Google Analytics were destroyed.")
	}
}

func cmdDeveloper(args []string) {
	if len(args) > 0 && args[0] == "on" {
		ui.Opoo("developer mode is not available in mise-brew.")
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
	ui.Opoo("mise-brew does not ship completions; `brew completions %s` did nothing.", args[0])
}
