package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/paths"
)

// IsolateMise points HOME and the mise config dirs at a temp dir and replaces
// mise with a stub, so nothing reads or edits the real user's configuration.
func IsolateMise(t *testing.T, stubScript string) string {
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
	Write(t, stub, "#!/bin/sh\n"+stubScript+"\n")
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	old := paths.MiseBinCache
	paths.MiseBinCache = stub
	t.Cleanup(func() { paths.MiseBinCache = old })
	return dir
}
