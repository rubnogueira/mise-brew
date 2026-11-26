package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// --- source files: formula / edit / cat / log -------------------------------

type sourceRef struct {
	owner, repo, path, ref string
}

func (s sourceRef) rawURL() string {
	return fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s", s.owner, s.repo, refOr(s.ref), s.path)
}

func sourceFor(name string, o Opts) sourceRef {
	if !o.onlyCask() {
		if f := lookupFormula(name); f != nil && f.RubySourcePath != "" {
			if f.Tap != "" && f.Tap != "homebrew/core" {
				ow, rp := tapRepo(f.Tap)
				if ow == "" {
					odie("%s is from %s, which is not hosted on GitHub; its source is not available.", name, f.Tap)
				}
				return sourceRef{ow, rp, f.RubySourcePath, ""}
			}
			return sourceRef{"Homebrew", "homebrew-core", f.RubySourcePath, f.TapGitHead}
		}
	}
	if !o.onlyFormula() {
		if c := lookupCask(name); c != nil && c.RubySourcePath != "" {
			if c.Tap != "" && c.Tap != "homebrew/cask" {
				ow, rp := tapRepo(c.Tap)
				if ow == "" {
					odie("%s is from %s, which is not hosted on GitHub; its source is not available.", name, c.Tap)
				}
				return sourceRef{ow, rp, c.RubySourcePath, ""}
			}
			return sourceRef{"Homebrew", "homebrew-cask", c.RubySourcePath, c.TapGitHead}
		}
	}
	odie("No available formula or cask with the name \"%s\".", name)
	return sourceRef{}
}

// localSource downloads the Ruby file into mise-brew's cache and returns its path.
func localSource(src sourceRef) string {
	p := filepath.Join(miseBrewCacheDir(), "sources", src.owner, src.repo, src.path)
	if fi, err := os.Stat(p); err == nil && time.Since(fi.ModTime()) < tapTTL {
		return p
	}
	body, err := httpGet(src.rawURL(), "")
	if err != nil {
		if fileExists(p) {
			return p
		}
		odie("could not fetch %s: %v", src.rawURL(), err)
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, body, 0o644); err != nil {
		odie("%v", err)
	}
	return p
}

func cmdCat(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) != 1 {
		odie("Invalid usage: This command requires exactly 1 formula or cask argument.")
	}
	data, err := os.ReadFile(localSource(sourceFor(o.Pos[0], o)))
	if err != nil {
		odie("%v", err)
	}
	os.Stdout.Write(data)
}

func cmdFormula(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula argument.")
	}
	for _, n := range o.Pos {
		o.flags["formula"] = ""
		fmt.Println(localSource(sourceFor(n, o)))
	}
}

func cmdEdit(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		fmt.Println(miseBrewCacheDir())
		return
	}
	var paths []string
	for _, n := range o.Pos {
		paths = append(paths, localSource(sourceFor(n, o)))
	}
	if o.Has("print-path") {
		fmt.Println(strings.Join(paths, "\n"))
		return
	}
	opoo("mise installs from the formulae.brew.sh API, so edits to these files do not change what gets installed.")
	editor := editorCommand()
	bin, err := exec.LookPath(editor[0])
	if err != nil {
		odie("%v", err)
	}
	if err := syscall.Exec(bin, append(editor, paths...), os.Environ()); err != nil {
		odie("%v", err)
	}
}

// editorCommand resolves the editor like Homebrew's which_editor: the first
// non-blank of HOMEBREW_EDITOR, HOMEBREW_VISUAL, VISUAL and EDITOR, split into
// words shell-style (so "code --wait" works), else vim, nano or vi.
func editorCommand() []string {
	for _, k := range []string{"HOMEBREW_EDITOR", "HOMEBREW_VISUAL", "VISUAL", "EDITOR"} {
		if args := shellSplit(os.Getenv(k)); len(args) > 0 {
			return args
		}
	}
	for _, e := range []string{"vim", "nano"} {
		if _, err := exec.LookPath(e); err == nil {
			return []string{e}
		}
	}
	return []string{"vi"}
}

