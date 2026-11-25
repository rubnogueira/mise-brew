package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// miseCaskRecord is what mise writes to Caskroom/<token>/<version>/.mise-cask.toml.
type miseCaskRecord struct {
	Version           string           `toml:"version"`
	AutoUpdates       bool             `toml:"auto_updates"`
	Apps              []any            `toml:"apps"`
	MetadataOnlyApps  []any            `toml:"metadata_only_apps"`
	Binaries          []any            `toml:"binaries"`
	Fonts             []any            `toml:"fonts"`
	Completions       []any            `toml:"completions"`
	FlightDirectories []any            `toml:"flight_directories"`
	Generic           []any            `toml:"generic"`
	PkgIDs            []string         `toml:"pkg_ids"`
	PruneSafe         bool             `toml:"prune_safe"`
	PruneBlocker      string           `toml:"prune_blocker"`
	Targets           []map[string]any `toml:"targets"`
}

func readCaskRecord(token, version string) *miseCaskRecord {
	p := filepath.Join(caskroom(), token, version, ".mise-cask.toml")
	var r miseCaskRecord
	if _, err := toml.DecodeFile(p, &r); err != nil {
		return nil
	}
	return &r
}

// pathsOf extracts paths from record entries that are strings or tables.
func pathsOf(xs []any) []string {
	var out []string
	for _, x := range xs {
		switch v := x.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			for _, k := range []string{"path", "target", "destination"} {
				if s, ok := v[k].(string); ok {
					out = append(out, s)
					break
				}
			}
		}
	}
	return out
}

// caskStanzas returns the uninstall or zap directives from the API record.
func caskStanzas(c *Cask, kind string) []map[string]json.RawMessage {
	var out []map[string]json.RawMessage
	if c == nil {
		return nil
	}
	for _, raw := range c.Artifacts {
		var m map[string][]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for _, entry := range m[kind] {
			var d map[string]json.RawMessage
			if json.Unmarshal(entry, &d) == nil {
				out = append(out, d)
			}
		}
	}
	return out
}

func stringsOf(raw json.RawMessage) []string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	var arr []any
	if json.Unmarshal(raw, &arr) == nil {
		var out []string
		for _, a := range arr {
			if s, ok := a.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// caskOps performs (or, in dry-run, describes) cask uninstall operations.
type caskOps struct {
	seen      map[string]bool
	dry       bool
	token     string
	stagedDir string
	failed    bool
}

func (o *caskOps) say(format string, a ...any) {
	if o.dry {
		fmt.Println("(dry run) " + fmt.Sprintf(format, a...))
		return
	}
	ohai(format, a...)
}

func (o *caskOps) run(sudo bool, name string, args ...string) error {
	if o.dry {
		return nil
	}
	if sudo && os.Geteuid() != 0 {
		args = append([]string{"-E", "--", name}, args...)
		name = "sudo"
	}
	odebug("%s %s", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

var undeletable = map[string]bool{
	"/": true, "/Applications": true, "/Library": true, "/System": true, "/Users": true,
	"/bin": true, "/sbin": true, "/usr": true, "/usr/bin": true, "/usr/local": true,
	"/usr/local/bin": true, "/opt": true, "/opt/homebrew": true, "/private": true, "/var": true,
	"/etc": true, "/tmp": true, "/Volumes": true,
}

func expandUserPath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), strings.TrimPrefix(p, "~"))
	}
	return p
}

// expandCaskPath resolves the placeholders cask stanzas use.
func expandCaskPath(p string) string {
	appdir := os.Getenv("MISE_BREW_CASK_OPT_APPDIR")
	if appdir == "" {
		appdir = "/Applications"
	}
	p = strings.NewReplacer(
		"$HOMEBREW_PREFIX", brewPrefix(), "{{HOMEBREW_PREFIX}}", brewPrefix(),
		"$APPDIR", appdir, "{{appdir}}", appdir, "$HOME", homeDir(),
	).Replace(p)
	return expandUserPath(p)
}

