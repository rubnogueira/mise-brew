package main

import (
	"fmt"
	"os"
	"strings"
)

// flagSpec is one option of a command, as Homebrew documents it.
type flagSpec struct {
	short string
	long  string
	value bool // takes a value (--long=VALUE or --long VALUE)
	// optional: the value may only be attached (--long=VALUE); a bare --long
	// is valid on its own and never consumes the next argument.
	optional bool
	desc     string
}

// Spec lines: "-s, --long=  description" ("=" marks a value flag, "[=]" an
// optional-value flag such as --json[=v2]; short is optional).
var globalFlagLines = `
-d, --debug  Display any debugging information.
-q, --quiet  Make some output more quiet.
-v, --verbose  Make some output more verbose.
-h, --help  Show this message.`

var caskDirFlagLines = `
--appdir=  Target location for Applications (default: /Applications).
--keyboard-layoutdir=  Target location for Keyboard Layouts.
--colorpickerdir=  Target location for Color Pickers.
--prefpanedir=  Target location for Preference Panes.
--qlplugindir=  Target location for Quick Look Plugins.
--mdimporterdir=  Target location for Spotlight Plugins.
--dictionarydir=  Target location for Dictionaries.
--fontdir=  Target location for Fonts.
--servicedir=  Target location for Services.
--input-methoddir=  Target location for Input Methods.
--internet-plugindir=  Target location for Internet Plugins.
--audio-unit-plugindir=  Target location for Audio Unit Plugins.
--vst-plugindir=  Target location for VST Plugins.
--vst3-plugindir=  Target location for VST3 Plugins.
--screen-saverdir=  Target location for Screen Savers.
--language=  Comma-separated list of language codes to prefer for cask installation.
--no-binaries  Disable linking of helper executables.
--binaries  Enable linking of helper executables.
--require-sha  Require all casks to have a checksum.
--quarantine  Set the quarantine attribute on the installed app.
--no-quarantine  Do not set the quarantine attribute (mise's default).
--skip-cask-deps  Skip installing cask dependencies.`

var installCommonLines = `
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.
-f, --force  Install formulae without checking for previously installed keg-only or non-migrated versions.
--ignore-dependencies  An unsupported Homebrew development option (mise always installs dependencies).
-s, --build-from-source  Compile formula from source (not supported: mise pours bottles).
--force-bottle  Install from a bottle if it exists.
--include-test  Install testing dependencies (not supported).
--HEAD  Install the HEAD version (not supported).
--fetch-HEAD  Fetch the upstream repository to detect if the HEAD installation is outdated.
--keep-tmp  Retain the temporary files created during installation.
--debug-symbols  Generate debug symbols on build (not supported).
--build-bottle  Prepare the formula for eventual bottling (not supported).
--skip-post-install  Install but skip any post-install steps (not supported by mise).
--skip-link  Install but skip linking the keg into the prefix.
--as-dependency  Install but mark as installed as a dependency and not on request.
--bottle-arch=  Optimise bottles for the specified architecture (not supported).
--cc=  Attempt to compile using the specified compiler (not supported).
-i, --interactive  Download and patch formula, then open a shell (not supported).
-g, --git  Create a Git repository (not supported).
--overwrite  Delete files that already exist in the prefix while linking.
--display-times  Print install times for each package at the end of the run.
--ask  Ask for confirmation before downloading and installing formulae.
--adopt  Adopt existing artifacts in the destination that are identical to those being installed.
--no-record  Install without recording the package in mise config (mise-brew).`

