// mise-brew is a Homebrew-compatible `brew` CLI whose package operations are
// performed by `mise bootstrap packages`. Read-only commands work directly on
// the Homebrew prefix and mise's cached copy of the formulae.brew.sh API.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
)

type command struct {
	run     func([]string)
	summary string
	usage   string
}

var commands map[string]*command

var aliases = map[string]string{
	"ls":            "list",
	"rm":            "uninstall",
	"remove":        "uninstall",
	"uninstal":      "uninstall",
	"instal":        "install",
	"abv":           "info",
	"dr":            "doctor",
	"-S":            "search",
	"ln":            "link",
	"homepage":      "home",
	"up":            "update",
	"--config":      "config",
	"environment":   "--env",
	"env":           "--env",
	"--repo":        "--repository",
	"-v":            "--version",
	"lc":            "livecheck",
	"tc":            "typecheck",
	"update-reset":  "update",
	"update-report": "update",
	"--help":        "help",
	"-h":            "help",
}

func init() {
	commands = map[string]*command{
		// package lifecycle (mise bootstrap packages)
		"install":     {cmdInstall, "Install a formula or cask (mise bootstrap packages use)", "install [--formula|--cask] [-n] [--no-record] formula|cask ..."},
		"reinstall":   {cmdReinstall, "Uninstall and then reinstall a formula", "reinstall [--formula] formula ..."},
		"uninstall":   {cmdUninstall, "Uninstall and undeclare a formula or cask", "uninstall [--formula|--cask] [-f] [--zap] [--ignore-dependencies] installed_formula|installed_cask ..."},
		"upgrade":     {cmdUpgrade, "Upgrade outdated formulae and casks (mise bootstrap packages upgrade)", "upgrade [--formula|--cask] [-n] [installed_formula|installed_cask ...]"},
		"update":      {cmdUpdate, "Refresh mise's copy of the Homebrew API", "update [-q] [-v]"},
		"outdated":    {cmdOutdated, "List installed formulae and casks that have a newer version", "outdated [--formula|--cask] [-q|-v] [--json[=v1|v2]] [--greedy] [installed_formula|installed_cask ...]"},
		"autoremove":  {cmdAutoremove, "Remove formulae no mise config declares or needs (mise bootstrap packages prune)", "autoremove [-n]"},
		"cleanup":     {cmdCleanup, "Remove old kegs, stale bottles and dead symlinks", "cleanup [-n] [-s] [--prune=days|all] [formula ...]"},
		"bundle":      {cmdBundle, "Install or check a Brewfile through mise", "bundle [install|check|list|dump|cleanup|exec] [--file=path] [--global]"},
		"tap":         {cmdTap, "Add or list taps (mise bootstrap packages brew tap)", "tap [user/repo] [URL]"},
		"untap":       {cmdUntap, "Remove taps (mise bootstrap packages brew untap)", "untap tap ..."},
		"tap-info":    {cmdTapInfo, "Show information about taps", "tap-info [--installed] [--json] [tap ...]"},
		"pin":         {cmdPin, "Pin a formula so upgrade skips it", "pin installed_formula ..."},
		"unpin":       {cmdUnpin, "Unpin a formula", "unpin installed_formula ..."},
		"link":        {cmdLink, "Symlink a formula's files into the prefix", "link [--overwrite] [-n] [-f] installed_formula ..."},
		"unlink":      {cmdUnlink, "Remove a formula's symlinks from the prefix", "unlink [-n] installed_formula ..."},
		"services":    {cmdServices, "Manage background services (launchctl / systemctl)", "services [list|info|run|start|stop|kill|restart|cleanup] [formula|--all]"},
		"postinstall": {cmdPostinstall, "Rerun the post-install steps for a formula (via a mise reinstall)", "postinstall installed_formula ..."},
		"tab":         {cmdTab, "Edit a keg's install receipt", "tab [--installed-on-request|--no-installed-on-request] installed_formula ..."},

		// queries
		"list":                   {cmdList, "List installed formulae and casks", "list [--formula|--cask] [--versions] [--pinned] [-1] [-l] [-t] [-r] [installed_formula|installed_cask ...]"},
		"info":                   {cmdInfo, "Show information about formulae and casks", "info [--json[=v1|v2]] [--installed] [--eval-all] [--formula|--cask] [formula|cask ...]"},
		"search":                 {cmdSearch, "Search formulae and casks", "search [--formula|--cask] [--desc] text|/regex/ ..."},
		"desc":                   {cmdDesc, "Show a formula's or cask's description", "desc [-s|-n|-d] [--formula|--cask] formula|cask|text ..."},
		"deps":                   {cmdDeps, "Show dependencies", "deps [--tree] [-n] [--installed] [--direct] [--include-build] [--include-test] [--union] [--for-each] [formula ...]"},
		"uses":                   {cmdUses, "Show formulae that depend on a formula", "uses [--installed] [--recursive] [--include-build] [--include-test] formula ..."},
		"leaves":                 {cmdLeaves, "List installed formulae that no other installed formula needs", "leaves [-r|--installed-on-request] [-p|--installed-as-dependency]"},
		"missing":                {cmdMissing, "Show missing runtime dependencies", "missing [installed_formula ...]"},
		"home":                   {cmdHome, "Open a formula's or cask's homepage", "home [formula|cask ...]"},
		"docs":                   {cmdDocs, "Open Homebrew's documentation", "docs"},
		"cat":                    {cmdCat, "Print a formula's or cask's source (fetched from GitHub)", "cat [--formula|--cask] formula|cask"},
		"formulae":               {cmdFormulae, "List all available formulae", "formulae"},
		"casks":                  {cmdCasks, "List all available casks", "casks"},
		"options":                {cmdOptions, "Show install options (bottles have none)", "options formula ..."},
		"which-formula":          {cmdWhichFormula, "Show which formula provides a command", "which-formula command ..."},
		"fetch":                  {cmdFetch, "Download a bottle (into mise's cache) or cask without installing it", "fetch [--deps] [-f] [--bottle-tag=tag] formula|cask ..."},
		"log":                    {cmdLog, "Show the git log of a formula or cask (GitHub API)", "log [--oneline] [-n N] [-p] [--stat] formula|cask"},
		"linkage":                {cmdLinkage, "Check the library links of installed kegs", "linkage [--test] [--strict] [--reverse] [installed_formula ...]"},
		"formula":                {cmdFormula, "Print the path to a formula's Ruby file (downloaded copy)", "formula formula ..."},
		"edit":                   {cmdEdit, "Open a formula or cask's Ruby file in an editor (read-only copy)", "edit [--print-path] formula|cask ..."},
		"unpack":                 {cmdUnpack, "Unpack the source of a formula", "unpack [--destdir=path] [-f] [-g] formula ..."},
		"alias":                  {cmdAlias, "Manage custom user-created brew aliases (~/.brew-aliases)", "alias [alias=command] [alias]"},
		"unalias":                {cmdUnalias, "Remove brew aliases", "unalias alias ..."},
		"command-not-found-init": {cmdCommandNotFoundInit, "Print a shell hook suggesting formulae for unknown commands", "command-not-found-init [bash|zsh|fish]"},
		"which-update":           {cmdWhichUpdate, "Refresh the executables database (built from the API)", "which-update"},
		"command":                {cmdCommand, "Display the path to the file being used when invoking brew cmd", "command cmd ..."},

		// environment
		"--prefix":             {cmdPrefix, "Print the Homebrew prefix or a formula's opt path", "--prefix [--installed] [formula ...]"},
		"--cellar":             {cmdCellar, "Print the Cellar path", "--cellar [formula ...]"},
		"--caskroom":           {cmdCaskroom, "Print the Caskroom path", "--caskroom [cask ...]"},
		"--cache":              {cmdCache, "Print mise's Homebrew download cache", "--cache [formula ...]"},
		"--repository":         {cmdRepository, "Print the Homebrew repository path", "--repository [tap ...]"},
		"--version":            {cmdVersion, "Print the version", "--version"},
		"--env":                {cmdEnv, "Print the build environment", "--env [--shell=bash|zsh|fish|plain] [formula ...]"},
		"shellenv":             {cmdShellenv, "Print shell commands that set up Homebrew's environment", "shellenv [bash|zsh|fish|csh|pwsh]"},
		"config":               {cmdConfig, "Show system configuration", "config"},
		"doctor":               {cmdDoctor, "Check the system for problems", "doctor"},
		"commands":             {cmdCommands, "List commands", "commands [--quiet]"},
		"help":                 {cmdHelp, "Show help", "help [command]"},
		"analytics":            {cmdAnalytics, "Analytics are always off", "analytics [state|on|off]"},
		"developer":            {cmdDeveloper, "Developer mode is always off", "developer [state|on|off]"},
		"completions":          {cmdCompletions, "Shell completions state", "completions [state|link|unlink]"},
		"sh":                   {cmdSh, "Start a shell with the build environment", "sh [--cmd=command]"},
		"migrate":              {noop("No migration needed."), "No-op", "migrate"},
		"readall":              {noop(""), "No-op", "readall"},
		"vendor-install":       {noop(""), "No-op", "vendor-install"},
		"install-bundler-gems": {noop(""), "No-op", "install-bundler-gems"},
		"setup-ruby":           {noop(""), "No-op", "setup-ruby"},
		"update-if-needed":     {func(a []string) { cmdUpdate(append([]string{"--quiet"}, a...)) }, "Update metadata", "update-if-needed"},
	}
}