// shellSplit splits s into words like a POSIX shell (quotes and backslashes,
// no expansions).
func shellSplit(s string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			if quote == '"' && !strings.ContainsRune("\\\"$`\n", r) {
				cur.WriteByte('\\')
			}
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

func cmdLog(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		odie("Invalid usage: `brew log` needs a formula or cask (mise-brew has no Homebrew git checkout).")
	}
	src := sourceFor(o.Pos[0], o)
	max := 30
	if o.Has("1") {
		max = 1
	}
	if v, ok := o.Val("max-count"); ok {
		fmt.Sscanf(v, "%d", &max)
	}
	q := url.Values{"path": {src.path}, "per_page": {strconv.Itoa(max)}}
	u := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?%s", url.PathEscape(src.owner), url.PathEscape(src.repo), q.Encode())
	body, err := httpGet(u, "application/vnd.github+json")
	if err != nil {
		odie("GitHub API: %v", err)
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name  string `json:"name"`
				Email string `json:"email"`
				Date  string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		odie("GitHub API: %v", err)
	}
	for i, c := range commits {
		if o.Has("oneline") {
			fmt.Printf("%s %s\n", paint(colorOut, "33", c.SHA[:10]), strings.SplitN(c.Commit.Message, "\n", 2)[0])
			continue
		}
		if i > 0 {
			fmt.Println()
		}
		fmt.Println(paint(colorOut, "33", "commit "+c.SHA))
		fmt.Printf("Author: %s <%s>\nDate:   %s\n\n", c.Commit.Author.Name, c.Commit.Author.Email, c.Commit.Author.Date)
		for _, l := range strings.Split(strings.TrimRight(c.Commit.Message, "\n"), "\n") {
			fmt.Println("    " + l)
		}
		if o.Has("patch") || o.Has("stat") {
			cb, err := httpGet(fmt.Sprintf("https://api.github.com/repos/%s/%s/commits/%s", url.PathEscape(src.owner), url.PathEscape(src.repo), url.PathEscape(c.SHA)), "application/vnd.github+json")
			if err != nil {
				continue
			}
			var detail struct {
				Files []struct {
					Filename  string `json:"filename"`
					Additions int    `json:"additions"`
					Deletions int    `json:"deletions"`
					Patch     string `json:"patch"`
				} `json:"files"`
			}
			_ = json.Unmarshal(cb, &detail)
			fmt.Println()
			for _, f := range detail.Files {
				if f.Filename != src.path {
					continue
				}
				if o.Has("stat") {
					fmt.Printf(" %s | %d %s%s\n", f.Filename, f.Additions+f.Deletions, strings.Repeat("+", min(f.Additions, 40)), strings.Repeat("-", min(f.Deletions, 40)))
				}
				if o.Has("patch") {
					fmt.Printf("--- a/%s\n+++ b/%s\n%s\n", f.Filename, f.Filename, f.Patch)
				}
			}
		}
	}
}

// --- fetch / unpack ---------------------------------------------------------

// download fetches url to dst (via a unique temp file in the same directory,
// so concurrent fetches cannot clobber each other), verifying sha256 when given.
func download(url, dst, sha string, auth bool) (err error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "mise-brew/"+miseBrewVersion)
	if auth {
		req.Header.Set("Authorization", "Bearer QQ==") // anonymous ghcr.io token Homebrew uses
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		return err
	}
	if sha != "" && sha != "no_check" {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, sha) {
			return fmt.Errorf("SHA256 mismatch\nExpected: %s\n  Actual: %s\n    File: %s", sha, got, url)
		}
	}
	if err = f.Chmod(0o644); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func fileSHA(p string) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))
}

// bottleCachePath is where mise looks for a formula's bottle.
func bottleCachePath(f *Formula) string {
	return filepath.Join(bottlesDir(), f.Name+"-"+f.PkgVersion()+".tar.gz")
}

