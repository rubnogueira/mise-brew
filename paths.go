package main

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// miseBrewVersion is overridden at release time with -ldflags "-X main.miseBrewVersion=..."
var miseBrewVersion = "0.1.0"

// defaultHomebrewVersion is reported by `brew --version` when no receipt says otherwise.
const defaultHomebrewVersion = "5.1.15"

// homeDir returns $HOME, else the account's home directory from the user
// database. It returns "" (never a relative path) when neither is usable, so
// callers must not join "~" paths onto it without checking.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && filepath.IsAbs(h) {
		return h
	}
	if h := userHomeLookup(); filepath.IsAbs(h) {
		return h
	}
	return ""
}

// userHomeLookup asks the user database for the home directory (a variable
// so tests can simulate an account without one).
var userHomeLookup = func() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

func defaultPrefix() string {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return "/opt/homebrew"
		}
		return "/usr/local"
	default:
		return "/home/linuxbrew/.linuxbrew"
	}
}

var prefixCache string

func brewPrefix() string {
	if prefixCache != "" {
		return prefixCache
	}
	p := os.Getenv("MISEBREW_PREFIX")
	if p == "" {
		p = defaultPrefix()
	}
	prefixCache = filepath.Clean(p)
	return prefixCache
}

func cellar() string   { return filepath.Join(brewPrefix(), "Cellar") }
func caskroom() string { return filepath.Join(brewPrefix(), "Caskroom") }
func optDir() string   { return filepath.Join(brewPrefix(), "opt") }
func linkedDir() string {
	return filepath.Join(brewPrefix(), "var", "homebrew", "linked")
}
func pinnedDir() string {
	return filepath.Join(brewPrefix(), "var", "homebrew", "pinned")
}

func brewRepository() string {
	if v := os.Getenv("MISEBREW_REPOSITORY"); v != "" {
		return v
	}
	if brewPrefix() == "/usr/local" {
		return "/usr/local/Homebrew"
	}
	if runtime.GOOS == "linux" && brewPrefix() == "/home/linuxbrew/.linuxbrew" {
		return "/home/linuxbrew/.linuxbrew/Homebrew"
	}
	return brewPrefix()
}

func miseCacheDir() string {
	if v := os.Getenv("MISE_CACHE_DIR"); v != "" {
		return v
	}
	// mise honours XDG_CACHE_HOME on every platform, macOS included.
	if x := os.Getenv("XDG_CACHE_HOME"); x != "" {
		return filepath.Join(x, "mise")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir(), "Library", "Caches", "mise")
	}
	return filepath.Join(homeDir(), ".cache", "mise")
}

// brewCacheDir is where mise keeps Homebrew bottles and API JSON.
func brewCacheDir() string { return filepath.Join(miseCacheDir(), "system-brew") }
func apiDir() string       { return filepath.Join(brewCacheDir(), "api") }
func bottlesDir() string   { return filepath.Join(brewCacheDir(), "bottles") }

var miseBinCache string

func miseBin() string {
	if miseBinCache != "" {
		return miseBinCache
	}
	if v := os.Getenv("MISEBREW_MISE"); v != "" {
		miseBinCache = v
		return v
	}
	if p, err := exec.LookPath("mise"); err == nil {
		miseBinCache = p
		return p
	}
	for _, p := range []string{
		filepath.Join(homeDir(), ".local", "bin", "mise"),
		"/usr/local/bin/mise",
		"/opt/homebrew/bin/mise",
		"/usr/bin/mise",
	} {
		if fileExists(p) {
			miseBinCache = p
			return p
		}
	}
	odie("mise was not found. Install it from https://mise.jdx.dev or set MISEBREW_MISE.")
	return ""
}

func fileExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func tildify(p string) string {
	h := homeDir()
	if h != "" && strings.HasPrefix(p, h+"/") {
		return "~" + p[len(h):]
	}
	return p
}
