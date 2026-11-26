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

// tapRetry is how long a stale cache is reused after a failed refresh before
// the network is tried again; tapFetchTimeout bounds tap metadata requests.
const (
	tapRetry        = time.Hour
	tapFetchTimeout = 10 * time.Second
)

var tapIndexCache = map[string]*tapIndex{}
var tapRefresh bool // set by `brew update`

// githubTokenSource returns the environment variable holding a GitHub token and its value.
func githubTokenSource() (string, string) {
	for _, k := range []string{"HOMEBREW_GITHUB_API_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		if v := os.Getenv(k); v != "" {
			return k, v
		}
	}
	return "", ""
}

func githubToken() string {
	_, v := githubTokenSource()
	return v
}

func httpGet(url string, accept string) ([]byte, error) {
	return httpGetWith(url, accept, 30*time.Second)
}

var githubWarned bool

// warnGitHubOnce reports a GitHub API authentication/rate-limit problem once per process.
func warnGitHubOnce(format string, a ...any) {
	if githubWarned {
		return
	}
	githubWarned = true
	opoo(format, a...)
}

// httpGetWith fetches url. api.github.com requests carry a GitHub token when one
// is set; an invalid token (HTTP 401) is retried once anonymously, since public
// endpoints work without one.
func httpGetWith(url, accept string, timeout time.Duration) ([]byte, error) {
	api := strings.HasPrefix(url, "https://api.github.com/")
	tokVar, tok := "", ""
	if api {
		tokVar, tok = githubTokenSource()
	}
	body, resp, err := httpDo(url, accept, tok, timeout)
	if err == nil && resp.StatusCode == 401 && tok != "" {
		body, resp, err = httpDo(url, accept, "", timeout)
		if err == nil && resp.StatusCode == 200 {
			warnGitHubOnce("GitHub rejected the token in %s (HTTP 401); continuing without authentication.\nSet HOMEBREW_GITHUB_API_TOKEN to a valid token.", tokVar)
		}
	}
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		if api {
			switch {
			case resp.StatusCode == 401:
				warnGitHubOnce("GitHub API request failed (HTTP 401: bad credentials).\nSet HOMEBREW_GITHUB_API_TOKEN to a valid token.")
			case resp.StatusCode == 429 || (resp.StatusCode == 403 && resp.Header.Get("X-RateLimit-Remaining") == "0"):
				warnGitHubOnce("GitHub API rate limit exceeded; third-party tap data may be stale or missing.\nSet HOMEBREW_GITHUB_API_TOKEN to raise the limit.")
			}
		}
		return body, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return body, nil
}

func httpDo(url, accept, token string, timeout time.Duration) ([]byte, *http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "mise-brew/"+miseBrewVersion)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return body, resp, nil
}

// githubURLRe matches GitHub remotes (https or ssh); the repo may contain dots.
var githubURLRe = regexp.MustCompile(`^(?:https?://(?:[^@/]+@)?(?:www\.)?|ssh://git@|git@)github\.com[/:]([^/]+)/([^/]+?)(?:\.git)?/?$`)

var nonGitHubWarned = map[string]bool{}

