package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/testutil"
)

// fakeAPI points mise-brew at a temporary mise cache holding the given API
// records, with no configured taps, and resets every lookup cache.
func fakeAPI(t *testing.T, formulae, casks []string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MISE_CACHE_DIR", dir)
	testutil.Write(t, filepath.Join(paths.ApiDir(), "formula.json"), "["+strings.Join(formulae, ",")+"]")
	testutil.Write(t, filepath.Join(paths.ApiDir(), "cask.json"), "["+strings.Join(casks, ",")+"]")
	reset := func() {
		homebrew.FormulaAPI, homebrew.CaskAPI = &homebrew.ApiSource{Kind: "formula"}, &homebrew.ApiSource{Kind: "cask"}
		homebrew.FormulaCache, homebrew.CaskCache, homebrew.TapFormulaCache = map[string]*homebrew.Formula{}, map[string]*homebrew.Cask{}, map[string]*homebrew.Formula{}
		homebrew.TapIndexCache = map[string]*homebrew.TapIndex{}
		homebrew.AliasIndex, homebrew.CaskOldTokenIndex, homebrew.TapURLCache = nil, nil, map[string]string{}
	}
	reset()
	t.Cleanup(func() { reset(); homebrew.TapURLCache = nil })
	return dir
}

// fakeTap caches a tap index and its Ruby sources (fresh, so nothing is fetched).
func fakeTap(t *testing.T, tap string, files map[string]string) {
	t.Helper()
	owner, repo := homebrew.TapRepo(tap)
	ti := homebrew.TapIndex{Fetched: time.Now(), Owner: owner, Repo: repo, Formulae: map[string]string{}, Casks: map[string]string{}}
	for p, src := range files {
		name := strings.TrimSuffix(filepath.Base(p), ".rb")
		if strings.HasPrefix(p, "Casks/") {
			ti.Casks[name] = p
		} else {
			ti.Formulae[name] = p
		}
		testutil.Write(t, filepath.Join(paths.MiseBrewCacheDir(), "taps", owner, repo, p), src)
	}
	b, _ := json.Marshal(ti)
	testutil.Write(t, filepath.Join(paths.MiseBrewCacheDir(), "taps", owner, repo+".json"), string(b))
}

// captureRun runs a brew command and returns its stdout and exit code.
func captureRun(t *testing.T, args ...string) (string, int) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	code := run(args)
	w.Close()
	os.Stdout = old
	return <-done, code
}

func depsFixture(t *testing.T) {
	testutil.FakePrefix(t)
	fakeAPI(t, []string{
		`{"name":"zlib","full_name":"zlib","tap":"homebrew/core","dependencies":[],"versions":{"stable":"1.3"}}`,
		`{"name":"pcre2","full_name":"pcre2","tap":"homebrew/core","dependencies":[],"versions":{"stable":"10"}}`,
		`{"name":"git","full_name":"git","tap":"homebrew/core","dependencies":["pcre2"],"versions":{"stable":"2.50"}}`,
	}, []string{
		`{"token":"app","full_token":"app","tap":"homebrew/cask","version":"1","depends_on":{"formula":["git"]}}`,
	})
	fakeTap(t, "acme/tools", map[string]string{
		"Formula/widget.rb": "class Widget < Formula\n  url \"https://x/widget-1.0.tar.gz\"\n  depends_on \"lib\"\nend\n",
		"Formula/lib.rb":    "class Lib < Formula\n  url \"https://x/lib-1.0.tar.gz\"\n  depends_on \"zlib\"\nend\n",
		"Formula/git.rb":    "class Git < Formula\n  url \"https://x/git-9.0.tar.gz\"\n  depends_on \"zlib\"\nend\n",
		"Casks/thing.rb":    "cask \"thing\" do\n  version \"1.0\"\n  url \"https://x/thing-#{version}.dmg\"\n  depends_on formula: \"acme/tools/widget\"\nend\n",
	})
}

