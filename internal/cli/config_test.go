package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUndeclareSameFileViaTwoPaths(t *testing.T) {
	home := testutil.IsolateMise(t, "")
	real := filepath.Join(home, "dotfiles", "mise.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n"
	testutil.Write(t, real, orig)
	link := filepath.Join(home, ".config", "mise", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MISEBREW_CONFIG", real)

	if files := mise.ConfigFiles(); len(files) != 1 {
		t.Fatalf("configFiles = %v, want one entry", files)
	}
	restore := mise.Undeclare("brew:jq")
	if s := readFile(t, real); s != "[bootstrap.packages]\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n" {
		t.Fatalf("after undeclare:\n%s", s)
	}
	restore()
	if s := readFile(t, real); s != orig {
		t.Fatalf("after restore:\n%s", s)
	}
	// a second call must not clobber later changes
	testutil.Write(t, real, "changed\n")
	restore()
	if s := readFile(t, real); s != "changed\n" {
		t.Fatalf("second restore rewrote the file:\n%s", s)
	}
}

func TestRemoveDeclarationsDuplicatePathsAndStaleLines(t *testing.T) {
	testutil.IsolateMise(t, "")
	dir := t.TempDir()
	real := filepath.Join(dir, "mise.toml")
	link := filepath.Join(dir, "link.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n"
	testutil.Write(t, real, orig)
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// both paths passed explicitly: the file is still edited once
	if err := mise.RemoveDeclarations(mise.FindDeclarations("brew:jq", []string{link, real})); err != nil {
		t.Fatal(err)
	}
	if s := readFile(t, real); s != "[bootstrap.packages]\n\"brew:fd\" = \"latest\"\n\"brew:rg\" = \"latest\"\n" {
		t.Fatalf("after remove:\n%s", s)
	}

	// file changed between find and rewrite: abort without writing
	testutil.Write(t, real, orig)
	decls := mise.FindDeclarations("brew:jq", []string{real})
	changed := "# new first line\n" + orig
	testutil.Write(t, real, changed)
	if err := mise.RemoveDeclarations(decls); err == nil {
		t.Fatal("expected an error for a changed file")
	}
	if s := readFile(t, real); s != changed {
		t.Fatalf("changed file was edited:\n%s", s)
	}
}

func TestDeclarationSubTable(t *testing.T) {
	testutil.IsolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	testutil.Write(t, cfg, `[tools]
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
	d := mise.FindDeclarations("brew:jq", []string{cfg})
	if len(d) != 1 || !d[0].Editable || d[0].Line != 6 || d[0].End != 11 {
		t.Fatalf("jq decls = %+v", d)
	}
	if err := mise.RemoveDeclarations(d); err != nil {
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

func TestDeclarationMultiLineValues(t *testing.T) {
	testutil.IsolateMise(t, "")
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
	testutil.Write(t, cfg, orig)
	for _, pkg := range []string{"brew:jq", "brew:fd", "brew:rg"} {
		d := mise.FindDeclarations(pkg, []string{cfg})
		if len(d) != 1 || d[0].Editable {
			t.Errorf("%s decls = %+v, want one non-editable", pkg, d)
		}
		if err := mise.RemoveDeclarations(d); err != nil {
			t.Fatal(err)
		}
	}
	if d := mise.FindDeclarations("brew:x", []string{cfg}); len(d) != 0 {
		t.Errorf("matched inside a multi-line string: %+v", d)
	}
	if d := mise.FindDeclarations("brew:ok", []string{cfg}); len(d) != 1 || !d[0].Editable || d[0].Line != 11 {
		t.Errorf("ok decls = %+v", d)
	}
	if s := readFile(t, cfg); s != orig {
		t.Fatalf("non-editable declarations were edited:\n%s", s)
	}
	inline := filepath.Join(filepath.Dir(cfg), "inline.toml")
	testutil.Write(t, inline, "[bootstrap.packages]\n\"brew:bat\" = { version = \"latest\",\n  os = [\"macos\"] }\n")
	if d := mise.FindDeclarations("brew:bat", []string{inline}); len(d) != 1 || d[0].Editable {
		t.Errorf("bat decls = %+v, want one non-editable", d)
	}

	// safety net: an edit that would break the TOML is refused
	bad := []mise.Declaration{{File: cfg, Line: 1, Text: `"brew:jq" = '''`, Editable: true, Key: "brew:jq"}}
	if err := mise.RemoveDeclarations(bad); err == nil {
		t.Fatal("expected the TOML check to refuse the edit")
	}
	if s := readFile(t, cfg); s != orig {
		t.Fatalf("file edited despite failed TOML check:\n%s", s)
	}
}

func TestRemoveDeclarationsTempFileSafety(t *testing.T) {
	testutil.IsolateMise(t, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mise.toml")
	testutil.Write(t, cfg, "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n\"brew:fd\" = \"latest\"\n")
	if err := os.Chmod(cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	testutil.Write(t, victim, "untouched\n")
	if err := os.Symlink(victim, cfg+".mise-brew.tmp"); err != nil {
		t.Fatal(err)
	}
	if err := mise.RemoveDeclarations(mise.FindDeclarations("brew:jq", []string{cfg})); err != nil {
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
	testutil.IsolateMise(t, "")
	a := filepath.Join(t.TempDir(), "a.toml")
	roDir := t.TempDir()
	b := filepath.Join(roDir, "b.toml")
	orig := "[bootstrap.packages]\n\"brew:jq\" = \"latest\"\n"
	testutil.Write(t, a, orig)
	testutil.Write(t, b, orig)
	if err := os.Chmod(roDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o755) })
	if _, err := mise.RewriteDeclarations(mise.FindDeclarations("brew:jq", []string{a, b})); err == nil {
		t.Fatal("expected a write error")
	}
	if s := readFile(t, a); s != orig {
		t.Fatalf("first file not restored:\n%s", s)
	}
}
