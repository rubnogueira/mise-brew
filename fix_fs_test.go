package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeHome points HOME at a fresh temp dir for the test.
func fakeHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("HOME", h)
	return h
}

var badNames = []string{"", ".", "..", "../x", "x/..", "/", "../Cellar", "foo/../..", ".hidden", "-rf"}

func TestInvalidNamesNeverResolve(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	os.MkdirAll(filepath.Join(p, "Caskroom", "bar", "1.0"), 0o755)
	// Cellar/. and Caskroom/.. exist as directories; they must not count.
	for _, n := range badNames {
		if vs := kegVersions(n); len(vs) > 0 {
			t.Errorf("kegVersions(%q) = %v", n, vs)
		}
		if isFormulaInstalled(n) || isCaskInstalled(n) {
			t.Errorf("%q reported installed", n)
		}
		if v := currentVersion(n); v != "" {
			t.Errorf("currentVersion(%q) = %q", n, v)
		}
		if vs := caskVersions(n); len(vs) > 0 {
			t.Errorf("caskVersions(%q) = %v", n, vs)
		}
		if r := resolveInstalledFormula(n); r != "" {
			t.Errorf("resolveInstalledFormula(%q) = %q", n, r)
		}
		if r := resolveInstalledCask(n); r != "" {
			t.Errorf("resolveInstalledCask(%q) = %q", n, r)
		}
		if isPinned(n) {
			t.Errorf("isPinned(%q)", n)
		}
	}
	for _, n := range []string{"acme/../foo", "../tap/foo", "acme/tap/..", "homebrew/core/..", "homebrew/cask/.."} {
		if r := resolveInstalledFormula(n); r != "" {
			t.Errorf("resolveInstalledFormula(%q) = %q", n, r)
		}
		if r := resolveInstalledCask(n); r != "" {
			t.Errorf("resolveInstalledCask(%q) = %q", n, r)
		}
	}
	if r := resolveInstalledFormula("homebrew/core/foo"); r != "foo" {
		t.Errorf("resolveInstalledFormula(homebrew/core/foo) = %q", r)
	}
	if r := resolveInstalledCask("bar"); r != "bar" {
		t.Errorf("resolveInstalledCask(bar) = %q", r)
	}
	// A record link pointing at ".." must not yield a version.
	os.MkdirAll(linkedDir(), 0o755)
	os.Symlink("..", filepath.Join(linkedDir(), "foo"))
	if v := readLinkVersion(filepath.Join(linkedDir(), "foo")); v != "" {
		t.Errorf("readLinkVersion -> %q", v)
	}
}

func TestRemovalRefusesUnsafePaths(t *testing.T) {
	p := fakePrefix(t)
	fakeHome(t)
	makeKeg(t, "foo", "1.0", "bin/foo")
	os.MkdirAll(filepath.Join(p, "Caskroom", "bar", "1.0"), 0o755)
	for _, n := range badNames {
		if err := removeKegs(n, nil); err == nil {
			t.Errorf("removeKegs(%q) accepted", n)
		}
		if err := nativeUninstallCask(n, false, false); err == nil {
			t.Errorf("nativeUninstallCask(%q) accepted", n)
		}
		if _, _, err := linkKeg(n, "1.0", true, false); err == nil {
			t.Errorf("linkKeg(%q) accepted", n)
		}
		if n, _ := unlinkKeg(n, "1.0", false); n != 0 {
			t.Errorf("unlinkKeg removed %d", n)
		}
	}
	for _, v := range []string{"", ".", "..", "../..", "1.0/../.."} {
		if err := removeKegs("foo", []string{v}); err == nil {
			t.Errorf("removeKegs(foo, %q) accepted", v)
		}
	}
	for _, x := range []string{"Cellar/foo/1.0/bin/foo", "Caskroom/bar/1.0"} {
		if !fileExists(filepath.Join(p, x)) {
			t.Fatalf("%s was removed", x)
		}
	}
}

func TestCaskProtectedPaths(t *testing.T) {
	p := fakePrefix(t)
	h := fakeHome(t)
	for _, d := range []string{"Library/Caches", "Library/Preferences", "Library/Application Support/MyApp", "Library/MyAppStuff", "Documents"} {
		os.MkdirAll(filepath.Join(h, d), 0o755)
	}
	os.MkdirAll(filepath.Join(p, "Cellar"), 0o755)
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	os.Symlink("/usr", filepath.Join(h, "usrlink"))
	o := &caskOps{dry: true}
	bad := []string{
		"/", "/usr/lib", "/usr/share", "/usr/local/bin", "/Library/Application Support",
		"/Library/Caches", "/Library/LaunchDaemons", "/System", "/private/var", "/opt",
		"~", "~/", "$HOME", "~/Library", "~/Library/Caches", "~/Library/Application Support",
		"~/Documents", "~/Library/Containers", h, filepath.Dir(h),
		p, filepath.Join(p, "Cellar"), filepath.Join(p, "bin"), filepath.Dir(p),
		"/tmp/../usr", "~/Library/../..", "~/Library/Caches/../../Documents",
		"~/usrlink/lib", "relative/path",
	}
	for _, pat := range bad {
		if got := o.safePaths([]string{pat}); len(got) != 0 {
			t.Errorf("safePaths(%q) = %v", pat, got)
		}
	}
	// Globs: protected matches are dropped, others kept.
	got := o.safePaths([]string{"~/Library/*", "~/Library/Application Support/*"})
	want := []string{filepath.Join(h, "Library/MyAppStuff"), filepath.Join(h, "Library/Application Support/MyApp")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("glob safePaths = %v, want %v", got, want)
	}
	if runtime.GOOS == "darwin" && !protectedPath("/usr/LIB") {
		t.Error("case-folded system path not protected on darwin")
	}
	if mayEscalate(filepath.Join(h, "Library", "MyAppStuff")) {
		t.Error("sudo allowed below HOME")
	}
	if !mayEscalate("/Library/Application Support/MyApp") {
		t.Error("sudo refused outside HOME")
	}
}

