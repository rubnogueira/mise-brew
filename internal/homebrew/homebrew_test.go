package homebrew

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestLinkUnlinkRoundTrip(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo", "lib/libfoo.dylib", "share/man/man1/foo.1", "share/foo/data.txt")
	// A file from another keg sharing share/man/man1 must survive unlink.
	testutil.MakeKeg(t, "bar", "2.0", "share/man/man1/bar.1")
	if _, _, err := LinkKeg("bar", "2.0", false, false); err != nil {
		t.Fatal(err)
	}
	n, conflicts, err := LinkKeg("foo", "1.0", false, false)
	if err != nil || len(conflicts) > 0 {
		t.Fatalf("link: n=%d conflicts=%v err=%v", n, conflicts, err)
	}
	if n != 4 {
		t.Errorf("created %d links, want 4", n)
	}
	target, err := os.Readlink(filepath.Join(p, "bin", "foo"))
	if err != nil || target != "../Cellar/foo/1.0/bin/foo" {
		t.Errorf("bin/foo -> %q (%v)", target, err)
	}
	if LinkedVersion("foo") != "1.0" || ReadLinkVersion(filepath.Join(paths.OptDir(), "foo")) != "1.0" {
		t.Error("linked/opt records not written")
	}
	removed, _ := UnlinkKeg("foo", "1.0", false)
	if removed != 4 {
		t.Errorf("removed %d, want 4", removed)
	}
	if paths.FileExists(filepath.Join(p, "bin", "foo")) || paths.FileExists(filepath.Join(p, "share", "foo")) {
		t.Error("foo links or empty dirs left behind")
	}
	if !paths.FileExists(filepath.Join(p, "share", "man", "man1", "bar.1")) {
		t.Error("unlink removed another keg's link")
	}
	if !paths.IsDir(filepath.Join(p, "bin")) {
		t.Error("top-level bin dir must not be removed")
	}
}

func TestLinkConflictAndOverwrite(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo")
	testutil.Write(t, filepath.Join(p, "bin", "foo"), "unmanaged")
	_, conflicts, _ := LinkKeg("foo", "1.0", false, false)
	if len(conflicts) != 1 || conflicts[0].Rel != "bin/foo" {
		t.Fatalf("conflicts=%v", conflicts)
	}
	if b, _ := os.ReadFile(filepath.Join(p, "bin", "foo")); string(b) != "unmanaged" {
		t.Error("conflicting file was modified without --overwrite")
	}
	if _, _, err := LinkKeg("foo", "1.0", true, false); err != nil {
		t.Fatal(err)
	}
	if !ResolvesInto(filepath.Join(p, "bin", "foo"), KegPath("foo", "1.0")) {
		t.Error("--overwrite did not link")
	}
}

func TestLinkExpandsForeignDirSymlink(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "a", "1", "share/common/a.txt")
	testutil.MakeKeg(t, "b", "1", "share/common/b.txt")
	// keg a owns share/common as a directory symlink (Homebrew does this).
	os.MkdirAll(filepath.Join(p, "share"), 0o755)
	os.Symlink("../Cellar/a/1/share/common", filepath.Join(p, "share", "common"))
	if _, c, err := LinkKeg("b", "1", false, false); err != nil || len(c) > 0 {
		t.Fatalf("link b: %v %v", c, err)
	}
	fi, _ := os.Lstat(filepath.Join(p, "share", "common"))
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("dir symlink not expanded")
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(p, "share", "common", f)); err != nil {
			t.Errorf("%s not reachable: %v", f, err)
		}
	}
}

