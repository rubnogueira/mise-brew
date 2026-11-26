package homebrew

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rubnogueira/mise-brew/internal/paths"
)

// Directories of a keg that Homebrew symlinks into the prefix.
var LinkableDirs = []string{"bin", "sbin", "include", "lib", "share", "etc", "Frameworks"}

var SkipLinkFiles = map[string]bool{
	"share/info/dir":            true,
	"lib/charset.alias":         true,
	"share/locale/locale.alias": true,
	".DS_Store":                 true,
}

// ResolvesInto reports whether the symlink at link points inside dir.
func ResolvesInto(link, dir string) bool {
	t, err := os.Readlink(link)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(link), t)
	}
	t = filepath.Clean(t)
	return t == dir || strings.HasPrefix(t, dir+string(os.PathSeparator))
}

// KegRoots are the paths a prefix symlink may point through for a keg.
func KegRoots(name, version string) []string {
	return []string{KegPath(name, version), filepath.Join(paths.OptDir(), name)}
}

func ResolvesIntoAny(link string, roots []string) bool {
	for _, r := range roots {
		if ResolvesInto(link, r) {
			return true
		}
	}
	return false
}

type linkConflict struct {
	Rel, Dst string
}

// validKeg reports whether name and version form a path strictly inside a rack.
func validKeg(name, version string) bool {
	return ValidName(name) && ValidVersion(version)
}

// isEmptyDir reports whether p is a real (non-symlink) directory with no entries.
func isEmptyDir(p string) bool {
	entries, err := os.ReadDir(p)
	return err == nil && len(entries) == 0
}

// statOK reports whether p (following symlinks) exists; false for a broken link.
func statOK(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// expandableDirLink reports whether the prefix symlink at p points into the
// Cellar or opt (so ensureRealDir may turn it into a real directory). Links the
// user made to anywhere else are left alone.
func expandableDirLink(p string) bool {
	return ResolvesInto(p, paths.Cellar()) || ResolvesInto(p, paths.OptDir())
}

// blockingDirLink returns the first directory between the prefix and dst that
// is a symlink ensureRealDir must not expand, or "".
func blockingDirLink(dst string) string {
	p := paths.BrewPrefix()
	var chain []string
	for d := filepath.Dir(dst); d != p && strings.HasPrefix(d, p+"/"); d = filepath.Dir(d) {
		chain = append(chain, d)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		fi, err := os.Lstat(chain[i])
		if err != nil {
			return ""
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if !expandableDirLink(chain[i]) {
				return chain[i]
			}
			return ""
		}
	}
	return ""
}

// LinkKeg symlinks files of the keg into the prefix. Directories are created
// as real directories; files (and symlinks inside the keg) become relative symlinks.
func LinkKeg(name, version string, overwrite, dryRun bool) (int, []linkConflict, error) {
	if !validKeg(name, version) {
		return 0, nil, fmt.Errorf("invalid keg %q %q", name, version)
	}
	keg := KegPath(name, version)
	roots := KegRoots(name, version)
	rack := filepath.Join(paths.Cellar(), name)
	type op struct {
		src, dst string
		replace  bool // remove dst first
		silent   bool // replacing a stale link, not a conflict --overwrite resolves
	}
	var ops []op
	var conflicts []linkConflict
	blocked := map[string]bool{}
	for _, top := range LinkableDirs {
		base := filepath.Join(keg, top)
		if !paths.IsDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(keg, p)
			if SkipLinkFiles[rel] || d.Name() == ".DS_Store" {
				return nil
			}
			dst := filepath.Join(paths.BrewPrefix(), rel)
			if d.IsDir() {
				return nil // directories are created on demand
			}
			if b := blockingDirLink(dst); b != "" {
				// A directory symlink the user made (pointing outside the
				// Cellar) is never exploded or replaced.
				if !blocked[b] {
					blocked[b] = true
					brel, _ := filepath.Rel(paths.BrewPrefix(), b)
					conflicts = append(conflicts, linkConflict{brel, b})
				}
				return nil
			}
			fi, err := os.Lstat(dst)
			isLink := err == nil && fi.Mode()&os.ModeSymlink != 0
			switch {
			case err != nil:
				ops = append(ops, op{p, dst, false, false})
			case isLink && ResolvesIntoAny(dst, roots):
				// already linked
			case isLink && (!statOK(dst) || ResolvesInto(dst, rack)):
				// Broken link, or a link into another version of this rack:
				// Homebrew replaces these without asking.
				ops = append(ops, op{p, dst, true, true})
			case overwrite && (isLink || !fi.IsDir() || isEmptyDir(dst)):
				ops = append(ops, op{p, dst, true, false})
			default:
				// Without --overwrite anything else conflicts; with it, a
				// non-empty real directory still does (it is never deleted).
				conflicts = append(conflicts, linkConflict{rel, dst})
			}
			return nil
		})
	}
	if len(conflicts) > 0 {
		return 0, conflicts, nil
	}
	if dryRun {
		for _, o := range ops {
			if o.replace && !o.silent {
				fmt.Println(o.dst)
			}
		}
		return len(ops), nil, nil
	}
	n := 0
	for _, o := range ops {
		if err := ensureRealDir(filepath.Dir(o.dst)); err != nil {
			return n, nil, err
		}
		if o.replace {
			// Unlink a file or symlink, or remove an empty directory; never
			// delete a directory tree.
			if err := os.Remove(o.dst); err != nil {
				return n, nil, err
			}
		}
		rel, err := filepath.Rel(filepath.Dir(o.dst), o.src)
		if err != nil {
			return n, nil, err
		}
		if err := os.Symlink(rel, o.dst); err != nil {
			return n, nil, err
		}
		n++
	}
	if err := SetRecordLink(paths.LinkedDir(), name, version); err != nil {
		return n, nil, err
	}
	if err := SetOptLink(name, version); err != nil {
		return n, nil, err
	}
	return n, nil, nil
}

