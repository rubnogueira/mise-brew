package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fbExpectExit runs f and returns the exit code it raised via exit/odie
// (-1 when it returned normally).
func fbExpectExit(t *testing.T, f func()) (code int) {
	t.Helper()
	code = -1
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(exitError)
			if !ok {
				panic(r)
			}
			code = e.code
		}
	}()
	f()
	return code
}

// fbCapture returns what f writes to stdout.
func fbCapture(t *testing.T, f func()) string {
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
	f()
	w.Close()
	os.Stdout = old
	return <-done
}

// fbSandbox gives the test a fake prefix, HOME, mise cache and a PATH holding
// only stub binaries, so nothing can reach the real system.
func fbSandbox(t *testing.T) (prefix, home, stubs string) {
	t.Helper()
	if isRoot() {
		t.Skip("must not run as root")
	}
	prefix = fakePrefix(t)
	home = t.TempDir()
	stubs = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MISE_CACHE_DIR", filepath.Join(home, "cache"))
	t.Setenv("MISE_CONFIG_DIR", filepath.Join(home, ".config", "mise"))
	t.Setenv("PATH", stubs)
	t.Setenv("STUB_LOG", filepath.Join(stubs, "log"))
	t.Setenv("STUB_DIR", stubs)
	for _, n := range []string{"launchctl", "systemctl"} {
		fbStub(t, stubs, n, `echo "$*" >> "$STUB_LOG"
case "$*" in *print*|*show*) exit 3;; esac
exit 0`)
	}
	fbStub(t, stubs, "mise", "exit 1")
	oldMise := miseBinCache
	miseBinCache = filepath.Join(stubs, "mise")
	t.Cleanup(func() { miseBinCache = oldMise })
	return prefix, home, stubs
}

func fbStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func fbLog(stubs string) string {
	b, _ := os.ReadFile(filepath.Join(stubs, "log"))
	return string(b)
}

// fbService creates an installed formula shipping a service file.
func fbService(t *testing.T, name, content string) *service {
	t.Helper()
	makeKeg(t, name, "1.0", "bin/"+name)
	file := "homebrew.mxcl." + name + ".plist"
	if runtime.GOOS != "darwin" {
		file = "homebrew." + name + ".service"
	}
	write(t, filepath.Join(optDir(), name, file), content)
	s := findService(name)
	if s == nil {
		t.Fatal("service not found")
	}
	return s
}

// Finding 1 and 20.
func TestServiceStartFileParam(t *testing.T) {
	_, _, stubs := fbSandbox(t)
	s := fbService(t, "foo", "keg service file")
	serviceStart(s, "", "")
	if b, _ := os.ReadFile(s.installedPath()); string(b) != "keg service file" {
		t.Errorf("installed %q, want the keg's service file", b)
	}
	if !strings.Contains(fbLog(stubs), s.Label) {
		t.Errorf("service manager not invoked: %q", fbLog(stubs))
	}
	custom := filepath.Join(t.TempDir(), "custom")
	write(t, custom, "custom service file")
	serviceStart(s, custom, "")
	if b, _ := os.ReadFile(s.installedPath()); string(b) != "custom service file" {
		t.Errorf("installed %q, want --file", b)
	}

	// --sudo-service-user without root fails before anything is installed.
	_ = os.Remove(s.installedPath())
	if code := fbExpectExit(t, func() { serviceStart(s, "", "nobody") }); code != 1 {
		t.Errorf("exit = %d", code)
	}
	if fileExists(s.installedPath()) {
		t.Error("service file installed although --sudo-service-user failed")
	}
}

