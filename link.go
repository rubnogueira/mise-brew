package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Directories of a keg that Homebrew symlinks into the prefix.
var linkableDirs = []string{"bin", "sbin", "include", "lib", "share", "etc", "Frameworks"}

var skipLinkFiles = map[string]bool{
	"share/info/dir":            true,
	"lib/charset.alias":         true,
	"share/locale/locale.alias": true,
	".DS_Store":                 true,
}

// resolvesInto reports whether the symlink at link points inside dir.
func resolvesInto(link, dir string) bool {
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

// kegRoots are the paths a prefix symlink may point through for a keg.
func kegRoots(name, version string) []string {
	return []string{kegPath(name, version), filepath.Join(optDir(), name)}
}

func resolvesIntoAny(link string, roots []string) bool {
	for _, r := range roots {
		if resolvesInto(link, r) {
			return true
		}
	}
	return false
}

type linkConflict struct {
	rel, dst string
}

// linkKeg symlinks files of the keg into the prefix. Directories are created
// as real directories; files (and symlinks inside the keg) become relative symlinks.
func linkKeg(name, version string, overwrite, dryRun bool) (int, []linkConflict, error) {
	keg := kegPath(name, version)
	roots := kegRoots(name, version)
	type op struct {
		src, dst string
		replace  bool
	}
	var ops []op
	var conflicts []linkConflict
	for _, top := range linkableDirs {
		base := filepath.Join(keg, top)
		if !isDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(keg, p)
			if skipLinkFiles[rel] || d.Name() == ".DS_Store" {
				return nil
			}
			dst := filepath.Join(brewPrefix(), rel)
			if d.IsDir() {
				return nil // directories are created on demand
			}
			fi, err := os.Lstat(dst)
			switch {
			case err != nil:
				ops = append(ops, op{p, dst, false})
			case fi.Mode()&os.ModeSymlink != 0 && resolvesIntoAny(dst, roots):
				// already linked
			case overwrite:
				ops = append(ops, op{p, dst, true})
			default:
				conflicts = append(conflicts, linkConflict{rel, dst})
			}
			return nil
		})
	}
	if len(conflicts) > 0 && !overwrite {
		return 0, conflicts, nil
	}
	if dryRun {
		for _, o := range ops {
			if o.replace {
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
			if err := os.RemoveAll(o.dst); err != nil {
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
	if err := setRecordLink(linkedDir(), name, version); err != nil {
		return n, nil, err
	}
	if err := setOptLink(name, version); err != nil {
		return n, nil, err
	}
	return n, nil, nil
}

// ensureRealDir makes dir (and parents) exist inside the prefix. A directory
// symlink that points into some keg is expanded into a real directory of
// per-entry symlinks, like Homebrew does when two kegs share a directory.
func ensureRealDir(dir string) error {
	p := brewPrefix()
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
	// Resolve only this link (not the whole path) so relative links stay valid.
	target, err := os.Readlink(dir)
	if err == nil && !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dir), target)
	}
	target = filepath.Clean(target)
	if err != nil || !isDir(target) {
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

func setRecordLink(dir, name, version string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	link := filepath.Join(dir, name)
	_ = os.Remove(link)
	rel, _ := filepath.Rel(dir, kegPath(name, version))
	return os.Symlink(rel, link)
}

func setOptLink(name, version string) error {
	link := filepath.Join(optDir(), name)
	if readLinkVersion(link) == version && isDir(link) {
		return nil
	}
	return setRecordLink(optDir(), name, version)
}

// unlinkKeg removes prefix symlinks that resolve into the keg, then prunes
// directories left empty (never the top-level prefix directories).
func unlinkKeg(name, version string, dryRun bool) (int, []string) {
	keg := kegPath(name, version)
	roots := kegRoots(name, version)
	removed := 0
	var paths []string
	var dirs []string
	for _, top := range linkableDirs {
		base := filepath.Join(keg, top)
		if !isDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(keg, p)
			dst := filepath.Join(brewPrefix(), rel)
			fi, lerr := os.Lstat(dst)
			if lerr != nil {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if fi.Mode()&os.ModeSymlink != 0 {
				if resolvesIntoAny(dst, roots) {
					paths = append(paths, dst)
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
		if linkedVersion(name) == version {
			_ = os.Remove(filepath.Join(linkedDir(), name))
		}
	}
	return removed, paths
}

// kegHasLinks reports whether any prefix symlink points into the keg.
func kegHasLinks(name, version string) bool {
	keg := kegPath(name, version)
	found := false
	for _, top := range linkableDirs {
		base := filepath.Join(keg, top)
		if !isDir(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil || found {
				return fs.SkipAll
			}
			rel, _ := filepath.Rel(keg, p)
			dst := filepath.Join(brewPrefix(), rel)
			if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 && resolvesInto(dst, keg) {
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

// isKegLinked reports whether brew/mise recorded the keg as linked.
func isKegLinked(name string) bool { return linkedVersion(name) != "" }

// removeRack deletes kegs of a formula (all when versions is nil) and cleans up
// opt/linked/pinned records. Prints Homebrew's "Uninstalling ..." lines.
func removeKegs(name string, versions []string) error {
	for _, v := range versions {
		keg := kegPath(name, v)
		summary := kegSummary(keg)
		unlinkKeg(name, v, false)
		fmt.Printf("Uninstalling %s... %s\n", keg, summary)
		if err := os.RemoveAll(keg); err != nil {
			return err
		}
	}
	rack := filepath.Join(cellar(), name)
	remaining := kegVersions(name)
	if len(remaining) == 0 {
		_ = os.RemoveAll(rack)
		removeLinksInto(optDir(), rack)
		_ = os.Remove(filepath.Join(linkedDir(), name))
		_ = os.Remove(filepath.Join(pinnedDir(), name))
	} else {
		// Repoint opt at the newest remaining keg if it pointed at a removed one.
		if !isDir(filepath.Join(optDir(), name)) {
			_ = setOptLink(name, remaining[len(remaining)-1])
		}
		if lv := linkedVersion(name); lv != "" && !isDir(kegPath(name, lv)) {
			_ = os.Remove(filepath.Join(linkedDir(), name))
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
		if e.Type()&os.ModeSymlink != 0 && resolvesInto(p, target) {
			_ = os.Remove(p)
		}
	}
}

// brokenSymlinks lists dangling symlinks below the prefix's linkable dirs.
func brokenSymlinks() []string {
	var out []string
	for _, top := range linkableDirs {
		base := filepath.Join(brewPrefix(), top)
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
