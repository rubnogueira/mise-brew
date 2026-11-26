package cask

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestHomeDirFallback(t *testing.T) {
	testutil.FakePrefix(t)
	orig := paths.UserHomeLookup
	t.Cleanup(func() { paths.UserHomeLookup = orig })
	paths.UserHomeLookup = func() string { return "/home/fallback" }
	t.Setenv("HOME", "")
	if h := paths.HomeDir(); h != "/home/fallback" {
		t.Errorf("homeDir() = %q, want the user database entry", h)
	}
	t.Setenv("HOME", "relative/home")
	if h := paths.HomeDir(); h != "/home/fallback" {
		t.Errorf("homeDir() with relative HOME = %q", h)
	}

	// No usable home at all: ~ and $HOME paths must fail, never become /...
	t.Setenv("HOME", "")
	paths.UserHomeLookup = func() string { return "" }
	if h := paths.HomeDir(); h != "" {
		t.Fatalf("homeDir() = %q", h)
	}
	if s := homebrew.ExpandUserPath("~/Library/X"); filepath.IsAbs(s) {
		t.Errorf("expandUserPath = %q", s)
	}
	for _, in := range []string{"~/Library/X", "~", "$HOME/Library/X"} {
		if s, ok := homebrew.ExpandCaskPathOK(in); ok || filepath.IsAbs(s) {
			t.Errorf("expandCaskPathOK(%q) = %q, %v", in, s, ok)
		}
	}
	if s, ok := homebrew.ExpandCaskPathOK("$HOMEBREW_PREFIX/bin/x"); !ok || s != filepath.Join(paths.BrewPrefix(), "bin/x") {
		t.Errorf("expandCaskPathOK($HOMEBREW_PREFIX) = %q, %v", s, ok)
	}
	o := &CaskOps{Dry: true}
	if got := o.SafePaths([]string{"~/Library/Caches/foo", "$HOME/Library/Foo"}); len(got) != 0 {
		t.Errorf("safePaths without HOME = %v", got)
	}
	if MayEscalate("/Library/Foo") {
		t.Error("sudo allowed with unknown HOME")
	}
	c := &homebrew.Cask{Artifacts: []json.RawMessage{json.RawMessage(`{"font":["Foo.ttf"]}`)}}
	if ts := CaskFallbackTargets(c); len(ts) != 0 {
		t.Errorf("font target without HOME = %v", ts)
	}
}
