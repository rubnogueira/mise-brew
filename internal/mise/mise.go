package mise

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/BurntSushi/toml"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/rubnogueira/mise-brew/internal/paths"
	"github.com/rubnogueira/mise-brew/internal/ui"
)

// RecursionEnv is set on every mise child so a mise that shells out to `brew`
// never re-enters mise-brew in a loop.
const RecursionEnv = "MISEBREW_ACTIVE"

func MiseCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(paths.MiseBin(), args...)
	cmd.Env = append(os.Environ(), RecursionEnv+"=1")
	if e := miseEnv(); e != "" {
		cmd.Env = append(cmd.Env, "MISE_ENV="+e)
	}
	return cmd
}

// miseEnv returns the MISE_ENV for mise children. MISEBREW_ENV makes `use -e`
// write to mise.<ENV>.toml, so every other call (config ls, status, prune,
// install) must load that environment too or those declarations are invisible.
func miseEnv() string {
	e := os.Getenv("MISEBREW_ENV")
	cur := os.Getenv("MISE_ENV")
	if e == "" {
		return ""
	}
	for _, v := range strings.Split(cur, ",") {
		if strings.TrimSpace(v) == e {
			return cur
		}
	}
	if strings.TrimSpace(cur) == "" {
		return e
	}
	return cur + "," + e
}