func caskCachePath(c *Cask) string {
	ext := filepath.Ext(strings.SplitN(c.URL, "?", 2)[0])
	if ext == "" || len(ext) > 6 {
		ext = ".download"
	}
	if strings.HasSuffix(strings.SplitN(c.URL, "?", 2)[0], ".tar.gz") {
		ext = ".tar.gz"
	}
	return filepath.Join(miseBrewCacheDir(), "downloads", c.Token+"--"+c.Version+ext)
}

// nativeBottleTag is the bottle tag mise itself would pour for f, ignoring
// any --bottle-tag override.
func nativeBottleTag(f *Formula) string {
	if v, ok := os.LookupEnv("MISEBREW_BOTTLE_TAG"); ok {
		os.Unsetenv("MISEBREW_BOTTLE_TAG")
		defer os.Setenv("MISEBREW_BOTTLE_TAG", v)
	}
	tag, _, _, _ := bottleFileFor(f)
	return tag
}

// bottleDownloadPath is where f's bottle for tag is stored: mise's bottle
// cache when it is the bottle mise would pour, else a separate tagged file so
// a foreign bottle (`--bottle-tag`) can never be poured by mise.
func bottleDownloadPath(f *Formula, tag string) string {
	if tag == nativeBottleTag(f) {
		return bottleCachePath(f)
	}
	return filepath.Join(miseBrewCacheDir(), "downloads", f.Name+"--"+f.PkgVersion()+"."+tag+".bottle.tar.gz")
}

// safeVersion reports whether a version (which tap Ruby files control) can be
// embedded in a single path component.
func safeVersion(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\\x00")
}

// checkPathParts refuses a name/version pair that could escape the cache or
// unpack directory it is joined onto.
func checkPathParts(name, version string) error {
	if !validName(name) {
		return fmt.Errorf("refusing to use unsafe name %q in a path", name)
	}
	if !safeVersion(version) {
		return fmt.Errorf("refusing to use unsafe version %q of %s in a path", version, name)
	}
	return nil
}

func cmdFetch(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula or cask argument.")
	}
	if o.Has("HEAD") {
		odie("--HEAD is not supported: mise installs Homebrew bottles.")
	}
	bottleTag, tagged := o.Val("bottle-tag")
	if tagged {
		if !validName(bottleTag) {
			odie("Invalid bottle tag: %s", bottleTag)
		}
		os.Setenv("MISEBREW_BOTTLE_TAG", bottleTag)
	}
	names := o.Pos
	if o.Has("deps") {
		var all []string
		for _, n := range names {
			all = append(all, n)
			if f := lookupFormula(n); f != nil {
				all = append(all, recursiveDeps(f, depFilter{})...)
			}
		}
		names = dedupe(all)
	}
	failed := false
	for _, n := range names {
		if !o.onlyCask() {
			if f := lookupFormula(n); f != nil {
				if err := checkPathParts(f.Name, f.Versions.Stable); err != nil {
					onoe("%v", err)
					failed = true
					continue
				}
				if o.Has("build-from-source") || o.Has("build-bottle") {
					p := filepath.Join(miseBrewCacheDir(), "downloads", f.Name+"--"+f.Versions.Stable+filepath.Ext(f.URLs.Stable.URL))
					if !fetchOne(f.URLs.Stable.URL, p, f.URLs.Stable.Checksum, false, o.Has("force")) {
						failed = true
					}
					continue
				}
				tag, url, sha, ok := bottleFileFor(f)
				if !ok {
					onoe("%s has no bottle for %s", f.Name, platformTag())
					failed = true
					continue
				}
				dst := bottleCachePath(f)
				if tagged {
					dst = bottleDownloadPath(f, tag)
				}
				if !fetchOne(url, dst, sha, true, o.Has("force")) {
					failed = true
				}
				continue
			}
		}
		if c := lookupCask(n); c != nil && !o.onlyFormula() {
			if err := checkPathParts(c.Token, c.Version); err != nil {
				onoe("%v", err)
				failed = true
				continue
			}
			if !fetchOne(c.URL, caskCachePath(c), caskSHA(c), false, o.Has("force")) {
				failed = true
			}
			continue
		}
		onoe("No available formula or cask with the name \"%s\".", n)
		failed = true
	}
	if failed {
		exit(1)
	}
}

