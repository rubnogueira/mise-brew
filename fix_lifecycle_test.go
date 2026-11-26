package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lcCatchExit runs fn and returns the code of an exit()/odie() inside it (0 if none).
func lcCatchExit(fn func()) (code int) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(exitError)
			if !ok {
				panic(r)
			}
			code = e.code
		}
	}()
	fn()
	return 0
}

// lcCapture returns what fn writes to os.Stdout.
func lcCapture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout = old
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// lcSandbox isolates a test from the real system: fake prefix, HOME, mise
// cache (with an empty API so ensureAPI passes) and a stub mise that fails.
func lcSandbox(t *testing.T) string {
	t.Helper()
	p := fakePrefix(t)
	t.Setenv("HOME", t.TempDir())
	cache := t.TempDir()
	t.Setenv("MISE_CACHE_DIR", cache)
	write(t, filepath.Join(cache, "system-brew", "api", "formula.json"), "[]")
	lcStubMise(t, "exit 97")
	oldStatus, oldErr := statusCache, statusErr
	statusCache, statusErr = map[string]managerStatus{}, nil
	oldV, oldQ, oldD, oldCmd := verbose, quiet, debug, currentCmd
	t.Cleanup(func() {
		statusCache, statusErr = oldStatus, oldErr
		verbose, quiet, debug, currentCmd = oldV, oldQ, oldD, oldCmd
	})
	return p
}

// lcStubMise points mise-brew at a shell script; its arguments are appended
// to the file returned.
func lcStubMise(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "mise")
	write(t, script, "#!/bin/sh\necho \"$@\" >> '"+log+"'\n"+body+"\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	old := miseBinCache
	miseBinCache = script
	t.Setenv("MISEBREW_MISE", script)
	t.Cleanup(func() { miseBinCache = old })
	return log
}

func lcSeedFormula(t *testing.T, name, version string) {
	t.Helper()
	f := &Formula{Name: name, FullName: name}
	f.Versions.Stable = version
	old, had := formulaCache[name]
	formulaCache[name] = f
	t.Cleanup(func() {
		if had {
			formulaCache[name] = old
		} else {
			delete(formulaCache, name)
		}
	})
}

