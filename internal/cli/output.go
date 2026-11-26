package cli

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rubnogueira/mise-brew/internal/homebrew"
	"github.com/rubnogueira/mise-brew/internal/mise"
	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// mise prints lines such as
//   mise brew:hello                  download hello-2.12.3.tar.gz
//   mise brew:hello                ✓ 2.12.3
//   pour hello/2.12.3 (requested)
// runMiseBrewStyle runs mise with its output piped and rewrites those lines
// into Homebrew's ("==> Downloading", "==> Pouring", "🍺  keg: N files, size").
// MISEBREW_RAW_OUTPUT=1 or --verbose shows mise's own output instead.

var (
	misePkgLineRe = regexp.MustCompile(`^mise\s+(brew|brew-cask):(\S+)\s+(.*)$`)
	pourLineRe    = regexp.MustCompile(`^\s*(pour|install|upgrade|remove|cask)\s+(\S+?)/(\S+)\s*(?:\((\w+)\))?\s*$`)
	addedLineRe   = regexp.MustCompile(`^mise\s+(.+?):\s+added\s+(\S+)$`)
	summaryLineRe = regexp.MustCompile(`^mise\s+(brew|brew-cask):\s+(installed|upgraded|\d+ package)`)
)

type miseRun struct {
	extraEnv []string
	start    time.Time
	last     time.Time
	timings  map[string]time.Duration
	poured   []string // name/version of kegs poured
	planned  [][3]string
}

func rawOutput() bool {
	return ui.Verbose || ui.Debug || os.Getenv("MISEBREW_RAW_OUTPUT") != ""
}

func runMiseBrewStyle(r *miseRun, args ...string) int {
	if r == nil {
		r = &miseRun{}
	}
	if rawOutput() {
		if len(r.extraEnv) == 0 {
			return mise.RunMise(args...)
		}
	}
	cmd := mise.MiseCmd(args...)
	cmd.Env = append(cmd.Env, r.extraEnv...)
	cmd.Stdin = os.Stdin
	if rawOutput() {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		return exitCode(cmd.Run())
	}
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	r.start = time.Now()
	r.timings = map[string]time.Duration{}
	if err := cmd.Start(); err != nil {
		ui.Onoe("failed to run mise: %v", err)
		return 1
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		pkgStart := map[string]time.Time{}
		for sc.Scan() {
			r.translate(sc.Text(), pkgStart)
		}
	}()
	err := cmd.Wait()
	pw.Close()
	wg.Wait()
	r.flushPlan()
	return exitCode(err)
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		ui.Onoe("failed to run mise: %v", err)
		return 1
	}
	return 0
}

func (r *miseRun) translate(line string, pkgStart map[string]time.Time) {
	if m := pourLineRe.FindStringSubmatch(line); m != nil {
		r.planned = append(r.planned, [3]string{m[2], m[3], m[4]})
		return
	}
	if m := addedLineRe.FindStringSubmatch(line); m != nil {
		if !ui.Quiet {
			fmt.Printf("Recorded %s in %s\n", m[2], paths.Tildify(m[1]))
		}
		return
	}
	if summaryLineRe.MatchString(line) {
		return
	}
	m := misePkgLineRe.FindStringSubmatch(line)
	if m == nil {
		if strings.Contains(line, "ERROR") || strings.Contains(line, "error") {
			fmt.Fprintln(os.Stderr, line)
		} else if strings.Contains(line, "WARN") {
			fmt.Fprintln(os.Stderr, line)
		} else if strings.TrimSpace(line) != "" && !ui.Quiet {
			fmt.Println(line)
		}
		return
	}
	mgr, name, rest := m[1], m[2], strings.TrimSpace(m[3])
	if _, ok := pkgStart[name]; !ok {
		pkgStart[name] = r.last
		if pkgStart[name].IsZero() {
			pkgStart[name] = r.start
		}
	}
	switch {
	case strings.HasPrefix(rest, "download "):
		file := strings.TrimPrefix(rest, "download ")
		if ui.Quiet {
			return
		}
		if mgr == "brew" {
			ui.Ohai("Downloading %s", bottleURLFor(name, file))
		} else {
			ui.Ohai("Downloading %s", file)
		}
	case strings.HasPrefix(rest, "extract ") && mgr == "brew":
		if !ui.Quiet {
			ver := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(rest, "extract "), homebrew.BaseName(name)+"-"), ".tar.gz")
			tag := homebrew.PlatformTag()
			if f := homebrew.LookupFormula(homebrew.BaseName(name)); f != nil {
				if t, _, _, ok := bottleFileFor(f); ok {
					tag = t
				}
			}
			ui.Ohai("Pouring %s--%s.%s.bottle.tar.gz", homebrew.BaseName(name), ver, tag)
		}
	case strings.HasPrefix(rest, "✓"):
		ver := strings.TrimSpace(strings.TrimPrefix(rest, "✓"))
		r.timings[name] = time.Since(pkgStart[name])
		r.last = time.Now()
		if mgr == "brew" {
			keg := homebrew.KegPath(homebrew.BaseName(name), ver)
			r.poured = append(r.poured, homebrew.BaseName(name)+"/"+ver)
			if !ui.Quiet {
				n, size := homebrew.DiskUsage(keg)
				fmt.Printf("🍺  %s: %s, %s\n", keg, ui.Plural(n, "file"), ui.HumanSize(size))
			}
		} else if !ui.Quiet {
			fmt.Printf("🍺  %s was successfully installed!\n", name)
		}
	case rest == "checksum" || rest == "extract" || rest == "relocate" || rest == "link" || strings.HasPrefix(rest, "extract "):
		// progress steps Homebrew does not print
	case strings.HasPrefix(rest, "waiting for another"):
		ui.Ohai("Waiting for another mise or brew process to finish...")
	case strings.HasPrefix(rest, "skipped"):
		ui.Opoo("%s: %s", name, rest)
	default:
		if strings.Contains(rest, "rror") {
			ui.Onoe("%s: %s", name, rest)
		} else if !ui.Quiet {
			fmt.Printf("==> %s: %s\n", name, rest)
		}
	}
}

