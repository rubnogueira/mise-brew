package mise

import (
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestDeclarationArrayOfTables(t *testing.T) {
	testutil.IsolateMise(t, "")
	cfg := filepath.Join(t.TempDir(), "mise.toml")
	orig := `[bootstrap.packages]
"brew:fd" = "latest"

[[tasks.x.steps]]
"brew:jq" = "keep"
`
	testutil.Write(t, cfg, orig)
	if d := FindDeclarations("brew:jq", []string{cfg}); len(d) != 0 {
		t.Fatalf("key under [[tasks.x.steps]] attributed to bootstrap.packages: %+v", d)
	}
	if d := FindDeclarations("brew:fd", []string{cfg}); len(d) != 1 || !d[0].Editable {
		t.Fatalf("fd decls = %+v", d)
	}
}

func TestMiseEnvForwarded(t *testing.T) {
	testutil.IsolateMise(t, `printf '%s' "$MISE_ENV"`)
	out, err := MiseOutput("config", "ls")
	if err != nil || string(out) != "" {
		t.Fatalf("no MISEBREW_ENV: %q %v", out, err)
	}
	t.Setenv("MISEBREW_ENV", "work")
	if out, _ := MiseOutput("config", "ls"); string(out) != "work" {
		t.Errorf("MISE_ENV = %q, want work", out)
	}
	t.Setenv("MISE_ENV", "dev")
	if out, _ := MiseOutput("config", "ls"); string(out) != "dev,work" {
		t.Errorf("MISE_ENV = %q, want dev,work", out)
	}
	t.Setenv("MISE_ENV", "work,dev")
	if out, _ := MiseOutput("config", "ls"); string(out) != "work,dev" {
		t.Errorf("MISE_ENV = %q, want work,dev", out)
	}
}