func TestHomeDirFallback(t *testing.T) {
	fakePrefix(t)
	orig := userHomeLookup
	t.Cleanup(func() { userHomeLookup = orig })
	userHomeLookup = func() string { return "/home/fallback" }
	t.Setenv("HOME", "")
	if h := homeDir(); h != "/home/fallback" {
		t.Errorf("homeDir() = %q, want the user database entry", h)
	}
	t.Setenv("HOME", "relative/home")
	if h := homeDir(); h != "/home/fallback" {
		t.Errorf("homeDir() with relative HOME = %q", h)
	}

	// No usable home at all: ~ and $HOME paths must fail, never become /...
	t.Setenv("HOME", "")
	userHomeLookup = func() string { return "" }
	if h := homeDir(); h != "" {
		t.Fatalf("homeDir() = %q", h)
	}
	if s := expandUserPath("~/Library/X"); filepath.IsAbs(s) {
		t.Errorf("expandUserPath = %q", s)
	}
	for _, in := range []string{"~/Library/X", "~", "$HOME/Library/X"} {
		if s, ok := expandCaskPathOK(in); ok || filepath.IsAbs(s) {
			t.Errorf("expandCaskPathOK(%q) = %q, %v", in, s, ok)
		}
	}
	if s, ok := expandCaskPathOK("$HOMEBREW_PREFIX/bin/x"); !ok || s != filepath.Join(brewPrefix(), "bin/x") {
		t.Errorf("expandCaskPathOK($HOMEBREW_PREFIX) = %q, %v", s, ok)
	}
	o := &caskOps{dry: true}
	if got := o.safePaths([]string{"~/Library/Caches/foo", "$HOME/Library/Foo"}); len(got) != 0 {
		t.Errorf("safePaths without HOME = %v", got)
	}
	if mayEscalate("/Library/Foo") {
		t.Error("sudo allowed with unknown HOME")
	}
	c := &Cask{Artifacts: []json.RawMessage{json.RawMessage(`{"font":["Foo.ttf"]}`)}}
	if ts := caskFallbackTargets(c); len(ts) != 0 {
		t.Errorf("font target without HOME = %v", ts)
	}
}

func TestMiseCacheDirHonoursXDG(t *testing.T) {
	t.Setenv("MISE_CACHE_DIR", "")
	t.Setenv("XDG_CACHE_HOME", "/xdg/cache")
	if d := miseCacheDir(); d != "/xdg/cache/mise" {
		t.Errorf("miseCacheDir() = %q", d)
	}
	t.Setenv("MISE_CACHE_DIR", "/m/cache")
	if d := miseCacheDir(); d != "/m/cache" {
		t.Errorf("miseCacheDir() = %q", d)
	}
}

func TestLinkOverwriteKeepsRealDirectories(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "py", "1.0", "lib/python3.12/sp-real/mod.py", "bin/py")
	keg := kegPath("py", "1.0")
	os.Symlink("sp-real", filepath.Join(keg, "lib/python3.12/site-packages"))
	// The prefix has a real site-packages full of other kegs' files.
	other := filepath.Join(p, "lib/python3.12/site-packages/other.pth")
	write(t, other, "x")
	for _, overwrite := range []bool{false, true} {
		_, conflicts, err := linkKeg("py", "1.0", overwrite, false)
		if err != nil || len(conflicts) != 1 || conflicts[0].rel != "lib/python3.12/site-packages" {
			t.Fatalf("overwrite=%v: conflicts=%v err=%v", overwrite, conflicts, err)
		}
		if !fileExists(other) {
			t.Fatalf("overwrite=%v deleted a real directory", overwrite)
		}
	}
	// An empty real directory is replaced with --overwrite.
	os.Remove(other)
	if _, conflicts, err := linkKeg("py", "1.0", true, false); err != nil || len(conflicts) > 0 {
		t.Fatalf("empty dir: conflicts=%v err=%v", conflicts, err)
	}
	if !resolvesInto(filepath.Join(p, "lib/python3.12/site-packages"), keg) {
		t.Error("site-packages not linked")
	}
}

