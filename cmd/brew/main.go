// Command brew is a drop-in Homebrew CLI whose installs are performed by
// `mise bootstrap packages`.
package main

import "github.com/rubnogueira/mise-brew/internal/cli"

func main() { cli.Main() }