// safePaths expands globs and drops paths Homebrew refuses to delete.
func (o *caskOps) safePaths(patterns []string) []string {
	var out []string
	for _, pat := range patterns {
		pat = expandCaskPath(pat)
		if !filepath.IsAbs(pat) {
			opoo("Skipping relative path %s", pat)
			continue
		}
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			m = filepath.Clean(m)
			if undeletable[m] || m == homeDir() || m == filepath.Join(homeDir(), "Library") || strings.Count(m, "/") < 2 {
				opoo("Skipping protected path %s", m)
				continue
			}
			out = append(out, m)
		}
	}
	return out
}

func (o *caskOps) removePaths(label string, paths []string) {
	for _, p := range o.safePaths(paths) {
		o.say("Removing %s '%s'", label, p)
		if o.dry {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			if err := o.run(true, "/bin/rm", "-rf", "--", p); err != nil {
				onoe("could not remove %s: %v", p, err)
				o.failed = true
			}
		}
	}
}

func (o *caskOps) trashPaths(paths []string) {
	trash := filepath.Join(homeDir(), ".Trash")
	for _, p := range o.safePaths(paths) {
		o.say("Trashing '%s'", p)
		if o.dry {
			continue
		}
		dst := filepath.Join(trash, filepath.Base(p))
		if fileExists(dst) {
			dst += " " + time.Now().Format("15.04.05")
		}
		if err := os.Rename(p, dst); err != nil {
			if err := o.run(true, "/bin/mv", "--", p, dst); err != nil {
				opoo("could not trash %s: %v", p, err)
			}
		}
	}
}

func (o *caskOps) rmdirPaths(paths []string) {
	for _, p := range o.safePaths(paths) {
		if !isDir(p) {
			continue
		}
		_ = os.Remove(filepath.Join(p, ".DS_Store"))
		entries, _ := os.ReadDir(p)
		if len(entries) > 0 {
			continue
		}
		o.say("Removing directory '%s'", p)
		if !o.dry {
			if os.Remove(p) != nil {
				_ = o.run(true, "/bin/rmdir", "--", p)
			}
		}
	}
}

func (o *caskOps) launchctl(labels []string) {
	uid := strconv.Itoa(os.Getuid())
	for _, l := range labels {
		user := exec.Command("launchctl", "print", "gui/"+uid+"/"+l).Run() == nil
		system := exec.Command("launchctl", "print", "system/"+l).Run() == nil
		if user {
			o.say("Removing launchctl service %s", l)
			_ = o.run(false, "launchctl", "bootout", "gui/"+uid+"/"+l)
		}
		if system {
			o.say("Removing launchctl service %s", l)
			_ = o.run(true, "launchctl", "bootout", "system/"+l)
		}
		for _, dir := range []string{filepath.Join(homeDir(), "Library", "LaunchAgents"), "/Library/LaunchAgents", "/Library/LaunchDaemons"} {
			p := filepath.Join(dir, l+".plist")
			if fileExists(p) {
				o.removePaths("launchctl plist", []string{p})
			}
		}
	}
}

func osascript(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return strings.TrimSpace(string(out)), err
}

func (o *caskOps) quit(bundleIDs []string) {
	for _, id := range bundleIDs {
		running, _ := osascript(fmt.Sprintf(`application id %q is running`, id))
		if running != "true" {
			continue
		}
		o.say("Quitting application '%s'...", id)
		if o.dry {
			continue
		}
		_, _ = osascript(fmt.Sprintf(`tell application id %q to quit`, id))
		for i := 0; i < 10; i++ {
			if r, _ := osascript(fmt.Sprintf(`application id %q is running`, id)); r != "true" {
				break
			}
			time.Sleep(time.Second)
		}
	}
}

func (o *caskOps) signal(raw json.RawMessage) {
	var pairs [][]string
	if json.Unmarshal(raw, &pairs) != nil {
		var one []string
		if json.Unmarshal(raw, &one) == nil && len(one) == 2 {
			pairs = [][]string{one}
		}
	}
	for _, p := range pairs {
		if len(p) != 2 {
			continue
		}
		sig, id := p[0], p[1]
		pids, _ := osascript(fmt.Sprintf(`tell application "System Events" to get the unix id of every process whose bundle identifier is %q`, id))
		for _, pid := range strings.Split(pids, ",") {
			pid = strings.TrimSpace(pid)
			if pid == "" {
				continue
			}
			o.say("Signalling '%s' to application ID '%s' (pid %s)", sig, id, pid)
			_ = o.run(false, "kill", "-"+sig, pid)
		}
	}
}