// Finding 3: only services this run started are stopped, without deleting
// registered files.
func TestRunWithServicesStopsOnlyItsOwn(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchctl stub")
	}
	_, home, stubs := fbSandbox(t)
	fbStub(t, stubs, "launchctl", `echo "$*" >> "$STUB_LOG"
case "$1" in
print) l="${2##*/}"; [ -e "$STUB_DIR/loaded.$l" ] && { echo "state = running"; echo "pid = 42"; exit 0; }; exit 113;;
bootstrap) l="${3##*/}"; l="${l%.plist}"; : > "$STUB_DIR/loaded.$l";;
bootout) l="${2##*/}"; /bin/rm -f "$STUB_DIR/loaded.$l";;
esac
exit 0`)
	old := stopWait
	stopWait = 2 * time.Second
	t.Cleanup(func() { stopWait = old })
	tmp := fbService(t, "tmp", "<plist/>")
	reg := fbService(t, "reg", "<plist/>")
	write(t, reg.installedPath(), "<plist/>") // registered by the user, not loaded
	code := fbExpectExit(t, func() {
		runWithServices([]string{"tmp", "reg"}, "/usr/bin/true", []string{"/usr/bin/true"}, os.Environ())
	})
	if code != 0 {
		t.Errorf("exit = %d", code)
	}
	log := fbLog(stubs)
	if !strings.Contains(log, "bootstrap") || !strings.Contains(log, "bootout gui/") || !strings.Contains(log, tmp.Label) {
		t.Errorf("tmp not started and stopped: %s", log)
	}
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, reg.Label) && !strings.HasPrefix(l, "print") {
			t.Errorf("registered service touched: %s", l)
		}
	}
	if !fileExists(reg.installedPath()) {
		t.Error("registered service file deleted")
	}
	_ = home
}

// Finding 4: an incomplete fallback parse refuses cleanup.
func TestFallbackIncompleteRefusesCleanup(t *testing.T) {
	fbSandbox(t)
	_, inc := parseBrewfileFallback(strings.NewReader("brew \"jq\"\ncask \"firefox\" if OS.mac?\n"), "Brewfile")
	if inc {
		t.Error("simple Brewfile reported incomplete")
	}
	for _, src := range []string{
		"brew \"jq\"\n%w[a b].each { |f| brew f }\n",
		"brew \"jq\" unless ENV[\"CI\"]\n",
		"if ENV[\"WORK\"]\n  brew \"x\"\nend\n",
		"[\"a\"].each do |f|\n  brew f\nend\n",
	} {
		if _, inc := parseBrewfileFallback(strings.NewReader(src), "Brewfile"); !inc {
			t.Errorf("not incomplete: %q", src)
		}
	}
	bf := filepath.Join(t.TempDir(), "Brewfile")
	write(t, bf, "brew \"jq\"\n%w[a b].each { |f| brew f }\n")
	t.Setenv("MISEBREW_BUNDLE_NO_RUBY", "1")
	makeKeg(t, "a", "1.0", "bin/a")
	o := parseArgs([]string{"--force", "--file=" + bf}, nil, nil)
	if code := fbExpectExit(t, func() { bundleCleanup(o) }); code != 1 {
		t.Errorf("cleanup --force exit = %d, want refusal", code)
	}
	o = parseArgs([]string{"--cleanup", "--file=" + bf}, nil, nil)
	if code := fbExpectExit(t, func() { bundleInstall(o) }); code != 1 {
		t.Errorf("install --cleanup exit = %d, want refusal", code)
	}
	if !isFormulaInstalled("a") {
		t.Error("formula removed")
	}
}

// Finding 4: the DSL runs on macOS's Ruby 2.6 and keeps stdout to itself.
func TestRubyDSLOnSystemRuby(t *testing.T) {
	ruby := "/usr/bin/ruby"
	if !fileExists(ruby) {
		t.Skip("no /usr/bin/ruby")
	}
	t.Setenv("MISEBREW_RUBY", "")
	t.Setenv("PATH", "/usr/bin")
	if got := findRuby(); got != ruby {
		t.Errorf("findRuby = %q, want %s accepted", got, ruby)
	}
	bf := filepath.Join(t.TempDir(), "Brewfile")
	write(t, bf, `cask_args appdir: "~/Applications"
puts "noise on stdout"
%w[jq wget].each { |f| brew f }
brew "pg", restart_service: :always, link: :overwrite, conflicts_with: ["pg@16"]
cask "firefox", args: { appdir: "/Applications/Web" }
mas "Xcode", id: 497799835
brew "linux-thing" if OS.linux?
`)
	var es []brewfileEntry
	var inc bool
	var err error
	fbCapture(t, func() { es, inc, err = rubyBrewfile(ruby, bf) })
	if err != nil {
		t.Fatal(err)
	}
	if inc {
		t.Error("complete Brewfile reported incomplete")
	}
	got := map[string]brewfileEntry{}
	for _, e := range es {
		got[e.Kind+":"+e.Name] = e
	}
	for _, k := range []string{"brew:jq", "brew:wget", "brew:pg", "cask:firefox", "mas:Xcode"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %s in %v", k, es)
		}
	}
	if l, ow, _ := got["brew:pg"].linkOption(); !l || !ow || got["brew:pg"].restartService() != "always" {
		t.Errorf("pg options = %v", got["brew:pg"].Options)
	}
	if got["mas:Xcode"].ID != "497799835" {
		t.Errorf("mas id = %q", got["mas:Xcode"].ID)
	}
	write(t, bf, "brew \"jq\"\nwhatever \"x\"\n")
	if _, inc, err := rubyBrewfile(ruby, bf); err != nil || !inc {
		t.Errorf("unknown entry: incomplete=%v err=%v", inc, err)
	}
}