func caskSHA(c *Cask) string {
	if c.Tap == "" || c.Tap == "homebrew/cask" {
		if m := rawCaskMap(c.Token); m != nil {
			if s, ok := m["sha256"].(string); ok {
				return s
			}
		}
		return ""
	}
	// Third-party tap casks: the sha256 parsed from the Ruby file, never the
	// core cask's.
	if c.SHA256 != "" {
		return c.SHA256
	}
	return ""
}

func fetchOne(url, dst, sha string, auth, force bool) bool {
	if fileExists(dst) && !force {
		if sha == "" || sha == "no_check" || strings.EqualFold(fileSHA(dst), sha) {
			fmt.Printf("Already downloaded: %s\n", dst)
			return true
		}
		opoo("Checksum of cached %s no longer matches; re-downloading", dst)
	}
	switch sha {
	case "":
		opoo("Cannot verify integrity of '%s': no checksum is available, so the download is only protected by TLS.", filepath.Base(dst))
	case "no_check":
		opoo("No checksum defined for '%s', skipping verification.", filepath.Base(dst))
	}
	ohai("Downloading %s", url)
	if err := download(url, dst, sha, auth); err != nil {
		onoe("%v", err)
		return false
	}
	fmt.Printf("Downloaded to: %s\nSHA256: %s\n", dst, fileSHA(dst))
	return true
}

func cmdUnpack(args []string) {
	o := parseArgs(args, nil, nil)
	o.applyGlobal()
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 formula argument.")
	}
	dest, _ := o.Val("destdir")
	if dest == "" {
		dest, _ = os.Getwd()
	}
	for _, n := range o.Pos {
		f := lookupFormula(n)
		if f == nil {
			odie("No available formula with the name \"%s\".", n)
		}
		if err := checkPathParts(f.Name, f.Versions.Stable); err != nil {
			odie("%v", err)
		}
		dir := filepath.Join(dest, f.Name+"-"+f.Versions.Stable)
		if fileExists(dir) {
			if !o.Has("force") {
				odie("Destination %s already exists!", dir)
			}
			_ = os.RemoveAll(dir)
		}
		ohai("Unpacking %s to: %s", f.Name, dir)
		if f.URLs.Stable.Using == "git" || strings.HasSuffix(f.URLs.Stable.URL, ".git") {
			a := []string{"clone", "--depth", "1"}
			if f.URLs.Stable.Tag != "" {
				a = append(a, "--branch", f.URLs.Stable.Tag)
			}
			cmd := exec.Command("git", append(a, f.URLs.Stable.URL, dir)...)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				odie("git clone failed: %v", err)
			}
			continue
		}
		archive := filepath.Join(miseBrewCacheDir(), "downloads", f.Name+"--"+f.Versions.Stable+"--"+filepath.Base(strings.SplitN(f.URLs.Stable.URL, "?", 2)[0]))
		if !fetchOne(f.URLs.Stable.URL, archive, f.URLs.Stable.Checksum, false, false) {
			exit(1)
		}
		tmp, err := os.MkdirTemp(dest, ".mise-brew-unpack-")
		if err != nil {
			odie("%v", err)
		}
		var cmd *exec.Cmd
		if strings.HasSuffix(archive, ".zip") {
			cmd = exec.Command("unzip", "-q", archive, "-d", tmp)
		} else {
			cmd = exec.Command("tar", "-xf", archive, "-C", tmp)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.RemoveAll(tmp)
			odie("could not extract %s: %v", archive, err)
		}
		// Strip a single top-level directory, as Homebrew does.
		entries, _ := os.ReadDir(tmp)
		src := tmp
		if len(entries) == 1 && entries[0].IsDir() {
			src = filepath.Join(tmp, entries[0].Name())
		}
		if err := os.Rename(src, dir); err != nil {
			odie("%v", err)
		}
		os.RemoveAll(tmp)
		if o.Has("git") {
			for _, a := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "Vanilla"}} {
				c := exec.Command("git", a...)
				c.Dir = dir
				_ = c.Run()
			}
		}
		if o.Has("patch") {
			opoo("--patch: formula patches are not available from the API; the source is unpatched.")
		}
	}
}