func TestLinkReplacesStaleLinks(t *testing.T) {
	p := fakePrefix(t)
	makeKeg(t, "foo", "0.9", "bin/foo-old")
	makeKeg(t, "foo", "1.0", "bin/foo", "bin/foo-old", "bin/foo-other")
	makeKeg(t, "bar", "1", "bin/foo-other")
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	// Dangling link left by a removed keg.
	os.Symlink("../Cellar/gone/1/bin/foo", filepath.Join(p, "bin", "foo"))
	// Link into another version of the same rack.
	os.Symlink("../Cellar/foo/0.9/bin/foo-old", filepath.Join(p, "bin", "foo-old"))
	// Link into another formula: still a conflict.
	os.Symlink("../Cellar/bar/1/bin/foo-other", filepath.Join(p, "bin", "foo-other"))
	_, conflicts, err := linkKeg("foo", "1.0", false, false)
	if err != nil || len(conflicts) != 1 || conflicts[0].rel != "bin/foo-other" {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}
	os.Remove(filepath.Join(p, "bin", "foo-other"))
	if _, conflicts, err := linkKeg("foo", "1.0", false, false); err != nil || len(conflicts) > 0 {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}
	for _, f := range []string{"foo", "foo-old", "foo-other"} {
		if !resolvesInto(filepath.Join(p, "bin", f), kegPath("foo", "1.0")) {
			t.Errorf("bin/%s not relinked", f)
		}
	}
}

func TestLinkLeavesForeignDirSymlinks(t *testing.T) {
	p := fakePrefix(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles", "foo")
	write(t, filepath.Join(dotfiles, "user.conf"), "mine")
	makeKeg(t, "foo", "1.0", "etc/foo/foo.conf", "bin/foo")
	os.MkdirAll(filepath.Join(p, "etc"), 0o755)
	link := filepath.Join(p, "etc", "foo")
	os.Symlink(dotfiles, link)
	for _, overwrite := range []bool{false, true} {
		_, conflicts, err := linkKeg("foo", "1.0", overwrite, false)
		if err != nil || len(conflicts) != 1 || conflicts[0].dst != link {
			t.Fatalf("overwrite=%v: conflicts=%v err=%v", overwrite, conflicts, err)
		}
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("user directory symlink was replaced")
	}
	if ents, _ := os.ReadDir(dotfiles); len(ents) != 1 {
		t.Errorf("dotfiles modified: %v", ents)
	}
	if err := ensureRealDir(link); err == nil {
		t.Error("ensureRealDir expanded a link pointing outside the Cellar")
	}

	// A directory link through opt is a keg link and is expanded.
	makeKeg(t, "a", "1", "share/common/a.txt")
	makeKeg(t, "b", "1", "share/common/b.txt")
	setOptLink("a", "1")
	os.MkdirAll(filepath.Join(p, "share"), 0o755)
	os.Symlink("../opt/a/share/common", filepath.Join(p, "share", "common"))
	if _, c, err := linkKeg("b", "1", false, false); err != nil || len(c) > 0 {
		t.Fatalf("link b: %v %v", c, err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(p, "share", "common", f)); err != nil {
			t.Errorf("%s not reachable: %v", f, err)
		}
	}
}

func TestCaskSymlinkedArtifactsAndFallbackTargets(t *testing.T) {
	p := fakePrefix(t)
	h := fakeHome(t)
	t.Setenv("MISE_BREW_CASK_OPT_APPDIR", "")
	// A Binary that is now a real file (not our symlink) is left alone.
	real := filepath.Join(p, "bin", "tool")
	write(t, real, "user's own")
	o := &caskOps{}
	o.removeArtifacts("Binary", []string{real})
	if !fileExists(real) {
		t.Error("non-symlink binary was removed")
	}

	c := &Cask{Artifacts: []json.RawMessage{
		json.RawMessage(`{"app":["Foo.app",{"target":"~/Applications/Foo.app"}]}`),
		json.RawMessage(`{"app":["Bar.app"]}`),
		json.RawMessage(`{"suite":["Foo Suite",{"target":"Suite Target"}]}`),
		json.RawMessage(`{"binary":["$APPDIR/Foo.app/Contents/MacOS/foo",{"target":"~/bin/foo"}]}`),
		json.RawMessage(`{"binary":["$APPDIR/Foo.app/Contents/MacOS/foocli"]}`),
		json.RawMessage(`{"app":["Evil.app",{"target":"../Users/x/Documents"}]}`),
	}}
	got := map[string]string{}
	for _, a := range caskFallbackTargets(c) {
		got[a.name] = a.kind + " " + a.target
	}
	want := map[string]string{
		"Foo.app":                               "App " + filepath.Join(h, "Applications/Foo.app"),
		"Bar.app":                               "App /Applications/Bar.app",
		"Foo Suite":                             "Suite /Applications/Suite Target",
		"$APPDIR/Foo.app/Contents/MacOS/foo":    "Binary " + filepath.Join(h, "bin/foo"),
		"$APPDIR/Foo.app/Contents/MacOS/foocli": "Binary " + filepath.Join(p, "bin/foocli"),
	}
	if len(got) != len(want) {
		t.Errorf("targets = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s -> %q, want %q", k, got[k], v)
		}
	}
}