// Finding 1: path-like names never reach filesystem operations.
func TestValidPackageArg(t *testing.T) {
	for name, want := range map[string]bool{
		"jq": true, "openssl@3": true, "homebrew/core/jq": true, "owner/tap/foo": true,
		".": false, "..": false, "../var": false, "homebrew/core/..": false,
		"owner/tap/..": false, "owner/../foo": false, "": false, "a/b": false,
	} {
		if got := validPackageArg(name); got != want {
			t.Errorf("validPackageArg(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRequireInstalledRejectsTraversal(t *testing.T) {
	p := lcSandbox(t)
	makeKeg(t, "foo", "1.0")
	write(t, filepath.Join(p, "var", "homebrew", "keep"), "x")
	for _, n := range []string{"..", ".", "../var"} {
		if code := lcCatchExit(func() { requireInstalled([]string{n}) }); code != 1 {
			t.Errorf("requireInstalled(%q) exit=%d, want 1", n, code)
		}
	}
	if got := requireInstalled([]string{"foo"}); len(got) != 1 || got[0] != "foo" {
		t.Errorf("requireInstalled(foo) = %v", got)
	}
}

func TestCleanupAllowed(t *testing.T) {
	p := lcSandbox(t)
	for path, want := range map[string]bool{
		filepath.Join(p, "Cellar", "foo", "1.0"):                    true,
		filepath.Join(p, "Cellar", "foo", ".mise-extract-abc"):      true,
		filepath.Join(p, "Caskroom", "firefox", "120.0"):            true,
		filepath.Join(bottlesDir(), "foo--1.0.tar.gz"):              true,
		filepath.Join(p, "Cellar", "foo"):                           false,
		filepath.Join(p, "Cellar"):                                  false,
		filepath.Join(p, "var"):                                     false,
		p + "/Cellar/../var/homebrew":                               false,
		filepath.Join(p, "Cellar", "foo", "1.0", "bin"):             false,
		filepath.Join(p, "Cellar", ".foo.mise-brew-reinstall", "1"): false,
		filepath.Join(p, "Cellar", "foo", ".."):                     false,
	} {
		if got := cleanupAllowed(path); got != want {
			t.Errorf("cleanupAllowed(%s) = %v, want %v", path, got, want)
		}
	}
}

func TestCleanupTraversalAndTypo(t *testing.T) {
	p := lcSandbox(t)
	makeKeg(t, "foo", "1.0")
	makeKeg(t, "foo", "2.0")
	makeKeg(t, "bar", "1.0")
	makeKeg(t, "bar", "2.0")
	write(t, filepath.Join(p, "var", "homebrew", "keep", "x"), "x")
	for _, args := range [][]string{{"../var"}, {".."}, {"typo-mbt-nothing"}} {
		out := lcCapture(t, func() {
			if code := lcCatchExit(func() { cmdCleanup(args) }); code != 0 {
				t.Errorf("cleanup %v exit=%d", args, code)
			}
		})
		if strings.Contains(out, "Removing") {
			t.Errorf("cleanup %v removed something:\n%s", args, out)
		}
	}
	for _, k := range []string{kegPath("foo", "1.0"), kegPath("bar", "1.0"), filepath.Join(p, "var", "homebrew", "keep", "x")} {
		if !fileExists(k) {
			t.Errorf("%s was removed", k)
		}
	}
	// A named cleanup only touches that formula.
	lcCapture(t, func() { cmdCleanup([]string{"foo"}) })
	if fileExists(kegPath("foo", "1.0")) || !fileExists(kegPath("foo", "2.0")) {
		t.Error("cleanup foo did not remove only the old foo keg")
	}
	if !fileExists(kegPath("bar", "1.0")) {
		t.Error("cleanup foo removed bar's old keg")
	}
}

// Finding 2: global flags before the command do not bypass the guard.
func TestRecursionGuardWithGlobalFlags(t *testing.T) {
	lcSandbox(t)
	t.Setenv(recursionEnv, "1")
	for _, argv := range [][]string{
		{"-v", "install", "x"}, {"--verbose", "install", "x"}, {"-d", "-q", "uninstall", "x"},
		{"--debug", "upgrade"}, {"update-if-needed"}, {"bundle"}, {"bundle", "install"},
		{"services", "start", "x"}, {"tap", "a/b"}, {"some-unknown-command"},
	} {
		if code := run(argv); code != 1 {
			t.Errorf("nested %v exit=%d, want 1", argv, code)
		}
	}
	verbose, debug, quiet = false, false, false
	if code := run([]string{"-q", "--prefix"}); code != 0 {
		t.Errorf("nested read-only --prefix exit=%d, want 0", code)
	}
	for _, c := range []struct {
		cmd  string
		args []string
		want bool
	}{
		{"list", nil, true}, {"info", []string{"--json=v2"}, true},
		{"bundle", []string{"check"}, true}, {"bundle", []string{"--file=x", "list"}, true},
		{"bundle", []string{"check", "--install"}, false}, {"bundle", nil, false},
		{"services", nil, true}, {"services", []string{"info", "x"}, true},
		{"services", []string{"stop", "x"}, false}, {"tap", nil, true},
		{"update-if-needed", nil, false}, {"update", nil, false}, {"cleanup", nil, false},
		{"alias", nil, true}, {"alias", []string{"x=list"}, false}, {"fetch", []string{"x"}, false},
		{"install", []string{"--help"}, true}, {"install", []string{"--", "--help"}, false},
		{"edit", []string{"--print-path", "jq"}, true}, {"edit", []string{"jq"}, false},
		{"home", []string{"jq"}, true},
	} {
		if got := isReadOnly(c.cmd, c.args); got != c.want {
			t.Errorf("isReadOnly(%s %v) = %v, want %v", c.cmd, c.args, got, c.want)
		}
	}
}

// Finding 3: --json takes a value only when attached.
func TestOptionalJSONFlag(t *testing.T) {
	old := currentCmd
	defer func() { currentCmd = old }()
	for _, cmd := range []string{"info", "outdated", "tap-info"} {
		currentCmd = cmd
		o := parseArgs([]string{"--json", "jq"}, nil, nil)
		if v, ok := o.Val("json"); !ok || v != "" {
			t.Errorf("%s: bare --json = %q, %v", cmd, v, ok)
		}
		if strings.Join(o.Pos, " ") != "jq" {
			t.Errorf("%s: --json consumed the next argument: pos=%v", cmd, o.Pos)
		}
		// Even when a caller lists json as a value flag explicitly.
		if o := parseArgs([]string{"--json", "jq"}, []string{"json"}, nil); len(o.Pos) != 1 {
			t.Errorf("%s: explicit value list made --json consume: %v", cmd, o.Pos)
		}
		if v, _ := parseArgs([]string{"--json=v2", "jq"}, nil, nil).Val("json"); v != "v2" {
			t.Errorf("%s: --json=v2 = %q", cmd, v)
		}
		for _, args := range [][]string{{"--json"}, {"--json", "jq"}, {"--json=v1"}} {
			if code := lcCatchExit(func() { validateFlags(cmd, args) }); code != 0 {
				t.Errorf("validateFlags(%s, %v) exit=%d", cmd, args, code)
			}
		}
	}
	var spec flagSpec
	for _, s := range commandSpecs["outdated"] {
		if s.long == "json" {
			spec = s
		}
	}
	if !spec.optional || spec.value {
		t.Errorf("outdated --json spec = %+v, want optional", spec)
	}
}

// Finding 4: outdated --json defaults to v2, takes positionals, and fails
// when a named package is outdated.
func TestOutdatedJSONAndExitCode(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "mbtjq", "1.7")
	makeKeg(t, "mbtok", "2.0")
	lcSeedFormula(t, "mbtjq", "1.8")
	lcSeedFormula(t, "mbtok", "2.0")

	var code int
	out := lcCapture(t, func() { code = run([]string{"outdated", "--json", "mbtjq"}) })
	if code != 1 {
		t.Errorf("outdated --json mbtjq exit=%d, want 1", code)
	}
	var v2 struct {
		Formulae []outdatedItem `json:"formulae"`
		Casks    []any          `json:"casks"`
	}
	if err := json.Unmarshal([]byte(out), &v2); err != nil || len(v2.Formulae) != 1 || v2.Formulae[0].Name != "mbtjq" || v2.Casks == nil {
		t.Errorf("bare --json is not v2 (%v):\n%s", err, out)
	}
	out = lcCapture(t, func() { code = run([]string{"outdated", "--json=v1", "mbtjq"}) })
	if code != 1 || !strings.HasPrefix(strings.TrimSpace(out), "[") {
		t.Errorf("--json=v1 exit=%d out=%s", code, out)
	}
	out = lcCapture(t, func() { code = run([]string{"outdated", "mbtjq"}) })
	if code != 1 || strings.TrimSpace(out) != "mbtjq" {
		t.Errorf("outdated mbtjq exit=%d out=%q, want 1 and the name", code, out)
	}
	lcCapture(t, func() { code = run([]string{"outdated", "mbtok"}) })
	if code != 0 {
		t.Errorf("outdated up-to-date package exit=%d, want 0", code)
	}
	if code = run([]string{"outdated", "--json=v9"}); code != 1 {
		t.Errorf("--json=v9 exit=%d, want 1", code)
	}
}

// Findings 5/6: reinstall keeps other versions and restores the keg on failure.
func TestReinstallFailureRestoresOnlyCurrentKeg(t *testing.T) {
	p := lcSandbox(t)
	makeKeg(t, "foo", "1.0", "bin/foo-old")
	makeKeg(t, "foo", "2.0", "bin/foo")
	if _, _, err := linkKeg("foo", "2.0", false, false); err != nil {
		t.Fatal(err)
	}
	calls := lcStubMise(t, "exit 3")
	var code int
	lcCapture(t, func() { code = lcCatchExit(func() { reinstallFormula("foo", false, nil) }) })
	if code != 3 {
		t.Errorf("exit=%d, want mise's 3", code)
	}
	if b, _ := os.ReadFile(calls); strings.TrimSpace(string(b)) != "bootstrap packages apply -y brew:foo" {
		t.Errorf("mise called with %q", b)
	}
	for _, v := range []string{"1.0", "2.0"} {
		if !isDir(kegPath("foo", v)) {
			t.Errorf("keg %s missing after failed reinstall", v)
		}
	}
	if fileExists(reinstallBackup("foo")) {
		t.Error("backup left behind")
	}
	if readLinkVersion(filepath.Join(optDir(), "foo")) != "2.0" || !resolvesInto(filepath.Join(p, "bin", "foo"), kegPath("foo", "2.0")) {
		t.Error("opt link or bin/foo not restored")
	}
}

func TestReinstallSuccessKeepsOtherVersions(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "foo", "1.0")
	makeKeg(t, "foo", "2.0")
	_ = setOptLink("foo", "2.0")
	keg := kegPath("foo", "2.0")
	lcStubMise(t, "test -d '"+keg+"' && exit 9\nmkdir -p '"+keg+"' && echo '{\"installed_on_request\":true}' > '"+keg+"/INSTALL_RECEIPT.json'")
	var code int
	lcCapture(t, func() { code = lcCatchExit(func() { reinstallFormula("foo", false, nil) }) })
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !isDir(kegPath("foo", "1.0")) {
		t.Error("reinstall deleted another installed version")
	}
	if fileExists(reinstallBackup("foo")) {
		t.Error("backup left behind")
	}
	if r := readReceipt(keg); r == nil || r.InstalledOnRequest {
		t.Errorf("receipt not reset to the old on-request state: %+v", r)
	}
}

func TestRestoreStaleReinstallBackup(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "foo", "1.0")
	write(t, filepath.Join(reinstallBackup("foo"), "2.0", "INSTALL_RECEIPT.json"), "{}")
	lcCapture(t, func() { restoreReinstallBackup("foo") })
	if !fileExists(filepath.Join(kegPath("foo", "2.0"), "INSTALL_RECEIPT.json")) || fileExists(reinstallBackup("foo")) {
		t.Error("stale backup not restored")
	}
	// Whole-rack backups from older mise-brew releases restore the same way.
	write(t, filepath.Join(reinstallBackup("bar"), "3.0", "INSTALL_RECEIPT.json"), "{}")
	lcCapture(t, func() { restoreReinstallBackup("bar") })
	if !isFormulaInstalled("bar") {
		t.Error("rack backup not restored")
	}
}