func TestDepsTapCask(t *testing.T) { // finding 1
	depsFixture(t)
	out, code := captureRun(t, "deps", "acme/tools/thing")
	if code != 0 || out != "acme/tools/lib\nacme/tools/widget\nzlib\n" {
		t.Errorf("deps tap cask: code=%d out=%q", code, out)
	}
	if c := homebrew.LookupCask("acme/tools/thing"); c == nil || c.URL != "https://x/thing-1.0.dmg" {
		t.Errorf("tap cask url not expanded: %+v", c)
	}
}

func TestDepsTapFormula(t *testing.T) { // finding 2
	depsFixture(t)
	if out, code := captureRun(t, "deps", "acme/tools/widget"); code != 0 || out != "acme/tools/lib\nzlib\n" {
		t.Errorf("deps acme/tools/widget: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "deps", "acme/tools/git"); out != "zlib\n" {
		t.Errorf("deps acme/tools/git must not use core git: %q", out)
	}
	if out, _ := captureRun(t, "deps", "git"); out != "pcre2\n" {
		t.Errorf("deps git: %q", out)
	}
	if out, _ := captureRun(t, "deps", "--tree", "acme/tools/widget"); !strings.Contains(out, "acme/tools/widget\n└── acme/tools/lib\n    └── zlib") {
		t.Errorf("deps --tree: %q", out)
	}
}

func TestDepsGraphCask(t *testing.T) { // findings 6, 12
	depsFixture(t)
	out, _ := captureRun(t, "deps", "--dot", "--cask", "app")
	if !strings.Contains(out, `"app" -> "git"`) || !strings.Contains(out, `"git" -> "pcre2"`) {
		t.Errorf("cask graph: %q", out)
	}
	if got := dotQuote(`a"b\c`); got != `"a\"b\\c"` {
		t.Errorf("dotQuote = %s", got)
	}
	if got := jsString("</script><b>&"); strings.ContainsAny(got, "<>&") {
		t.Errorf("jsString = %s", got)
	}
}

func TestInfoJSONTapFormula(t *testing.T) { // finding 3
	depsFixture(t)
	out, code := captureRun(t, "info", "--json=v2", "acme/tools/git")
	var v struct {
		Formulae []map[string]any `json:"formulae"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || len(v.Formulae) != 1 {
		t.Fatalf("code=%d err=%v out=%s", code, err, out)
	}
	f := v.Formulae[0]
	if f["full_name"] != "acme/tools/git" || f["tap"] != "acme/tools" || f["versions"].(map[string]any)["stable"] != "9.0" {
		t.Errorf("tap formula JSON: %v", f)
	}
	if deps, _ := f["dependencies"].([]any); len(deps) != 1 || deps[0] != "zlib" {
		t.Errorf("dependencies = %v", f["dependencies"])
	}
	if b, _ := f["build_dependencies"].([]any); b == nil {
		t.Errorf("build_dependencies should be [] not %v", f["build_dependencies"])
	}
}

func TestCaskVariations(t *testing.T) { // finding 5
	t.Setenv("MISEBREW_BOTTLE_TAG", "sonoma")
	rec := `{"token":"x","version":"2.0","url":"https://x/arm.dmg","sha256":"aa",` +
		`"artifacts":[{"app":["X.app"]},{"uninstall":[{"quit":"x.arm"}]}],` +
		`"variations":{"sonoma":{"url":"https://x/intel.dmg","sha256":"bb","artifacts":[{"app":["X.app"]},{"uninstall":[{"quit":"x.intel"}]}]},"arm64_sonoma":{"url":"nope"}}}`
	var c homebrew.Cask
	if err := json.Unmarshal(homebrew.MergeVariationsRaw(json.RawMessage(rec)), &c); err != nil {
		t.Fatal(err)
	}
	if c.URL != "https://x/intel.dmg" || c.SHA256 != "bb" || c.Version != "2.0" || !strings.Contains(string(c.Artifacts[1]), "x.intel") {
		t.Errorf("variation not applied: %+v", c)
	}
	fakeAPI(t, nil, []string{rec})
	if c := homebrew.LookupCask("x"); c == nil || c.URL != "https://x/intel.dmg" {
		t.Errorf("lookupCask ignores variations: %+v", c)
	}
	if m := homebrew.RawCaskMap("x"); m == nil || m["url"] != "https://x/intel.dmg" || m["variations"] != nil {
		t.Errorf("rawCaskMap = %v", m)
	}
	t.Setenv("MISEBREW_BOTTLE_TAG", "arm64_golden_gate")
	if out := homebrew.MergeVariationsRaw(json.RawMessage(rec)); !strings.Contains(string(out), "arm.dmg") || strings.Contains(string(out), "variations") {
		t.Errorf("no matching variation: %s", out)
	}
}

func TestCaskOldTokens(t *testing.T) { // finding 4
	fakeAPI(t, nil, []string{`{"token":"new","old_tokens":["old"],"version":"1"}`})
	if c := homebrew.LookupCask("old"); c == nil || c.Token != "new" {
		t.Errorf("old token: %+v", c)
	}
	if homebrew.LookupCask("missing") != nil {
		t.Error("unknown token resolved")
	}
}

func receiptWithDeps(t *testing.T, name, version string, deps ...string) {
	t.Helper()
	var rd []homebrew.RuntimeDep
	for _, d := range deps {
		rd = append(rd, homebrew.RuntimeDep{FullName: d, Version: "1", PkgVersion: "1_2", Declared: true})
	}
	b, _ := json.Marshal(homebrew.Receipt{RuntimeDependencies: rd})
	testutil.Write(t, filepath.Join(homebrew.KegPath(name, version), "INSTALL_RECEIPT.json"), string(b))
}

func TestMissing(t *testing.T) { // finding 7
	testutil.FakePrefix(t)
	receiptWithDeps(t, "a", "1", "b", "c")
	receiptWithDeps(t, "b", "1")
	if out, code := captureRun(t, "missing", "a"); code != 1 || out != "c\n" {
		t.Errorf("missing a: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "missing", "--hide=b", "a"); out != "b c\n" {
		t.Errorf("missing --hide=b a: %q", out)
	}
	if out, _ := captureRun(t, "missing"); out != "a: c\n" {
		t.Errorf("missing (all): %q", out)
	}
}

func TestUsesUnknownAndCask(t *testing.T) { // finding 8
	depsFixture(t)
	if out, code := captureRun(t, "uses", "nosuchformula"); code != 0 || out != "" {
		t.Errorf("uses unknown: code=%d out=%q", code, out)
	}
	if out, _ := captureRun(t, "uses", "--cask", "git"); out != "app\n" {
		t.Errorf("uses --cask git: %q", out)
	}
	if out, _ := captureRun(t, "uses", "git"); out != "app\n" {
		t.Errorf("uses git: %q", out)
	}
	if out, _ := captureRun(t, "uses", "pcre2"); out != "git\n" {
		t.Errorf("uses pcre2: %q", out)
	}
}

func TestRawIndexBounds(t *testing.T) { // finding 11
	fakeAPI(t, []string{`{"name":"a"}`, `{"name":"b"}`}, nil)
	data, _ := os.ReadFile(homebrew.FormulaAPI.JsonPath())
	size := int64(len(data))
	off := int64(strings.Index(string(data), `{"name":"b"}`))
	for _, loc := range [][2]int64{{-1, 5}, {0, 0}, {size - 2, 10}, {0, 1 << 40}, {1 << 62, 1 << 62}, {off, 5}} {
		idx, _ := json.Marshal(map[string]any{"source_size": size, "formulae": map[string][2]int64{"a": {1, 12}, "b": loc}})
		testutil.Write(t, homebrew.FormulaAPI.IndexPath(), string(idx))
		homebrew.FormulaAPI = &homebrew.ApiSource{Kind: "formula"}
		raw, ok := homebrew.FormulaAPI.Raw("b")
		if !ok || string(raw) != `{"name":"b"}` {
			t.Errorf("loc %v: got %q %v", loc, raw, ok)
		}
	}
}

func TestTapRepo(t *testing.T) { // finding 13
	fakeAPI(t, nil, nil)
	homebrew.TapURLCache = map[string]string{
		"a/dots":   "https://github.com/a/homebrew-my.tools.git",
		"a/ssh":    "git@github.com:a/b.git",
		"a/gitlab": "https://gitlab.com/a/homebrew-x",
		"a/sneaky": "https://evil.example/github.com/a/x",
	}
	cases := map[string]string{"a/dots": "a/homebrew-my.tools", "a/ssh": "a/b", "a/gitlab": "/", "a/sneaky": "/", "a/plain": "a/homebrew-plain"}
	for tap, want := range cases {
		if o, r := homebrew.TapRepo(tap); o+"/"+r != want {
			t.Errorf("tapRepo(%s) = %s/%s, want %s", tap, o, r, want)
		}
	}
	if homebrew.LoadTapIndex("a/gitlab") != nil {
		t.Error("non-GitHub tap must not be read from github.com")
	}
}

const goreleaserFormula = `# typed: false
class Tool < Formula
  desc "A tool"
  homepage "https://example.com"
  version "1.2.3"

  on_macos do
    if Hardware::CPU.intel?
      url "https://x/tool_darwin_amd64.tar.gz"
      sha256 "1111111111111111111111111111111111111111111111111111111111111111"
    end
    on_arm do
      url "https://x/tool_darwin_arm64.tar.gz"
      sha256 "2222222222222222222222222222222222222222222222222222222222222222"
    end
    depends_on "macdep"
  end

  on_linux do
    on_intel do
      url "https://x/tool_linux_amd64.tar.gz"
      sha256 "3333333333333333333333333333333333333333333333333333333333333333"
    end
    depends_on "glibc"
  end

  on_sonoma :or_newer do
    depends_on "newdep"
  end

  head do
    url "https://x/tool.git"
    depends_on "go" => :build
  end

  depends_on "../../etc"
  depends_on "acme/tools/lib"

  resource "extra" do
    url "https://x/extra.tar.gz"
  end

  def install
    bin.install "tool"
  end
end
`

func TestParseTapPlatformBlocks(t *testing.T) { // findings 14b, 14c
	cases := []struct {
		tag, url, sha string
		deps          string
	}{
		{"arm64_sonoma", "https://x/tool_darwin_arm64.tar.gz", "2222", "macdep,newdep,acme/tools/lib"},
		{"ventura", "https://x/tool_darwin_amd64.tar.gz", "1111", "macdep,acme/tools/lib"},
		{"x86_64_linux", "https://x/tool_linux_amd64.tar.gz", "3333", "glibc,acme/tools/lib"},
	}
	for _, c := range cases {
		t.Setenv("MISEBREW_BOTTLE_TAG", c.tag)
		f := homebrew.ParseTapFormula(goreleaserFormula, "acme/tools", "tool", "Formula/tool.rb")
		if f.URLs.Stable.URL != c.url || !strings.HasPrefix(f.URLs.Stable.Checksum, c.sha) {
			t.Errorf("%s: url=%q sha=%q", c.tag, f.URLs.Stable.URL, f.URLs.Stable.Checksum)
		}
		if got := strings.Join(f.Dependencies, ","); got != c.deps || len(f.BuildDependencies) != 0 {
			t.Errorf("%s: deps=%s build=%v", c.tag, got, f.BuildDependencies)
		}
		if f.Desc != "A tool" || f.Versions.Stable != "1.2.3" {
			t.Errorf("%s: desc/version %q %q", c.tag, f.Desc, f.Versions.Stable)
		}
	}
}

func TestRuntimeDepRevision(t *testing.T) { // finding 9
	depsFixture(t)
	receiptWithDeps(t, "git", "2.50", "pcre2")
	out, _ := captureRun(t, "info", "--json=v2", "git")
	if !strings.Contains(out, `"revision": 2`) {
		t.Errorf("runtime_dependencies revision not derived from pkg_version: %s", out)
	}
}
