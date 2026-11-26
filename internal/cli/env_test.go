package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rubnogueira/mise-brew/internal/buildinfo"
	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// expectExit runs f and reports whether it exited via odie/exit.
func expectExit(t *testing.T, f func()) (exited bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			var ee ui.ExitError
			if err, ok := r.(error); ok && errors.As(err, &ee) {
				exited = true
				return
			}
			panic(r)
		}
	}()
	f()
	return false
}

// evalShellenv evaluates shellenv's output in shell, then again followed by
// whatever shellenv prints for the resulting PATH, and returns PATH, INFOPATH,
// HOMEBREW_PREFIX, MANPATH and, for zsh, fpath[1], FPATH's type and #fpath.
func evalShellenv(t *testing.T, shell, path, prefix string) (first, second []string) {
	t.Helper()
	bin, err := exec.LookPath(shell)
	if err != nil {
		t.Skipf("%s not available", shell)
	}
	report := `printf '%s\n' "$PATH" "${INFOPATH-}" "${HOMEBREW_PREFIX-}" "${MANPATH-}"`
	if shell == "zsh" {
		report += `; printf '%s\n' "${fpath[1]}" "${(t)FPATH}" "${#fpath}"`
	}
	run := func(script, again, path string) []string {
		args := []string{"-c", `eval "$1"; eval "$2"; ` + report, "shellenv-test", script, again}
		if shell == "zsh" {
			args = append([]string{"-f"}, args...)
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = []string{"PATH=" + path, "HOME=" + t.TempDir(), "MANPATH=::/usr/share/man::"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\nscript:\n%s", shell, err, script)
		}
		return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	}
	script := shellenv(shell, path, prefix, prefix+"/Cellar", prefix)
	if script == "" {
		t.Fatalf("shellenv printed nothing for PATH %q", path)
	}
	first = run(script, "", path)
	// Re-evaluating with the new PATH must be a no-op (upstream returns early).
	again := shellenv(shell, first[0], prefix, prefix+"/Cellar", prefix)
	if again != "" {
		t.Errorf("second shellenv should print nothing, got:\n%s", again)
	}
	second = run(script, again, path)
	return first, second
}

func TestShellenvEvaluates(t *testing.T) {
	base := t.TempDir()
	for _, prefix := range []string{
		filepath.Join(base, "my prefix"),
		filepath.Join(base, "we$ird \"pre\" `fix` 'q' !x"),
	} {
		for _, shell := range []string{"bash", "zsh"} {
			// path_helper style: brew's dirs present, but behind the system ones.
			path := "/usr/bin:/bin:" + prefix + "/bin:" + prefix + "/sbin"
			first, second := evalShellenv(t, shell, path, prefix)
			want := prefix + "/bin:" + prefix + "/sbin:/usr/bin:/bin:"
			if !strings.HasPrefix(first[0], want) {
				t.Errorf("%s: PATH = %q, want prefix %q", shell, first[0], want)
			}
			if first[1] != prefix+"/share/info:" {
				t.Errorf("%s: INFOPATH = %q", shell, first[1])
			}
			if first[2] != prefix {
				t.Errorf("%s: HOMEBREW_PREFIX = %q, want %q", shell, first[2], prefix)
			}
			if first[3] != ":/usr/share/man" {
				t.Errorf("%s: MANPATH = %q", shell, first[3])
			}
			if shell == "zsh" {
				if first[4] != prefix+"/share/zsh/site-functions" {
					t.Errorf("zsh: fpath[1] = %q", first[4])
				}
				if !strings.Contains(first[5], "export") {
					t.Errorf("zsh: FPATH not exported (%q)", first[5])
				}
			}
			if !reflect.DeepEqual(first, second) {
				t.Errorf("%s: second eval changed the environment:\n%q\n%q", shell, first, second)
			}
		}
	}
}

func TestShellenvEarlyReturn(t *testing.T) {
	p := "/opt/home brew"
	for _, sh := range []string{"bash", "zsh", "fish", "tcsh", "pwsh"} {
		if out := shellenv(sh, p+"/bin:"+p+"/sbin", p, p, p); out != "" {
			t.Errorf("%s: expected no output, got %q", sh, out)
		}
		if out := shellenv(sh, p+"/bin:"+p+"/sbin:/usr/bin", p, p, p); out != "" {
			t.Errorf("%s: expected no output, got %q", sh, out)
		}
		if out := shellenv(sh, "/usr/bin:"+p+"/bin:"+p+"/sbin", p, p, p); !strings.Contains(out, "PATH") {
			t.Errorf("%s: expected a PATH prepend, got %q", sh, out)
		}
	}
	// Upstream's output for an ordinary prefix is reproduced byte for byte.
	got := shellenv("csh", "/usr/bin", "/opt/homebrew", "/opt/homebrew/Cellar", "/opt/homebrew")
	if !strings.Contains(got, "setenv HOMEBREW_PREFIX /opt/homebrew;\n") ||
		!strings.Contains(got, `setenv PATH "/opt/homebrew/bin:/opt/homebrew/sbin:$PATH";`) ||
		!strings.Contains(got, `setenv INFOPATH "/opt/homebrew/share/info:${INFOPATH}";`) {
		t.Errorf("csh output:\n%s", got)
	}
}

func TestShellenvCsh(t *testing.T) {
	bin, err := exec.LookPath("tcsh")
	if err != nil {
		t.Skip("tcsh not available")
	}
	prefix := filepath.Join(t.TempDir(), "my pre$fix 'q' !x")
	script := shellenv("tcsh", "/usr/bin:/bin", prefix, prefix+"/Cellar", prefix)
	f := filepath.Join(t.TempDir(), "env.csh")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-f", "-c", "eval `cat \"$F\"`; printenv PATH; printenv INFOPATH; printenv HOMEBREW_PREFIX")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "F=" + f}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tcsh: %v\n%s", err, script)
	}
	want := prefix + "/bin:" + prefix + "/sbin:/usr/bin:/bin\n" + prefix + "/share/info:\n" + prefix + "\n"
	if string(out) != want {
		t.Errorf("tcsh got %q, want %q", out, want)
	}
}