// Developer / Ruby-runtime commands that cannot work without Homebrew itself.
var unsupported = []string{
	"audit", "bottle", "bump", "bump-cask-pr", "bump-formula-pr", "bump-revision",
	"bump-unversioned-casks", "contributions", "create",
	"debugger", "determine-test-runners", "dispatch-build-bottle", "extract",
	"formula-analytics", "generate-cask-api", "generate-formula-api",
	"generate-man-completions", "gist-logs", "irb",
	"livecheck", "pr-automerge", "pr-publish", "pr-pull", "pr-upload", "prof",
	"release", "rubocop", "ruby", "source", "style", "tap-new", "test",
	"test-bot", "tests", "typecheck", "unbottled", "update-license-data",
	"update-maintainers", "update-python-resources", "update-sponsors", "update-test",
	"vendor-gems", "verify", "mcp-server",
}

func noop(msg string) func([]string) {
	return func([]string) {
		if msg != "" {
			fmt.Println(msg)
		}
	}
}

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(argv []string) (code int) {
	initUI()
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(exitError); ok {
				code = e.code
				return
			}
			panic(r)
		}
	}()
	full := argv

	// Leading global flags (brew -v install foo, brew --debug ...).
	for len(argv) > 0 {
		switch argv[0] {
		case "--verbose":
			verbose = true
		case "-v":
			if len(argv) == 1 {
				goto dispatch
			}
			verbose = true
		case "--debug", "-d":
			debug = true
		case "--quiet", "-q":
			quiet = true
		default:
			goto dispatch
		}
		argv = argv[1:]
	}
