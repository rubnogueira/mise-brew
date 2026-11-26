package homebrew

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestLinkOverwriteKeepsRealDirectories(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "py", "1.0", "lib/python3.12/sp-real/mod.py", "bin/py")
	keg := KegPath("py", "1.0")
	os.Symlink("sp-real", filepath.Join(keg, "lib/python3.12/site-packages"))
	// The prefix has a real site-packages full of other kegs' files.
	other := filepath.Join(p, "lib/python3.12/site-packages/other.pth")
	testutil.Write(t, other, "x")
	for _, overwrite := range []bool{false, true} {
		_, conflicts, err := LinkKeg("py", "1.0", overwrite, false)
		if err != nil || len(conflicts) != 1 || conflicts[0].Rel != "lib/python3.12/site-packages" {
			t.Fatalf("overwrite=%v: conflicts=%v err=%v", overwrite, conflicts, err)
		}
		if !paths.FileExists(other) {
			t.Fatalf("overwrite=%v deleted a real directory", overwrite)
		}
	}
	// An empty real directory is replaced with --overwrite.
	os.Remove(other)
	if _, conflicts, err := LinkKeg("py", "1.0", true, false); err != nil || len(conflicts) > 0 {
		t.Fatalf("empty dir: conflicts=%v err=%v", conflicts, err)
	}
	if !ResolvesInto(filepath.Join(p, "lib/python3.12/site-packages"), keg) {
		t.Error("site-packages not linked")
	}
}

func TestLinkReplacesStaleLinks(t *testing.T) {
	p := testutil.FakePrefix(t)
	testutil.MakeKeg(t, "foo", "0.9", "bin/foo-old")
	testutil.MakeKeg(t, "foo", "1.0", "bin/foo", "bin/foo-old", "bin/foo-other")
	testutil.MakeKeg(t, "bar", "1", "bin/foo-other")
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	// Dangling link left by a removed keg.
	os.Symlink("../Cellar/gone/1/bin/foo", filepath.Join(p, "bin", "foo"))
	// Link into another version of the same rack.
	os.Symlink("../Cellar/foo/0.9/bin/foo-old", filepath.Join(p, "bin", "foo-old"))
	// Link into another formula: still a conflict.
	os.Symlink("../Cellar/bar/1/bin/foo-other", filepath.Join(p, "bin", "foo-other"))
	_, conflicts, err := LinkKeg("foo", "1.0", false, false)
	if err != nil || len(conflicts) != 1 || conflicts[0].Rel != "bin/foo-other" {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}
	os.Remove(filepath.Join(p, "bin", "foo-other"))
	if _, conflicts, err := LinkKeg("foo", "1.0", false, false); err != nil || len(conflicts) > 0 {
		t.Fatalf("conflicts=%v err=%v", conflicts, err)
	}
	for _, f := range []string{"foo", "foo-old", "foo-other"} {
		if !ResolvesInto(filepath.Join(p, "bin", f), KegPath("foo", "1.0")) {
			t.Errorf("bin/%s not relinked", f)
		}
	}
}

func TestLinkLeavesForeignDirSymlinks(t *testing.T) {
	p := testutil.FakePrefix(t)
	dotfiles := filepath.Join(t.TempDir(), "dotfiles", "foo")
	testutil.Write(t, filepath.Join(dotfiles, "user.conf"), "mine")
	testutil.MakeKeg(t, "foo", "1.0", "etc/foo/foo.conf", "bin/foo")
	os.MkdirAll(filepath.Join(p, "etc"), 0o755)
	link := filepath.Join(p, "etc", "foo")
	os.Symlink(dotfiles, link)
	for _, overwrite := range []bool{false, true} {
		_, conflicts, err := LinkKeg("foo", "1.0", overwrite, false)
		if err != nil || len(conflicts) != 1 || conflicts[0].Dst != link {
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
	testutil.MakeKeg(t, "a", "1", "share/common/a.txt")
	testutil.MakeKeg(t, "b", "1", "share/common/b.txt")
	SetOptLink("a", "1")
	os.MkdirAll(filepath.Join(p, "share"), 0o755)
	os.Symlink("../opt/a/share/common", filepath.Join(p, "share", "common"))
	if _, c, err := LinkKeg("b", "1", false, false); err != nil || len(c) > 0 {
		t.Fatalf("link b: %v %v", c, err)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(p, "share", "common", f)); err != nil {
			t.Errorf("%s not reachable: %v", f, err)
		}
	}
}
