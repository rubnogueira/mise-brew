# mise-brew

[![CI](https://github.com/rubnogueira/mise-brew/actions/workflows/ci.yml/badge.svg)](https://github.com/rubnogueira/mise-brew/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/rubnogueira/mise-brew)](https://github.com/rubnogueira/mise-brew/releases/latest)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A drop-in `brew` command for machines whose Homebrew packages are managed by
[`mise bootstrap packages`](https://mise.jdx.dev/bootstrap/packages/) instead of Homebrew.

`brew install jq` becomes `mise bootstrap packages use -g brew:jq`: mise pours the
bottle **and records `brew:jq` in your mise config**, so your machine stays
reproducible from one file. Everything else (queries, linking, services, Brewfiles,
cleanup, cask removal) is implemented natively against the Homebrew prefix, mise's
cached copy of the formulae.brew.sh API and GitHub for third-party taps. Output, flags,
exit codes and `--help` follow Homebrew's, so scripts, editors and muscle memory keep
working.

> [!NOTE]
> mise-brew is not affiliated with Homebrew. `mise bootstrap packages` is still
> experimental in mise; mise-brew is tested against mise 2026.11.

## Install

mise-brew installs itself as `brew` in the Homebrew prefix (`/opt/homebrew` on Apple
Silicon, `/usr/local` on Intel Macs, `/home/linuxbrew/.linuxbrew` on Linux), which is
where scripts and `eval "$(brew shellenv)"` expect it. If Homebrew itself is installed
there, this replaces its `brew` command.

**From a release** (macOS and Linux, arm64 and amd64):

```sh
os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSLO "https://github.com/rubnogueira/mise-brew/releases/latest/download/mise-brew-$os-$arch.tar.gz"
curl -fsSL "https://github.com/rubnogueira/mise-brew/releases/latest/download/checksums.txt" |
  grep " mise-brew-$os-$arch.tar.gz\$" | shasum -a 256 -c -
tar -xzf "mise-brew-$os-$arch.tar.gz"
prefix=$("./mise-brew-$os-$arch/brew" --prefix)
mkdir -p "$prefix/bin" && install -m 0755 "mise-brew-$os-$arch/brew" "$prefix/bin/brew"
```

**With Go** (1.23 or newer):

```sh
go install github.com/rubnogueira/mise-brew/cmd/brew@latest
install -m 0755 "$(go env GOPATH)/bin/brew" "$("$(go env GOPATH)/bin/brew" --prefix)/bin/brew"
```

**From source**:

```sh
git clone https://github.com/rubnogueira/mise-brew && cd mise-brew
mise run install   # builds bin/brew and installs it as <prefix>/bin/brew
```

Then make sure the prefix is on your `PATH` (`eval "$(brew shellenv)"` in your shell
rc file) and run `brew doctor`.

To uninstall, delete `<prefix>/bin/brew`. Packages stay installed and declared in your
mise config.

## How commands map

| brew | mise-brew does |
| --- | --- |
| `install F` / `install --cask C` | `mise bootstrap packages use -g -y brew:F` / `brew-cask:C`; already declared anywhere → `apply` (no duplicate entry). Output is rewritten into Homebrew's (`==> Downloading`, `==> Pouring`, `🍺`), caveats are shown, and old versions are cleaned up afterwards. `--ask`, `--skip-link`, `--overwrite`, `--as-dependency`, `--only-dependencies`, `--require-sha`, `--quarantine`, `--display-times`, `--appdir`/`HOMEBREW_CASK_OPTS` work. |
| `install --no-record F` | `mise bootstrap packages apply brew:F` (install without writing config) |
| `uninstall F` | removes `brew:F` from **every** mise config file that declares it, refuses if installed formulae or casks depend on it, deletes the keg (`--force`: all versions), then autoremoves orphaned dependencies (`HOMEBREW_NO_AUTOREMOVE=1` to skip) |
| `uninstall --cask C [--zap]` | removes `brew-cask:C` from config, runs the cask's `uninstall` stanza (launchctl, quit, signal, login items, kext, scripts, pkgutil, delete, trash, rmdir), removes the artifacts mise recorded in `.mise-cask.toml`, then `zap` with `--zap`. `-n` shows the plan. |
| `reinstall F` / `reinstall --cask C` | formula: the current keg is moved aside, `apply`, restored on failure or Ctrl-C; cask: Caskroom backed up, native uninstall + `apply`, Caskroom restored on failure |
| `upgrade [F…]` | every outdated formula/cask (as `brew outdated` reports, minus pins) plus outdated dependents, passed explicitly to `mise bootstrap packages upgrade`; `--greedy*`, `HOMEBREW_UPGRADE_GREEDY(_CASKS)`, `-n`, `--ask` |
| `update` | refreshes mise's API cache and third-party tap metadata, lists outdated packages |
| `autoremove` | Homebrew's rule: removes formulae installed as dependencies that nothing needs (`--mise` = `mise bootstrap packages prune`); does nothing if mise's package status can't be read |
| `postinstall F` | reinstall through mise (mise runs post-install steps when pouring) |
| `tap U/R [URL]` / `untap` | `mise bootstrap packages brew tap` / `untap` |
| `bundle …` | Brewfiles are evaluated with Ruby ≥ 2.6 when available (full DSL), else a built-in parser. `brew`/`cask`/`mas`/`flatpak` → `packages use`, `go`/`cargo`/`npm`/`uv` → `mise use`, `vscode`/`cursor` → the editor CLI, `whalebrew` → `whalebrew`. Honours `link:` (incl. `:overwrite`), `conflicts_with:`, `restart_service:`, `start_service:`, `postinstall:`, `cask_args`/`args: { appdir: }`, `greedy:`; upgrades outdated entries unless `--no-upgrade`. `check`, `list`, `dump` (with mas names, VS Code extensions, mise tools), `cleanup`, `exec`/`sh`/`env` (`--services`, `--check`), `add`, `remove`, `edit`. |
| `services …` | `launchctl` (macOS) / `systemctl --user` (Linux) with the plist/unit (and timer) shipped in the keg; `--all`, `--file`, `--sudo-service-user`, `--max-wait`, `--no-wait`, `--keep`, `--json` |
| `link`, `unlink`, `pin`, `unpin`, `tab`, `cleanup` | native on the prefix, holding mise's own lock (`var/homebrew/locks/mise-bootstrap.lock`) so they never race a mise run |
| `list`, `info`, `search`, `desc`, `deps`, `uses`, `leaves`, `missing`, `outdated`, `options`, `formulae`, `casks`, `which-formula`, `home`, `cat`, `log`, `formula`, `edit`, `linkage`, `fetch`, `unpack` | native: Cellar/Caskroom + API cache (with per-platform `variations` for formulae and casks, macOS bounds, requirements); third-party taps from GitHub; analytics, `log`, PR search and sources from the web |
| `--prefix`, `--cellar`, `--caskroom`, `--cache`, `--repository`, `--version`, `--env`, `shellenv`, `config`, `doctor`, `commands`, `command`, `help`, `sh`, `alias`, `unalias`, `command-not-found-init` | native; `shellenv` matches upstream's output for bash, zsh, fish, csh and pwsh; `--env`/`sh` provide a superenv with compiler shims that add dependency include/lib paths |

Homebrew aliases work (`ls`, `rm`, `remove`, `abv`, `dr`, `-S`, `ln`, `up`, `homepage`,
`--repo`, `--config`, …), as do external commands (`brew foo` runs `brew-foo` from
`PATH`) and `brew alias` aliases (`~/.brew-aliases`). Unknown options are rejected like
Homebrew does (`MISEBREW_LENIENT=1` to ignore them).

## Safety

mise-brew deletes files and edits your config, so it is deliberately conservative:

- Every formula, cask, tap, alias and version name is validated before it touches a
  path; `brew uninstall ..` is an error, not a way to delete the prefix. Deletions are
  re-checked to stay inside the Cellar, Caskroom or cache.
- Cask `uninstall`/`zap` stanzas never delete Homebrew's protected system and home
  directories (or anything reached through `..`), skip `~` paths when `$HOME` can't be
  resolved, and only use `sudo` outside your home directory.
- mise config edits go through a temporary file and an atomic rename, are re-parsed as
  TOML before they are written, are applied once per file even when it is reached
  through a symlink, and are rolled back if a later step fails.
- `brew bundle cleanup --force` refuses to run when the Brewfile could not be fully
  understood, and skips packages declared in project (non-global) mise configs.
- mise runs its children with `MISEBREW_ACTIVE=1`; a `brew` called from inside a
  mise run (a cask script, a post-install step) answers read-only queries only.

See [SECURITY.md](SECURITY.md) to report a problem.

## Configuration

| Variable | Effect |
| --- | --- |
| `MISEBREW_CONFIG` | where new entries are written: `global` (default, `-g`), `local` (nearest `mise.toml`), or a config file path (`-p`) |
| `MISEBREW_ENV` | write to `mise.ENV.toml` (`-e ENV`) and run mise with `MISE_ENV=ENV` so those entries are seen everywhere |
| `MISEBREW_RECORD=0` | install without recording, like `--no-record` |
| `MISEBREW_PREFIX` | Homebrew prefix (default `/opt/homebrew`, `/usr/local`, or `/home/linuxbrew/.linuxbrew`) |
| `MISEBREW_MISE` | path to the `mise` binary |
| `MISEBREW_RAW_OUTPUT=1` | show mise's own output instead of Homebrew-style output (also with `--verbose`) |
| `MISEBREW_RUBY` / `MISEBREW_BUNDLE_NO_RUBY=1` | Ruby used to evaluate Brewfiles / force the built-in parser |
| `MISEBREW_BOTTLE_TAG`, `MISEBREW_MACOS_VERSION` | override platform detection |
| `MISEBREW_HOMEBREW_VERSION` | version printed by `brew --version` |
| `MISEBREW_LENIENT=1` | accept unknown options |
| `MISE_CACHE_DIR`, `XDG_CACHE_HOME` | where mise's cache (API, bottles) is read from, as mise resolves it |
| Homebrew's own: `HOMEBREW_NO_INSTALL_CLEANUP`, `HOMEBREW_CLEANUP_PERIODIC_FULL_DAYS`, `HOMEBREW_CLEANUP_MAX_AGE_DAYS`, `HOMEBREW_NO_AUTOREMOVE`, `HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK`, `HOMEBREW_UPGRADE_GREEDY(_CASKS)`, `HOMEBREW_CASK_OPTS`, `HOMEBREW_NO_ANALYTICS`, `HOMEBREW_NO_ENV_HINTS`, `HOMEBREW_BUNDLE_FILE(_GLOBAL)`, `HOMEBREW_BUNDLE_NO_UPGRADE`, `HOMEBREW_GITHUB_API_TOKEN`, `HOMEBREW_EDITOR`, `HOMEBREW_BROWSER`, `HOMEBREW_CC`, `HOMEBREW_MAKE_JOBS`, `HOMEBREW_ENV=std`, `HOMEBREW_(NO_)COLOR`, `HOMEBREW_VERBOSE`, `HOMEBREW_DEBUG` | as in Homebrew |

Third-party taps are read through the GitHub API. Unauthenticated requests are limited
to 60 an hour; set `HOMEBREW_GITHUB_API_TOKEN` if you use many taps.

## What cannot match Homebrew

These need either Homebrew's Ruby runtime or features mise does not have:

- **Source builds and build options**: `--build-from-source`, `--HEAD`, `--interactive`,
  `--cc`, `--debug-symbols`, `--build-bottle`, `--bottle-arch`, installing `.rb` files or
  URLs. mise pours bottles (it builds tap formulae without bottles itself).
- **mise install behaviour**: `--ignore-dependencies`, `--skip-post-install`, `--adopt`,
  cask dirs other than `--appdir`, `--language`, `--no-binaries` are warned about or
  rejected; quarantine is off by default (`--quarantine` sets it afterwards).
- **Pins** are honoured by mise-brew only; running `mise bootstrap packages upgrade`
  directly ignores them.
- **Developer commands**: `audit`, `test`, `create`, `bump-*`, `livecheck`, `style`,
  `bottle`, `pr-*`, `tap-new`, `ruby`, `irb`, `typecheck`, `mcp-server`, … exit 1.
- **No git checkout**: taps are read from GitHub (and cached), not cloned, so taps hosted
  elsewhere can be installed by mise but not queried by mise-brew.
  `brew --repository TAP` prints the path Homebrew would use. `edit` opens a downloaded
  copy that does not affect installs.
- **Tap formulae and casks** are parsed from their Ruby source with a block-aware
  reader, not evaluated; unusual DSL can be misread (mise itself does the install).
- **`reinstall --cask`** restores the Caskroom if mise fails, but not app bundles the
  cask's `uninstall` stanza already removed; rerun it to finish.
- `Brewfile.lock.json` is not written.

## Development

```sh
mise run test    # go vet + go test ./...
mise run build   # bin/brew
```

The tests use a temporary prefix, `HOME` and a stub `mise`, so they never touch
`/opt/homebrew` or your config. See [CONTRIBUTING.md](CONTRIBUTING.md) for the code
layout.

## License

[MIT](LICENSE)