// ensureRealDir makes dir (and parents) exist inside the prefix. A directory
// symlink that points into some keg is expanded into a real directory of
// per-entry symlinks, like Homebrew does when two kegs share a directory.
// Symlinks pointing anywhere else are refused.
func ensureRealDir(dir string) error {
	p := paths.BrewPrefix()
	if dir == p || !strings.HasPrefix(dir, p+"/") {
		return os.MkdirAll(dir, 0o755)
	}
	if err := ensureRealDir(filepath.Dir(dir)); err != nil {
		return err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return os.Mkdir(dir, 0o755)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return nil
	}
	if !expandableDirLink(dir) {
		return fmt.Errorf("%s is a symlink that does not point into the Cellar", dir)
	}
	// Resolve only this link (not the whole path) so relative links stay valid.
	target, err := os.Readlink(dir)
	if err == nil && !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dir), target)
	}
	target = filepath.Clean(target)
	if err != nil || !paths.IsDir(target) {
		return fmt.Errorf("%s is a symlink that does not point to a directory", dir)
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return err
	}
	for _, e := range entries {
		rel, _ := filepath.Rel(dir, filepath.Join(target, e.Name()))
		if err := os.Symlink(rel, filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func SetRecordLink(dir, name, version string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(dir, name)
	_ = os.Remove(link)
	rel, _ := filepath.Rel(dir, KegPath(name, version))
	return os.Symlink(rel, link)
}

func SetOptLink(name, version string) error {
	link := filepath.Join(paths.OptDir(), name)
	if ReadLinkVersion(link) == version && paths.IsDir(link) {
		return nil
	}
	return SetRecordLink(paths.OptDir(), name, version)
}

// UnlinkKeg removes prefix symlinks that resolve into the keg, then prunes
// directories left empty (never the top-level prefix directories).
func UnlinkKeg(name, version string, dryRun bool) (int, []string) {
	if !validKeg(name, version) {
		return 0, nil
	}
	keg := KegPath(name, version)
	roots := KegRoots(name, version)
	removed := 0
	var ps []string
	var dirs []string
	for _, top := range LinkableDirs {
		base := filepath.Join(keg, top)
		if !paths.IsDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(keg, p)
			dst := filepath.Join(paths.BrewPrefix(), rel)
			fi, lerr := os.Lstat(dst)
			if lerr != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				if ResolvesIntoAny(dst, roots) {
					ps = append(ps, dst)
					if !dryRun {
						if os.Remove(dst) == nil {
							removed++
						}
					} else {
						removed++
					}
				}
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() && fi.IsDir() && rel != top {
				dirs = append(dirs, dst)
			}
			return nil
		})
	}
	if !dryRun {
		sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
		for _, d := range dirs {
			_ = os.Remove(d) // only succeeds when empty
		}
		if LinkedVersion(name) == version {
			_ = os.Remove(filepath.Join(paths.LinkedDir(), name))
		}
	}
	return removed, ps
}

