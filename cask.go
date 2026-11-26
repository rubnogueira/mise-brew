package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// undeletable lists system directories cask stanzas may never remove (a port
// of Homebrew's MacOS::SYSTEM_DIRS). Every top-level directory is protected
// as well, see protectedPath.
var undeletable = setOf(
	"/", "/Applications", "/Applications/Utilities", "/Incompatible Software",
	"/Library", "/Library/Application Support", "/Library/Audio", "/Library/Caches",
	"/Library/ColorPickers", "/Library/ColorSync", "/Library/Components",
	"/Library/Compositions", "/Library/Contextual Menu Items", "/Library/CoreMediaIO",
	"/Library/Desktop Pictures", "/Library/Developer", "/Library/Dictionaries",
	"/Library/DirectoryServices", "/Library/Documentation", "/Library/DriverExtensions",
	"/Library/Extensions", "/Library/Filesystems", "/Library/Fonts", "/Library/Frameworks",
	"/Library/Graphics", "/Library/Image Capture", "/Library/Input Methods",
	"/Library/Internet Plug-Ins", "/Library/Java", "/Library/Java/Extensions",
	"/Library/Java/JavaVirtualMachines", "/Library/Keyboard Layouts", "/Library/Keychains",
	"/Library/LaunchAgents", "/Library/LaunchDaemons", "/Library/Logs", "/Library/Managed Preferences",
	"/Library/Messages", "/Library/Modem Scripts", "/Library/OpenDirectory",
	"/Library/PDF Services", "/Library/Perl", "/Library/PreferencePanes",
	"/Library/Preferences", "/Library/Printers", "/Library/PrivilegedHelperTools",
	"/Library/Python", "/Library/QuickLook", "/Library/QuickTime", "/Library/Receipts",
	"/Library/Ruby", "/Library/Sandbox", "/Library/Screen Savers",
	"/Library/ScriptingAdditions", "/Library/Scripts", "/Library/Security",
	"/Library/Speech", "/Library/Spelling", "/Library/Spotlight", "/Library/StartupItems",
	"/Library/SystemExtensions", "/Library/SystemProfiler", "/Library/Updates",
	"/Library/User Pictures", "/Library/Video", "/Library/WebServer", "/Library/Widgets",
	"/Library/iTunes",
	"/Network", "/System", "/System/Library", "/System/Applications", "/System/Volumes",
	"/Users", "/Users/Shared", "/Volumes", "/bin", "/cores", "/dev", "/etc", "/home",
	"/net", "/opt", "/opt/homebrew", "/private", "/private/etc", "/private/tmp",
	"/private/var", "/private/var/db", "/private/var/folders", "/private/var/log",
	"/private/var/root", "/private/var/tmp", "/private/var/vm", "/sbin", "/tmp", "/var",
	"/var/db", "/var/folders", "/var/log", "/var/root", "/var/tmp",
	"/usr", "/usr/bin", "/usr/lib", "/usr/libexec", "/usr/sbin", "/usr/share",
	"/usr/share/man", "/usr/standalone", "/usr/X11", "/usr/X11R6",
	"/usr/local", "/usr/local/Caskroom", "/usr/local/Cellar", "/usr/local/Frameworks",
	"/usr/local/Homebrew", "/usr/local/Library", "/usr/local/bin", "/usr/local/etc",
	"/usr/local/include", "/usr/local/lib", "/usr/local/libexec", "/usr/local/opt",
	"/usr/local/sbin", "/usr/local/share", "/usr/local/share/doc", "/usr/local/share/info",
	"/usr/local/share/locale", "/usr/local/share/man", "/usr/local/share/man/man1",
	"/usr/local/share/man/man2", "/usr/local/share/man/man3", "/usr/local/share/man/man4",
	"/usr/local/share/man/man5", "/usr/local/share/man/man6", "/usr/local/share/man/man7",
	"/usr/local/share/man/man8", "/usr/local/share/man/man9", "/usr/local/share/man/mann",
	"/usr/local/var", "/usr/local/var/homebrew", "/usr/local/var/lib", "/usr/local/var/log",
	"/usr/local/var/run",
)

