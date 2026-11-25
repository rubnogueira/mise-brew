package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// Homebrew builds with "superenv": compiler shims on PATH that inject the
// include/library paths of a formula's dependencies. mise-brew generates
// equivalent shims under <mise cache>/mise-brew/shims/super.

const shimScript = `#!/bin/bash
# mise-brew superenv shim (see brew --env)
name="${0##*/}"
case "$name" in
  c++|g++|clang++|*++*) real="${HOMEBREW_CXX_REAL:-}" ; [ -n "$real" ] || real="$(xcrun -f clang++ 2>/dev/null || command -v g++ || echo /usr/bin/c++)" ;;
  cpp) real="${HOMEBREW_CPP_REAL:-/usr/bin/cpp}" ;;
  ld) real="${HOMEBREW_LD_REAL:-/usr/bin/ld}" ;;
  *) real="${HOMEBREW_CC_REAL:-}" ; [ -n "$real" ] || real="$(xcrun -f clang 2>/dev/null || command -v gcc || echo /usr/bin/cc)" ;;
esac
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

func shimsDir() string { return filepath.Join(miseBrewCacheDir(), "shims", "super") }

func ensureShims() string {
	dir := shimsDir()
	names := []string{"cc", "c++", "clang", "clang++", "gcc", "g++", "cpp", "ld"}
	if fileExists(filepath.Join(dir, "cc")) {
		if b, err := os.ReadFile(filepath.Join(dir, "cc")); err == nil && string(b) == shimScript {
			return dir
		}
	}
	_ = os.MkdirAll(dir, 0o755)
	for _, n := range names {
		_ = os.WriteFile(filepath.Join(dir, n), []byte(shimScript), 0o755)
	}
	return dir
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
		ff := lookupFormula(f)
		if ff == nil {
			continue
		}
		for _, d := range recursiveDeps(ff, depFilter{build: true}) {
			if !seen[d] && isFormulaInstalled(d) {
				seen[d] = true
				roots = append(roots, filepath.Join(optDir(), d))
			}
		}
	}
	return append(roots, brewPrefix())
}

func buildEnv(formulae []string, std bool) [][2]string {
	p := brewPrefix()
	var pkg, inc, lib, cmake, aclocal []string
	for _, root := range depRoots(formulae) {
		for _, sub := range []string{"lib/pkgconfig", "share/pkgconfig"} {
			if isDir(filepath.Join(root, sub)) {
				pkg = append(pkg, filepath.Join(root, sub))
			}
		}
		if isDir(filepath.Join(root, "include")) {
			inc = append(inc, filepath.Join(root, "include"))
		}
		if isDir(filepath.Join(root, "lib")) {
			lib = append(lib, filepath.Join(root, "lib"))
		}
		if isDir(filepath.Join(root, "share", "aclocal")) {
			aclocal = append(aclocal, filepath.Join(root, "share", "aclocal"))
		}
		cmake = append(cmake, root)
	}
	cc, cxx := "clang", "clang++"
	if runtime.GOOS == "linux" {
		cc, cxx = "gcc", "g++"
	}
	if v := os.Getenv("HOMEBREW_CC"); v != "" {
		cc = v
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
		{"HOMEBREW_CELLAR", cellar()},
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
	return append(env,
		[2]string{"CC", filepath.Join(shims, cc)}, [2]string{"CXX", filepath.Join(shims, cxx)},
		[2]string{"OBJC", filepath.Join(shims, cc)}, [2]string{"OBJCXX", filepath.Join(shims, cxx)},
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
	if shell == "auto" || (shell == "" && !stdoutTTY) {
		shell = strings.TrimPrefix(filepath.Base(os.Getenv("SHELL")), "-")
	}
	std := os.Getenv("HOMEBREW_ENV") == "std"
	for _, kv := range buildEnv(o.Pos, std) {
		k, v := kv[0], kv[1]
		switch shell {
		case "fish":
			fmt.Printf("set -gx %s %q\n", k, v)
		case "csh", "tcsh":
			fmt.Printf("setenv %s %s;\n", k, v)
		case "bash", "zsh", "sh", "ksh", "dash":
			fmt.Printf("export %s=%q\n", k, v)
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
		zdot := filepath.Join(miseBrewCacheDir(), "zdotdir")
		_ = os.MkdirAll(zdot, 0o755)
		_ = os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte("PROMPT='brew %~%# '\n"), 0o644)
		env = append(env, "ZDOTDIR="+zdot)
	case "bash":
		argv = append(argv, "--norc", "--noprofile")
	}
	if c, ok := o.Val("cmd"); ok {
		argv = append(argv, "-c", c)
	} else {
		fmt.Println("Your shell has been configured to use Homebrew's build environment;\nthis should help you build stuff. Notably though, the system versions of\ngem and pip will ignore our configuration and insist on using the\nenvironment they were built under (mostly). Sadly, scons will also\nignore our configuration.\nWhen done, type `exit`.")
	}
	if err := syscall.Exec(sh, argv, env); err != nil {
		odie("%v", err)
	}
}

func cmdCommand(args []string) {
	if len(args) == 0 {
		odie("Invalid usage: This command requires at least 1 command argument.")
	}
	self, _ := os.Executable()
	for _, a := range args {
		c := canonical(a)
		if _, ok := commands[c]; ok {
			fmt.Println(self)
			continue
		}
		if p, err := exec.LookPath("brew-" + a); err == nil {
			fmt.Println(p)
			continue
		}
		if p := filepath.Join(aliasDir(), a); fileExists(p) {
			fmt.Println(p)
			continue
		}
		odie("Unknown command: brew %s", a)
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
