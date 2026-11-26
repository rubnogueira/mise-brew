package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// Homebrew builds with "superenv": compiler shims on PATH that inject the
// include/library paths of a formula's dependencies. mise-brew generates
// equivalent shims under <mise cache>/mise-brew/shims/super.

const shimScript = `#!/bin/bash
# mise-brew superenv shim (see brew --env)
name="${0##*/}"
dir="${0%/*}"
# Drop this directory from PATH so neither the lookups below nor the real
# compiler's own sub-invocations can find the shims again.
IFS=: read -ra parts <<< "${PATH:-}"
PATH=""
for p in "${parts[@]}"; do
  [ -n "$p" ] && [ "$p" -ef "$dir" ] && continue
  PATH="${PATH:+$PATH:}$p"
done
export PATH
case "$name" in
  c++|g++|clang++) var=CXX ; real="${HOMEBREW_CXX_REAL:-}" ; [ -n "$real" ] || real="$(xcrun -f clang++ 2>/dev/null || command -v g++ || command -v c++)" ;;
  cpp) var=CPP ; real="${HOMEBREW_CPP_REAL:-}" ; [ -n "$real" ] || real="$(command -v cpp)" ;;
  ld) var=LD ; real="${HOMEBREW_LD_REAL:-}" ; [ -n "$real" ] || real="$(command -v ld)" ;;
  *) var=CC ; real="${HOMEBREW_CC_REAL:-}" ; [ -n "$real" ] || real="$(xcrun -f clang 2>/dev/null || command -v gcc || command -v cc)" ;;
esac
case "$real" in */*) ;; ?*) real="$(command -v "$real")" ;; esac
if [ -z "$real" ] || [ "${real%/*}" -ef "$dir" ]; then
  echo "mise-brew superenv: cannot find the real compiler for '$name' (set HOMEBREW_${var}_REAL)" >&2
  exit 1
fi
extra=()
if [ "$name" != "ld" ]; then
  IFS=: read -ra inc <<< "${HOMEBREW_ISYSTEM_PATHS:-}"
  for p in "${inc[@]}"; do [ -n "$p" ] && extra+=(-isystem "$p"); done
  [ -n "${HOMEBREW_OPTIMIZATION_LEVEL:-}" ] && extra+=("-${HOMEBREW_OPTIMIZATION_LEVEL}")
  [ -n "${HOMEBREW_SDKROOT:-}" ] && extra+=(-isysroot "$HOMEBREW_SDKROOT")
  extra+=(-Wno-unused-command-line-argument)
fi
IFS=: read -ra lib <<< "${HOMEBREW_LIBRARY_PATHS:-}"
for p in "${lib[@]}"; do [ -n "$p" ] && extra+=("-L$p"); done
exec "$real" "$@" "${extra[@]}"
`

func shimsDir() string { return filepath.Join(paths.MiseBrewCacheDir(), "shims", "super") }

// shimNames are the only shims that exist; any other requested compiler is
// reached through cc/c++ with HOMEBREW_CC_REAL/HOMEBREW_CXX_REAL.
var shimNames = []string{"cc", "c++", "clang", "clang++", "gcc", "g++", "cpp", "ld"}