// --- linkage ----------------------------------------------------------------

var dylibRe = regexp.MustCompile(`^\s+(\S.*?) \(compatibility`)

func machoLinks(path string) []string {
	var out []string
	if runtime.GOOS == "darwin" {
		b, err := exec.Command("otool", "-L", path).Output()
		if err != nil {
			return nil
		}
		// otool -L lists a dylib's own install name first; that is not a link.
		self := ""
		if d, err := exec.Command("otool", "-D", path).Output(); err == nil {
			if ls := strings.Split(strings.TrimSpace(string(d)), "\n"); len(ls) > 1 {
				self = strings.TrimSpace(ls[len(ls)-1])
			}
		}
		for _, l := range strings.Split(string(b), "\n")[1:] {
			if m := dylibRe.FindStringSubmatch(l); m != nil && m[1] != self {
				out = append(out, m[1])
			}
		}
		return out
	}
	b, err := exec.Command("ldd", path).Output()
	if err != nil {
		return nil
	}
	for _, l := range strings.Split(string(b), "\n") {
		if i := strings.Index(l, "=> "); i >= 0 {
			p := strings.Fields(l[i+3:])
			if len(p) > 0 {
				out = append(out, p[0])
			}
		}
	}
	return out
}

func isBinaryObject(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	magic := hex.EncodeToString(b)
	switch magic {
	case "cffaedfe", "cefaedfe", "feedfacf", "feedface", "cafebabe", "7f454c46":
		return true
	}
	return false
}