var commandFlagLines = map[string]string{
	"install": `
-n, --dry-run  Show what would be installed, but do not actually install anything.
--only-dependencies  Install the dependencies but do not install the formula itself.` + installCommonLines + caskDirFlagLines,
	"reinstall": installCommonLines + caskDirFlagLines + `
--zap  For use with brew reinstall --cask. Remove all files associated with a cask.`,
	"upgrade": `
-n, --dry-run  Show what would be upgraded, but do not actually upgrade anything.
--greedy  Also include casks with auto_updates true or version :latest.
--greedy-latest  Also include casks with version :latest.
--greedy-auto-updates  Also include casks with auto_updates true.` + installCommonLines + caskDirFlagLines,
	"uninstall": `
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.
-f, --force  Delete all installed versions of formula. Uninstall even if cask is not installed.
--zap  Remove all files associated with a cask. May remove files shared between applications.
--ignore-dependencies  Don't fail uninstall, even if formula is a dependency of any installed formulae.
-n, --dry-run  Show what would be removed without removing anything (mise-brew).`,
	"update": `
-f, --force  Always do a slower, full update check.
--merge  Use git merge to apply updates (ignored).
--auto-update  Run on auto-updates (ignored).`,
	"outdated": `
--formula, --formulae  List only outdated formulae.
--cask, --casks  List only outdated casks.
--json[=]  Print output in JSON format (v2, the default, or v1).
--fetch-HEAD  Fetch the upstream repository to detect if the HEAD installation is outdated.
-g, --greedy  Also include outdated casks with auto_updates true or version :latest.
--greedy-latest  Also include outdated casks including those with version :latest.
--greedy-auto-updates  Also include outdated casks including those with auto_updates true.`,
	"autoremove": `
-n, --dry-run  List what would be uninstalled, but do not actually uninstall anything.
--mise  Use mise's rule instead: remove every formula no mise config declares or needs.`,
	"cleanup": `
--prune=  Remove all cache files older than specified days. If you want to remove everything, use --prune=all.
-n, --dry-run  Show what would be removed, but do not actually remove anything.
-s, --scrub  Scrub the cache, including downloads for even the latest versions.
--prune-prefix  Only prune the symlinks and directories from the prefix and remove no other files.`,
	"bundle": `
--file=  Read from or write to the Brewfile from this location. Use --file=- to pipe to stdin/stdout.
--global  Read from or write to the Brewfile from $HOMEBREW_BUNDLE_FILE_GLOBAL or ~/.Brewfile.
--no-upgrade  install does not run brew upgrade on outdated dependencies.
--upgrade  install runs brew upgrade on outdated dependencies, even if HOMEBREW_BUNDLE_NO_UPGRADE is set.
--install  Run install before continuing to other operations.
-f, --force  dump overwrites an existing Brewfile; cleanup actually performs its cleanup operations.
--cleanup  install performs cleanup operation, same as running cleanup --force.
--no-lock  No lockfile is written (always the case).
--all  list all dependencies.
--formula, --formulae, --brews  list or dump Homebrew formula dependencies.
--cask, --casks  list or dump Homebrew cask dependencies.
--tap, --taps  list or dump tap dependencies.
--mas  list or dump Mac App Store dependencies.
--vscode  list or dump VSCode (and forks/variants) extensions.
--go  list or dump Go packages.
--cargo  list or dump Cargo packages.
--uv  list or dump uv tools.
--npm  list or dump npm packages.
--flatpak  list or dump Flatpak packages.
--whalebrew  list or dump Whalebrew dependencies.
--no-vscode  dump without VSCode extensions.
--no-go  dump without Go packages.
--no-cargo  dump without Cargo packages.
--no-uv  dump without uv tools.
--no-npm  dump without npm packages.
--no-flatpak  dump without Flatpak packages.
--no-mas  dump without Mac App Store apps.
--no-tap  dump without taps.
--no-brew  dump without formulae.
--no-cask  dump without casks.
--describe  dump adds a description comment above each line.
--no-restart  dump does not add restart_service to formula lines.
--zap  cleanup casks using the zap command instead of uninstall.
--check  Check that all dependencies in the Brewfile are installed before running exec, sh, or env.
--services  Temporarily start services while running exec or sh.
--no-secrets  Attempt to remove secrets from the environment before exec, sh, or env.`,
	"tap": `
--custom-remote  Install or change a tap with a custom remote.
--repair  Add missing symlinks to tap manpages and shell completions (no-op).
--eval-all  Evaluate all formulae, casks and aliases in the new tap to check validity.
-f, --force  Force install core taps even under API mode.`,
	"untap": `
-f, --force  Untap even if formulae or casks from this tap are currently installed.`,
	"tap-info": `
--installed  Show information on each installed tap.
--json[=]  Print a JSON representation of tap. Currently the default and only accepted value is v1.`,
	"link": `
--overwrite  Delete files that already exist in the prefix while linking.
-n, --dry-run  List files which would be linked or deleted by brew link --overwrite without actually linking or deleting any files.
-f, --force  Allow keg-only formulae to be linked.
--HEAD  Link the HEAD version of the formula if it is installed.`,
	"unlink": `
-n, --dry-run  List files which would be unlinked without actually unlinking or deleting any files.`,
	"services": `
--all  Run subcommand on all services.
--file=  Use the service file from this location to start the service.
--sudo-service-user=  When run as root on macOS, run the service(s) as this user.
--max-wait=  Wait at most this many seconds for stop to finish stopping a service.
--no-wait  Don't wait for stop to finish stopping the service.
--keep  When stopped, don't unregister the service from launching at login (or boot).
--json  Output as JSON.`,
	"tab": `
--formula, --formulae  Only mark formulae.
--cask, --casks  Only mark casks.
--installed-on-request  Mark installed_formula or installed_cask as installed on request.
--no-installed-on-request  Mark installed_formula or installed_cask as not installed on request.`,
	"list": `
--formula, --formulae  List only formulae.
--cask, --casks  List only casks.
--full-name  Print formulae with fully-qualified names.
--versions  Show the version number for installed formulae.
--multiple  Only show formulae with multiple versions installed.
--pinned  List only pinned formulae.
--installed-on-request  List the formulae installed on request.
--installed-as-dependency  List the formulae installed as dependencies.
--poured-from-bottle  List the formulae installed from a bottle.
--built-from-source  List the formulae compiled from source.
-1  Force output to be one entry per line.
-l  List formulae and/or casks in long format.
-r  Reverse the order of formulae and/or casks sort.
-t  Sort formulae and/or casks by time modified, listing most recently modified first.`,
	"info": `
--analytics  List global Homebrew analytics data or, if specified, installation and build error data for formula.
--days=  How many days of analytics data to retrieve (30, 90 or 365).
--category=  Which type of analytics data to retrieve (install, install-on-request, build-error, cask-install).
--github  Open the GitHub source page for formula and cask in a browser.
--fetch-manifest  Fetch GitHub Packages manifest for extra information when formula is not installed.
--json[=]  Print a JSON representation (v1 or v2).
--installed  Print JSON of formulae that are currently installed.
--eval-all  Evaluate all available formulae and casks to print their JSON.
--variations  Include the variations hash in each formula's JSON output.
--sizes  Show the size of installed formulae and casks.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"search": `
--formula, --formulae  Search for formulae.
--cask, --casks  Search for casks.
--desc  Search for formulae with a description matching text and casks with a name or description matching text.
--eval-all  Evaluate all available formulae and casks.
--pull-request  Search for GitHub pull requests containing text.
--open  Search for only open GitHub pull requests.
--closed  Search for only closed GitHub pull requests.
--alpine  Search for text in the given database.
--repology  Search for text in the given database.
--macports  Search for text in the given database.
--fink  Search for text in the given database.
--opensuse  Search for text in the given database.
--fedora  Search for text in the given database.
--archlinux  Search for text in the given database.
--debian  Search for text in the given database.
--ubuntu  Search for text in the given database.`,
	"desc": `
-s, --search  Search both names and descriptions for text.
-n, --name  Search just names for text.
-d, --description  Search just descriptions for text.
--eval-all  Evaluate all available formulae and casks.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"deps": `
-n, --topological  Sort dependencies in topological order.
-1, --direct  Show only the direct dependencies declared in the formula.
--union  Show the union of dependencies for multiple formula, instead of the intersection.
--full-name  List dependencies by their full name.
--include-build  Include :build dependencies for formula.
--include-optional  Include :optional dependencies for formula.
--include-test  Include :test dependencies for formula (non-recursive unless --include-build).
--skip-recommended  Skip :recommended dependencies for formula.
--include-requirements  Include requirements in addition to dependencies for formula.
--tree  Show dependencies as a tree.
--prune  Prune parts of tree already seen.
--graph  Show dependencies as a directed graph.
--dot  Show text-based graph description in DOT format.
--annotate  Mark any build, test, implicit, optional, or recommended dependencies as such in the output.
--installed  List dependencies for formulae that are currently installed.
--missing  Show only missing dependencies.
--declared  Show only the dependencies declared in the formula, not the installed runtime dependencies.
--eval-all  Evaluate all available formulae and casks.
--for-each  Switch into the mode used by the --eval-all option, but only list dependencies for each provided formula.
--HEAD  Show dependencies for HEAD version instead of stable version.
--os=  Show dependencies for the given operating system.
--arch=  Show dependencies for the given CPU architecture.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"uses": `
--recursive  Resolve more than one level of dependencies.
--installed  Only list formulae and casks that are currently installed.
--missing  Only list formulae and casks that are not currently installed.
--eval-all  Evaluate all available formulae and casks.
--include-build  Include formulae that specify formula as a :build dependency.
--include-test  Include formulae that specify formula as a :test dependency.
--include-optional  Include formulae that specify formula as an :optional dependency.
--skip-recommended  Skip all formulae that specify formula as :recommended type dependency.
--formula, --formulae  Include only formulae.
--cask, --casks  Include only casks.`,
	"leaves": `
-r, --installed-on-request  Only list leaves that were manually installed.
-p, --installed-as-dependency  Only list leaves that were installed as dependencies.`,
	"missing": `
--hide=  Act as if none of the specified hidden are installed.`,
	"home": `
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"cat": `
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"options": `
--compact  Show all options on a single line separated by spaces.
--installed  Show options for formulae that are currently installed.
--eval-all  Evaluate all available formulae and casks.
--command=  Show options for the specified command.`,
	"which-formula": `
--explain  Output explanation of how to get cmd by installing one of the providing formulae.
--skip-update  Skip updating the executables database if any version exists on disk.`,
	"fetch": `
--os=  Download for the given operating system.
--arch=  Download for the given CPU architecture.
--bottle-tag=  Download a bottle for given tag.
--HEAD  Fetch HEAD version instead of stable version (not supported).
-f, --force  Remove a previously cached version and re-fetch.
--retry  Retry if downloading fails or re-download if the checksum of a previously cached version no longer matches.
--deps  Also download dependencies for any listed formula.
-s, --build-from-source  Download source packages rather than a bottle.
--build-bottle  Download source packages (for eventual bottling) rather than a bottle.
--force-bottle  Download a bottle if it exists for the current or newest version of macOS.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"--prefix": `
--unbrewed  List files in Homebrew's prefix not installed by Homebrew.
--installed  Outputs nothing and returns a failing status code if formula is not installed.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"--cellar":     ``,
	"--caskroom":   ``,
	"--repository": ``,
	"--version":    ``,
	"--cache": `
-s, --build-from-source  Show the cache file used when building from source.
--force-bottle  Show the cache file used when pouring a bottle.
--bottle-tag=  Show the cache file used when pouring a bottle for the given tag.
--HEAD  Show the cache file used when building from HEAD.
--os=  Show cache file for the given operating system.
--arch=  Show cache file for the given CPU architecture.
--formula, --formulae  Only show cache files for formulae.
--cask, --casks  Only show cache files for casks.`,
	"--env": `
--shell=  Generate a list of environment variables for the specified shell, or --shell=auto to detect the current shell.
--plain  Generate plain output even when piped.`,
	"shellenv": ``,
	"config":   ``,
	"doctor": `
--list-checks  List all audit methods.
-D, --audit-debug  Enable debugging and profiling of audit methods.`,
	"commands": `
--include-aliases  Include aliases of internal commands.`,
	"help":        ``,
	"analytics":   ``,
	"developer":   ``,
	"completions": ``,
	"sh": `
--env=  Use the standard PATH instead of superenv's when std is passed.
-c, --cmd=  Execute commands in a non-interactive shell.`,
	"log": `
-p, --patch  Also print patch from commit.
--stat  Also print diffstat from commit.
--oneline  Print only one commit per line.
-1  Print only one commit.
-n, --max-count=  Print only a specified number of commits.
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.`,
	"linkage": `
--test  Show only missing libraries and exit with a non-zero status if any missing libraries are found.
--strict  Exit with a non-zero status if any undeclared dependencies with linkage are found.
--reverse  For every library that a keg references, print its dylib path followed by the binaries that link to it.
--cached  Print the cached linkage values stored (computed fresh by mise-brew).`,
	"unpack": `
--destdir=  Create subdirectories in the directory named by path instead.
--patch  Patches for formula will be applied to the unpacked source.
-g, --git  Initialise a Git repository in the unpacked source.
-f, --force  Overwrite the destination directory if it already exists.`,
	"edit": `
--formula, --formulae  Treat all named arguments as formulae.
--cask, --casks  Treat all named arguments as casks.
--print-path  Print the file path to be edited, without opening an editor.`,
	"formula":                ``,
	"postinstall":            ``,
	"alias":                  `--edit  Edit aliases in a text editor.`,
	"unalias":                ``,
	"command-not-found-init": ``,
	"which-update":           ``,
	"command":                ``,
	"pin":                    ``,
	"unpin":                  ``,
	"formulae":               ``,
	"casks":                  ``,
	"docs":                   ``,
	"migrate":                `-f, --force  Treat installed formula and provided formula as if they are from the same taps and migrate them anyway.`,
	"readall": `--os=  Read using the given operating system.
--arch=  Read using the given CPU architecture.
--aliases  Verify any alias symlinks in each tap.
--syntax  Syntax-check all of Homebrew's Ruby files.
--eval-all  Evaluate all available formulae and casks.
--no-simulate  Don't simulate other system configurations.`,
	"vendor-install": ``,
	"install-bundler-gems": `--groups=  Installs the specified comma-separated list of gem groups.
--add-groups=  Installs the specified comma-separated list of gem groups, in addition to those already installed.`,
	"setup-ruby":       ``,
	"update-if-needed": ``,
}