// undeletableInHome lists directories below $HOME cask stanzas may never
// remove (a port of Homebrew's MacOS::UNDELETABLE_PATHS); $HOME itself is
// protected too.
var undeletableInHome = setOf(
	"Applications", "Desktop", "Documents", "Downloads", "Mail", "Movies", "Music",
	"Music/iTunes", "News", "Pictures", "Public", "Sites", "Utilities",
	".Trash", ".config", ".local", ".local/bin", ".local/share", ".cache", ".ssh",
	"Library", "Library/Accessibility", "Library/Accounts", "Library/Address Book Plug-Ins",
	"Library/Application Scripts", "Library/Application Support",
	"Library/Application Support/Apple", "Library/Application Support/com.apple.TCC",
	"Library/Assistants", "Library/Audio", "Library/Automator", "Library/Autosave Information",
	"Library/Caches", "Library/Calendars", "Library/ColorPickers", "Library/Colors",
	"Library/Components", "Library/Compositions", "Library/Containers", "Library/Contextual Menu Items",
	"Library/Cookies", "Library/CoreData", "Library/Developer", "Library/Dictionaries",
	"Library/Extensions", "Library/Favorites", "Library/Fonts", "Library/Frameworks",
	"Library/Group Containers", "Library/HTTPStorages", "Library/iMovie", "Library/Input Methods",
	"Library/Internet Plug-Ins", "Library/iTunes", "Library/Keyboard Layouts", "Library/Keychains",
	"Library/LaunchAgents", "Library/Logs", "Library/Mail", "Library/Messages",
	"Library/PreferencePanes", "Library/Preferences", "Library/Printers", "Library/QuickLook",
	"Library/Safari", "Library/Saved Application State", "Library/Screen Savers",
	"Library/Services", "Library/Sounds", "Library/Speech", "Library/Spelling",
	"Library/Widgets", "Library/WebKit", "Library/Workflows",
)

func setOf(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[foldPath(x)] = true
	}
	return m
}

// foldPath normalises case on darwin, whose default filesystem ignores it
// (so /usr/LIB must count as /usr/lib).
func foldPath(p string) string {
	if runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// within reports whether p is dir or below it (case-folded on darwin).
func within(dir, p string) bool {
	d, q := foldPath(filepath.Clean(dir)), foldPath(filepath.Clean(p))
	return q == d || strings.HasPrefix(q, strings.TrimSuffix(d, "/")+"/")
}

// hasDotDot reports whether p has a ".." component.
func hasDotDot(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

// protectedPath reports whether Homebrew would refuse to delete p: system
// directories, every top-level directory, $HOME and its standard
// subdirectories, the brew prefix and its direct children, and any ancestor
// of $HOME or the prefix.
func protectedPath(p string) bool {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || protectedLexically(p) {
		return true
	}
	// Removal acts on the last component without following it, but its
	// parent is resolved: ~/link/lib with link -> /usr is /usr/lib.
	if parent, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		if r := filepath.Join(parent, filepath.Base(p)); r != p && protectedLexically(r) {
			return true
		}
	}
	return false
}

func protectedLexically(p string) bool {
	if strings.Count(p, "/") < 2 || undeletable[foldPath(p)] {
		return true
	}
	prefix := brewPrefix()
	if within(p, prefix) || foldPath(filepath.Dir(p)) == foldPath(prefix) {
		return true
	}
	for _, h := range homeDirs() {
		if within(p, h) {
			return true // $HOME or an ancestor of it
		}
		if within(h, p) && undeletableInHome[strings.TrimPrefix(foldPath(p), foldPath(h)+"/")] {
			return true
		}
	}
	return false
}

// homeDirs returns $HOME as given and symlink-resolved (if different).
func homeDirs() []string {
	h := homeDir()
	if h == "" {
		return nil
	}
	h = filepath.Clean(h)
	if r, err := filepath.EvalSymlinks(h); err == nil && r != h {
		return []string{h, r}
	}
	return []string{h}
}

// mayEscalate reports whether a failed removal of p may be retried with sudo:
// only for paths outside $HOME (and never when $HOME is unknown).
func mayEscalate(p string) bool {
	hs := homeDirs()
	for _, h := range hs {
		if within(h, p) {
			return false
		}
	}
	return len(hs) > 0
}

// expandUserPath expands a leading ~. When the home directory is unknown the
// path is returned unchanged (still relative), never rooted at /.
func expandUserPath(p string) string {
	s, _ := expandUserPathOK(p)
	return s
}

// expandUserPathOK is expandUserPath that also reports whether a ~ could be expanded.
func expandUserPathOK(p string) (string, bool) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h := homeDir()
		if h == "" {
			return p, false
		}
		return filepath.Join(h, strings.TrimPrefix(p, "~")), true
	}
	return p, true
}

