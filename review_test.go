package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A Brewfile on stdin is read once and reused: install --cleanup parses it
// twice, and an empty second read made cleanup remove every package.
func TestStdinBrewfileReadOnce(t *testing.T) {
	fbSandbox(t)
	t.Setenv("MISEBREW_BUNDLE_NO_RUBY", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("brew \"jq\"\nbrew \"wget\"\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	stdinBrewfileOnce, stdinBrewfileData = sync.Once{}, nil
	t.Cleanup(func() {
		os.Stdin = oldStdin
		stdinBrewfileOnce, stdinBrewfileData = sync.Once{}, nil
	})
	o := parseArgs([]string{"--file=-"}, nil, nil)
	for i := 0; i < 2; i++ {
		entries, incomplete := readBrewfile(o)
		if len(entries) != 2 || incomplete {
			t.Fatalf("read %d: %d entries, incomplete=%v", i+1, len(entries), incomplete)
		}
	}
}

// When Ruby cannot evaluate a Brewfile, the fallback parse must not be
// trusted by destructive cleanup.
func TestRubyFailureMarksIncomplete(t *testing.T) {
	fbSandbox(t)
	for _, r := range []string{"/usr/bin/ruby", "/opt/homebrew/bin/ruby", "/usr/local/bin/ruby"} {
		if fileExists(r) {
			t.Setenv("MISEBREW_RUBY", r)
			break
		}
	}
	if findRuby() == "" {
		t.Skip("no usable ruby")
	}
	bf := filepath.Join(t.TempDir(), "Brewfile")
	write(t, bf, "brew \"jq\"\nraise \"boom\"\n")
	o := parseArgs([]string{"--file=" + bf}, nil, nil)
	var incomplete bool
	lcCapture(t, func() { _, incomplete = readBrewfile(o) })
	if !incomplete {
		t.Error("Brewfile that Ruby failed on was not marked incomplete")
	}
}

// With several versions installed, mise must still see the formula as
// missing and pour it; the other versions come back afterwards.
func TestReinstallWithOtherVersionsPours(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "foo", "1.0")
	makeKeg(t, "foo", "2.0")
	_ = setOptLink("foo", "2.0")
	rack := filepath.Join(cellar(), "foo")
	keg := kegPath("foo", "2.0")
	// Like mise: nothing to do while any version of the rack is installed.
	lcStubMise(t, "ls '"+rack+"' 2>/dev/null | grep -q . && exit 0\nmkdir -p '"+keg+"' && echo '{}' > '"+keg+"/INSTALL_RECEIPT.json'")
	var code int
	lcCapture(t, func() { code = lcCatchExit(func() { reinstallFormula("foo", false, nil) }) })
	if code != 0 {
		t.Fatalf("exit=%d, want success", code)
	}
	for _, v := range []string{"1.0", "2.0"} {
		if !isDir(kegPath("foo", v)) {
			t.Errorf("keg %s missing after reinstall", v)
		}
	}
	if fileExists(reinstallBackup("foo")) {
		t.Error("backup left behind")
	}
}

// A signal received while held is acted on once the critical section ends.
func TestHoldSignalsExitsAfterRelease(t *testing.T) {
	release := holdSignals()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if code := lcCatchExit(release); code != 128+int(syscall.SIGHUP) {
		t.Errorf("exit=%d, want %d", code, 128+int(syscall.SIGHUP))
	}
	// Without a signal, release just returns.
	if code := lcCatchExit(holdSignals()); code != 0 {
		t.Errorf("exit=%d without a signal", code)
	}
}

// Keg versions with commas are valid for reinstall, as for link/cleanup.
func TestReinstallAcceptsCommaVersion(t *testing.T) {
	lcSandbox(t)
	makeKeg(t, "weird", "2024.1,abc")
	_ = setOptLink("weird", "2024.1,abc")
	lcStubMise(t, "exit 4")
	var code int
	out := lcCapture(t, func() { code = lcCatchExit(func() { reinstallFormula("weird", false, nil) }) })
	if code != 4 || strings.Contains(out, "No such keg") {
		t.Errorf("exit=%d out=%q; want mise to run and fail with 4", code, out)
	}
	if !isDir(kegPath("weird", "2024.1,abc")) {
		t.Error("keg not restored")
	}
}

// info --json=v2 reports a tap cask from its tap, not a core cask with the
// same token (or a stub).
func TestInfoJSONTapCask(t *testing.T) {
	depsFixture(t)
	fakeTap(t, "acme/tools", map[string]string{
		"Casks/app.rb": "cask \"app\" do\n  version \"4.5.6\"\n  url \"https://x/app-#{version}.dmg\"\n  name \"Acme App\"\n  app \"App.app\"\nend\n",
	})
	out, code := captureRun(t, "info", "--json=v2", "--cask", "acme/tools/app")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	var doc struct {
		Casks []map[string]any `json:"casks"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Casks) != 1 {
		t.Fatalf("bad JSON %v: %s", err, out)
	}
	c := doc.Casks[0]
	if c["tap"] != "acme/tools" || c["version"] != "4.5.6" || c["full_token"] != "acme/tools/app" {
		t.Errorf("tap cask JSON = %v", c)
	}
}

// GoReleaser writes `Hardware::CPU.intel? and Hardware::CPU.is_64_bit?`.
func TestRbCondRubyAnd(t *testing.T) {
	t.Setenv("MISEBREW_BOTTLE_TAG", "arm64_linux")
	if v, ok := rbCond("Hardware::CPU.intel? and Hardware::CPU.is_64_bit?"); !ok || v {
		t.Errorf("intel and 64-bit on arm64 = %v, %v; want false, true", v, ok)
	}
	if v, ok := rbCond("Hardware::CPU.arm? and Hardware::CPU.is_64_bit?"); !ok || !v {
		t.Errorf("arm and 64-bit on arm64 = %v, %v; want true, true", v, ok)
	}
	// `and` inside a word is not the keyword.
	if _, ok := rbCond("OS.android?"); ok {
		t.Error("unknown condition evaluated")
	}
}

// `binary "#{appdir}/X.app/..."` expands instead of producing an empty artifact.
func TestTapCaskAppdirBinary(t *testing.T) {
	depsFixture(t)
	fakeTap(t, "acme/tools", map[string]string{
		"Casks/app.rb": "cask \"app\" do\n  version \"1\"\n  url \"https://x/a.dmg\"\n  app \"App.app\"\n  binary \"#{appdir}/App.app/Contents/bin/app\"\nend\n",
	})
	c := lookupCask("acme/tools/app")
	if c == nil {
		t.Fatal("cask not found")
	}
	for _, raw := range c.Artifacts {
		var m map[string][]string
		if json.Unmarshal(raw, &m) == nil {
			if b, ok := m["binary"]; ok && (len(b) == 0 || b[0] != caskAppdir()+"/App.app/Contents/bin/app") {
				t.Errorf("binary artifact = %v", b)
			}
		}
	}
}

// brew --env output is quoted for the shell, not with Go's %q: values with
// $, quotes, backquotes and non-ASCII survive a round trip through bash.
func TestEnvQuoting(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	v := "/opt/a $HOME \"q\" `id` é\\x"
	out, err := exec.Command(bash, "-c", `export K="`+shDQ(v)+`"; printf %s "$K"`).Output()
	if err != nil || string(out) != v {
		t.Errorf("round trip = %q (%v), want %q", out, err, v)
	}
}