dispatch:
	if len(argv) == 0 {
		printUsage(os.Stderr)
		return 1
	}
	name := canonical(argv[0])
	rest := argv[1:]
	if os.Getenv(recursionEnv) != "" && os.Getenv("MISEBREW_ALLOW_NESTED") == "" {
		// mise itself invoked `brew`: answer read-only queries, refuse anything else.
		if !isReadOnly(name, rest) {
			onoe("mise-brew was called from inside mise (%s); refusing to recurse.", strings.Join(full, " "))
			return 1
		}
	}
	if name == "--version" || (len(argv) == 1 && argv[0] == "-v") {
		cmdVersion(rest)
		return 0
	}
	c, ok := commands[name]
	if !ok {
		for _, u := range unsupported {
			if u == name {
				onoe("`brew %s` needs Homebrew's Ruby runtime, which mise-brew replaces with mise.", name)
				return 1
			}
		}
		if code, ok := runAlias(argv[0], rest); ok {
			return code
		}
		if path, err := exec.LookPath("brew-" + name); err == nil {
			return execExternal(path, rest)
		}
		if path, err := exec.LookPath("brew-" + name + ".rb"); err == nil {
			onoe("external Ruby command %s is not supported by mise-brew", path)
			return 1
		}
		onoe("Unknown command: brew %s", argv[0])
		return 1
	}
	currentCmd = name
	for _, a := range rest {
		if a == "--" {
			break
		}
		if a == "--help" || a == "-h" {
			if name != "help" {
				printCommandHelp(name)
				return 0
			}
		}
	}
	validateFlags(name, rest)
	c.run(rest)
	return 0
}

func canonical(n string) string {
	if a, ok := aliases[n]; ok {
		return a
	}
	return n
}