// caskAppdir is where cask apps live (mise's appdir override, else /Applications).
func caskAppdir() string {
	if appdir := os.Getenv("MISE_BREW_CASK_OPT_APPDIR"); appdir != "" {
		return appdir
	}
	return "/Applications"
}

// expandCaskPath resolves the placeholders cask stanzas use.
func expandCaskPath(p string) string {
	s, _ := expandCaskPathOK(p)
	return s
}

// expandCaskPathOK is expandCaskPath that reports false (and leaves p alone)
// when p needs the home directory and it is unknown.
func expandCaskPathOK(p string) (string, bool) {
	h := homeDir()
	if h == "" && strings.Contains(strings.ReplaceAll(p, "$HOMEBREW_PREFIX", ""), "$HOME") {
		return p, false
	}
	appdir := caskAppdir()
	p = strings.NewReplacer(
		"$HOMEBREW_PREFIX", brewPrefix(), "{{HOMEBREW_PREFIX}}", brewPrefix(),
		"$APPDIR", appdir, "{{appdir}}", appdir, "$HOME", h,
	).Replace(p)
	return expandUserPathOK(p)
}

// safePaths expands globs and drops paths Homebrew refuses to delete.
func (o *caskOps) safePaths(patterns []string) []string {
	var out []string
	for _, raw := range patterns {
		pat, ok := expandCaskPathOK(raw)
		if !ok {
			opoo("Skipping %s: the home directory is unknown", raw)
			continue
		}
		if !filepath.IsAbs(pat) {
			opoo("Skipping relative path %s", pat)
			continue
		}
		if hasDotDot(pat) {
			opoo("Skipping path with '..' %s", pat)
			continue
		}
		if protectedPath(pat) {
			opoo("Skipping protected path %s", pat)
			continue
		}
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			m = filepath.Clean(m)
			if hasDotDot(m) || protectedPath(m) {
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
		if err := o.removeAll(p); err != nil {
			onoe("could not remove %s: %v", p, err)
			o.failed = true
		}
	}
}

// removeAll deletes p, retrying with sudo only for paths outside $HOME.
func (o *caskOps) removeAll(p string) error {
	err := os.RemoveAll(p)
	if err != nil && mayEscalate(p) {
		err = o.run(true, "/bin/rm", "-rf", "--", p)
	}
	return err
}

func (o *caskOps) trashPaths(paths []string) {
	h := homeDir()
	if h == "" {
		if len(paths) > 0 {
			opoo("Skipping trash: the home directory is unknown")
		}
		return
	}
	trash := filepath.Join(h, ".Trash")
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
			if !mayEscalate(p) {
				opoo("could not trash %s: %v", p, err)
			} else if err := o.run(true, "/bin/mv", "--", p, dst); err != nil {
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
			if os.Remove(p) != nil && mayEscalate(p) {
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
		if !validVersion(l) {
			continue // a label is a single file name, never a path
		}
		dirs := []string{"/Library/LaunchAgents", "/Library/LaunchDaemons"}
		if h := homeDir(); h != "" {
			dirs = append([]string{filepath.Join(h, "Library", "LaunchAgents")}, dirs...)
		}
		for _, dir := range dirs {
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
		if protectedPath(d) {
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
	room := filepath.Join(caskroom(), token)
	if !validName(token) || filepath.Dir(filepath.Clean(room)) != caskroom() {
		return fmt.Errorf("refusing to uninstall %q: not a Caskroom entry", token)
	}
	c := lookupCask(token)
	version := currentCaskVersion(token)
	staged := filepath.Join(room, version)
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
		for _, a := range caskFallbackTargets(c) {
			o.removeArtifacts(a.kind, []string{a.target})
		}
	}
	if zap {
		if !dry {
			ohai("Implied `brew uninstall --cask %s`", token)
		}
		o.runStanzas(caskStanzas(c, "zap"))
	}
	o.say("Purging files for version %s of Cask %s", version, token)
	if !dry {
		if err := o.removeAll(room); err != nil {
			return err
		}
		fmt.Printf("🍺  %s was successfully uninstalled!\n", token)
	}
	if o.failed {
		return fmt.Errorf("some files of %s could not be removed", token)
	}
	return nil
}

// symlinkedArtifacts are linked (not moved) into place; like Homebrew's
// Symlinked#unlink they are removed only while they are still symlinks.
var symlinkedArtifacts = map[string]bool{"Binary": true, "Completion": true, "Manpage": true}

// removeArtifacts removes symlinks and recorded artifacts (apps, fonts, ...).
func (o *caskOps) removeArtifacts(kind string, paths []string) {
	if o.seen == nil {
		o.seen = map[string]bool{}
	}
	for _, raw := range paths {
		p, ok := expandCaskPathOK(raw)
		if !ok {
			opoo("Skipping %s '%s': the home directory is unknown", kind, raw)
			continue
		}
		if !filepath.IsAbs(p) || hasDotDot(p) {
			opoo("Skipping %s with unsafe path '%s'", kind, p)
			continue
		}
		p = filepath.Clean(p)
		if o.seen[p] {
			continue
		}
		o.seen[p] = true
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		isLink := fi.Mode()&os.ModeSymlink != 0
		if symlinkedArtifacts[kind] && !isLink {
			continue
		}
		verb := "Removing"
		if isLink {
			verb = "Unlinking"
		}
		if protectedPath(p) {
			opoo("Skipping protected path %s", p)
			continue
		}
		o.say("%s %s '%s'", verb, kind, p)
		if o.dry {
			continue
		}
		if err := o.removeAll(p); err != nil {
			onoe("could not remove %s: %v", p, err)
			o.failed = true
		}
	}
}

// caskFallbackTargets lists where Homebrew put a cask's artifacts, for casks
// installed without a mise record (.mise-cask.toml), e.g. by Homebrew itself.
func caskFallbackTargets(c *Cask) []caskArtifact {
	var out []caskArtifact
	for _, raw := range c.Artifacts {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		for k, v := range m {
			var list []any
			if json.Unmarshal(v, &list) != nil || len(list) == 0 {
				continue
			}
			source, _ := list[0].(string)
			if source == "" {
				continue
			}
			target := filepath.Base(source)
			if len(list) > 1 {
				if opts, ok := list[1].(map[string]any); ok {
					if t, ok := opts["target"].(string); ok && t != "" {
						target = t
					}
				}
			}
			var kind, dir string
			switch k {
			case "app":
				kind, dir = "App", caskAppdir()
			case "suite":
				kind, dir = "Suite", caskAppdir()
			case "binary":
				kind, dir = "Binary", filepath.Join(brewPrefix(), "bin")
			case "font":
				kind, dir = "Font", "~/Library/Fonts"
			case "manpage":
				section := strings.TrimPrefix(filepath.Ext(strings.TrimSuffix(target, ".gz")), ".")
				if section == "" {
					continue
				}
				kind, dir = "Manpage", filepath.Join(brewPrefix(), "share", "man", "man"+section)
			case "bash_completion":
				kind, dir = "Completion", filepath.Join(brewPrefix(), "etc", "bash_completion.d")
				target = strings.TrimSuffix(target, ".bash")
			case "zsh_completion":
				kind, dir = "Completion", filepath.Join(brewPrefix(), "share", "zsh", "site-functions")
				if target = strings.TrimSuffix(target, ".zsh"); !strings.HasPrefix(filepath.Base(target), "_") {
					target = filepath.Join(filepath.Dir(target), "_"+filepath.Base(target))
				}
			case "fish_completion":
				kind, dir = "Completion", filepath.Join(brewPrefix(), "share", "fish", "vendor_completions.d")
				target = strings.TrimSuffix(target, ".fish") + ".fish"
			default:
				continue
			}
			if hasDotDot(target) {
				opoo("Skipping %s with unsafe target '%s'", kind, target)
				continue
			}
			p, ok := expandCaskPathOK(target)
			if ok && !filepath.IsAbs(p) {
				p, ok = expandCaskPathOK(filepath.Join(dir, p))
			}
			if !ok {
				opoo("Skipping %s '%s': the home directory is unknown", kind, target)
				continue
			}
			out = append(out, caskArtifact{kind, source, p})
		}
	}
	return out
}
