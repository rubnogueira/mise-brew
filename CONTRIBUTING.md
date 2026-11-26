# Contributing

Bug reports and pull requests are welcome.

## Setup

```sh
mise install      # Go version from mise.toml
mise run test     # go vet + go test ./...
mise run build    # bin/brew
```

## Layout

| Path | What lives there |
| --- | --- |
| `cmd/brew` | `main` package: calls `cli.Main` |
| `internal/cli` | command table, flag specs, every `brew` command |
| `internal/homebrew` | API cache, taps, platform/bottle tags, Cellar/Caskroom, linking, prefix lock |
| `internal/mise` | running mise and editing mise config declarations |
| `internal/cask` | native cask `uninstall`/`zap` stanzas |
| `internal/brewfile` | Brewfile evaluation (Ruby DSL and built-in parser) |
| `internal/version` | Homebrew version comparison |
| `internal/paths`, `internal/ui`, `internal/buildinfo` | locations, output helpers, version stamp |

## Rules of thumb

- Match Homebrew's output, flags and exit codes; document anything that can't match
  in the README's "What cannot match Homebrew" section.
- Every name a user types must pass `homebrew.ValidName` before it is joined onto a
  path, and anything that deletes must check it stays inside the Cellar, Caskroom or
  the cask's declared paths.
- Tests must never touch the real prefix, mise config or `$HOME`: use
  `testutil.FakePrefix` and `t.Setenv("HOME", t.TempDir())`.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/).