var commandSpecs map[string][]flagSpec

func parseFlagLines(lines string) []flagSpec {
	var out []flagSpec
	for _, l := range strings.Split(lines, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		head, desc, _ := strings.Cut(l, "  ")
		var short []string
		var longs []string
		for _, part := range strings.Split(head, ",") {
			part = strings.TrimSpace(part)
			switch {
			case strings.HasPrefix(part, "--"):
				longs = append(longs, part[2:])
			case strings.HasPrefix(part, "-"):
				short = append(short, part[1:])
			}
		}
		value, optional := false, false
		for i, lg := range longs {
			if strings.HasSuffix(lg, "[=]") {
				optional = true
				longs[i] = strings.TrimSuffix(lg, "[=]")
			} else if strings.HasSuffix(lg, "=") {
				value = true
				longs[i] = strings.TrimSuffix(lg, "=")
			}
		}
		s := ""
		if len(short) > 0 {
			s = short[0]
		}
		if len(longs) == 0 { // short-only flags such as -1, -l
			out = append(out, flagSpec{short: s, long: s, desc: strings.TrimSpace(desc)})
			continue
		}
		for i, lg := range longs {
			fs := flagSpec{long: lg, value: value, optional: optional, desc: strings.TrimSpace(desc)}
			if i == 0 {
				fs.short = s
			}
			out = append(out, fs)
		}
	}
	return out
}