func TestShellenvQuoting(t *testing.T) {
	p := `/x'y"z$w`
	if got := pwshQuote(p); got != `'/x''y"z$w'` {
		t.Errorf("pwshQuote = %s", got)
	}
	if got := fishQuote(p); got != `"/x'y\"z\$w"` {
		t.Errorf("fishQuote = %s", got)
	}
}

func TestHomebrewVersionUsesMax(t *testing.T) {
	prefix := testutil.FakePrefix(t)
	t.Setenv("MISEBREW_HOMEBREW_VERSION", "")
	receipt := func(name, v string) {
		testutil.Write(t, filepath.Join(prefix, "opt", name, "INSTALL_RECEIPT.json"), `{"homebrew_version":"`+v+`"}`)
	}
	receipt("aaa", "3.1.0")
	if got := homebrewVersion(); got != buildinfo.DefaultHomebrewVersion {
		t.Errorf("old receipt only: got %s, want %s", got, buildinfo.DefaultHomebrewVersion)
	}
	receipt("mmm", "99.1.0 (mise)")
	receipt("zzz", "4.0.0")
	if got := homebrewVersion(); got != "99.1.0" {
		t.Errorf("got %s, want 99.1.0", got)
	}
}

func TestConfigEnvValue(t *testing.T) {
	for _, c := range []struct{ k, v, want string }{
		{"HOMEBREW_GITHUB_API_TOKEN", "ghp_x", "set"},
		{"HOMEBREW_DOCKER_REGISTRY_BASIC_AUTH_TOKEN", "x", "set"},
		{"MISEBREW_SECRET", "x", "set"},
		{"HOMEBREW_SOME_API_KEY", "x", "set"},
		{"HOMEBREW_CREDENTIALS_FILE", "x", "set"},
		{"HOMEBREW_ARTIFACT_DOMAIN", "https://user:p@ss@artifacts.example.com/path?a=b", "https://artifacts.example.com/path?a=b"},
		{"HOMEBREW_BOTTLE_DOMAIN", "https://mirror.example.com", "https://mirror.example.com"},
		{"HOMEBREW_PREFIX", "/opt/homebrew", "/opt/homebrew"},
	} {
		if got := configEnvValue(c.k, c.v); got != c.want {
			t.Errorf("configEnvValue(%s, %s) = %q, want %q", c.k, c.v, got, c.want)
		}
	}
}

func TestAliasNamesConfined(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	zshrc := filepath.Join(home, ".zshrc")
	testutil.Write(t, zshrc, "keep")
	testutil.Write(t, filepath.Join(home, "x.sh"), "touch pwned")

	for _, n := range []string{"../.zshrc", "../x.sh", "a/b", ".hidden", "-x", "", "a\nb", "a b"} {
		if _, ok := aliasPath(n); ok {
			t.Errorf("aliasPath(%q) accepted", n)
		}
		if _, ok := runAlias(n, nil); ok {
			t.Errorf("runAlias(%q) ran something", n)
		}
		if v := readAlias(n); v != "" {
			t.Errorf("readAlias(%q) = %q", n, v)
		}
	}
	if !expectExit(t, func() { cmdAlias([]string{"../.zshrc=list"}) }) {
		t.Error("alias with traversal name did not fail")
	}
	if !expectExit(t, func() { cmdUnalias([]string{"../.zshrc"}) }) {
		t.Error("unalias with traversal name did not fail")
	}
	if b, _ := os.ReadFile(zshrc); string(b) != "keep" {
		t.Errorf("~/.zshrc was modified: %q", b)
	}
	if !expectExit(t, func() { cmdAlias([]string{"evil=list\ntouch pwned"}) }) {
		t.Error("multi-line alias command accepted")
	}
	if paths.FileExists(filepath.Join(aliasDir(), "evil")) {
		t.Error("multi-line alias was written")
	}

	cmdAlias([]string{"lv=list --versions"})
	if got := readAlias("lv"); got != "list --versions" {
		t.Errorf("readAlias(lv) = %q", got)
	}
	cmdUnalias([]string{"lv"})
	if paths.FileExists(filepath.Join(aliasDir(), "lv")) {
		t.Error("unalias did not remove lv")
	}
}