// readOnlyCommands never change the prefix, mise config or caches through
// mise, so they are safe to answer when mise itself runs `brew`. Anything not
// listed (including aliases, external commands and unknown commands) is
// refused under MISEBREW_ACTIVE.
var readOnlyCommands = map[string]bool{
	"list": true, "info": true, "search": true, "desc": true, "deps": true, "uses": true,
	"leaves": true, "missing": true, "cat": true, "formulae": true, "casks": true,
	"options": true, "which-formula": true, "log": true, "linkage": true, "formula": true,
	"command": true, "outdated": true, "tap-info": true, "command-not-found-init": true,
	"--prefix": true, "--cellar": true, "--caskroom": true, "--cache": true,
	"--repository": true, "--version": true, "--env": true, "shellenv": true,
	"config": true, "doctor": true, "commands": true, "help": true,
	"analytics": true, "developer": true, "completions": true,
}

// isReadOnly reports whether `brew cmd args...` is a read-only invocation.
// cmd must already be canonical and stripped of leading global flags.
func isReadOnly(cmd string, args []string) bool {
	if readOnlyCommands[cmd] {
		return true
	}
	sub := ""
	if cmd == "bundle" || cmd == "services" || cmd == "tap" {
		prev := currentCmd
		currentCmd = cmd
		o := parseArgs(args, nil, nil)
		currentCmd = prev
		if len(o.Pos) > 0 {
			sub = o.Pos[0]
		}
		switch cmd {
		case "bundle":
			return (sub == "check" || sub == "list") && !o.Has("install")
		case "services":
			return sub == "" || sub == "list" || sub == "ls" || sub == "info" || sub == "i"
		case "tap":
			return sub == "" // listing taps
		}
	}
	return false
}

func execExternal(path string, args []string) int {
	pairs := [][2]string{
		{"HOMEBREW_PREFIX", brewPrefix()},
		{"HOMEBREW_CELLAR", cellar()},
		{"HOMEBREW_REPOSITORY", brewRepository()},
		{"HOMEBREW_CACHE", brewCacheDir()},
	}
	if self, err := os.Executable(); err == nil {
		pairs = append(pairs, [2]string{"HOMEBREW_BREW_FILE", self})
	}
	env := mergeEnv(os.Environ(), pairs)
	if err := syscall.Exec(path, append([]string{path}, args...), env); err != nil {
		onoe("%v", err)
		return 1
	}
	return 0
}

func printUsage(w *os.File) {
	fmt.Fprint(w, `Example usage:
  brew search TEXT|/REGEX/
  brew info [FORMULA|CASK...]
  brew install FORMULA|CASK...
  brew update
  brew upgrade [FORMULA|CASK...]
  brew uninstall FORMULA|CASK...
  brew list [FORMULA|CASK...]

Troubleshooting:
  brew config
  brew doctor

mise-brew:
  Every install, upgrade and uninstall is performed by
  `+"`mise bootstrap packages`"+` and recorded in your mise config.
  brew commands      list every supported command
  brew help COMMAND  show a command's usage

Further help:
  https://github.com/rubnogueira/mise-brew
  https://mise.jdx.dev/bootstrap/packages/
`)
}

func printCommandHelp(name string) {
	if commands[name] == nil {
		onoe("Unknown command: %s", name)
		exit(1)
	}
	printCommandHelpTo(os.Stdout, name)
}

var extraHelp = map[string]string{
	"install": `Packages are added to [bootstrap.packages] with ` + "`mise bootstrap packages use`" + `
(global config by default) and installed. Packages already declared in any
config are installed with ` + "`mise bootstrap packages apply`" + ` instead.

Environment:
  MISEBREW_CONFIG   global (default), local, or a path to a mise config file
  MISEBREW_ENV      write to mise.<ENV>.toml
  MISEBREW_RECORD=0 install without recording (same as --no-record)
`,
	"uninstall": `Removes the package's entries from every mise config file that declares it,
then removes the keg (formulae) or prunes the cask through mise.
`,
	"autoremove": `Runs ` + "`mise bootstrap packages prune`" + `, which removes every formula that no mise
config declares and no declared formula or cask needs.
`,
}

func cmdHelp(args []string) {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return
	}
	printCommandHelp(canonical(args[0]))
}

func cmdCommands(args []string) {
	o := parseArgs(args, nil, nil)
	var names []string
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	if o.Has("quiet") {
		for _, n := range names {
			fmt.Println(n)
		}
		return
	}
	ohai("Built-in commands")
	putsColumns(names)
	fmt.Println()
	ohai("Aliases")
	var al []string
	for a, t := range aliases {
		al = append(al, a+" → "+t)
	}
	sort.Strings(al)
	putsColumns(al)
}