func init() {
	commandSpecs = map[string][]flagSpec{}
	global := parseFlagLines(globalFlagLines)
	for name, lines := range commandFlagLines {
		specs := parseFlagLines(lines)
		// Command-specific short letters (e.g. desc -d) override the global ones.
		taken := map[string]bool{}
		for _, s := range specs {
			if s.short != "" {
				taken[s.short] = true
			}
		}
		for _, g := range global {
			if taken[g.short] {
				g.short = ""
			}
			specs = append(specs, g)
		}
		commandSpecs[name] = specs
	}
}

// shortMap returns the short->long mapping for a command.
func shortMap(cmd string) map[string]string {
	m := map[string]string{}
	for _, s := range commandSpecs[cmd] {
		if s.short != "" {
			m[s.short] = s.long
		}
	}
	return m
}

func valueFlags(cmd string) []string {
	var out []string
	for _, s := range commandSpecs[cmd] {
		if s.value {
			out = append(out, s.long)
		}
	}
	return out
}

// optionalValueFlags lists flags whose value must be attached (--json=v2); a
// bare --json is complete and never takes the next argument.
func optionalValueFlags(cmd string) []string {
	var out []string
	for _, s := range commandSpecs[cmd] {
		if s.optional {
			out = append(out, s.long)
		}
	}
	return out
}