// RunMise runs mise attached to the terminal and returns its exit code.
func RunMise(args ...string) int {
	if ui.Verbose || ui.Debug {
		fmt.Fprintln(os.Stderr, ui.Paint(ui.ColorErr, "2", "mise "+strings.Join(args, " ")))
	}
	cmd := MiseCmd(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
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

func MiseOutput(args ...string) ([]byte, error) {
	ui.Odebug("mise %s", strings.Join(args, " "))
	cmd := MiseCmd(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, err
}

func MiseCombined(args ...string) ([]byte, error) {
	ui.Odebug("mise %s", strings.Join(args, " "))
	return MiseCmd(args...).CombinedOutput()
}

// ScopeArgs returns where `mise bootstrap packages use` writes new entries.
// MISEBREW_CONFIG: "global" (default), "local", or a path. MISEBREW_ENV adds -e.
func ScopeArgs() []string {
	var out []string
	switch v := os.Getenv("MISEBREW_CONFIG"); v {
	case "", "global":
		out = append(out, "-g")
	case "local":
	default:
		out = append(out, "-p", v)
	}
	if e := os.Getenv("MISEBREW_ENV"); e != "" {
		out = append(out, "-e", e)
	}
	return out
}

// TapScopeArgs mirrors scopeArgs for `mise bootstrap packages brew tap`.
func TapScopeArgs() []string {
	switch v := os.Getenv("MISEBREW_CONFIG"); v {
	case "", "global":
		return nil
	case "local":
		return []string{"-l"}
	default:
		return []string{"-p", v}
	}
}

func RecordEnabled() bool {
	v := strings.ToLower(os.Getenv("MISEBREW_RECORD"))
	return !(v == "0" || v == "false" || v == "no")
}

// --- config files -----------------------------------------------------------

// ConfigFiles lists the mise config files that may declare packages. A file
// reached through several paths (symlinks) is listed once, under the first
// path seen, so it is never edited twice.
func ConfigFiles() []string {
	seen := map[string]bool{}
	var files []string
	add := func(p string) {
		if p == "" || !paths.FileExists(p) {
			return
		}
		key := p
		if real, err := filepath.EvalSymlinks(p); err == nil {
			key = real
		}
		if seen[key] {
			return
		}
		seen[key] = true
		files = append(files, p)
	}
	if v := os.Getenv("MISEBREW_CONFIG"); v != "" && v != "global" && v != "local" {
		add(v)
	}
	if out, err := MiseOutput("config", "ls", "--json"); err == nil {
		var entries []struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(out, &entries) == nil {
			for _, e := range entries {
				add(e.Path)
			}
		}
	}
	cfg := os.Getenv("MISE_CONFIG_DIR")
	if cfg == "" {
		x := os.Getenv("XDG_CONFIG_HOME")
		if x == "" {
			x = filepath.Join(paths.HomeDir(), ".config")
		}
		cfg = filepath.Join(x, "mise")
	}
	add(filepath.Join(cfg, "config.toml"))
	return files
}

type Declaration struct {
	File     string
	Line     int // 0-based
	Text     string
	Editable bool
	Key      string // package key, e.g. "brew:jq"
	End      int    // 0-based exclusive last line; a sub-table spans several lines
}

// lineEnd returns the exclusive end line of d.
func (d Declaration) lineEnd() int {
	if d.End <= d.Line {
		return d.Line + 1
	}
	return d.End
}

var (
	tableHeaderRe = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)
	arrayHeaderRe = regexp.MustCompile(`^\s*\[\[\s*([^\[\]]+?)\s*\]\]\s*(#.*)?$`)
)

func normalizeTOMLKey(k string) string {
	k = strings.TrimSpace(k)
	var parts []string
	for _, p := range splitDotted(k) {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"'`)
		parts = append(parts, p)
	}
	return strings.Join(parts, ".")
}

// splitDotted splits a TOML dotted key respecting quotes.
func splitDotted(k string) []string {
	var parts []string
	var cur strings.Builder
	var q rune
	for _, r := range k {
		switch {
		case q != 0:
			cur.WriteRune(r)
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
			cur.WriteRune(r)
		case r == '.':
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	return parts
}

// keyOfLine returns the (normalized) key of a `key = value` line, if any.
func keyOfLine(line string) (string, string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", "", false
	}
	// find '=' outside quotes
	var q rune
	for i, r := range t {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '=':
			return normalizeTOMLKey(t[:i]), strings.TrimSpace(t[i+1:]), true
		}
	}
	return "", "", false
}

// tomlScan tracks whether a TOML value continues onto the next line: inside a
// multi-line string, or with an array or inline table still open.
type tomlScan struct {
	ml    string // `"""` or `'''` while inside a multi-line string
	depth int    // unclosed [ and { outside strings
}

func (s *tomlScan) open() bool { return s.ml != "" || s.depth > 0 }

func (s *tomlScan) feed(text string) {
	for i := 0; i < len(text); {
		if s.ml != "" {
			switch {
			case s.ml == `"""` && text[i] == '\\':
				i += 2
			case strings.HasPrefix(text[i:], s.ml):
				// up to two quotes may directly precede the closing delimiter
				j := i + 3
				for j < len(text) && j < i+5 && text[j] == s.ml[0] {
					j++
				}
				i, s.ml = j, ""
			default:
				i++
			}
			continue
		}
		switch c := text[i]; {
		case c == '#':
			return
		case strings.HasPrefix(text[i:], `"""`) || strings.HasPrefix(text[i:], `'''`):
			s.ml = text[i : i+3]
			i += 3
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(text) && text[j] != c {
				if c == '"' && text[j] == '\\' {
					j++
				}
				j++
			}
			i = j + 1
		case c == '[' || c == '{':
			s.depth++
			i++
		case c == ']' || c == '}':
			if s.depth > 0 {
				s.depth--
			}
			i++
		default:
			i++
		}
	}
}

// FindDeclarations finds `"<pkg>" = ...` entries of [bootstrap.packages] (and
// [bootstrap.packages."<pkg>"] sub-tables) in all config files.
func FindDeclarations(pkg string, files []string) []Declaration {
	var out []Declaration
	target := "bootstrap.packages." + pkg
	quoted := func(line string) bool {
		return strings.Contains(line, `"`+pkg+`"`) || strings.Contains(line, `'`+pkg+`'`)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		table := ""
		var cont tomlScan // a multi-line value being skipped
		contKey := ""
		sub, subLast := -1, 0 // open [bootstrap.packages."<pkg>"] declaration and its last content line
		closeSub := func() {
			if sub >= 0 {
				out[sub].End = subLast + 1
				out[sub].Text = strings.Join(lines[out[sub].Line:subLast+1], "\n")
				sub = -1
			}
		}
		for i, line := range lines {
			if cont.open() {
				inString := cont.ml != ""
				cont.feed(line)
				if sub >= 0 {
					subLast = i
				}
				if !inString && strings.HasPrefix(contKey, "bootstrap") && quoted(line) {
					out = append(out, Declaration{File: f, Line: i, Text: line, Key: pkg, End: i + 1})
				}
				continue
			}
			m := arrayHeaderRe.FindStringSubmatch(line)
			isArray := m != nil
			if m == nil {
				m = tableHeaderRe.FindStringSubmatch(line)
			}
			if m != nil {
				closeSub()
				table = normalizeTOMLKey(m[1])
				switch {
				case table == target && !isArray:
					out = append(out, Declaration{File: f, Line: i, Text: line, Editable: true, Key: pkg, End: i + 1})
					sub, subLast = len(out)-1, i
				case table == target || strings.HasPrefix(table, target+"."):
					out = append(out, Declaration{File: f, Line: i, Text: line, Key: pkg, End: i + 1})
				}
				continue
			}
			key, val, ok := keyOfLine(line)
			if !ok {
				t := strings.TrimSpace(line)
				if t == "" || strings.HasPrefix(t, "#") {
					continue
				}
				if sub >= 0 {
					subLast = i
				}
				if quoted(line) {
					out = append(out, Declaration{File: f, Line: i, Text: line, Key: pkg, End: i + 1})
				}
				continue
			}
			if sub >= 0 {
				subLast = i
			}
			full := key
			if table != "" {
				full = table + "." + key
			}
			var s tomlScan
			s.feed(val)
			if s.open() {
				cont, contKey = s, full
			}
			if full == target {
				// a value spanning lines can't be removed line by line
				out = append(out, Declaration{File: f, Line: i, Text: line, Editable: !s.open(), Key: pkg, End: i + 1})
			} else if quoted(line) && strings.HasPrefix(full, "bootstrap") {
				out = append(out, Declaration{File: f, Line: i, Text: line, Key: pkg, End: i + 1})
			}
		}
		closeSub()
	}
	return out
}

func stripComment(v string) string {
	var q rune
	for i, r := range v {
		switch {
		case q != 0:
			if r == q {
				q = 0
			}
		case r == '"' || r == '\'':
			q = r
		case r == '#':
			return v[:i]
		}
	}
	return v
}

// configBackup is a config file's content before mise-brew rewrote it.
type configBackup struct {
	Path string // real path (symlinks resolved)
	Data []byte
	Mode os.FileMode
}

// RemoveDeclarations deletes the given lines, writing through symlinks atomically.
func RemoveDeclarations(decls []Declaration) error {
	_, err := removeDeclarationsBackup(decls)
	return err
}

// removeDeclarationsBackup removes lines and returns the original contents per real file.
func removeDeclarationsBackup(decls []Declaration) (map[string][]byte, error) {
	backups, err := RewriteDeclarations(decls)
	out := map[string][]byte{}
	for _, b := range backups {
		out[b.Path] = b.Data
	}
	return out, err
}

// RewriteDeclarations removes the editable declarations, editing each real
// file once. Every file is checked before any is written: the recorded lines
// must still be there, and the result must still be TOML without the key. If
// a write fails, the files already rewritten are restored. It returns the
// original contents in the order the files were written.
func RewriteDeclarations(decls []Declaration) ([]configBackup, error) {
	var order []string
	byReal := map[string][]Declaration{}
	for _, d := range decls {
		if !d.Editable {
			continue
		}
		real, err := filepath.EvalSymlinks(d.File)
		if err != nil {
			return nil, err
		}
		if _, ok := byReal[real]; !ok {
			order = append(order, real)
		}
		byReal[real] = append(byReal[real], d)
	}
	var backups []configBackup
	var contents [][]byte
	for _, real := range order {
		fi, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(real)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		drop := map[int]bool{}
		keys := map[string]bool{}
		for _, d := range byReal[real] {
			end := d.lineEnd()
			if d.Line < 0 || end > len(lines) || strings.Join(lines[d.Line:end], "\n") != d.Text {
				return nil, fmt.Errorf("%s changed while editing it (line %d is no longer %q); not editing it", paths.Tildify(d.File), d.Line+1, strings.TrimSpace(d.Text))
			}
			for n := d.Line; n < end; n++ {
				drop[n] = true
			}
			if d.Key != "" {
				keys[d.Key] = true
			}
		}
		var kept []string
		for i, l := range lines {
			if !drop[i] {
				kept = append(kept, l)
			}
		}
		edited := []byte(strings.Join(kept, "\n"))
		if err := checkEditedTOML(data, edited, keys); err != nil {
			return nil, fmt.Errorf("%s: %v; not editing it", paths.Tildify(real), err)
		}
		backups = append(backups, configBackup{real, data, fi.Mode().Perm()})
		contents = append(contents, edited)
	}
	for i, b := range backups {
		if err := writeConfigAtomic(b.Path, contents[i], b.Mode); err != nil {
			restoreConfigs(backups[:i], nil)
			return nil, err
		}
	}
	return backups, nil
}

// checkEditedTOML is a safety net for the line-based edit: when the original
// parses, the edited content must parse too and no longer declare keys.
func checkEditedTOML(orig, edited []byte, keys map[string]bool) error {
	var m map[string]any
	if _, err := toml.Decode(string(orig), &m); err != nil {
		return nil // nothing to compare against
	}
	m = nil
	if _, err := toml.Decode(string(edited), &m); err != nil {
		return fmt.Errorf("the edit would leave invalid TOML (%v)", err)
	}
	b, _ := m["bootstrap"].(map[string]any)
	pkgs, _ := b["packages"].(map[string]any)
	for k := range keys {
		if _, ok := pkgs[k]; ok {
			return fmt.Errorf("%s would still be declared", k)
		}
	}
	return nil
}

// writeConfigAtomic replaces path with data via a fresh temp file in the same
// directory (never following a planted symlink), keeping mode.
func writeConfigAtomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".mise-brew-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// restoreConfigs writes the backups back, most recent edit first.
func restoreConfigs(backups []configBackup, restored func(path string)) {
	for i := len(backups) - 1; i >= 0; i-- {
		b := backups[i]
		if err := writeConfigAtomic(b.Path, b.Data, b.Mode); err != nil {
			ui.Onoe("could not restore %s: %v", paths.Tildify(b.Path), err)
			continue
		}
		if restored != nil {
			restored(b.Path)
		}
	}
}

// Undeclare removes pkg from every config file and reports what it did.
// It returns a function that puts the removed lines back; calling it again
// does nothing.
func Undeclare(pkg string) (restore func()) {
	restore = func() {}
	decls := FindDeclarations(pkg, ConfigFiles())
	if len(decls) == 0 {
		return
	}
	backups, err := RewriteDeclarations(decls)
	if err != nil {
		ui.Odie("could not edit mise config: %v", err)
	}
	var once sync.Once
	restore = func() {
		once.Do(func() {
			restoreConfigs(backups, func(f string) {
				fmt.Printf("Restored %s in %s\n", pkg, paths.Tildify(f))
			})
		})
	}
	for _, d := range decls {
		if d.Editable {
			fmt.Printf("Removed %s from %s\n", pkg, paths.Tildify(d.File))
		} else {
			ui.Opoo("%s is declared in %s:%d in a form mise-brew can't edit; remove it by hand:\n  %s", pkg, paths.Tildify(d.File), d.Line+1, strings.TrimSpace(d.Text))
		}
	}
	return restore
}

func IsDeclared(pkg string, files []string) bool {
	for _, d := range FindDeclarations(pkg, files) {
		if d.Editable {
			return true
		}
	}
	return false
}

// --- status -----------------------------------------------------------------

type PkgStatus struct {
	Package          string `json:"package"`
	RequestedVersion string `json:"requested_version"`
	DesiredState     string `json:"desired_state"`
	State            string `json:"state"`
	InstalledVersion string `json:"installed_version"`
}

type ManagerStatus struct {
	Available bool        `json:"available"`
	Packages  []PkgStatus `json:"packages"`
}

var (
	StatusCache map[string]ManagerStatus
	StatusErr   error
)

// PackagesStatus returns mise's view of declared packages. On failure it
// returns an empty map; callers that must not act on missing data (e.g.
// autoremove) check packagesStatusErr.
func PackagesStatus() map[string]ManagerStatus {
	if StatusCache != nil {
		return StatusCache
	}
	StatusCache = map[string]ManagerStatus{}
	out, err := MiseOutput("bootstrap", "packages", "status", "--json")
	if err == nil {
		err = json.Unmarshal(out, &StatusCache)
	}
	if err != nil {
		StatusErr = err
		ui.Odebug("status failed: %v", err)
	}
	return StatusCache
}

// PackagesStatusErr reports why packagesStatus could not read mise's status.
func PackagesStatusErr() error {
	PackagesStatus()
	return StatusErr
}

func SortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
