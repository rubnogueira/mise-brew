package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateMise points HOME and the mise config dirs at a temp dir and replaces
// mise with a stub, so nothing reads or edits the real user's configuration.
func isolateMise(t *testing.T, stubScript string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))
	t.Setenv("MISE_CONFIG_DIR", filepath.Join(dir, ".config", "mise"))
	t.Setenv("MISEBREW_CONFIG", "")
	t.Setenv("MISEBREW_ENV", "")
	t.Setenv("MISE_ENV", "")
	if stubScript == "" {
		stubScript = "echo '[]'"
	}
	stub := filepath.Join(dir, "bin", "mise")
	write(t, stub, "#!/bin/sh\n"+stubScript+"\n")
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := miseBinCache
	miseBinCache = stub
	t.Cleanup(func() { miseBinCache = old })
	return dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUndeclareSameFileViaTwoPaths(t *testing.T) {
	home := isolateMise(t, "")
	real := filepath.Join(home, "dotfiles", "mise.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n"
	write(t, real, orig)
	link := filepath.Join(home, ".config", "mise", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISEBREW_CONFIG", real)

	if files := configFiles(); len(files) != 1 {
		t.Fatalf("configFiles = %v, want one entry", files)
	}
	restore := undeclare("brew:jq")
	if s := readFile(t, real); s != "[bootstrap.packages]\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n" {
		t.Fatalf("after undeclare:\n%s", s)
	}
	restore()
	if s := readFile(t, real); s != orig {
		t.Fatalf("after restore:\n%s", s)
	}
	// a second call must not clobber later changes
	write(t, real, "changed\n")
	restore()
	if s := readFile(t, real); s != "changed\n" {
		t.Fatalf("second restore rewrote the file:\n%s", s)
	}
}

func TestRemoveDeclarationsDuplicatePathsAndStaleLines(t *testing.T) {
	isolateMise(t, "")
	dir := t.TempDir()
	real := filepath.Join(dir, "mise.toml")
	link := filepath.Join(dir, "link.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n"
	write(t, real, orig)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// both paths passed explicitly: the file is still edited once
	if err := removeDeclarations(findDeclarations("brew:jq", []string{link, real})); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, real); s != "[bootstrap.packages]\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n" {
		t.Fatalf("after remove:\n%s", s)
	}

	// file changed between find and rewrite: abort without writing
	write(t, real, orig)
	decls := findDeclarations("brew:jq", []string{real})
	changed := "# new first line\n" + orig
	write(t, real, changed)
	if err := removeDeclarations(decls); err == nil {
		t.Fatal("expected an error for a changed file")
	}
	if s := readFile(t, real); s != changed {
		t.Fatalf("changed file was edited:\n%s", s)
	}
}

func TestDeclarationSubTable(t *testing.T) {
	isolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	write(t, cfg, `[tools]
node = "24"

[bootstrap.packages]
"brew:fd" = "latest"

[bootstrap.packages."brew:jq"]
version = "latest"
os = [
  "macos",
]

# settings follow
[settings]
x = 1
`)
	d := findDeclarations("brew:jq", []string{cfg})
	if len(d) != 1 || !d[0].Editable || d[0].Line != 6 || d[0].End != 11 {
		t.Fatalf("jq decls = %+v", d)
	}
	if err := removeDeclarations(d); err != nil {
		t.Fatal(err)
	}
	want := `[tools]
node = "24"

[bootstrap.packages]
"brew:fd" = "latest"


# settings follow
[settings]
x = 1
`
	if s := readFile(t, cfg); s != want {
		t.Fatalf("after remove:\n%s", s)
	}
}

func TestDeclarationArrayOfTables(t *testing.T) {
	isolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	orig := `[bootstrap.packages]
"brew:fd" = "latest"

[[tasks.x.steps]]
"brew:jq" = "keep"
`
	write(t, cfg, orig)
	if d := findDeclarations("brew:jq", []string{cfg}); len(d) != 0 {
		t.Fatalf("key under [[tasks.x.steps]] attributed to bootstrap.packages: %+v", d)
	}
	if d := findDeclarations("brew:fd", []string{cfg}); len(d) != 1 || !d[0].Editable {
		t.Fatalf("fd decls = %+v", d)
	}
}

func TestDeclarationMultiLineValues(t *testing.T) {
	isolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	orig := `[bootstrap.packages]
"brew:jq" = '''
latest
'''
"brew:fd" = [
  "latest",
]
"brew:rg" = """
[bootstrap.packages]
"brew:x" = 1
"""
"brew:ok" = "latest"
`
	write(t, cfg, orig)
	for _, pkg := range []string{"brew:jq", "brew:fd", "brew:rg"} {
		d := findDeclarations(pkg, []string{cfg})
		if len(d) != 1 || d[0].Editable {
			t.Errorf("%s decls = %+v, want one non-editable", pkg, d)
		}
		if err := removeDeclarations(d); err != nil {
			t.Fatal(err)
		}
	}
	if d := findDeclarations("brew:x", []string{cfg}); len(d) != 0 {
		t.Errorf("matched inside a multi-line string: %+v", d)
	}
	if d := findDeclarations("brew:ok", []string{cfg}); len(d) != 1 || !d[0].Editable || d[0].Line != 11 {
		t.Errorf("ok decls = %+v", d)
	}
	if s := readFile(t, cfg); s != orig {
		t.Fatalf("non-editable declarations were edited:\n%s", s)
	}
	inline := filepath.Join(filepath.Dir(cfg), "inline.toml")
	write(t, inline, "[bootstrap.packages]\n\"brew:bat\" = { version = \"latest\",\n  os = [\"macos\"] }\n")
	if d := findDeclarations("brew:bat", []string{inline}); len(d) != 1 || d[0].Editable {
		t.Errorf("bat decls = %+v, want one non-editable", d)
	}

	// safety net: an edit that would break the TOML is refused
	bad := []declaration{{File: cfg, Line: 1, Text: `"brew:jq" = '''`, Editable: true, Key: "brew:jq"}}
	if err := removeDeclarations(bad); err == nil {
		t.Fatal("expected the TOML check to refuse the edit")
	}
	if s := readFile(t, cfg); s != orig {
		t.Fatalf("file edited despite failed TOML check:\n%s", s)
	}
}

func TestRemoveDeclarationsTempFileSafety(t *testing.T) {
	isolateMise(t, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mise.toml")
	write(t, cfg, "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n")
	if err := os.Chmod(cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	write(t, victim, "untouched\n")
	if err := os.Symlink(victim, cfg+".mise-brew.tmp"); err != nil {
		t.Fatal(err)
	}
	if err := removeDeclarations(findDeclarations("brew:jq", []string{cfg})); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, victim); s != "untouched\n" {
		t.Fatalf("write followed the planted symlink: %q", s)
	}
	if s := readFile(t, cfg); strings.Contains(s, "jq") || !strings.Contains(s, "fd") {
		t.Fatalf("after remove:\n%s", s)
	}
	fi, err := os.Stat(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".mise-brew-*"))
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestRewriteDeclarationsRollsBack(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write read-only directories")
	}
	isolateMise(t, "")
	a := filepath.Join(t.TempDir(), "a.toml")
	roDir := t.TempDir()
	b := filepath.Join(roDir, "b.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n"
	write(t, a, orig)
	write(t, b, orig)
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	if _, err := rewriteDeclarations(findDeclarations("brew:jq", []string{a, b})); err == nil {
		t.Fatal("expected a write error")
	}
	if s := readFile(t, a); s != orig {
		t.Fatalf("first file not restored:\n%s", s)
	}
}

func TestMiseEnvForwarded(t *testing.T) {
	isolateMise(t, `printf '%s' "$MISE_ENV"`)
	out, err := miseOutput("config", "ls")
	if err != nil || string(out) != "" {
		t.Fatalf("no MISEBREW_ENV: %q %v", out, err)
	}
	t.Setenv("MISEBREW_ENV", "work")
	if out, _ := miseOutput("config", "ls"); string(out) != "work" {
		t.Errorf("MISE_ENV = %q, want work", out)
	}
	t.Setenv("MISE_ENV", "dev")
	if out, _ := miseOutput("config", "ls"); string(out) != "dev,work" {
		t.Errorf("MISE_ENV = %q, want dev,work", out)
	}
	t.Setenv("MISE_ENV", "work,dev")
	if out, _ := miseOutput("config", "ls"); string(out) != "work,dev" {
		t.Errorf("MISE_ENV = %q, want work,dev", out)
	}
}
