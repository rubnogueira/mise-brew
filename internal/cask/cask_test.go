package cask

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestNativeCaskUninstall(t *testing.T) {
	p := testutil.FakePrefix(t)
	room := filepath.Join(p, "Caskroom", "mbt-cask", "1.0")
	testutil.Write(t, filepath.Join(room, "tool", "bin", "tool"), "#!/bin/sh\n")
	os.MkdirAll(filepath.Join(p, "bin"), 0o755)
	link := filepath.Join(p, "bin", "mbt-tool")
	os.Symlink(filepath.Join(room, "tool", "bin", "tool"), link)
	testutil.Write(t, filepath.Join(room, ".mise-cask.toml"), `schema_version = 3
version = "1.0"
binaries = ["`+link+`"]
pkg_ids = []

[[targets]]
path = "`+link+`"
`)
	if err := NativeUninstallCask("mbt-cask", false, true); err != nil {
		t.Fatal(err)
	}
	if !paths.FileExists(link) || !paths.IsDir(room) {
		t.Fatal("dry run removed files")
	}
	if err := NativeUninstallCask("mbt-cask", false, false); err != nil {
		t.Fatal(err)
	}
	if paths.FileExists(link) || paths.FileExists(filepath.Join(p, "Caskroom", "mbt-cask")) {
		t.Error("cask artifacts left behind")
	}
}

func TestCaskSafePaths(t *testing.T) {
	o := &CaskOps{Dry: true}
	got := o.SafePaths([]string{"/", "/Applications", "~/Library", "relative/path"})
	if len(got) != 0 {
		t.Errorf("protected paths passed: %v", got)
	}
}