func cmdLinkage(args []string) {
	o := parseArgs(args, nil, nil)
	names := o.Pos
	if len(names) == 0 {
		names = installedFormulae()
	} else {
		names = requireInstalled(names)
	}
	broken := false
	for i, n := range names {
		keg := kegPath(n, currentVersion(n))
		declared := map[string]bool{}
		if r := readReceipt(keg); r != nil {
			for _, d := range r.RuntimeDependencies {
				declared[baseName(d.FullName)] = true
			}
		}
		system, brewed, missing := map[string]bool{}, map[string]map[string]bool{}, map[string]bool{}
		undeclared := map[string]bool{}
		reverse := map[string][]string{}
		_ = filepath.WalkDir(keg, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Type()&os.ModeSymlink != 0 || !isBinaryObject(p) {
				return nil
			}
			for _, lib := range machoLinks(p) {
				if strings.HasPrefix(lib, "@") {
					continue
				}
				reverse[lib] = append(reverse[lib], p)
				switch {
				case strings.HasPrefix(lib, brewPrefix()+"/"):
					if !fileExists(lib) {
						missing[lib] = true
						continue
					}
					rel := strings.TrimPrefix(lib, brewPrefix()+"/")
					parts := strings.Split(rel, "/")
					owner := ""
					if len(parts) > 1 && (parts[0] == "opt" || parts[0] == "Cellar") {
						owner = parts[1]
					} else if real, err := filepath.EvalSymlinks(lib); err == nil && strings.HasPrefix(real, cellar()+"/") {
						owner = strings.Split(strings.TrimPrefix(real, cellar()+"/"), "/")[0]
					}
					if owner == n {
						continue
					}
					if brewed[owner] == nil {
						brewed[owner] = map[string]bool{}
					}
					brewed[owner][lib] = true
					if owner != "" && !declared[owner] {
						undeclared[owner] = true
					}
				default:
					if runtime.GOOS == "darwin" || fileExists(lib) {
						system[lib] = true
					} else {
						missing[lib] = true
					}
				}
			}
			return nil
		})
		if len(missing) > 0 {
			broken = true
		}
		if o.Has("test") {
			if len(missing) > 0 {
				fmt.Printf("Missing libraries:\n  %s\n", strings.Join(sortedKeys(missing), "\n  "))
			}
			continue
		}
		if len(names) > 1 {
			if i > 0 {
				fmt.Println()
			}
			ohai("%s", n)
		}
		if o.Has("reverse") {
			libs := make([]string, 0, len(reverse))
			for l := range reverse {
				libs = append(libs, l)
			}
			sort.Strings(libs)
			for _, l := range libs {
				fmt.Println(l)
				for _, b := range dedupe(reverse[l]) {
					fmt.Println("  " + b)
				}
			}
			continue
		}
		if len(system) > 0 {
			fmt.Printf("System libraries:\n  %s\n", strings.Join(sortedKeys(system), "\n  "))
		}
		var bl []string
		for owner, libs := range brewed {
			for l := range libs {
				bl = append(bl, fmt.Sprintf("%s (%s)", l, owner))
			}
		}
		sort.Strings(bl)
		if len(bl) > 0 {
			fmt.Printf("Homebrew libraries:\n  %s\n", strings.Join(bl, "\n  "))
		}
		if len(missing) > 0 {
			fmt.Printf("Missing libraries:\n  %s\n", strings.Join(sortedKeys(missing), "\n  "))
		}
		if len(undeclared) > 0 {
			fmt.Printf("Undeclared dependencies with linkage:\n  %s\n", strings.Join(sortedKeys(undeclared), "\n  "))
			if o.Has("strict") {
				broken = true
			}
		}
	}
	if broken && (o.Has("test") || o.Has("strict")) {
		exit(1)
	}
}

// --- aliases ----------------------------------------------------------------

func aliasDir() string { return filepath.Join(homeDir(), ".brew-aliases") }

var aliasNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// aliasPath returns ~/.brew-aliases/<name>, or false when name is not a valid
// alias name (so it can never point outside the alias directory).
func aliasPath(name string) (string, bool) {
	if !aliasNameRe.MatchString(name) {
		return "", false
	}
	return filepath.Join(aliasDir(), name), true
}

func mustAliasPath(name string) string {
	p, ok := aliasPath(name)
	if !ok {
		odie("Invalid alias name: %q (use letters, digits, '.', '_' and '-')", name)
	}
	return p
}

func cmdAlias(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		for _, n := range listDirNames(aliasDir(), false) {
			if v := readAlias(n); v != "" && aliasNameRe.MatchString(n) {
				fmt.Printf("brew alias %s='%s'\n", n, v)
			}
		}
		return
	}
	spec := strings.Join(o.Pos, " ")
	name, val, hasVal := strings.Cut(spec, "=")
	name = strings.TrimSpace(name)
	p := mustAliasPath(name)
	if !hasVal {
		if o.Has("edit") {
			openInEditor(p)
			return
		}
		if v := readAlias(name); v != "" {
			fmt.Printf("brew alias %s='%s'\n", name, v)
			return
		}
		odie("No such alias: %s", name)
	}
	if _, ok := commands[canonical(name)]; ok {
		odie("'%s' is a reserved command. Sorry.", name)
	}
	val = strings.Trim(strings.TrimSpace(val), `'"`)
	// The command is shell code by design (as in Homebrew), but it must stay on
	// one line so it cannot escape the comment lines it is also written into.
	if strings.ContainsAny(val, "\n\r\x00") {
		odie("Invalid alias command: it must be a single line.")
	}
	_ = os.MkdirAll(aliasDir(), 0o755)
	content := fmt.Sprintf("#!/bin/bash\n# alias: brew %s\n#:  * `%s` [args...]\n#:    `brew %s` is an alias for `brew %s`\n\"${HOMEBREW_BREW_FILE:-brew}\" %s \"$@\"\n", name, name, name, val, val)
	if strings.HasPrefix(val, "!") {
		content = fmt.Sprintf("#!/bin/bash\n# alias: brew %s\n%s \"$@\"\n", name, strings.TrimPrefix(val, "!"))
	}
	if fi, err := os.Lstat(p); err == nil && !fi.Mode().IsRegular() {
		odie("%s exists and is not an alias file.", p)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		odie("%v", err)
	}
	if o.Has("edit") {
		openInEditor(p)
	}
}