func (o *caskOps) loginItems(names []string) {
	for _, n := range names {
		o.say("Removing login item %s", n)
		if !o.dry {
			_, _ = osascript(fmt.Sprintf(`tell application "System Events" to delete every login item whose name is %q`, n))
		}
	}
}

func (o *caskOps) kext(ids []string) {
	for _, id := range ids {
		o.say("Unloading kernel extension %s", id)
		_ = o.run(true, "kextunload", "-b", id)
		out, _ := exec.Command("kextfind", "-b", id).Output()
		var paths []string
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l != "" {
				paths = append(paths, l)
			}
		}
		o.removePaths("kernel extension", paths)
	}
}

func (o *caskOps) script(raw json.RawMessage) {
	var s struct {
		Executable  string   `json:"executable"`
		Args        []string `json:"args"`
		Sudo        bool     `json:"sudo"`
		MustSucceed *bool    `json:"must_succeed"`
	}
	if json.Unmarshal(raw, &s) != nil {
		var exe string
		if json.Unmarshal(raw, &exe) != nil {
			return
		}
		s.Executable = exe
	}
	exe := expandUserPath(s.Executable)
	if !filepath.IsAbs(exe) {
		exe = filepath.Join(o.stagedDir, exe)
	}
	o.say("Running uninstall script %s", s.Executable)
	if err := o.run(s.Sudo, exe, s.Args...); err != nil && (s.MustSucceed == nil || *s.MustSucceed) {
		opoo("uninstall script %s failed: %v", s.Executable, err)
	}
}

func (o *caskOps) pkgutil(patterns []string) {
	for _, pat := range patterns {
		out, _ := exec.Command("pkgutil", "--pkgs="+pat).Output()
		for _, id := range strings.Fields(string(out)) {
			o.forgetPkg(id)
		}
	}
}

func (o *caskOps) forgetPkg(id string) {
	info, err := exec.Command("pkgutil", "--pkg-info-plist", id).Output()
	if err != nil {
		return
	}
	volume, location := "/", ""
	if v := plistString(info, "volume"); v != "" {
		volume = v
	}
	location = plistString(info, "install-location")
	root := filepath.Join(volume, location)
	files, _ := exec.Command("pkgutil", "--only-files", "--files", id).Output()
	dirs, _ := exec.Command("pkgutil", "--only-dirs", "--files", id).Output()
	var fl []string
	for _, f := range strings.Split(strings.TrimSpace(string(files)), "\n") {
		if f != "" {
			fl = append(fl, filepath.Join(root, f))
		}
	}
	o.say("Uninstalling packages with sudo; the password may be necessary: %s (%d files)", id, len(fl))
	if !o.dry && len(fl) > 0 {
		cmd := exec.Command("sudo", "/usr/bin/xargs", "-0", "/bin/rm", "-f", "--")
		cmd.Stdin = bytes.NewBufferString(strings.Join(fl, "\x00"))
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			opoo("removing files of %s: %v", id, err)
		}
	}
	var dl []string
	for _, d := range strings.Split(strings.TrimSpace(string(dirs)), "\n") {
		if d != "" && d != "." {
			dl = append(dl, filepath.Join(root, d))
		}
	}
	sort.Slice(dl, func(i, j int) bool { return len(dl[i]) > len(dl[j]) })
	for _, d := range dl {
		if undeletable[d] {
			continue
		}
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 && !o.dry {
			_ = o.run(true, "/bin/rmdir", "--", d)
		}
	}
	_ = o.run(true, "pkgutil", "--forget", id)
}

// plistString pulls a <key>k</key><string>v</string> value out of XML plist output.
func plistString(plist []byte, key string) string {
	s := string(plist)
	i := strings.Index(s, "<key>"+key+"</key>")
	if i < 0 {
		return ""
	}
	rest := s[i:]
	a := strings.Index(rest, "<string>")
	b := strings.Index(rest, "</string>")
	if a < 0 || b < a {
		return ""
	}
	return rest[a+8 : b]
}