func (r *miseRun) flushPlan() {
	if len(r.planned) == 0 || ui.Quiet {
		return
	}
	var req, deps []string
	for _, p := range r.planned {
		s := p[0] + " " + p[1]
		if p[2] == "dependency" {
			deps = append(deps, s)
		} else {
			req = append(req, s)
		}
	}
	if len(req) > 0 {
		ui.Ohai("Would install %s:", ui.Plural(len(req), "formula"))
		fmt.Println(strings.Join(req, "\n"))
	}
	if len(deps) > 0 {
		ui.Ohai("Would install %d %s:", len(deps), map[bool]string{true: "dependency", false: "dependencies"}[len(deps) == 1])
		fmt.Println(strings.Join(deps, "\n"))
	}
	r.planned = nil
}

func (r *miseRun) printTimes() {
	if len(r.timings) == 0 {
		return
	}
	ui.Ohai("Timing")
	for n, d := range r.timings {
		fmt.Printf("%s: %.1f seconds\n", n, d.Seconds())
	}
}

// bottleURLFor returns the ghcr.io URL Homebrew would print for a bottle download.
func bottleURLFor(name, file string) string {
	f := homebrew.LookupFormula(homebrew.BaseName(name))
	if f == nil {
		return file
	}
	if _, url, _, ok := bottleFileFor(f); ok {
		return url
	}
	return file
}

// miseBottleTags lists bottle tags in the order the installed mise picks them:
// mise only knows macOS codenames up to the one it was built with, so a newer
// macOS pours the newest bottle mise knows (e.g. arm64_tahoe on macOS 27).
func miseBottleTags() []string {
	if v := os.Getenv("MISEBREW_BOTTLE_TAG"); v != "" {
		return []string{v, "all"}
	}
	if runtime.GOOS != "darwin" {
		return homebrew.BottleTagCandidates()
	}
	var out []string
	for _, t := range homebrew.BottleTagCandidates() {
		name := strings.TrimPrefix(t, "arm64_")
		if name == "all" || miseKnowsCodename(name) {
			out = append(out, t)
		}
	}
	return out
}

var codenameCache = map[string]bool{}

func miseKnowsCodename(name string) bool {
	if v, ok := codenameCache[name]; ok {
		return v
	}
	bin, _ := filepath.EvalSymlinks(paths.MiseBin())
	fi, err := os.Stat(bin)
	if err != nil {
		return true
	}
	stamp := filepath.Join(paths.MiseBrewCacheDir(), "mise-codenames", fmt.Sprintf("%s-%d", name, fi.ModTime().Unix()))
	if b, err := os.ReadFile(stamp); err == nil {
		codenameCache[name] = string(b) == "1"
		return codenameCache[name]
	}
	data, err := os.ReadFile(bin)
	known := err != nil || bytes.Contains(data, []byte(name))
	codenameCache[name] = known
	_ = os.MkdirAll(filepath.Dir(stamp), 0o755)
	_ = os.WriteFile(stamp, []byte(map[bool]string{true: "1", false: "0"}[known]), 0o644)
	return known
}

// bottleFileFor returns the bottle mise would pour on this machine.
func bottleFileFor(f *homebrew.Formula) (tag, url, sha string, ok bool) {
	for _, t := range miseBottleTags() {
		if b, found := f.Bottle.Stable.Files[t]; found {
			return t, b.URL, b.SHA256, true
		}
	}
	return "", "", "", false
}