// Finding 7: autoremove refuses to act without mise's status.
func TestAutoremoveNeedsStatus(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "mbt-orphan", "1")
	write(t, filepath.Join(kegPath("mbt-orphan", "1"), "INSTALL_RECEIPT.json"), `{"installed_on_request":false,"installed_as_dependency":true}`)
	if got := autoremoveCandidates(); len(got) != 1 {
		t.Fatalf("candidates with status = %v", got)
	}
	statusErr = os.ErrNotExist
	if got := autoremoveCandidates(); len(got) != 0 {
		t.Errorf("candidates without status = %v, want none", got)
	}
}

func TestMarkOnRequest(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "dep", "1")
	write(t, filepath.Join(kegPath("dep", "1"), "INSTALL_RECEIPT.json"), `{"installed_on_request":false,"installed_as_dependency":true,"time":1}`)
	markOnRequest("dep")
	if r := readReceipt(kegPath("dep", "1")); r == nil || !r.InstalledOnRequest || r.InstalledAsDependency || r.Time != 1 {
		t.Errorf("receipt = %+v", r)
	}
}

// Finding 10: a homebrew/cask/ name does not make later names casks.
func TestResolvePackageDoesNotLeakCaskFlag(t *testing.T) {
	lcSandbox(t)
	lcSeedFormula(t, "mbtjq", "1.8")
	old, had := caskCache["mbtfox"]
	caskCache["mbtfox"] = &Cask{Token: "mbtfox", FullToken: "mbtfox", Version: "1"}
	t.Cleanup(func() {
		if had {
			caskCache["mbtfox"] = old
		} else {
			delete(caskCache, "mbtfox")
		}
	})
	o := Opts{flags: map[string]string{}}
	if r := resolvePackage("homebrew/cask/mbtfox", o); r.kind != "cask" {
		t.Fatalf("homebrew/cask/mbtfox resolved to %+v", r)
	}
	if o.Has("cask") {
		t.Fatal("resolvePackage mutated the shared options")
	}
	var r pkgRef
	if code := lcCatchExit(func() { r = resolvePackage("mbtjq", o) }); code != 0 || r.kind != "formula" {
		t.Errorf("mbtjq after a cask name: exit=%d ref=%+v", code, r)
	}
	if code := lcCatchExit(func() { resolvePackage("../var", o) }); code != 1 {
		t.Errorf("resolvePackage(../var) exit=%d, want 1", code)
	}
}

// Finding 15: link --dry-run lists every file that would be linked.
func TestLinkDryRunListsTargets(t *testing.T) {
	p := lcSandbox(t)
	makeKeg(t, "foo", "1.0", "bin/foo", "share/foo/a")
	write(t, filepath.Join(p, "share", "foo", "a"), "unmanaged")
	got := linkDryRun("foo", "1.0", false)
	want := filepath.Join(p, "bin", "foo") + "," + filepath.Join(p, "share", "foo", "a")
	if strings.Join(got, ",") != want {
		t.Errorf("dry run = %v", got)
	}
	if got := linkDryRun("foo", "1.0", true); strings.Join(got, ",") != filepath.Join(p, "share", "foo", "a") {
		t.Errorf("dry run --overwrite = %v", got)
	}
	if fileExists(filepath.Join(p, "bin", "foo")) {
		t.Error("dry run linked something")
	}
}
