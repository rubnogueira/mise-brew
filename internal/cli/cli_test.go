package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

func TestSplitBottleName(t *testing.T) {
	inst := map[string]string{"aws-c-common": "1.0.6", "aws-c": "9", "openssl@4": "4.1.0"}
	if n, v := splitBottleName("aws-c-common-1.0.4.tar.gz", inst); n != "aws-c-common" || v != "1.0.4" {
		t.Errorf("got %s %s", n, v)
	}
	if n, v := splitBottleName("openssl@4-4.1.0.tar.gz", inst); n != "openssl@4" || v != "4.1.0" {
		t.Errorf("got %s %s", n, v)
	}
	if n, _ := splitBottleName("deno-2.9.8.tar.gz", inst); n != "" {
		t.Errorf("uninstalled bottle matched %s", n)
	}
}

func TestParseArgs(t *testing.T) {
	o := parseArgs([]string{"install", "-qn", "--cask", "--json=v2", "--file", "Brewfile", "foo", "--", "-x"}, []string{"file"}, nil)
	if !o.Has("quiet") || !o.Has("dry-run") || !o.Has("cask") {
		t.Errorf("flags = %v", o.flags)
	}
	if v, _ := o.Val("json"); v != "v2" {
		t.Errorf("json = %q", v)
	}
	if v, _ := o.Val("file"); v != "Brewfile" {
		t.Errorf("file = %q", v)
	}
	if strings.Join(o.Pos, " ") != "install foo -x" {
		t.Errorf("pos = %v", o.Pos)
	}
}

func TestRecursionGuard(t *testing.T) {
	t.Setenv(mise.RecursionEnv, "1")
	if code := run([]string{"install", "hello"}); code != 1 {
		t.Fatalf("nested install exit=%d, want 1", code)
	}
}

func TestFlagValidation(t *testing.T) {
	if code := run([]string{"list", "--bogus-flag"}); code != 1 {
		t.Errorf("unknown flag exit=%d, want 1", code)
	}
	if code := run([]string{"--prefix", "--installed", "--formula"}); code != 0 {
		t.Errorf("known flags exit=%d, want 0", code)
	}
	if specs := commandSpecs["desc"]; len(specs) == 0 || shortMap("desc")["d"] != "description" {
		t.Errorf("desc -d should mean --description, got %q", shortMap("desc")["d"])
	}
}

func TestAutoremoveCandidates(t *testing.T) {
	testutil.FakePrefix(t)
	testutil.MakeKeg(t, "mbt-app", "1")
	testutil.MakeKeg(t, "mbt-lib", "1")
	testutil.MakeKeg(t, "mbt-orphan", "1")
	testutil.Write(t, filepath.Join(homebrew.KegPath("mbt-app", "1"), "INSTALL_RECEIPT.json"),
		`{"installed_on_request":true,"runtime_dependencies":[{"full_name":"mbt-lib","version":"1"}]}`)
	for _, n := range []string{"mbt-lib", "mbt-orphan"} {
		testutil.Write(t, filepath.Join(homebrew.KegPath(n, "1"), "INSTALL_RECEIPT.json"), `{"installed_on_request":false,"installed_as_dependency":true}`)
	}
	// Pretend mise reported an empty status so the test doesn't run the real mise.
	oldCache, oldErr := mise.StatusCache, mise.StatusErr
	mise.StatusCache, mise.StatusErr = map[string]mise.ManagerStatus{}, nil
	t.Cleanup(func() { mise.StatusCache, mise.StatusErr = oldCache, oldErr })
	if got := autoremoveCandidates(); strings.Join(got, ",") != "mbt-orphan" {
		t.Errorf("candidates = %v", got)
	}
}

func TestMergeEnv(t *testing.T) {
	out := mergeEnv([]string{"PATH=/usr/bin", "HOME=/h"}, [][2]string{{"PATH", "/shims:/usr/bin"}})
	if strings.Join(out, ";") != "HOME=/h;PATH=/shims:/usr/bin" {
		t.Errorf("mergeEnv = %v", out)
	}
}