func readAlias(name string) string {
	p, ok := aliasPath(name)
	if !ok {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "#:    `brew "+name+"` is an alias for `brew ") {
			return strings.TrimSuffix(strings.TrimPrefix(l, "#:    `brew "+name+"` is an alias for `brew "), "`")
		}
		if strings.HasPrefix(l, `"${HOMEBREW_BREW_FILE:-brew}" `) {
			return strings.TrimSuffix(strings.TrimPrefix(l, `"${HOMEBREW_BREW_FILE:-brew}" `), ` "$@"`)
		}
	}
	return ""
}

func cmdUnalias(args []string) {
	o := parseArgs(args, nil, nil)
	if len(o.Pos) == 0 {
		odie("Invalid usage: This command requires at least 1 alias argument.")
	}
	for _, n := range o.Pos {
		p, ok := aliasPath(n)
		if !ok {
			odie("No such alias: %s", n)
		}
		if err := os.Remove(p); err != nil {
			odie("No such alias: %s", n)
		}
	}
}

func openInEditor(p string) {
	editor := editorCommand()
	cmd := exec.Command(editor[0], append(editor[1:], p)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	_ = cmd.Run()
}

// runAlias executes ~/.brew-aliases/<name> if it exists.
func runAlias(name string, args []string) (int, bool) {
	p, ok := aliasPath(name)
	if !ok {
		return 0, false
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return 0, false
	}
	cmd := exec.Command("/bin/bash", append([]string{p}, args...)...)
	if self, err := os.Executable(); err == nil {
		cmd.Env = mergeEnv(os.Environ(), [][2]string{{"HOMEBREW_BREW_FILE", self}})
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return exitCode(cmd.Run()), true
}

// --- command-not-found ------------------------------------------------------

func cmdCommandNotFoundInit(args []string) {
	shell := filepath.Base(os.Getenv("SHELL"))
	if len(args) > 0 {
		shell = args[0]
	}
	self, _ := os.Executable()
	if self == "" {
		self = "brew"
	}
	switch shell {
	case "fish":
		fmt.Printf(`function fish_command_not_found
    set -l txt (%q which-formula --explain $argv[1] 2>/dev/null)
    if test -n "$txt"
        printf '%%s\n' $txt
    else
        __fish_default_command_not_found_handler $argv
    end
end
`, self)
	default:
		fn := "command_not_found_handle"
		if shell == "zsh" {
			fn = "command_not_found_handler"
		}
		fmt.Printf(`%s() {
  local cmd="$1"
  local txt
  if [ -n "${MC_SID:-}" ] || ! [ -t 1 ]; then
    echo "${SHELL##*/}: command not found: ${cmd}" >&2
    return 127
  fi
  txt="$(%q which-formula --explain "${cmd}" 2>/dev/null)"
  if [ -z "${txt}" ]; then
    echo "${SHELL##*/}: command not found: ${cmd}" >&2
  else
    echo "${txt}"
  fi
  return 127
}
`, fn, self)
	}
}

func cmdWhichUpdate(args []string) {
	// The executables database comes from the API's `executables` field, which
	// `brew update` refreshes; nothing else to maintain.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		opoo("which-update: the executables database is built from the formulae API; %s was not written.", args[0])
	}
}