// Finding 5.
func TestTapsToUntapKeepsQualifiedAndReceiptTaps(t *testing.T) {
	fbSandbox(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	write(t, filepath.Join(kegPath("foo", "1.0"), "INSTALL_RECEIPT.json"), `{"source":{"tap":"Kept/Rcpt"}}`)
	makeKeg(t, "gone", "1.0", "bin/gone")
	write(t, filepath.Join(kegPath("gone", "1.0"), "INSTALL_RECEIPT.json"), `{"source":{"tap":"gone/tap"}}`)
	entries := []brewfileEntry{
		{Kind: "tap", Name: "a/b"},
		{Kind: "brew", Name: "acme/tools/bar"},
		{Kind: "brew", Name: "foo"},
		{Kind: "cask", Name: "x", Options: map[string]any{"full_name": "caskt/ap/x"}},
		{Kind: "cask", Name: "other/casks/y"},
	}
	current := map[string]string{"a/b": "", "acme/tools": "", "kept/rcpt": "", "caskt/ap": "", "other/casks": "", "other/tap": "", "gone/tap": "", "homebrew/core": ""}
	got := tapsToUntap(entries, map[string]bool{"foo": true, "bar": true}, current)
	if strings.Join(got, ",") != "gone/tap,other/tap" {
		t.Errorf("untap = %v", got)
	}
}

// Findings 6 and 7.
func TestBrewfileOptionInterpretation(t *testing.T) {
	es := parseBrewfile(strings.NewReader(`brew "a", link: :overwrite
brew "b", link: false
brew "c", restart_service: true
brew "d", restart_service: :always
brew "e", restart_service: :changed, start_service: false
brew "f", start_service: true
brew "g"
`), "Brewfile")
	got := map[string]brewfileEntry{}
	for _, e := range es {
		got[e.Name] = e
	}
	cases := []struct {
		name                 string
		link, overwrite, set bool
		restart              string
		start                bool
	}{
		{"a", true, true, true, "", false},
		{"b", false, false, true, "", false},
		{"c", false, false, false, "changed", true},
		{"d", false, false, false, "always", true},
		{"e", false, false, false, "changed", false},
		{"f", false, false, false, "", true},
		{"g", false, false, false, "", false},
	}
	for _, c := range cases {
		e := got[c.name]
		l, ow, ok := e.linkOption()
		if l != c.link || ow != c.overwrite || ok != c.set {
			t.Errorf("%s: linkOption = %v %v %v", c.name, l, ow, ok)
		}
		if r := e.restartService(); r != c.restart {
			t.Errorf("%s: restartService = %q want %q", c.name, r, c.restart)
		}
		if s := e.startService(); s != c.start {
			t.Errorf("%s: startService = %v", c.name, s)
		}
	}
	bools := brewfileEntry{Options: map[string]any{"link": false, "restart_service": true}}
	if l, _, ok := bools.linkOption(); l || !ok || bools.restartService() != "changed" {
		t.Error("bool options from Ruby JSON")
	}
}

// Finding 6: conflicts are reported, :overwrite overwrites.
func TestBundleLinkOverwrite(t *testing.T) {
	p, _, _ := fbSandbox(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	write(t, filepath.Join(p, "bin", "foo"), "unmanaged")
	if bundleLink("foo", true, false) {
		t.Error("conflict not reported")
	}
	if isKegLinked("foo") {
		t.Error("linked despite conflict")
	}
	if !bundleLink("foo", true, true) || !resolvesInto(filepath.Join(p, "bin", "foo"), kegPath("foo", "1.0")) {
		t.Error("link: :overwrite did not link")
	}
}

// Finding 9.
func TestServiceFileFormula(t *testing.T) {
	cases := []struct {
		name, goos, unit, formula string
		ok                        bool
	}{
		{"homebrew.mxcl.foo.plist", "darwin", "homebrew.mxcl.foo", "foo", true},
		{"sh.brew.foo.plist", "darwin", "sh.brew.foo", "foo", true},
		{"homebrew.mxcl.foo.plist.bak", "darwin", "", "", false},
		{"homebrew.foo.service", "darwin", "", "", false},
		{"other.plist", "darwin", "", "", false},
		{"homebrew.foo.service", "linux", "homebrew.foo.service", "foo", true},
		{"homebrew.foo.timer", "linux", "homebrew.foo.timer", "foo", true},
		{"sh.brew.foo@2.timer", "linux", "sh.brew.foo@2.timer", "foo@2", true},
		{"homebrew.foo.service.bak", "linux", "", "", false},
		{"homebrew.foo.plist", "linux", "", "", false},
	}
	for _, c := range cases {
		u, f, ok := serviceFileFormula(c.name, c.goos)
		if u != c.unit || f != c.formula || ok != c.ok {
			t.Errorf("%s/%s = %q %q %v", c.name, c.goos, u, f, ok)
		}
	}
}

func TestServicesCleanupKeepsInstalledAndForeignFiles(t *testing.T) {
	fbSandbox(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	dir := installDir()
	var keep, gone []string
	if runtime.GOOS == "darwin" {
		keep = []string{"homebrew.mxcl.foo.plist", "homebrew.mxcl.gone.plist.bak", "com.other.plist"}
		gone = []string{"homebrew.mxcl.gone.plist"}
	} else {
		keep = []string{"homebrew.foo.service", "homebrew.foo.timer", "homebrew.gone.service.bak"}
		gone = []string{"homebrew.gone.service", "homebrew.gone.timer"}
	}
	for _, n := range append(append([]string{}, keep...), gone...) {
		write(t, filepath.Join(dir, n), "x")
	}
	out := fbCapture(t, servicesCleanup)
	for _, n := range keep {
		if !fileExists(filepath.Join(dir, n)) {
			t.Errorf("%s removed", n)
		}
	}
	for _, n := range gone {
		if fileExists(filepath.Join(dir, n)) {
			t.Errorf("%s kept", n)
		}
	}
	if strings.Count(out, "Removing unused service file: gone") != 1 {
		t.Errorf("output %q", out)
	}
}

// Finding 14.
func TestServiceStatusSemantics(t *testing.T) {
	zero, one := 0, 1
	cases := []struct {
		st    serviceState
		timed bool
		want  string
	}{
		{serviceState{Loaded: true, Running: true, PID: 3}, false, "started"},
		{serviceState{}, false, "none"},
		{serviceState{ExitCode: &one}, false, "none"},
		{serviceState{Loaded: true, ExitCode: &zero}, false, "stopped"},
		{serviceState{Loaded: true, ExitCode: &zero}, true, "scheduled"},
		{serviceState{Loaded: true, ExitCode: &one}, false, "error"},
		{serviceState{Loaded: true}, false, "other"},
	}
	for _, c := range cases {
		if got := serviceStatus(c.st, c.timed); got != c.want {
			t.Errorf("%+v timed=%v: %s want %s", c.st, c.timed, got, c.want)
		}
	}
}

// Finding 11.
func TestFallbackParserBlocks(t *testing.T) {
	src := `if OS.linux?
  brew "linux-a"
elsif OS.mac?
  brew "mac-a"
else
  brew "other-a"
end
if false
  brew "never"
elsif true
  brew "elsif-taken"
elsif true
  brew "second-elsif"
else
  brew "else-after-taken"
end
if OS.mac?
  [1].each do |x|
    puts x
  end
  brew "mac-after-block"
end
brew "multi",
  link: false,
  restart_service: :always
brew "args", args: [
  "with-foo",
]
brew "tail" # comment, with comma,
`
	es, inc := parseBrewfileFallback(strings.NewReader(src), "Brewfile")
	if !inc {
		t.Error("do block should mark the parse incomplete")
	}
	got := map[string]brewfileEntry{}
	for _, e := range es {
		got[e.Name] = e
	}
	mac := runtime.GOOS == "darwin"
	want := map[string]bool{
		"linux-a": runtime.GOOS == "linux", "mac-a": mac, "other-a": !mac && runtime.GOOS != "linux",
		"never": false, "elsif-taken": true, "second-elsif": false, "else-after-taken": false,
		"mac-after-block": mac, "multi": true, "args": true, "tail": true,
	}
	for n, w := range want {
		if _, ok := got[n]; ok != w {
			t.Errorf("%s present=%v want %v (got %v)", n, ok, w, es)
		}
	}
	m := got["multi"]
	if l, _, ok := m.linkOption(); !ok || l || m.restartService() != "always" {
		t.Errorf("multi options = %v", m.Options)
	}
	if a := got["args"].optStrings("args"); len(a) != 1 || a[0] != "with-foo" {
		t.Errorf("args = %v", got["args"].Options)
	}
	if m.Line != 23 {
		t.Errorf("multi line = %d", m.Line)
	}
}

// Finding 12.
func TestDumpCasksKeepsTapNames(t *testing.T) {
	fbSandbox(t)
	old := statusCache
	statusCache = map[string]managerStatus{"brew-cask": {Available: true, Packages: []pkgStatus{
		{Package: "owner/tap/foo", DesiredState: "present"},
		{Package: "bar", DesiredState: "present"},
		{Package: "old/tap/gone", DesiredState: "absent"},
	}}}
	t.Cleanup(func() { statusCache = old })
	out := fbCapture(t, func() { bundleDump(parseArgs([]string{"--cask", "--file=-"}, nil, nil)) })
	if out != "cask \"bar\"\ncask \"owner/tap/foo\"\n" {
		t.Errorf("dump = %q", out)
	}
}

// Finding 13.
func TestBrewfileRemove(t *testing.T) {
	lines := strings.Split(`tap "acme/tools"
brew "jq"
  brew("jq", link: false)
brew "jq" if OS.mac?
brew 'jq',
  restart_service: :always,
  link: false
brew "jqx"
cask "jq"
# brew "jq"
brew "wget"`, "\n")
	all := []string{"tap", "brew", "cask", "mas", "vscode", "whalebrew", "go", "cargo", "npm", "uv", "flatpak"}
	got := strings.Join(editBrewfileLines(lines, all, []string{"jq"}, false), "\n")
	if got != "tap \"acme/tools\"\nbrew \"jqx\"\n# brew \"jq\"\nbrew \"wget\"" {
		t.Errorf("remove all kinds:\n%s", got)
	}
	got = strings.Join(editBrewfileLines(lines, []string{"cask"}, []string{"jq"}, false), "\n")
	if strings.Contains(got, `cask "jq"`) || !strings.Contains(got, `brew "jq" if`) {
		t.Errorf("remove --cask:\n%s", got)
	}
	added := editBrewfileLines([]string{`brew "jq" if OS.mac?`}, []string{"brew"}, []string{"jq", "wget"}, true)
	if strings.Join(added, "\n") != "brew \"jq\" if OS.mac?\nbrew \"wget\"" {
		t.Errorf("add = %v", added)
	}

	bf := filepath.Join(t.TempDir(), "Brewfile")
	o := parseArgs([]string{"--file=" + bf}, nil, nil)
	if code := fbExpectExit(t, func() { bundleEdit(o, []string{"jq"}, false) }); code != 1 {
		t.Errorf("remove without Brewfile exit = %d", code)
	}
	if fileExists(bf) {
		t.Error("remove created a Brewfile")
	}
}

// Findings 2 and 15.
func TestSuperenvCompilers(t *testing.T) {
	for cc, want := range map[string]string{"gcc": "g++", "gcc-13": "g++-13", "clang": "clang++", "clang-17": "clang++-17", "cc": "c++", "/opt/x/bin/gcc-14": "/opt/x/bin/g++-14", "x86_64-linux-gnu-gcc-12": "x86_64-linux-gnu-g++-12"} {
		if got := cxxFor(cc); got != want {
			t.Errorf("cxxFor(%s) = %s want %s", cc, got, want)
		}
	}
	if shimFor("gcc-13", false) != "cc" || shimFor("g++-13", true) != "c++" || shimFor("clang", false) != "clang" {
		t.Error("shimFor")
	}

	_, home, _ := fbSandbox(t)
	bin := filepath.Join(home, "fakebin")
	for _, n := range []string{"gcc-13", "g++-13", "gcc", "cpp", "ld"} {
		fbStub(t, bin, n, `echo "real $0 PATH=$PATH"`)
	}
	shims := ensureShims()
	t.Setenv("PATH", shims+":"+bin)
	if got := realTool("gcc", os.Getenv("PATH"), shims); got != filepath.Join(bin, "gcc") {
		t.Errorf("realTool(gcc) = %q, must skip the shims", got)
	}
	t.Setenv("HOMEBREW_CC", "gcc-13")
	env := map[string]string{}
	for _, kv := range buildEnv(nil, false) {
		env[kv[0]] = kv[1]
	}
	if env["CC"] != filepath.Join(shims, "cc") || env["CXX"] != filepath.Join(shims, "c++") {
		t.Errorf("CC=%s CXX=%s", env["CC"], env["CXX"])
	}
	if env["HOMEBREW_CC_REAL"] != filepath.Join(bin, "gcc-13") || env["HOMEBREW_CXX_REAL"] != filepath.Join(bin, "g++-13") || env["HOMEBREW_CXX"] != "g++-13" {
		t.Errorf("real compilers: %v", env)
	}
	for _, k := range []string{"HOMEBREW_CC_REAL", "HOMEBREW_CXX_REAL", "HOMEBREW_CPP_REAL", "HOMEBREW_LD_REAL"} {
		if strings.HasPrefix(env[k], shims) {
			t.Errorf("%s points into the shims: %s", k, env[k])
		}
	}
}

// Finding 2: the shim strips itself from PATH and never execs itself.
func TestShimDoesNotLoop(t *testing.T) {
	if !fileExists("/bin/bash") {
		t.Skip("no /bin/bash")
	}
	_, home, _ := fbSandbox(t)
	bin := filepath.Join(home, "fakebin")
	fbStub(t, bin, "gcc", `echo "real PATH=$PATH"`)
	shims := ensureShims()
	run := func(env ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, filepath.Join(shims, "cc"), "-c", "x.c")
		cmd.Env = append([]string{"PATH=" + shims + ":" + bin, "HOME=" + home}, env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatal("shim looped")
		}
		return out.String(), err
	}
	out, err := run()
	if err != nil || !strings.HasPrefix(out, "real PATH=") || strings.Contains(out, shims) {
		t.Errorf("fallback lookup: %q %v", out, err)
	}
	out, err = run("HOMEBREW_CC_REAL=" + filepath.Join(shims, "gcc"))
	if err == nil || !strings.Contains(out, "cannot find the real compiler") {
		t.Errorf("self-reference not refused: %q %v", out, err)
	}
}

// Finding 19.
func TestServicesJSONEmpty(t *testing.T) {
	fbSandbox(t)
	if out := fbCapture(t, func() { servicesInfo(allServices(), true) }); strings.TrimSpace(out) != "[]" {
		t.Errorf("info --json = %q", out)
	}
	if out := fbCapture(t, func() { servicesList(true) }); strings.TrimSpace(out) != "[]" {
		t.Errorf("list --json = %q", out)
	}
}

// Finding 21: cleanup leaves packages declared in a project's config alone.
func TestCleanupSkipsProjectDeclared(t *testing.T) {
	_, home, _ := fbSandbox(t)
	global := filepath.Join(home, ".config", "mise", "config.toml")
	write(t, global, "[bootstrap.packages]\n\"brew:foo\" = \"latest\"\n")
	project := filepath.Join(t.TempDir(), "mise.toml")
	write(t, project, "[bootstrap.packages]\n\"brew:bar\" = \"latest\"\n")
	files := []string{global, project}
	if !isGlobalMiseConfig(global) || isGlobalMiseConfig(project) {
		t.Error("isGlobalMiseConfig")
	}
	keys := func(n string) []string { return []string{"brew:" + n} }
	got := skipProjectDeclared([]string{"foo", "bar", "baz"}, keys, files)
	if strings.Join(got, ",") != "foo,baz" {
		t.Errorf("kept for uninstall = %v", got)
	}
}