func ensureShims() string {
	dir := shimsDir()
	if paths.FileExists(filepath.Join(dir, "cc")) {
		if b, err := os.ReadFile(filepath.Join(dir, "cc")); err == nil && string(b) == shimScript {
			return dir
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	for _, n := range shimNames {
		_ = os.WriteFile(filepath.Join(dir, n), []byte(shimScript), 0o755)
	}
	return dir
}

// shimFor returns the shim that stands in for compiler: its own shim when one
// exists, else the generic cc (or c++).
func shimFor(compiler string, cxx bool) string {
	if slices.Contains(shimNames, compiler) {
		return compiler
	}
	if cxx {
		return "c++"
	}
	return "cc"
}

// cxxFor maps a C compiler to its C++ counterpart: gcc-14 → g++-14,
// clang → clang++, clang-17 → clang++-17, cc → c++.
func cxxFor(cc string) string {
	dir, base := filepath.Split(cc)
	switch {
	case base == "llvm_clang":
		return base
	case strings.Contains(base, "clang"):
		i := strings.LastIndex(base, "clang") + len("clang")
		return dir + base[:i] + "++" + base[i:]
	case strings.Contains(base, "gcc"):
		i := strings.LastIndex(base, "gcc")
		return dir + base[:i] + "g++" + base[i+len("gcc"):]
	case base == "cc":
		return dir + "c++"
	}
	return "c++"
}

// lookPathExcluding finds name on path, skipping the shims directory.
func lookPathExcluding(name, path, exclude string) string {
	ex, _ := filepath.EvalSymlinks(exclude)
	for _, d := range filepath.SplitList(path) {
		if d == "" || filepath.Clean(d) == filepath.Clean(exclude) {
			continue
		}
		if r, err := filepath.EvalSymlinks(d); err == nil && ex != "" && r == ex {
			continue
		}
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// realTool resolves the compiler a shim should exec, never the shim itself.
// HOMEBREW_CC=llvm_clang means Homebrew's llvm, as in Homebrew.
func realTool(name, path, shims string) string {
	if name == "llvm_clang" {
		name = filepath.Join(paths.OptDir(), "llvm", "bin", "clang")
	}
	if filepath.IsAbs(name) {
		if fi, err := os.Stat(name); err == nil && !fi.IsDir() && filepath.Dir(name) != filepath.Clean(shims) {
			return name
		}
		return ""
	}
	if p := lookPathExcluding(name, path, shims); p != "" {
		return p
	}
	if runtime.GOOS == "darwin" {
		if out, err := exec.Command("xcrun", "--find", name).Output(); err == nil {
			if p := strings.TrimSpace(string(out)); p != "" && filepath.Dir(p) != filepath.Clean(shims) {
				return p
			}
		}
	}
	return ""
}

func sdkRoot() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("xcrun", "--show-sdk-path").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// depRoots returns opt prefixes of the formulae's dependencies (all of them, so
// keg-only ones are found too), followed by the Homebrew prefix.
func depRoots(formulae []string) []string {
	var roots []string
	seen := map[string]bool{}
	for _, f := range formulae {
		ff := homebrew.LookupFormula(f)
		if ff == nil {
			continue
		}
		for _, d := range recursiveDeps(ff, depFilter{build: true}) {
			d = homebrew.BaseName(d) // tap dependencies are keyed by their rack name
			if !seen[d] && homebrew.IsFormulaInstalled(d) {
				seen[d] = true
				roots = append(roots, filepath.Join(paths.OptDir(), d))
			}
		}
	}
	return append(roots, paths.BrewPrefix())
}

func buildEnv(formulae []string, std bool) [][2]string {
	p := paths.BrewPrefix()
	var pkg, inc, lib, cmake, aclocal []string
	for _, root := range depRoots(formulae) {
		for _, sub := range []string{"lib/pkgconfig", "share/pkgconfig"} {
			if paths.IsDir(filepath.Join(root, sub)) {
				pkg = append(pkg, filepath.Join(root, sub))
			}
		}
		if paths.IsDir(filepath.Join(root, "include")) {
			inc = append(inc, filepath.Join(root, "include"))
		}
		if paths.IsDir(filepath.Join(root, "lib")) {
			lib = append(lib, filepath.Join(root, "lib"))
		}
		if paths.IsDir(filepath.Join(root, "share", "aclocal")) {
			aclocal = append(aclocal, filepath.Join(root, "share", "aclocal"))
		}
		cmake = append(cmake, root)
	}
	cc := "clang"
	if runtime.GOOS == "linux" {
		cc = "gcc"
	}
	if v := os.Getenv("HOMEBREW_CC"); v != "" {
		cc = v
	}
	cxx := cxxFor(cc)
	if cc == "llvm_clang" && std {
		cc, cxx = filepath.Join(paths.OptDir(), "llvm", "bin", "clang"), filepath.Join(paths.OptDir(), "llvm", "bin", "clang++")
	}
	jobs := strconv.Itoa(runtime.NumCPU())
	if v := os.Getenv("HOMEBREW_MAKE_JOBS"); v != "" {
		jobs = v
	}
	sysPath := "/usr/bin:/bin:/usr/sbin:/sbin"
	env := [][2]string{
		{"HOMEBREW_CC", cc},
		{"HOMEBREW_CXX", cxx},
		{"MAKEFLAGS", "-j" + jobs},
		{"CMAKE_PREFIX_PATH", strings.Join(cmake, ":")},
		{"CMAKE_INCLUDE_PATH", strings.Join(inc, ":")},
		{"CMAKE_LIBRARY_PATH", strings.Join(lib, ":")},
		{"PKG_CONFIG_PATH", strings.Join(pkg, ":")},
		{"PKG_CONFIG_LIBDIR", "/usr/lib/pkgconfig"},
		{"ACLOCAL_PATH", strings.Join(aclocal, ":")},
		{"HOMEBREW_PREFIX", p},
		{"HOMEBREW_CELLAR", paths.Cellar()},
	}
	if git, err := exec.LookPath("git"); err == nil {
		env = append(env, [2]string{"HOMEBREW_GIT", git})
	}
	if sdk := sdkRoot(); sdk != "" {
		env = append(env, [2]string{"HOMEBREW_SDKROOT", sdk})
	}
	if std {
		var cpp, ld []string
		for _, i := range inc {
			cpp = append(cpp, "-I"+i)
		}
		for _, l := range lib {
			ld = append(ld, "-L"+l)
		}
		env = append(env,
			[2]string{"CC", cc}, [2]string{"CXX", cxx}, [2]string{"OBJC", cc}, [2]string{"OBJCXX", cxx},
			[2]string{"CFLAGS", "-Os -w -pipe"}, [2]string{"CXXFLAGS", "-Os -w -pipe"},
			[2]string{"CPPFLAGS", strings.Join(cpp, " ")}, [2]string{"LDFLAGS", strings.Join(ld, " ")},
			[2]string{"PATH", filepath.Join(p, "bin") + ":" + filepath.Join(p, "sbin") + ":" + sysPath})
		return env
	}
	shims := ensureShims()
	// Resolve the real tools here, with the shims off PATH, so a shim never
	// finds itself (or another shim) as its compiler.
	searchPath := filepath.Join(p, "bin") + ":" + filepath.Join(p, "sbin") + ":" + sysPath + ":" + os.Getenv("PATH")
	realCXX := cxx
	if cc == "llvm_clang" {
		realCXX = filepath.Join(paths.OptDir(), "llvm", "bin", "clang++")
	}
	for _, t := range [][2]string{{"HOMEBREW_CC_REAL", cc}, {"HOMEBREW_CXX_REAL", realCXX}, {"HOMEBREW_CPP_REAL", "cpp"}, {"HOMEBREW_LD_REAL", "ld"}} {
		if r := realTool(t[1], searchPath, shims); r != "" {
			env = append(env, [2]string{t[0], r})
		}
	}
	ccShim, cxxShim := shimFor(cc, false), shimFor(cxx, true)
	return append(env,
		[2]string{"CC", filepath.Join(shims, ccShim)}, [2]string{"CXX", filepath.Join(shims, cxxShim)},
		[2]string{"OBJC", filepath.Join(shims, ccShim)}, [2]string{"OBJCXX", filepath.Join(shims, cxxShim)},
		[2]string{"HOMEBREW_ISYSTEM_PATHS", strings.Join(inc, ":")},
		[2]string{"HOMEBREW_LIBRARY_PATHS", strings.Join(lib, ":")},
		[2]string{"HOMEBREW_OPTIMIZATION_LEVEL", "Os"},
		[2]string{"PATH", shims + ":" + filepath.Join(p, "bin") + ":" + filepath.Join(p, "sbin") + ":" + sysPath})
}

func cmdEnv(args []string) {
	o := parseArgs(args, nil, nil)
	shell, _ := o.Val("shell")
	if o.Has("plain") {
		shell = "plain"
	}
	if shell == "auto" || (shell == "" && !ui.StdoutTTY) {
		shell = strings.TrimPrefix(filepath.Base(os.Getenv("SHELL")), "-")
	}
	std := os.Getenv("HOMEBREW_ENV") == "std"
	for _, kv := range buildEnv(o.Pos, std) {
		k, v := kv[0], kv[1]
		switch shell {
		case "fish":
			fmt.Printf("set -gx %s %s\n", k, fishQuote(v))
		case "csh", "tcsh":
			fmt.Printf("setenv %s %s;\n", k, cshQuote(v, ""))
		case "bash", "zsh", "sh", "ksh", "dash":
			fmt.Printf("export %s=\"%s\"\n", k, shDQ(v))
		default:
			fmt.Printf("%s: %s\n", k, v)
		}
	}
}

func cmdSh(args []string) {
	o := parseArgs(args, nil, nil)
	envMode, _ := o.Val("env")
	pairs := buildEnv(nil, envMode == "std")
	for i, kv := range pairs {
		if kv[0] == "PATH" {
			pairs[i][1] = kv[1] + ":" + os.Getenv("PATH")
		}
	}
	env := mergeEnv(os.Environ(), pairs)
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	argv := []string{sh}
	// Keep the user's shell startup files from resetting PATH (and so the
	// shims), as Homebrew does for `brew sh`.
	switch filepath.Base(sh) {
	case "zsh":
		zdot := filepath.Join(paths.MiseBrewCacheDir(), "zdotdir")
		_ = os.MkdirAll(zdot, 0o755)
		_ = os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte("PROMPT='brew %~%# '\n"), 0o644)
		env = mergeEnv(env, [][2]string{{"ZDOTDIR", zdot}})
	case "bash":
		argv = append(argv, "--norc", "--noprofile")
	}
	if c, ok := o.Val("cmd"); ok {
		argv = append(argv, "-c", c)
	} else {
		fmt.Println("Your shell has been configured to use Homebrew's build environment;\nthis should help you build stuff. Notably though, the system versions of\ngem and pip will ignore our configuration and insist on using the\nenvironment they were built under (mostly). Sadly, scons will also\nignore our configuration.\nWhen done, type `exit`.")
	}
	if err := syscall.Exec(sh, argv, env); err != nil {
		ui.Odie("%v", err)
	}
}

func cmdCommand(args []string) {
	if len(args) == 0 {
		ui.Odie("Invalid usage: This command requires at least 1 command argument.")
	}
	self, _ := os.Executable()
	for _, a := range args {
		c := canonical(a)
		if _, ok := commands[c]; ok {
			fmt.Println(self)
			continue
		}
		if !homebrew.ValidName(a) {
			ui.Odie("Unknown command: brew %s", a)
		}
		if p, err := exec.LookPath("brew-" + a); err == nil {
			fmt.Println(p)
			continue
		}
		if p, ok := aliasPath(a); ok && paths.FileExists(p) {
			fmt.Println(p)
			continue
		}
		ui.Odie("Unknown command: brew %s", a)
	}
}

// mergeEnv overrides (rather than duplicates) variables in base.
func mergeEnv(base []string, pairs [][2]string) []string {
	set := map[string]bool{}
	for _, kv := range pairs {
		set[kv[0]] = true
	}
	var out []string
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if !set[k] {
			out = append(out, e)
		}
	}
	for _, kv := range pairs {
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}