func TestRemoveKegsCleansRecords(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo")
	testutil.MakeKeg(t, "foo", "2.0", "bin/foo")
	LinkKeg("foo", "2.0", false, false)
	SetRecordLink(paths.PinnedDir(), "foo", "2.0")
	if err := RemoveKegs("foo", []string{"2.0"}); err != nil {
		t.Fatal(err)
	}
	if !paths.IsDir(KegPath("foo", "1.0")) || ReadLinkVersion(filepath.Join(paths.OptDir(), "foo")) != "1.0" {
		t.Error("opt should be repointed at the remaining keg")
	}
	if err := RemoveKegs("foo", KegVersions("foo")); err != nil {
		t.Fatal(err)
	}
	for _, x := range []string{"Cellar/foo", "opt/foo", "var/homebrew/linked/foo", "var/homebrew/pinned/foo", "bin/foo"} {
		if paths.FileExists(filepath.Join(p, x)) {
			t.Errorf("%s left behind", x)
		}
	}
}

func TestApplyVariations(t *testing.T) {
	raw := []byte(`{"name":"x","dependencies":["a"],"variations":{"` + PlatformTag() + `":{"dependencies":["a","b"]}}}`)
	var f Formula
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	applyVariations(raw, &f)
	if strings.Join(f.Dependencies, ",") != "a,b" {
		t.Errorf("deps = %v", f.Dependencies)
	}
	m := map[string]any{"dependencies": []any{"a"}, "variations": map[string]any{PlatformTag(): map[string]any{"dependencies": []any{"z"}}}}
	applyVariationsMap(m, false)
	if _, ok := m["variations"]; ok || fmt.Sprint(m["dependencies"]) != "[z]" {
		t.Errorf("map = %v", m)
	}
}

func TestMacosBounds(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	f := &Formula{
		UsesFromMacos:       []json.RawMessage{json.RawMessage(`"zlib"`), json.RawMessage(`{"python":"build"}`)},
		UsesFromMacosBounds: []map[string]string{{"since": "big_sur"}, {"since": "99"}},
	}
	if MacosDepApplies(f, 0) {
		t.Error("zlib is provided by macOS since big_sur")
	}
	if !MacosDepApplies(f, 1) {
		t.Error("python is needed before macOS 99")
	}
}

func TestOutdatedIgnoresMiseSchemeZero(t *testing.T) {
	testutil.FakePrefix(t)
	testutil.MakeKeg(t, "lcms", "2.20")
	testutil.MakeKeg(t, "ossl", "3.6.4_1")
	same := &Formula{Name: "lcms", VersionScheme: 1}
	same.Versions.Stable = "2.20"
	if KegOutdated("lcms", same) {
		t.Error("identical version must not be outdated even with a scheme bump")
	}
	bumped := &Formula{Name: "ossl", VersionScheme: 1}
	bumped.Versions.Stable = "3.5.9"
	if !KegOutdated("ossl", bumped) {
		t.Error("version_scheme bump must make an older-scheme keg outdated")
	}
}

func TestParseTapFormula(t *testing.T) {
	src := `class Foo < Formula
  desc "Does \"foo\" things"
  homepage "https://example.com/foo"
  url "https://example.com/foo/archive/v1.4.2.tar.gz"
  sha256 "abc"
  license "MIT"
  revision 2

  depends_on "pkgconf" => :build
  depends_on "openssl@3"
  depends_on "python" => [:build, :test]

  def caveats
    <<~EOS
      Run foo --init first.
    EOS
  end
  service do
    run [opt_bin/"foo"]
  end
end
`
	f := ParseTapFormula(src, "acme/tools", "foo", "Formula/foo.rb")
	if f.Desc != `Does "foo" things` || f.Versions.Stable != "1.4.2" || f.PkgVersion() != "1.4.2_2" || f.License != "MIT" {
		t.Errorf("parsed %+v", f)
	}
	if strings.Join(f.Dependencies, ",") != "openssl@3" || strings.Join(f.BuildDependencies, ",") != "pkgconf,python" {
		t.Errorf("deps %v build %v", f.Dependencies, f.BuildDependencies)
	}
	if f.Caveats == nil || !strings.Contains(*f.Caveats, "foo --init") || len(f.Service) == 0 {
		t.Error("caveats/service not parsed")
	}
	if v := versionFromURL("https://x.org/releases/bar-2.0.1-src.tar.xz"); v != "2.0.1" {
		t.Errorf("versionFromURL = %q", v)
	}
}
