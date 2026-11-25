# mise-brew

A drop-in `brew` CLI for machines where Homebrew packages are managed by
[`mise bootstrap packages`](https://mise.jdx.dev/bootstrap/packages/) instead of Homebrew.

`brew install jq` becomes `mise bootstrap packages use -g brew:jq`: mise installs the
package **and records it in your mise config**. Everything else (queries, linking,
services, bundles, cleanup, cask removal) is implemented natively against the
Homebrew prefix, mise's cached copy of the formulae.brew.sh API, and GitHub for
third-party taps. Output, flags, exit codes and `--help` follow Homebrew's.

## Install

```sh
mise run install          # builds bin/brew and installs it as /opt/homebrew/bin/brew
# or
make install PREFIX=/opt/homebrew
```

Or from a release (macOS/Linux, arm64/amd64):

```sh
gh release download -R rubnogueira/mise-brew -p "*darwin-arm64.tar.gz" -O - | tar -xz
install -m 0755 mise-brew-*/brew /opt/homebrew/bin/brew
```

Releases are published by pushing a tag: `git tag v0.1.0 && git push origin v0.1.0`.

`/opt/homebrew/bin/brew` is where scripts and `eval "$(brew shellenv)"` expect it.
Uninstall with `rm /opt/homebrew/bin/brew`.

## How commands map

| brew | mise-brew does |
| --- | --- |
| `install F` / `install --cask C` | `mise bootstrap packages use -g -y brew:F` / `brew-cask:C`; already declared anywhere → `apply` (no duplicate entry). Output is rewritten into Homebrew's (`==> Downloading`, `==> Pouring`, `🍺`), caveats are shown, and old versions are cleaned up afterwards. `--ask`, `--skip-link`, `--overwrite`, `--as-dependency`, `--only-dependencies`, `--require-sha`, `--quarantine`, `--display-times`, `--appdir`/`HOMEBREW_CASK_OPTS` work. |
| `install --no-record F` | `mise bootstrap packages apply brew:F` (install without writing config) |
| `uninstall F` | removes `brew:F` from **every** mise config file that declares it, refuses if installed formulae depend on it, deletes the keg (`--force`: all versions), then autoremoves orphaned dependencies (`HOMEBREW_NO_AUTOREMOVE=1` to skip) |
| `uninstall --cask C [--zap]` | removes `brew-cask:C` from config, runs the cask's `uninstall` stanza (launchctl, quit, signal, login items, kext, scripts, pkgutil, delete, trash, rmdir), removes the artifacts mise recorded in `.mise-cask.toml`, then `zap` with `--zap`. `-n` shows the plan. |
| `reinstall F` / `reinstall --cask C` | formula: keg moved aside, `apply`, restored on failure; cask: native uninstall + `apply` |
| `upgrade [F…]` | every outdated formula/cask (as `brew outdated` reports, minus pins) plus outdated dependents, passed explicitly to `mise bootstrap packages upgrade`; `--greedy*`, `HOMEBREW_UPGRADE_GREEDY(_CASKS)`, `-n`, `--ask` |
| `update` | refreshes mise's API cache and third-party tap metadata, lists outdated packages |
| `autoremove` | Homebrew's rule: removes formulae installed as dependencies that nothing needs (`--mise` = `mise bootstrap packages prune`) |
| `postinstall F` | reinstall through mise (mise runs post-install steps when pouring) |
| `tap U/R [URL]` / `untap` | `mise bootstrap packages brew tap` / `untap` |
| `bundle …` | Brewfiles are evaluated with Ruby when available (full DSL), else a built-in parser. `brew`/`cask`/`mas`/`flatpak` → `packages use`, `go`/`cargo`/`npm`/`uv` → `mise use`, `vscode`/`cursor` → the editor CLI, `whalebrew` → `whalebrew`. Honours `link:`, `conflicts_with:`, `restart_service:`, `start_service:`, `postinstall:`, `cask_args`/`args: { appdir: }`, `greedy:`; upgrades outdated entries unless `--no-upgrade`. `check`, `list`, `dump` (with mas names, VS Code extensions, mise tools), `cleanup`, `exec`/`sh`/`env` (`--services`, `--check`), `add`, `remove`, `edit`. |
| `services …` | `launchctl` (macOS) / `systemctl --user` (Linux) with the plist/unit (and timer) shipped in the keg; `--all`, `--file`, `--sudo-service-user`, `--max-wait`, `--no-wait`, `--keep`, `--json` |
| `link`, `unlink`, `pin`, `unpin`, `tab`, `cleanup` | native on the prefix, holding mise's own lock (`var/homebrew/locks/mise-bootstrap.lock`) so they never race a mise run |
| `list`, `info`, `search`, `desc`, `deps`, `uses`, `leaves`, `missing`, `outdated`, `options`, `formulae`, `casks`, `which-formula`, `home`, `cat`, `log`, `formula`, `edit`, `linkage`, `fetch`, `unpack` | native: Cellar/Caskroom + API cache (with per-platform `variations`, macOS bounds, requirements); third-party taps from GitHub; analytics, `log`, PR search and sources from the web |
| `--prefix`, `--cellar`, `--caskroom`, `--cache`, `--repository`, `--version`, `--env`, `shellenv`, `config`, `doctor`, `commands`, `command`, `help`, `sh`, `alias`, `unalias`, `command-not-found-init` | native; `--env`/`sh` provide a superenv with compiler shims that add dependency include/lib paths |

Homebrew aliases work (`ls`, `rm`, `remove`, `abv`, `dr`, `-S`, `ln`, `up`, `homepage`,
`--repo`, `--config`, …), as do external commands (`brew foo` runs `brew-foo` from
`PATH`) and `brew alias` aliases (`~/.brew-aliases`). Unknown options are rejected like
Homebrew does (`MISEBREW_LENIENT=1` to ignore them).

## Configuration

| Variable | Effect |
| --- | --- |
| `MISEBREW_CONFIG` | where new entries are written: `global` (default, `-g`), `local` (nearest `mise.toml`), or a config file path (`-p`) |
| `MISEBREW_ENV` | also pass `-e ENV` (writes `mise.ENV.toml`) |
| `MISEBREW_RECORD=0` | install without recording, like `--no-record` |
| `MISEBREW_PREFIX` | Homebrew prefix (default `/opt/homebrew`, `/usr/local`, or `/home/linuxbrew/.linuxbrew`) |
| `MISEBREW_MISE` | path to the `mise` binary |
| `MISEBREW_RAW_OUTPUT=1` | show mise's own output instead of Homebrew-style output (also with `--verbose`) |
| `MISEBREW_RUBY` / `MISEBREW_BUNDLE_NO_RUBY=1` | Ruby used to evaluate Brewfiles / force the built-in parser |
| `MISEBREW_BOTTLE_TAG`, `MISEBREW_MACOS_VERSION` | override platform detection |
| `MISEBREW_HOMEBREW_VERSION` | version printed by `brew --version` |
| `MISEBREW_LENIENT=1` | accept unknown options |
| Homebrew's own: `HOMEBREW_NO_INSTALL_CLEANUP`, `HOMEBREW_CLEANUP_PERIODIC_FULL_DAYS`, `HOMEBREW_CLEANUP_MAX_AGE_DAYS`, `HOMEBREW_NO_AUTOREMOVE`, `HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK`, `HOMEBREW_UPGRADE_GREEDY(_CASKS)`, `HOMEBREW_CASK_OPTS`, `HOMEBREW_NO_ANALYTICS`, `HOMEBREW_NO_ENV_HINTS`, `HOMEBREW_BUNDLE_FILE(_GLOBAL)`, `HOMEBREW_BUNDLE_NO_UPGRADE`, `HOMEBREW_GITHUB_API_TOKEN`, `HOMEBREW_EDITOR`, `HOMEBREW_BROWSER`, `HOMEBREW_CC`, `HOMEBREW_MAKE_JOBS`, `HOMEBREW_ENV=std`, `HOMEBREW_(NO_)COLOR`, `HOMEBREW_VERBOSE`, `HOMEBREW_DEBUG` | as in Homebrew |

mise children run with `MISEBREW_ACTIVE=1`; if anything run by mise calls `brew`
again, mise-brew answers read-only queries and refuses to install or remove.

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
- **No git checkout**: taps are read from GitHub (and cached), not cloned;
  `brew --repository TAP` prints the path Homebrew would use. `edit` opens a
  downloaded copy that does not affect installs.
- `Brewfile.lock.json` is not written.

## Development

```sh
mise run test   # go test ./...
mise run build  # bin/brew
```

The tests use a temporary prefix, so they don't touch `/opt/homebrew`.
