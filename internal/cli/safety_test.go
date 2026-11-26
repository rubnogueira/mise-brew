package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/cask"
	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
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
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo")
	os.MkdirAll(filepath.Join(p, "Caskroom", "bar", "1.0"), 0o755)
	// Cellar/. and Caskroom/.. exist as directories; they must not count.
	for _, n := range badNames {
		if vs := homebrew.KegVersions(n); len(vs) > 0 {
			t.Errorf("kegVersions(%q) = %v", n, vs)
		}
		if homebrew.IsFormulaInstalled(n) || homebrew.IsCaskInstalled(n) {
			t.Errorf("%q reported installed", n)
		}
		if v := homebrew.CurrentVersion(n); v != "" {
			t.Errorf("currentVersion(%q) = %q", n, v)
		}
		if vs := homebrew.CaskVersions(n); len(vs) > 0 {
			t.Errorf("caskVersions(%q) = %v", n, vs)
		}
		if r := homebrew.ResolveInstalledFormula(n); r != "" {
			t.Errorf("resolveInstalledFormula(%q) = %q", n, r)
		}
		if r := homebrew.ResolveInstalledCask(n); r != "" {
			t.Errorf("resolveInstalledCask(%q) = %q", n, r)
		}
		if homebrew.IsPinned(n) {
			t.Errorf("isPinned(%q)", n)
		}
	}
	for _, n := range []string{"acme/../foo", "../tap/foo", "acme/tap/..", "homebrew/core/..", "homebrew/cask/.."} {
		if r := homebrew.ResolveInstalledFormula(n); r != "" {
			t.Errorf("resolveInstalledFormula(%q) = %q", n, r)
		}
		if r := homebrew.ResolveInstalledCask(n); r != "" {
			t.Errorf("resolveInstalledCask(%q) = %q", n, r)
		}
	}
	if r := homebrew.ResolveInstalledFormula("homebrew/core/foo"); r != "foo" {
		t.Errorf("resolveInstalledFormula(homebrew/core/foo) = %q", r)
	}
	if r := homebrew.ResolveInstalledCask("bar"); r != "bar" {
		t.Errorf("resolveInstalledCask(bar) = %q", r)
	}
	// A record link pointing at ".." must not yield a version.
	os.MkdirAll(paths.LinkedDir(), 0o755)
	os.Symlink("..", filepath.Join(paths.LinkedDir(), "foo"))
	if v := homebrew.ReadLinkVersion(filepath.Join(paths.LinkedDir(), "foo")); v != "" {
		t.Errorf("readLinkVersion -> %q", v)
	}
}

func TestRemovalRefusesUnsafePaths(t *testing.T) {
	p := testutil.FakePrefix(t)
	fakeHome(t)
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo")
	os.MkdirAll(filepath.Join(p, "Caskroom", "bar", "1.0"), 0o755)
	for _, n := range badNames {
		if err := homebrew.RemoveKegs(n, nil); err == nil {
			t.Errorf("removeKegs(%q) accepted", n)
		}
		if err := cask.NativeUninstallCask(n, false, false); err == nil {
			t.Errorf("nativeUninstallCask(%q) accepted", n)
		}
		if _, _, err := homebrew.LinkKeg(n, "1.0", true, false); err == nil {
			t.Errorf("linkKeg(%q) accepted", n)
		}
		if n, _ := homebrew.UnlinkKeg(n, "1.0", false); n != 0 {
			t.Errorf("unlinkKeg removed %d", n)
		}
	}
	for _, v := range []string{"", ".", "..", "../..", "1.0/../.."} {
		if err := homebrew.RemoveKegs("foo", []string{v}); err == nil {
			t.Errorf("removeKegs(foo, %q) accepted", v)
		}
	}
	for _, x := range []string{"Cellar/foo/1.0/bin/foo", "Caskroom/bar/1.0"} {
		if !paths.FileExists(filepath.Join(p, x)) {
			t.Fatalf("%s was removed", x)
		}
	}
}

func TestCaskProtectedPaths(t *testing.T) {
	p := testutil.FakePrefix(t)
	h := fakeHome(t)
	for _, d := range []string{"Library/Caches", "Library/Preferences", "Library/Application Support/MyApp", "Library/MyAppStuff", "Documents"} {
		os.MkdirAll(filepath.Join(h, d), 0o755)
	}
	os.MkdirAll(filepath.Join(p, "Cellar"), 0o755)
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	os.Symlink("/usr", filepath.Join(h, "usrlink"))
	o := &cask.CaskOps{Dry: true}
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
		if got := o.SafePaths([]string{pat}); len(got) != 0 {
			t.Errorf("safePaths(%q) = %v", pat, got)
		}
	}
	// Globs: protected matches are dropped, others kept.
	got := o.SafePaths([]string{"~/Library/*", "~/Library/Application Support/*"})
	want := []string{filepath.Join(h, "Library/MyAppStuff"), filepath.Join(h, "Library/Application Support/MyApp")}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("glob safePaths = %v, want %v", got, want)
	}
	if runtime.GOOS == "darwin" && !cask.ProtectedPath("/usr/LIB") {
		t.Error("case-folded system path not protected on darwin")
	}
	if cask.MayEscalate(filepath.Join(h, "Library", "MyAppStuff")) {
		t.Error("sudo allowed below HOME")
	}
	if !cask.MayEscalate("/Library/Application Support/MyApp") {
		t.Error("sudo refused outside HOME")
	}
}

func TestCaskSymlinkedArtifactsAndFallbackTargets(t *testing.T) {
	p := testutil.FakePrefix(t)
	h := fakeHome(t)
	t.Setenv("MISE_BREW_CASK_OPT_APPDIR", "")
	// A Binary that is now a real file (not our symlink) is left alone.
	real := filepath.Join(p, "bin", "tool")
	testutil.Write(t, real, "user's own")
	o := &cask.CaskOps{}
	o.RemoveArtifacts("Binary", []string{real})
	if !paths.FileExists(real) {
		t.Error("non-symlink binary was removed")
	}

	c := &homebrew.Cask{Artifacts: []json.RawMessage{
		json.RawMessage(`{"app":["Foo.app",{"target":"~/Applications/Foo.app"}]}`),
		json.RawMessage(`{"app":["Bar.app"]}`),
		json.RawMessage(`{"suite":["Foo Suite",{"target":"Suite Target"}]}`),
		json.RawMessage(`{"binary":["$APPDIR/Foo.app/Contents/MacOS/foo",{"target":"~/bin/foo"}]}`),
		json.RawMessage(`{"binary":["$APPDIR/Foo.app/Contents/MacOS/foocli"]}`),
		json.RawMessage(`{"app":["Evil.app",{"target":"../Users/x/Documents"}]}`),
	}}
	got := map[string]string{}
	for _, a := range cask.CaskFallbackTargets(c) {
		got[a.Name] = a.Kind + " " + a.Target
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
