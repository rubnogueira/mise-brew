package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// recursionEnv is set on every mise child so a mise that shells out to `brew`
// never re-enters mise-brew in a loop.
const recursionEnv = "MISEBREW_ACTIVE"

func miseCmd(args ...string) *exec.Cmd {
	cmd := exec.Command(miseBin(), args...)
	cmd.Env = append(os.Environ(), recursionEnv+"=1")
	return cmd
}

// runMise runs mise attached to the terminal and returns its exit code.
func runMise(args ...string) int {
	if verbose || debug {
		fmt.Fprintln(os.Stderr, paint(colorErr, "2", "mise "+strings.Join(args, " ")))
	}
	cmd := miseCmd(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		onoe("failed to run mise: %v", err)
		return 1
	}
	return 0
}

func miseOutput(args ...string) ([]byte, error) {
	odebug("mise %s", strings.Join(args, " "))
	cmd := miseCmd(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		return out, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, err
}

func miseCombined(args ...string) ([]byte, error) {
	odebug("mise %s", strings.Join(args, " "))
	return miseCmd(args...).CombinedOutput()
}

// scopeArgs returns where `mise bootstrap packages use` writes new entries.
// MISEBREW_CONFIG: "global" (default), "local", or a path. MISEBREW_ENV adds -e.
func scopeArgs() []string {
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

// tapScopeArgs mirrors scopeArgs for `mise bootstrap packages brew tap`.
func tapScopeArgs() []string {
	switch v := os.Getenv("MISEBREW_CONFIG"); v {
	case "", "global":
		return nil
	case "local":
		return []string{"-l"}
	default:
		return []string{"-p", v}
	}
}

func recordEnabled() bool {
	v := strings.ToLower(os.Getenv("MISEBREW_RECORD"))
	return !(v == "0" || v == "false" || v == "no")
}

// --- config files -----------------------------------------------------------

func configFiles() []string {
	seen := map[string]bool{}
	var files []string
	add := func(p string) {
		if p == "" || seen[p] || !fileExists(p) {
			return
		}
		seen[p] = true
		files = append(files, p)
	}
	if v := os.Getenv("MISEBREW_CONFIG"); v != "" && v != "global" && v != "local" {
		add(v)
	}
	if out, err := miseOutput("config", "ls", "--json"); err == nil {
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
			x = filepath.Join(homeDir(), ".config")
		}
		cfg = filepath.Join(x, "mise")
	}
	add(filepath.Join(cfg, "config.toml"))
	return files
}

type declaration struct {
	File     string
	Line     int // 0-based
	Text     string
	Editable bool
}

var tableHeaderRe = regexp.MustCompile(`^\s*\[\s*([^\[\]]+?)\s*\]\s*(#.*)?$`)

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

// findDeclarations finds `"<pkg>" = ...` entries of [bootstrap.packages] in all config files.
func findDeclarations(pkg string, files []string) []declaration {
	var out []declaration
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(data), "\n")
		table := ""
		for i, line := range lines {
			if m := tableHeaderRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(strings.TrimSpace(line), "[[") {
				table = normalizeTOMLKey(m[1])
				continue
			}
			key, val, ok := keyOfLine(line)
			if !ok {
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				if strings.Contains(line, `"`+pkg+`"`) || strings.Contains(line, `'`+pkg+`'`) {
					out = append(out, declaration{f, i, line, false})
				}
				continue
			}
			full := key
			if table != "" {
				full = table + "." + key
			}
			if full == "bootstrap.packages."+pkg {
				editable := !strings.HasPrefix(val, "{") || strings.HasSuffix(strings.TrimSpace(stripComment(val)), "}")
				out = append(out, declaration{f, i, line, editable})
			} else if strings.Contains(line, `"`+pkg+`"`) && (full == "bootstrap.packages" || strings.HasPrefix(full, "bootstrap")) {
				out = append(out, declaration{f, i, line, false})
			}
		}
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

// removeDeclarations deletes the given lines, writing through symlinks atomically.
func removeDeclarations(decls []declaration) error {
	_, err := removeDeclarationsBackup(decls)
	return err
}

// removeDeclarationsBackup removes lines and returns the original contents per real file.
func removeDeclarationsBackup(decls []declaration) (map[string][]byte, error) {
	backup := map[string][]byte{}
	byFile := map[string][]int{}
	for _, d := range decls {
		if d.Editable {
			byFile[d.File] = append(byFile[d.File], d.Line)
		}
	}
	for f, lineNos := range byFile {
		real, err := filepath.EvalSymlinks(f)
		if err != nil {
			return backup, err
		}
		fi, err := os.Stat(real)
		if err != nil {
			return backup, err
		}
		data, err := os.ReadFile(real)
		if err != nil {
			return backup, err
		}
		backup[real] = data
		lines := strings.Split(string(data), "\n")
		drop := map[int]bool{}
		for _, n := range lineNos {
			drop[n] = true
		}
		var kept []string
		for i, l := range lines {
			if !drop[i] {
				kept = append(kept, l)
			}
		}
		tmp := real + ".mise-brew.tmp"
		if err := os.WriteFile(tmp, []byte(strings.Join(kept, "\n")), fi.Mode().Perm()); err != nil {
			return backup, err
		}
		if err := os.Rename(tmp, real); err != nil {
			_ = os.Remove(tmp)
			return backup, err
		}
	}
	return backup, nil
}

// undeclare removes pkg from every config file and reports what it did.
// It returns a function that puts the removed lines back.
func undeclare(pkg string) (restore func()) {
	restore = func() {}
	decls := findDeclarations(pkg, configFiles())
	if len(decls) == 0 {
		return
	}
	backup, err := removeDeclarationsBackup(decls)
	if err != nil {
		odie("could not edit mise config: %v", err)
	}
	restore = func() {
		for f, data := range backup {
			if err := os.WriteFile(f, data, 0o644); err != nil {
				onoe("could not restore %s: %v", f, err)
				continue
			}
			fmt.Printf("Restored %s in %s\n", pkg, tildify(f))
		}
	}
	for _, d := range decls {
		if d.Editable {
			fmt.Printf("Removed %s from %s\n", pkg, tildify(d.File))
		} else {
			opoo("%s is declared in %s:%d in a form mise-brew can't edit; remove it by hand:\n  %s", pkg, tildify(d.File), d.Line+1, strings.TrimSpace(d.Text))
		}
	}
	return restore
}

func isDeclared(pkg string, files []string) bool {
	for _, d := range findDeclarations(pkg, files) {
		if d.Editable {
			return true
		}
	}
	return false
}

// --- status -----------------------------------------------------------------

type pkgStatus struct {
	Package          string `json:"package"`
	RequestedVersion string `json:"requested_version"`
	DesiredState     string `json:"desired_state"`
	State            string `json:"state"`
	InstalledVersion string `json:"installed_version"`
}

type managerStatus struct {
	Available bool        `json:"available"`
	Packages  []pkgStatus `json:"packages"`
}

var statusCache map[string]managerStatus

func packagesStatus() map[string]managerStatus {
	if statusCache != nil {
		return statusCache
	}
	statusCache = map[string]managerStatus{}
	out, err := miseOutput("bootstrap", "packages", "status", "--json")
	if err != nil {
		odebug("status failed: %v", err)
		return statusCache
	}
	_ = json.Unmarshal(out, &statusCache)
	return statusCache
}

// declaredSet returns declared (present) package names for a manager.
func declaredSet(manager string) map[string]bool {
	out := map[string]bool{}
	for _, p := range packagesStatus()[manager].Packages {
		if p.DesiredState == "" || p.DesiredState == "present" {
			out[baseName(p.Package)] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// pruneCandidates parses `mise bootstrap packages prune --dry-run` for a manager.
func pruneCandidates(manager string, force bool) ([]string, string) {
	args := []string{"bootstrap", "packages", "prune", "-m", manager, "--dry-run"}
	if force {
		args = append(args, "--force")
	}
	out, _ := miseCombined(args...)
	var names []string
	re := regexp.MustCompile(`(?m)^\s*(?:mise\s+)?(?:` + regexp.QuoteMeta(manager) + `:\s*)?remove\s+` + regexp.QuoteMeta(manager) + `:(\S+)`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		n := m[1]
		// strip trailing @version (names may contain @ themselves, e.g. openssl@3@3.6.4)
		if i := strings.LastIndex(n, "@"); i > 0 && !isFormulaInstalled(n) && !isCaskInstalled(n) {
			if rest := n[i+1:]; len(rest) > 0 && (rest[0] >= '0' && rest[0] <= '9' || rest == "latest") {
				n = n[:i]
			}
		}
		names = append(names, n)
	}
	return names, string(out)
}
