package mise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestDeclarationsEditThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real", "mise.toml")
	testutil.Write(t, real, `# my config
[tools]
node = "24"

[bootstrap.packages]
"brew:jq" = "latest" # json
"brew:wget" = "latest"
# "brew:wget" = "old"
'brew:fd' = { version = "latest", os = ["macos"] }
"brew-cask:firefox" = "latest"
`)
	link := filepath.Join(dir, "conf.d", "mise.toml")
	os.MkdirAll(filepath.Dir(link), 0o755)
	os.Symlink(real, link)
	other := filepath.Join(dir, "dotted.toml")
	testutil.Write(t, other, "bootstrap.packages.\"brew:wget\" = \"latest\"\n[bootstrap]\npackages.\"brew:jq\" = \"1\"\n")

	files := []string{link, other}
	d := FindDeclarations("brew:wget", files)
	if len(d) != 2 || !d[0].Editable || !d[1].Editable {
		t.Fatalf("wget decls = %+v", d)
	}
	if fd := FindDeclarations("brew:fd", files); len(fd) != 1 || !fd[0].Editable {
		t.Fatalf("fd decls = %+v", fd)
	}
	if jq := FindDeclarations("brew:jq", files); len(jq) != 2 {
		t.Fatalf("jq decls = %+v", jq)
	}
	if err := RemoveDeclarations(d); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlinked config was replaced by a regular file")
	}
	b, _ := os.ReadFile(real)
	s := string(b)
	if strings.Contains(s, "\"brew:wget\" = \"latest\"") || !strings.Contains(s, "# \"brew:wget\" = \"old\"") || !strings.Contains(s, "brew:jq") {
		t.Errorf("unexpected result:\n%s", s)
	}
	if b, _ := os.ReadFile(other); strings.Contains(string(b), "wget") {
		t.Errorf("dotted key not removed:\n%s", b)
	}
	if FindDeclarations("brew:firefox", files) != nil {
		t.Error("brew:firefox must not match brew-cask:firefox")
	}
}

func TestUndeclareRestore(t *testing.T) {
	testutil.IsolateMise(t, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "mise.toml")
	orig := "[bootstrap.packages]\n\"brew:keep-me\" = \"latest\"\n\"brew-cask:mise-brew-test-cask\" = \"latest\"\n"
	testutil.Write(t, cfg, orig)
	t.Setenv("MISEBREW_CONFIG", cfg)
	restore := Undeclare("brew-cask:mise-brew-test-cask")
	b, _ := os.ReadFile(cfg)
	if strings.Contains(string(b), "mise-brew-test-cask") || !strings.Contains(string(b), "keep-me") {
		t.Fatalf("after undeclare:\n%s", b)
	}
	restore()
	if b, _ := os.ReadFile(cfg); string(b) != orig {
		t.Fatalf("after restore:\n%s", b)
	}
}