// runStanzas executes uninstall/zap directives in Homebrew's order.
func (o *caskOps) runStanzas(stanzas []map[string]json.RawMessage) {
	order := []string{"early_script", "launchctl", "quit", "signal", "login_item", "kext", "script", "pkgutil", "delete", "trash", "rmdir"}
	for _, key := range order {
		for _, st := range stanzas {
			raw, ok := st[key]
			if !ok {
				continue
			}
			switch key {
			case "early_script", "script":
				o.script(raw)
			case "launchctl":
				o.launchctl(stringsOf(raw))
			case "quit":
				o.quit(stringsOf(raw))
			case "signal":
				o.signal(raw)
			case "login_item":
				o.loginItems(stringsOf(raw))
			case "kext":
				o.kext(stringsOf(raw))
			case "pkgutil":
				o.pkgutil(stringsOf(raw))
			case "delete":
				o.removePaths("file", stringsOf(raw))
			case "trash":
				o.trashPaths(stringsOf(raw))
			case "rmdir":
				o.rmdirPaths(stringsOf(raw))
			}
		}
	}
}

// nativeUninstallCask removes a cask the way `brew uninstall --cask` does:
// uninstall stanza, the artifacts mise recorded, then the Caskroom entry.
func nativeUninstallCask(token string, zap, dry bool) error {
	c := lookupCask(token)
	version := currentCaskVersion(token)
	staged := filepath.Join(caskroom(), token, version)
	o := &caskOps{dry: dry, token: token, stagedDir: staged}
	if !dry {
		ohai("Uninstalling Cask %s", token)
	}
	o.runStanzas(caskStanzas(c, "uninstall"))

	if rec := readCaskRecord(token, version); rec != nil {
		for _, id := range rec.PkgIDs {
			if out, err := exec.Command("pkgutil", "--pkg-info", id).Output(); err == nil && len(out) > 0 {
				o.forgetPkg(id)
			}
		}
		o.removeArtifacts("App", pathsOf(rec.Apps))
		o.removeArtifacts("Binary", pathsOf(rec.Binaries))
		o.removeArtifacts("Font", pathsOf(rec.Fonts))
		o.removeArtifacts("Completion", pathsOf(rec.Completions))
		o.removeArtifacts("Artifact", pathsOf(rec.Generic))
		for _, t := range rec.Targets {
			if u, ok := t["uninstall"].(bool); ok && !u {
				continue
			}
			if p, ok := t["path"].(string); ok {
				o.removeArtifacts("Target", []string{p})
			}
		}
	} else if c != nil {
		for _, a := range caskArtifacts(c) {
			if a.target != "" {
				o.removeArtifacts(artifactSingular[a.kind], []string{a.target})
			}
		}
	}
	if zap {
		if !dry {
			ohai("Implied `brew uninstall --cask %s`", token)
		}
		o.runStanzas(caskStanzas(c, "zap"))
	}
	room := filepath.Join(caskroom(), token)
	o.say("Purging files for version %s of Cask %s", version, token)
	if !dry {
		if err := os.RemoveAll(room); err != nil {
			if err := o.run(true, "/bin/rm", "-rf", "--", room); err != nil {
				return err
			}
		}
		fmt.Printf("🍺  %s was successfully uninstalled!\n", token)
	}
	if o.failed {
		return fmt.Errorf("some files of %s could not be removed", token)
	}
	return nil
}

// removeArtifacts removes symlinks and recorded artifacts (apps, fonts, ...).
func (o *caskOps) removeArtifacts(kind string, paths []string) {
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	for _, p := range paths {
		p = expandCaskPath(p)
		if o.seen[p] {
			continue
		}
		o.seen[p] = true
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		verb := "Removing"
		if fi.Mode()&os.ModeSymlink != 0 {
			verb = "Unlinking"
		}
		if undeletable[filepath.Clean(p)] {
			continue
		}
		o.say("%s %s '%s'", verb, kind, p)
		if o.dry {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			if err := o.run(true, "/bin/rm", "-rf", "--", p); err != nil {
				onoe("could not remove %s: %v", p, err)
				o.failed = true
			}
		}
	}
}
