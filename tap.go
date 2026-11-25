package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Third-party taps have no formulae.brew.sh API, so mise-brew reads them from
// GitHub: the repository tree lists formulae/casks, and each Ruby file is parsed
// for the metadata Homebrew shows. Everything is cached under
// <mise cache>/mise-brew/taps and refreshed by `brew update`.

type tapIndex struct {
	Fetched  time.Time         `json:"fetched"`
	Owner    string            `json:"owner"`
	Repo     string            `json:"repo"`
	Formulae map[string]string `json:"formulae"` // name -> path in repo
	Casks    map[string]string `json:"casks"`
}

const tapTTL = 24 * time.Hour

var tapIndexCache = map[string]*tapIndex{}
var tapRefresh bool // set by `brew update`

func githubToken() string {
	for _, k := range []string{"HOMEBREW_GITHUB_API_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

func httpGet(url string, accept string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mise-brew/"+miseBrewVersion)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if strings.HasPrefix(url, "https://api.github.com/") {
		if t := githubToken(); t != "" {
			req.Header.Set("Authorization", "Bearer "+t)
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return body, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

var githubURLRe = regexp.MustCompile(`github\.com[/:]([^/]+)/([^/.]+?)(?:\.git)?/?$`)

// tapRepo returns the GitHub owner/repo backing a tap.
func tapRepo(tap string) (string, string) {
	tap = strings.ToLower(tap)
	if u := configuredTapURLs()[tap]; u != "" {
		if m := githubURLRe.FindStringSubmatch(u); m != nil {
			return m[1], m[2]
		}
	}
	parts := strings.SplitN(tap, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], "homebrew-" + strings.TrimPrefix(parts[1], "homebrew-")
}

var tapURLCache map[string]string

func configuredTapURLs() map[string]string {
	if tapURLCache == nil {
		tapURLCache = configuredTapsFromFiles()
	}
	return tapURLCache
}

func loadTapIndex(tap string) *tapIndex {
	tap = strings.ToLower(tap)
	if ti, ok := tapIndexCache[tap]; ok {
		return ti
	}
	owner, repo := tapRepo(tap)
	if owner == "" {
		return nil
	}
	cacheFile := filepath.Join(miseBrewCacheDir(), "taps", owner, repo+".json")
	var ti tapIndex
	if data, err := os.ReadFile(cacheFile); err == nil && json.Unmarshal(data, &ti) == nil && !tapRefresh && time.Since(ti.Fetched) < tapTTL {
		tapIndexCache[tap] = &ti
		return &ti
	}
	body, err := httpGet(fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/HEAD?recursive=1", owner, repo), "application/vnd.github+json")
	if err != nil {
		odebug("tap %s: %v", tap, err)
		if ti.Formulae != nil { // stale cache beats nothing
			tapIndexCache[tap] = &ti
			return &ti
		}
		tapIndexCache[tap] = nil
		return nil
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if json.Unmarshal(body, &tree) != nil {
		return nil
	}
	ti = tapIndex{Fetched: time.Now(), Owner: owner, Repo: repo, Formulae: map[string]string{}, Casks: map[string]string{}}
	for _, e := range tree.Tree {
		if e.Type != "blob" || !strings.HasSuffix(e.Path, ".rb") {
			continue
		}
		name := strings.TrimSuffix(path.Base(e.Path), ".rb")
		switch {
		case strings.HasPrefix(e.Path, "Casks/"):
			ti.Casks[name] = e.Path
		case strings.HasPrefix(e.Path, "Formula/") || strings.HasPrefix(e.Path, "HomebrewFormula/") || !strings.Contains(e.Path, "/"):
			ti.Formulae[name] = e.Path
		}
	}
	_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
	if data, err := json.Marshal(ti); err == nil {
		_ = os.WriteFile(cacheFile, data, 0o644)
	}
	tapIndexCache[tap] = &ti
	return &ti
}

// tapSource returns the Ruby source of a tap formula or cask.
func tapSource(tap, relPath string) (string, error) {
	ti := loadTapIndex(tap)
	if ti == nil {
		return "", fmt.Errorf("tap %s is not available", tap)
	}
	cacheFile := filepath.Join(miseBrewCacheDir(), "taps", ti.Owner, ti.Repo, relPath)
	if fi, err := os.Stat(cacheFile); err == nil && !tapRefresh && time.Since(fi.ModTime()) < tapTTL {
		b, err := os.ReadFile(cacheFile)
		return string(b), err
	}
	body, err := httpGet(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", ti.Owner, ti.Repo, relPath), "")
	if err != nil {
		if b, rerr := os.ReadFile(cacheFile); rerr == nil {
			return string(b), nil
		}
		return "", err
	}
	_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
	_ = os.WriteFile(cacheFile, body, 0o644)
	return string(body), nil
}

var (
	rbStr       = `"((?:[^"\\]|\\.)*)"`
	rbDescRe    = regexp.MustCompile(`(?m)^\s*desc\s+` + rbStr)
	rbHomeRe    = regexp.MustCompile(`(?m)^\s*homepage\s+` + rbStr)
	rbURLRe     = regexp.MustCompile(`(?m)^\s*url\s+` + rbStr)
	rbHeadRe    = regexp.MustCompile(`(?m)^\s*head\s+` + rbStr)
	rbVersionRe = regexp.MustCompile(`(?m)^\s*version\s+` + rbStr)
	rbRevRe     = regexp.MustCompile(`(?m)^\s*revision\s+(\d+)`)
	rbSchemeRe  = regexp.MustCompile(`(?m)^\s*version_scheme\s+(\d+)`)
	rbLicenseRe = regexp.MustCompile(`(?m)^\s*license\s+(?:` + rbStr + `|:(\w+))`)
	rbDepRe     = regexp.MustCompile(`(?m)^\s*depends_on\s+` + rbStr + `(?:\s*=>\s*(\[[^\]]*\]|:\w+))?`)
	rbKegOnlyRe = regexp.MustCompile(`(?m)^\s*keg_only\b`)
	rbServiceRe = regexp.MustCompile(`(?m)^\s*service\s+do\b`)
	rbCaveatRe  = regexp.MustCompile(`(?s)def caveats\s*(?:<<~|<<-)?\s*(?:<<~EOS|<<-EOS|<<EOS)?\s*\n(.*?)\n\s*EOS`)
	rbNameRe    = regexp.MustCompile(`(?m)^\s*name\s+` + rbStr)
	rbAppRe     = regexp.MustCompile(`(?m)^\s*app\s+` + rbStr)
	rbBinaryRe  = regexp.MustCompile(`(?m)^\s*binary\s+` + rbStr)
	rbCaskDepRe = regexp.MustCompile(`(?m)^\s*depends_on\s+formula:\s*` + rbStr)
	urlVerRe    = regexp.MustCompile(`(?i)[-_/v]v?(\d+(?:\.\d+)+(?:[-_.]?(?:alpha|beta|rc|pre|p)\d*)?)(?:[-_.](?:src|source|orig|tar|tgz|zip|darwin|macos|linux|amd64|arm64|x86_64|all))*\.(?:tar\.(?:gz|bz2|xz|zst)|tgz|tbz2?|txz|zip|dmg|pkg|gem|jar)`)
)

func rbUnquote(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// versionFromURL approximates Homebrew's Version.parse for common archive names.
func versionFromURL(u string) string {
	if m := urlVerRe.FindAllStringSubmatch(u, -1); len(m) > 0 {
		return m[len(m)-1][1]
	}
	if i := strings.LastIndex(u, "/"); i >= 0 {
		if m := regexp.MustCompile(`(\d+(?:\.\d+)+)`).FindAllString(u[i:], -1); len(m) > 0 {
			return m[len(m)-1]
		}
	}
	return ""
}

func parseTapFormula(src, tap, name, relPath string) *Formula {
	f := &Formula{Name: name, FullName: tap + "/" + name, Tap: tap, RubySourcePath: relPath}
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(src); m != nil {
			return rbUnquote(m[1])
		}
		return ""
	}
	f.Desc = get(rbDescRe)
	f.Homepage = get(rbHomeRe)
	f.URLs.Stable.URL = get(rbURLRe)
	f.Versions.Head = get(rbHeadRe)
	f.Versions.Stable = get(rbVersionRe)
	if f.Versions.Stable == "" {
		f.Versions.Stable = versionFromURL(f.URLs.Stable.URL)
	}
	if m := rbRevRe.FindStringSubmatch(src); m != nil {
		f.Revision, _ = strconv.Atoi(m[1])
	}
	if m := rbSchemeRe.FindStringSubmatch(src); m != nil {
		f.VersionScheme, _ = strconv.Atoi(m[1])
	}
	if m := rbLicenseRe.FindStringSubmatch(src); m != nil {
		f.License = m[1]
		if f.License == "" {
			f.License = m[2]
		}
	}
	for _, m := range rbDepRe.FindAllStringSubmatch(src, -1) {
		dep, typ := m[1], m[2]
		switch {
		case strings.Contains(typ, "build"):
			f.BuildDependencies = append(f.BuildDependencies, dep)
		case strings.Contains(typ, "test"):
			f.TestDependencies = append(f.TestDependencies, dep)
		case strings.Contains(typ, "optional"):
			f.OptionalDependencies = append(f.OptionalDependencies, dep)
		case strings.Contains(typ, "recommended"):
			f.RecommendedDependencies = append(f.RecommendedDependencies, dep)
		default:
			f.Dependencies = append(f.Dependencies, dep)
		}
	}
	f.KegOnly = rbKegOnlyRe.MatchString(src)
	if rbServiceRe.MatchString(src) {
		f.Service = json.RawMessage(`{}`)
	}
	if m := rbCaveatRe.FindStringSubmatch(src); m != nil {
		c := dedent(m[1])
		f.Caveats = &c
	}
	return f
}

func parseTapCask(src, tap, token, relPath string) *Cask {
	c := &Cask{Token: token, FullToken: tap + "/" + token, Tap: tap, RubySourcePath: relPath}
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(src); m != nil {
			return rbUnquote(m[1])
		}
		return ""
	}
	c.Version = get(rbVersionRe)
	if strings.Contains(src, "version :latest") {
		c.Version = "latest"
	}
	c.Homepage = get(rbHomeRe)
	c.URL = get(rbURLRe)
	if d := get(rbDescRe); d != "" {
		c.Desc = &d
	}
	for _, m := range rbNameRe.FindAllStringSubmatch(src, -1) {
		c.Name = append(c.Name, rbUnquote(m[1]))
	}
	for _, m := range rbAppRe.FindAllStringSubmatch(src, -1) {
		b, _ := json.Marshal(map[string][]string{"app": {rbUnquote(m[1])}})
		c.Artifacts = append(c.Artifacts, b)
	}
	for _, m := range rbBinaryRe.FindAllStringSubmatch(src, -1) {
		b, _ := json.Marshal(map[string][]string{"binary": {rbUnquote(m[1])}})
		c.Artifacts = append(c.Artifacts, b)
	}
	for _, m := range rbCaskDepRe.FindAllStringSubmatch(src, -1) {
		c.DependsOn.Formula = append(c.DependsOn.Formula, rbUnquote(m[1]))
	}
	if strings.Contains(src, "auto_updates true") {
		t := true
		c.AutoUpdates = &t
	}
	return c
}

func dedent(s string) string {
	lines := strings.Split(s, "\n")
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if min < 0 || n < min {
			min = n
		}
	}
	for i, l := range lines {
		if len(l) >= min && min > 0 {
			lines[i] = l[min:]
		}
	}
	return strings.Join(lines, "\n")
}

func splitTapName(full string) (tap, name string, ok bool) {
	full = strings.ToLower(full)
	if strings.Count(full, "/") != 2 {
		return "", "", false
	}
	i := strings.LastIndex(full, "/")
	return full[:i], full[i+1:], true
}

var tapFormulaCache = map[string]*Formula{}

func lookupTapFormula(full string) *Formula {
	if f, ok := tapFormulaCache[full]; ok {
		return f
	}
	tap, name, ok := splitTapName(full)
	if !ok {
		return nil
	}
	var f *Formula
	if ti := loadTapIndex(tap); ti != nil {
		if p, ok := ti.Formulae[name]; ok {
			if src, err := tapSource(tap, p); err == nil {
				f = parseTapFormula(src, tap, name, p)
			}
		}
	}
	tapFormulaCache[full] = f
	return f
}

func lookupTapCask(full string) *Cask {
	tap, token, ok := splitTapName(full)
	if !ok {
		return nil
	}
	if ti := loadTapIndex(tap); ti != nil {
		if p, ok := ti.Casks[token]; ok {
			if src, err := tapSource(tap, p); err == nil {
				return parseTapCask(src, tap, token, p)
			}
		}
	}
	return nil
}

// installedTapOf returns the third-party tap an installed formula came from.
func installedTapOf(name string) string {
	if !isFormulaInstalled(name) {
		return ""
	}
	if r := readReceipt(kegPath(name, currentVersion(name))); r != nil {
		t := strings.ToLower(r.Source.Tap)
		if t != "" && t != "homebrew/core" {
			return t
		}
	}
	return ""
}

// tapNames lists formula and cask names (fully qualified) of all configured taps.
func tapNames() (formulae, casks []string) {
	for tap := range configuredTaps() {
		ti := loadTapIndex(tap)
		if ti == nil {
			continue
		}
		for n := range ti.Formulae {
			formulae = append(formulae, tap+"/"+n)
		}
		for n := range ti.Casks {
			casks = append(casks, tap+"/"+n)
		}
	}
	sort.Strings(formulae)
	sort.Strings(casks)
	return
}