// tapRepo returns the GitHub owner/repo backing a tap, or "" when the tap's
// remote is not on GitHub (mise-brew can only read taps through GitHub).
func tapRepo(tap string) (string, string) {
	tap = strings.ToLower(tap)
	if u := configuredTapURLs()[tap]; u != "" {
		m := githubURLRe.FindStringSubmatch(strings.TrimSpace(u))
		if m == nil || !validName(m[1]) || !validName(m[2]) {
			if !nonGitHubWarned[tap] {
				nonGitHubWarned[tap] = true
				opoo("Tap %s has a non-GitHub remote (%s); mise-brew can only read third-party taps hosted on GitHub.", tap, u)
			}
			return "", ""
		}
		return m[1], m[2]
	}
	parts := strings.SplitN(tap, "/", 2)
	if len(parts) != 2 || !validName(parts[0]) || !validName(parts[1]) {
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
	cached := false
	if data, err := os.ReadFile(cacheFile); err == nil && json.Unmarshal(data, &ti) == nil {
		cached = ti.Formulae != nil
	}
	if cached && !tapRefresh && time.Since(ti.Fetched) < tapTTL {
		tapIndexCache[tap] = &ti
		return &ti
	}
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	body, err := httpGetWith(fmt.Sprintf("https://api.github.com/repos/%s/%s/git/trees/HEAD?recursive=1", owner, repo), "application/vnd.github+json", tapFetchTimeout)
	if err == nil {
		err = json.Unmarshal(body, &tree)
	}
	if err != nil {
		odebug("tap %s: %v", tap, err)
		if cached {
			// A stale cache beats nothing; back off instead of re-hitting the
			// network on every command.
			ti.Fetched = time.Now().Add(tapRetry - tapTTL)
			if data, err := json.Marshal(ti); err == nil {
				_ = os.WriteFile(cacheFile, data, 0o644)
			}
			tapIndexCache[tap] = &ti
			return &ti
		}
		tapIndexCache[tap] = nil
		return nil
	}
	ti = tapIndex{Fetched: time.Now(), Owner: owner, Repo: repo, Formulae: map[string]string{}, Casks: map[string]string{}}
	for _, e := range tree.Tree {
		if e.Type != "blob" || !strings.HasSuffix(e.Path, ".rb") {
			continue
		}
		name := strings.TrimSuffix(path.Base(e.Path), ".rb")
		if !validName(name) || strings.Contains(e.Path, "..") {
			continue
		}
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
	body, err := httpGetWith(fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/HEAD/%s", ti.Owner, ti.Repo, relPath), "", tapFetchTimeout)
	if err != nil {
		if b, rerr := os.ReadFile(cacheFile); rerr == nil {
			odebug("tap %s: %v (using cached %s)", tap, err, relPath)
			back := time.Now().Add(tapRetry - tapTTL)
			_ = os.Chtimes(cacheFile, back, back)
			return string(b), nil
		}
		return "", err
	}
	_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
	_ = os.WriteFile(cacheFile, body, 0o644)
	return string(body), nil
}

var (
	rbStr           = `"((?:[^"\\]|\\.)*)"`
	rbDescRe        = regexp.MustCompile(`(?m)^\s*desc\s+` + rbStr)
	rbHomeRe        = regexp.MustCompile(`(?m)^\s*homepage\s+` + rbStr)
	rbURLRe         = regexp.MustCompile(`(?m)^\s*url\s+` + rbStr)
	rbHeadRe        = regexp.MustCompile(`(?m)^\s*head\s+` + rbStr)
	rbVersionRe     = regexp.MustCompile(`(?m)^\s*version\s+` + rbStr)
	rbSHARe         = regexp.MustCompile(`(?m)^\s*sha256\s+(?:"([0-9a-fA-F]{64})"|(:no_check)\b|(\w+:\s*"[0-9a-fA-F]{64}"(?:\s*,\s*\w+:\s*"[0-9a-fA-F]{64}")*))`)
	rbArchRe        = regexp.MustCompile(`(?m)^\s*arch\s+(\w+:\s*"[^"]*"(?:\s*,\s*\w+:\s*"[^"]*")*)`)
	rbKVRe          = regexp.MustCompile(`(\w+):\s*"([^"]*)"`)
	rbRevRe         = regexp.MustCompile(`(?m)^\s*revision\s+(\d+)`)
	rbSchemeRe      = regexp.MustCompile(`(?m)^\s*version_scheme\s+(\d+)`)
	rbLicenseRe     = regexp.MustCompile(`(?m)^\s*license\s+(?:` + rbStr + `|:(\w+))`)
	rbDepRe         = regexp.MustCompile(`(?m)^\s*depends_on\s+` + rbStr + `(?:\s*=>\s*(\[[^\]]*\]|:\w+))?`)
	rbKegOnlyRe     = regexp.MustCompile(`(?m)^\s*keg_only\b`)
	rbServiceRe     = regexp.MustCompile(`(?m)^\s*service\s+do\b`)
	rbCaveatRe      = regexp.MustCompile(`(?m)^[ \t]*(?:def[ \t]+caveats[ \t]*\n[ \t]*|caveats[ \t]+)(?:<<([~-]?)["']?(\w+)["']?[^\n]*\n|` + rbStr + `)`)
	rbNameRe        = regexp.MustCompile(`(?m)^\s*name\s+` + rbStr)
	rbAppRe         = regexp.MustCompile(`(?m)^\s*app\s+` + rbStr)
	rbBinaryRe      = regexp.MustCompile(`(?m)^\s*binary\s+` + rbStr)
	rbCaskDepRe     = regexp.MustCompile(`(?m)^\s*depends_on\s+formula:\s*` + rbStr)
	rbCaskCaskDepRe = regexp.MustCompile(`(?m)^\s*depends_on\s+cask:\s*` + rbStr)
	rbInterpRe      = regexp.MustCompile(`#\{([^}]*)\}`)
	urlVerRe        = regexp.MustCompile(`(?i)[-_/v]v?(\d+(?:\.\d+)+(?:[-_.]?(?:alpha|beta|rc|pre|p)\d*)?)(?:[-_.](?:src|source|orig|tar|tgz|zip|darwin|macos|linux|amd64|arm64|x86_64|all))*\.(?:tar\.(?:gz|bz2|xz|zst)|tgz|tbz2?|txz|zip|dmg|pkg|gem|jar)`)
)

// Block structure of a formula/cask file, used to read only the stanzas that
// apply here: on_macos/on_linux/on_arm/on_intel/on_<codename> and
// `if OS.mac?`-style blocks for other systems are skipped, as are blocks whose
// contents are not part of the stable spec (resource, patch, head, bottle, ...).
var (
	rbHeredocRe  = regexp.MustCompile(`<<([~-]?)["']?([A-Za-z_]\w*)["']?`)
	rbOnRe       = regexp.MustCompile(`^on_(\w+)(?:\s+:(or_newer|or_older))?\s+do\b`)
	rbOnSystemRe = regexp.MustCompile(`^on_system\b(.*)\bdo\b`)
	rbSkipRe     = regexp.MustCompile(`^(?:resource|patch|head|bottle|livecheck|test|service|preflight|postflight|uninstall_preflight|uninstall_postflight|caveats|def)\b`)
	rbDoRe       = regexp.MustCompile(`\bdo(?:\s*\|[^|]*\|)?\s*(?:#.*)?$`)
	rbOpenRe     = regexp.MustCompile(`^(?:(?:if|unless|case|while|until|begin|for|class|module)\b|[\w.@\[\]:]+\s*\|?\|?=\s*(?:if|unless|case|begin)\b)`)
	rbEndRe      = regexp.MustCompile(`^end\b`)
	rbEndlessDef = regexp.MustCompile(`^def\s+[\w.?!]+(?:\([^)]*\))?\s*=[^=~]`)
)

// rbTarget is the system tap stanzas are evaluated for (follows platformTag,
// so `brew deps --os/--arch` simulation applies too).
func rbTarget() (linux, arm bool, macos int) {
	tag := platformTag()
	if strings.HasSuffix(tag, "_linux") {
		return true, strings.HasPrefix(tag, "arm64"), 0
	}
	return false, strings.HasPrefix(tag, "arm64_"), macosMajorFor(strings.TrimPrefix(tag, "arm64_"))
}

// rbOnMatches evaluates on_<name> [:or_newer|:or_older].
func rbOnMatches(name, mod string) bool {
	linux, arm, macos := rbTarget()
	switch name {
	case "macos":
		return !linux
	case "linux":
		return linux
	case "arm":
		return arm
	case "intel":
		return !arm
	}
	want := macosMajorFor(name)
	if want == 0 || linux {
		return false
	}
	switch mod {
	case "or_newer":
		return macos >= want
	case "or_older":
		return macos <= want
	}
	return macos == want
}

var rbOnSystemMacRe = regexp.MustCompile(`macos:\s*:(\w+?)(?:_(or_newer|or_older))?\b`)

func rbOnSystemMatches(args string) bool {
	linux, _, _ := rbTarget()
	if linux {
		return strings.Contains(args, ":linux")
	}
	if m := rbOnSystemMacRe.FindStringSubmatch(args); m != nil {
		return rbOnMatches(m[1], m[2])
	}
	return false
}

// rbCond evaluates simple platform conditions (`OS.mac? && Hardware::CPU.arm?`);
// ok is false for anything else.
func rbCond(cond string) (val, ok bool) {
	linux, arm, _ := rbTarget()
	cond = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(cond), "then"))
	val = true
	for _, t := range strings.Split(cond, "&&") {
		t = strings.TrimSpace(t)
		neg := strings.HasPrefix(t, "!")
		t = strings.TrimSpace(strings.TrimPrefix(t, "!"))
		var v bool
		switch t {
		case "OS.mac?":
			v = !linux
		case "OS.linux?":
			v = linux
		case "Hardware::CPU.arm?":
			v = arm
		case "Hardware::CPU.intel?":
			v = !arm
		case "Hardware::CPU.is_64_bit?":
			v = true
		default:
			return false, false
		}
		val = val && v != neg
	}
	return val, true
}

// rbActiveSource returns the lines of src that apply to this system's stable
// spec, with heredoc bodies and non-matching or non-spec blocks removed.
func rbActiveSource(src string) string {
	type block struct {
		parent, keep, cond, taken bool
	}
	var stack []block
	keep := func() bool { return len(stack) == 0 || stack[len(stack)-1].keep }
	var out []string
	heredoc := ""
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if heredoc != "" {
			if t == heredoc {
				heredoc = ""
			}
			continue
		}
		t = rbCode(t)
		if t == "" {
			continue
		}
		if tag := rbHeredocTag(t); tag != "" {
			heredoc = tag
		}
		parent := keep()
		push := func(k bool) { stack = append(stack, block{parent: parent, keep: parent && k}) }
		oneLine := strings.HasSuffix(t, " end") || strings.HasSuffix(t, ";end")
		switch {
		case rbEndRe.MatchString(t):
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		case strings.HasPrefix(t, "elsif ") || t == "else":
			if n := len(stack); n > 0 {
				b := &stack[n-1]
				if b.cond {
					v, ok := true, false
					if t != "else" {
						v, ok = rbCond(strings.TrimPrefix(t, "elsif "))
					}
					b.keep = b.parent && !b.taken && (v || !ok)
					b.taken = b.taken || (ok && v)
				}
			}
			continue
		case oneLine:
		case rbOnSystemRe.MatchString(t):
			push(rbOnSystemMatches(rbOnSystemRe.FindStringSubmatch(t)[1]))
			continue
		case rbOnRe.MatchString(t):
			m := rbOnRe.FindStringSubmatch(t)
			push(rbOnMatches(m[1], m[2]))
			continue
		case strings.HasPrefix(t, "if ") || strings.HasPrefix(t, "unless "):
			kw, cond, _ := strings.Cut(t, " ")
			v, ok := rbCond(cond)
			if kw == "unless" {
				v = !v
			}
			push(v || !ok)
			stack[len(stack)-1].cond = true
			stack[len(stack)-1].taken = ok && v
			continue
		case rbEndlessDef.MatchString(t):
			continue
		case rbSkipRe.MatchString(t) && (strings.HasPrefix(t, "def") || rbDoRe.MatchString(t)):
			push(false)
			continue
		case rbDoRe.MatchString(t) || rbOpenRe.MatchString(t):
			push(true)
		}
		if parent {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

// rbCode strips a trailing comment (a # outside string literals, not #{).
func rbCode(t string) string {
	var quote byte
	for i := 0; i < len(t); i++ {
		switch ch := t[i]; {
		case quote != 0 && ch == '\\':
			i++
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '#' && (i+1 >= len(t) || t[i+1] != '{'):
			return strings.TrimSpace(t[:i])
		}
	}
	return t
}

// rbHeredocTag returns the terminator of a heredoc started on this line
// (outside string literals): <<~TAG, <<-TAG, or <<TAG with an upper-case TAG.
func rbHeredocTag(t string) string {
	for _, loc := range rbHeredocRe.FindAllStringSubmatchIndex(t, -1) {
		if strings.Count(t[:loc[0]], `"`)%2 != 0 {
			continue
		}
		flag, tag := t[loc[2]:loc[3]], t[loc[4]:loc[5]]
		if flag == "" && strings.ToUpper(tag) != tag {
			continue
		}
		return tag
	}
	return ""
}

func rbUnquote(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// rbExpand substitutes #{version}-style interpolation. It returns "" when an
// interpolation remains that it cannot evaluate, so callers never use (or
// fetch) a URL with a literal #{...} in it.
func rbExpand(s, version string, vars map[string]string) string {
	if !strings.Contains(s, "#{") {
		return s
	}
	ok := true
	out := rbInterpRe.ReplaceAllStringFunc(s, func(m string) string {
		v, found := rbEval(strings.TrimSpace(m[2:len(m)-1]), version, vars)
		if !found {
			ok = false
		}
		return v
	})
	if !ok || strings.Contains(out, "#{") {
		return ""
	}
	return out
}

var rbIndexRe = regexp.MustCompile(`^(\w+)\[(\d+)\]$`)

// rbEval evaluates `version`, `version.major`, `version.csv.first`, ... and plain variables.
func rbEval(expr, version string, vars map[string]string) (string, bool) {
	if v, ok := vars[expr]; ok {
		return v, true
	}
	methods := strings.Split(expr, ".")
	if methods[0] != "version" || version == "" {
		return "", false
	}
	cur := version
	var list []string
	isList := false
	nth := func(i int) bool {
		if !isList || i >= len(list) {
			return false
		}
		cur, isList = list[i], false
		return true
	}
	parts := func() []string {
		return strings.FieldsFunc(cur, func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == ',' || r == ':' })
	}
	firstN := func(n int) bool {
		p := parts()
		if len(p) < n {
			return false
		}
		cur = strings.Join(p[:n], ".")
		return true
	}
	for _, m := range methods[1:] {
		ok := true
		if im := rbIndexRe.FindStringSubmatch(m); im != nil && im[1] == "csv" {
			i, _ := strconv.Atoi(im[2])
			list, isList = strings.Split(cur, ","), true
			ok = nth(i)
		} else {
			switch m {
			case "csv":
				list, isList = strings.Split(cur, ","), true
			case "first":
				ok = nth(0)
			case "second":
				ok = nth(1)
			case "third":
				ok = nth(2)
			case "fourth":
				ok = nth(3)
			case "last":
				ok = isList && nth(len(list)-1)
			case "major":
				ok = firstN(1)
			case "minor":
				p := parts()
				ok = len(p) > 1
				if ok {
					cur = p[1]
				}
			case "patch":
				p := parts()
				ok = len(p) > 2
				if ok {
					cur = p[2]
				}
			case "major_minor":
				ok = firstN(2)
			case "major_minor_patch":
				ok = firstN(3)
			case "before_comma":
				cur, _, _ = strings.Cut(cur, ",")
			case "after_comma":
				_, cur, ok = strings.Cut(cur, ",")
			case "before_colon":
				cur, _, _ = strings.Cut(cur, ":")
			case "after_colon":
				_, cur, ok = strings.Cut(cur, ":")
			case "no_dots":
				cur = strings.ReplaceAll(cur, ".", "")
			case "no_hyphens":
				cur = strings.ReplaceAll(cur, "-", "")
			case "dots_to_underscores":
				cur = strings.ReplaceAll(cur, ".", "_")
			case "dots_to_hyphens":
				cur = strings.ReplaceAll(cur, ".", "-")
			case "dots_to_slashes":
				cur = strings.ReplaceAll(cur, ".", "/")
			case "to_s":
			default:
				ok = false
			}
		}
		if !ok {
			return "", false
		}
	}
	if isList {
		return "", false
	}
	return cur, true
}

// rbCaveats extracts `def caveats` (formulae) or a `caveats` stanza (casks):
// a heredoc ending at its own terminator line, or a single string literal.
func rbCaveats(src string) *string {
	loc := rbCaveatRe.FindStringSubmatchIndex(src)
	if loc == nil {
		return nil
	}
	if loc[6] >= 0 { // string literal
		c := rbUnquote(src[loc[6]:loc[7]])
		return &c
	}
	squiggly, tag := src[loc[2]:loc[3]], src[loc[4]:loc[5]]
	var body []string
	for _, l := range strings.Split(src[loc[1]:], "\n") {
		if (squiggly == "" && l == tag) || (squiggly != "" && strings.TrimSpace(l) == tag) {
			c := strings.Join(body, "\n")
			if squiggly == "~" {
				c = dedent(c)
			}
			return &c
		}
		body = append(body, l)
	}
	return nil
}

// validDepName accepts a formula name or a tap-qualified user/repo/name.
func validDepName(d string) bool {
	parts := strings.Split(d, "/")
	if len(parts) != 1 && len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if !validName(p) {
			return false
		}
	}
	return true
}

// safeVersion rejects versions that cannot be used in file names.
func safeVersion(v string) string {
	if strings.ContainsAny(v, `/\`) || strings.Contains(v, "..") {
		return ""
	}
	return v
}

// rbSHA256 returns the checksum stanza: a hex digest, "no_check", or the
// arch-specific digest of `sha256 arm: "...", intel: "..."`.
func rbSHA256(src string) string {
	m := rbSHARe.FindStringSubmatch(src)
	switch {
	case m == nil:
		return ""
	case m[1] != "":
		return strings.ToLower(m[1])
	case m[2] != "":
		return "no_check"
	}
	linux, arm, _ := rbTarget()
	kv := rbKV(m[3])
	switch {
	case linux && arm:
		return strings.ToLower(kv["arm64_linux"])
	case linux:
		return strings.ToLower(kv["x86_64_linux"])
	case arm:
		return strings.ToLower(kv["arm"])
	}
	return strings.ToLower(kv["intel"])
}

func rbKV(s string) map[string]string {
	out := map[string]string{}
	for _, m := range rbKVRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = m[2]
	}
	return out
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
	spec := rbActiveSource(src)
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(spec); m != nil {
			return rbUnquote(m[1])
		}
		return ""
	}
	f.Desc = get(rbDescRe)
	f.Homepage = get(rbHomeRe)
	f.Versions.Stable = safeVersion(get(rbVersionRe))
	vars := map[string]string{"name": name}
	f.URLs.Stable.URL = rbExpand(get(rbURLRe), f.Versions.Stable, vars)
	f.URLs.Stable.Checksum = rbSHA256(spec)
	f.Versions.Head = rbExpand(get(rbHeadRe), f.Versions.Stable, vars)
	if f.Versions.Stable == "" {
		f.Versions.Stable = safeVersion(versionFromURL(f.URLs.Stable.URL))
	}
	if m := rbRevRe.FindStringSubmatch(spec); m != nil {
		f.Revision, _ = strconv.Atoi(m[1])
	}
	if m := rbSchemeRe.FindStringSubmatch(spec); m != nil {
		f.VersionScheme, _ = strconv.Atoi(m[1])
	}
	if m := rbLicenseRe.FindStringSubmatch(spec); m != nil {
		f.License = m[1]
		if f.License == "" {
			f.License = m[2]
		}
	}
	for _, m := range rbDepRe.FindAllStringSubmatch(spec, -1) {
		dep, typ := rbUnquote(m[1]), m[2]
		if !validDepName(dep) {
			odebug("%s: ignoring invalid dependency name %q", f.FullName, dep)
			continue
		}
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
	f.KegOnly = rbKegOnlyRe.MatchString(spec)
	if rbServiceRe.MatchString(src) {
		f.Service = json.RawMessage(`{}`)
	}
	f.Caveats = rbCaveats(src)
	return f
}

func parseTapCask(src, tap, token, relPath string) *Cask {
	c := &Cask{Token: token, FullToken: tap + "/" + token, Tap: tap, RubySourcePath: relPath}
	spec := rbActiveSource(src)
	get := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(spec); m != nil {
			return rbUnquote(m[1])
		}
		return ""
	}
	c.Version = safeVersion(get(rbVersionRe))
	if regexp.MustCompile(`(?m)^\s*version\s+:latest\b`).MatchString(spec) {
		c.Version = "latest"
	}
	vars := map[string]string{"token": token}
	if m := rbArchRe.FindStringSubmatch(spec); m != nil {
		kv := rbKV(m[1])
		if _, arm, _ := rbTarget(); arm {
			vars["arch"] = kv["arm"]
		} else {
			vars["arch"] = kv["intel"]
		}
	}
	c.Homepage = rbExpand(get(rbHomeRe), c.Version, vars)
	c.URL = rbExpand(get(rbURLRe), c.Version, vars)
	c.SHA256 = rbSHA256(spec)
	if d := get(rbDescRe); d != "" {
		c.Desc = &d
	}
	for _, m := range rbNameRe.FindAllStringSubmatch(spec, -1) {
		c.Name = append(c.Name, rbUnquote(m[1]))
	}
	for _, m := range rbAppRe.FindAllStringSubmatch(spec, -1) {
		b, _ := json.Marshal(map[string][]string{"app": {rbExpand(rbUnquote(m[1]), c.Version, vars)}})
		c.Artifacts = append(c.Artifacts, b)
	}
	for _, m := range rbBinaryRe.FindAllStringSubmatch(spec, -1) {
		b, _ := json.Marshal(map[string][]string{"binary": {rbExpand(rbUnquote(m[1]), c.Version, vars)}})
		c.Artifacts = append(c.Artifacts, b)
	}
	for _, m := range rbCaskDepRe.FindAllStringSubmatch(spec, -1) {
		if d := rbUnquote(m[1]); validDepName(d) {
			c.DependsOn.Formula = append(c.DependsOn.Formula, d)
		}
	}
	for _, m := range rbCaskCaskDepRe.FindAllStringSubmatch(spec, -1) {
		if d := rbUnquote(m[1]); validDepName(d) {
			c.DependsOn.Cask = append(c.DependsOn.Cask, d)
		}
	}
	if regexp.MustCompile(`(?m)^\s*auto_updates\s+true\b`).MatchString(spec) {
		t := true
		c.AutoUpdates = &t
	}
	c.Caveats = rbCaveats(src)
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