func lenient() bool { return os.Getenv("MISEBREW_LENIENT") != "" }

// validateFlags rejects options Homebrew would reject, like brew's own parser.
func validateFlags(cmd string, args []string) {
	specs, ok := commandSpecs[cmd]
	if !ok || lenient() {
		return
	}
	known := map[string]flagSpec{}
	short := map[string]bool{}
	for _, s := range specs {
		known[s.long] = s
		if s.short != "" {
			short[s.short] = true
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return
		}
		switch {
		case strings.HasPrefix(a, "--"):
			k, _, hasV := strings.Cut(a[2:], "=")
			s, ok := known[k]
			if !ok {
				invalidOption(cmd, a)
			}
			if s.value && !s.optional && !hasV {
				if i+1 >= len(args) {
					onoe("Invalid usage: option --%s requires an argument", k)
					exit(1)
				}
				i++
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if long := shortMap(cmd)[string(a[1])]; long != "" && known[long].value {
				if len(a) == 2 {
					i++
				}
				continue
			}
			for _, r := range a[1:] {
				if !short[string(r)] && known[string(r)].long == "" {
					invalidOption(cmd, a)
				}
			}
		}
	}
}

func invalidOption(cmd, opt string) {
	printCommandHelpTo(os.Stderr, cmd)
	fmt.Fprintln(os.Stderr)
	onoe("invalid option: %s", opt)
	exit(1)
}

func printCommandHelpTo(w *os.File, name string) {
	c := commands[name]
	if c == nil {
		return
	}
	fmt.Fprintf(w, "Usage: brew %s\n\n%s\n", c.usage, c.summary)
	if h, ok := extraHelp[name]; ok {
		fmt.Fprint(w, "\n"+h)
	}
	var specs, globals []flagSpec
	for _, s := range commandSpecs[name] {
		if isGlobalFlag(s.long) {
			globals = append(globals, s)
		} else {
			specs = append(specs, s)
		}
	}
	specs = append(specs, globals...)
	if len(specs) == 0 {
		return
	}
	fmt.Fprintln(w)
	for i := 0; i < len(specs); i++ {
		s := specs[i]
		left := "--" + s.long
		if s.long == s.short {
			left = "-" + s.short
		} else if s.short != "" {
			left = "-" + s.short + ", " + left
		}
		// Aliases share a description: print them on one line (--formula, --formulae).
		for i+1 < len(specs) && specs[i+1].desc == s.desc && specs[i+1].short == "" {
			i++
			left += ", --" + specs[i].long
		}
		if s.value {
			left += "="
		}
		fmt.Fprintf(w, "  %s%s\n", padRight(left, 30), wrapDesc(s.desc, 32))
	}
}

func isGlobalFlag(l string) bool {
	switch l {
	case "debug", "quiet", "verbose", "help":
		return true
	}
	return false
}

func wrapDesc(s string, indent int) string {
	width := 80 - indent
	var lines []string
	cur := ""
	for _, w := range strings.Fields(s) {
		if cur != "" && len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
			continue
		}
		if cur == "" {
			cur = w
		} else {
			cur += " " + w
		}
	}
	lines = append(lines, cur)
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}