// kegHasLinks reports whether any prefix symlink points into the keg.
func kegHasLinks(name, version string) bool {
	keg := KegPath(name, version)
	found := false
	for _, top := range LinkableDirs {
		base := filepath.Join(keg, top)
		if !paths.IsDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || found {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(keg, p)
			dst := filepath.Join(paths.BrewPrefix(), rel)
			if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 && ResolvesInto(dst, keg) {
				found = true
				return fs.SkipAll
			}
			return nil
		})
		if found {
			return true
		}
	}
	return false
}

// IsKegLinked reports whether brew/mise recorded the keg as linked.
func IsKegLinked(name string) bool { return LinkedVersion(name) != "" }

// removeRack deletes kegs of a formula (all when versions is nil) and cleans up
// opt/linked/pinned records. Prints Homebrew's "Uninstalling ..." lines.
func RemoveKegs(name string, versions []string) error {
	rack := filepath.Join(paths.Cellar(), name)
	if !ValidName(name) || filepath.Dir(filepath.Clean(rack)) != paths.Cellar() {
		return fmt.Errorf("refusing to remove %s: not a Cellar rack", rack)
	}
	for _, v := range versions {
		if keg := filepath.Join(rack, v); !ValidVersion(v) || filepath.Dir(filepath.Clean(keg)) != rack {
			return fmt.Errorf("refusing to remove %s: not a keg of %s", keg, name)
		}
	}
	for _, v := range versions {
		keg := KegPath(name, v)
		summary := KegSummary(keg)
		UnlinkKeg(name, v, false)
		fmt.Printf("Uninstalling %s... %s\n", keg, summary)
		if err := os.RemoveAll(keg); err != nil {
			return err
		}
	}
	remaining := KegVersions(name)
	if len(remaining) == 0 {
		_ = os.RemoveAll(rack)
		removeLinksInto(paths.OptDir(), rack)
		_ = os.Remove(filepath.Join(paths.LinkedDir(), name))
		_ = os.Remove(filepath.Join(paths.PinnedDir(), name))
	} else {
		// Repoint opt at the newest remaining keg if it pointed at a removed one.
		if !paths.IsDir(filepath.Join(paths.OptDir(), name)) {
			_ = SetOptLink(name, remaining[len(remaining)-1])
		}
		if lv := LinkedVersion(name); lv != "" && !paths.IsDir(KegPath(name, lv)) {
			_ = os.Remove(filepath.Join(paths.LinkedDir(), name))
		}
	}
	return nil
}

// removeLinksInto removes symlinks directly in dir that resolve into target.
func removeLinksInto(dir, target string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 && ResolvesInto(p, target) {
			_ = os.Remove(p)
		}
	}
}

// BrokenSymlinks lists dangling symlinks below the prefix's linkable dirs.
func BrokenSymlinks() []string {
	var out []string
	for _, top := range LinkableDirs {
		base := filepath.Join(paths.BrewPrefix(), top)
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if _, err := os.Stat(p); err != nil {
					out = append(out, p)
				}
			}
			return nil
		})
	}
	return out
}