func TestCachePathParts(t *testing.T) {
	for _, v := range []string{"1.2.3", "1.0,abc", "latest", "2024-01-01_1", "1.0..2"} {
		if !homebrew.SafeVersion(v) {
			t.Errorf("safeVersion(%q) = false", v)
		}
	}
	for _, v := range []string{"", ".", "..", "x/../../..", `..\..`, "a/b", "a\x00b"} {
		if homebrew.SafeVersion(v) {
			t.Errorf("safeVersion(%q) = true", v)
		}
	}
	if err := checkPathParts("gtk+3", "3.24.0"); err != nil {
		t.Error(err)
	}
	if checkPathParts("..", "1.0") == nil || checkPathParts("foo", "x/../../..") == nil || checkPathParts("a/b", "1") == nil {
		t.Error("checkPathParts accepted an unsafe name or version")
	}
}

func TestCaskSHAIgnoresCoreForTapCask(t *testing.T) {
	// A third-party tap cask must never be verified against (or claim) the
	// core cask's checksum just because the tokens match.
	c := &homebrew.Cask{Token: "firefox", Tap: "someone/tap"}
	if got := caskSHA(c); got != "" {
		t.Errorf("caskSHA(tap cask) = %q", got)
	}
}

func TestBottleDownloadPathForeignTag(t *testing.T) {
	t.Setenv("MISE_CACHE_DIR", t.TempDir())
	t.Setenv("MISEBREW_BOTTLE_TAG", "imaginary_tag")
	f := &homebrew.Formula{Name: "foo"}
	f.Versions.Stable = "1.0"
	f.Bottle.Stable.Files = map[string]struct {
		Cellar string `json:"cellar"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	}{"imaginary_tag": {URL: "https://example.invalid/foo"}}
	tag, _, _, ok := bottleFileFor(f)
	if !ok || tag != "imaginary_tag" {
		t.Fatalf("bottleFileFor = %q, %v", tag, ok)
	}
	got := bottleDownloadPath(f, tag)
	want := filepath.Join(paths.MiseBrewCacheDir(), "downloads", "foo--1.0.imaginary_tag.bottle.tar.gz")
	if got != want || got == bottleCachePath(f) {
		t.Errorf("bottleDownloadPath = %q, want %q", got, want)
	}
	if os.Getenv("MISEBREW_BOTTLE_TAG") != "imaginary_tag" {
		t.Error("nativeBottleTag did not restore MISEBREW_BOTTLE_TAG")
	}
}

func TestDownloadTempFile(t *testing.T) {
	body := []byte("hello bottle")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(body) }))
	defer srv.Close()
	dir := t.TempDir()
	dst := filepath.Join(dir, "x.tar.gz")
	sum := sha256.Sum256(body)
	if err := download(srv.URL, dst, hex.EncodeToString(sum[:]), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != string(body) {
		t.Errorf("dst = %q", b)
	}
	if err := download(srv.URL, filepath.Join(dir, "bad"), strings.Repeat("0", 64), false); err == nil {
		t.Error("checksum mismatch not reported")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover files: %v", names)
	}
}

func TestEditorCommand(t *testing.T) {
	for _, k := range []string{"HOMEBREW_EDITOR", "HOMEBREW_VISUAL", "VISUAL", "EDITOR"} {
		t.Setenv(k, "")
	}
	t.Setenv("HOMEBREW_EDITOR", "   ")
	t.Setenv("EDITOR", "nano")
	t.Setenv("VISUAL", "code --wait")
	if got := editorCommand(); !reflect.DeepEqual(got, []string{"code", "--wait"}) {
		t.Errorf("editorCommand = %q", got)
	}
	t.Setenv("HOMEBREW_EDITOR", `"/Applications/Sublime Text.app/bin/subl" -w`)
	if got := editorCommand(); !reflect.DeepEqual(got, []string{"/Applications/Sublime Text.app/bin/subl", "-w"}) {
		t.Errorf("editorCommand = %q", got)
	}
	for _, k := range []string{"HOMEBREW_EDITOR", "VISUAL", "EDITOR"} {
		t.Setenv(k, " \t ")
	}
	if got := editorCommand(); len(got) != 1 || got[0] == "" {
		t.Errorf("fallback editorCommand = %q", got)
	}
	for in, want := range map[string][]string{
		`a\ b 'c d' "e\"f" g`: {"a b", "c d", `e"f`, "g"},
		`''`:                  {""},
		"  ":                  nil,
	} {
		if got := shellSplit(in); !reflect.DeepEqual(got, want) {
			t.Errorf("shellSplit(%q) = %q, want %q", in, got, want)
		}
	}
}
