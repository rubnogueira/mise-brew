package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

var (
	stdoutTTY bool
	stderrTTY bool
	colorOut  bool
	colorErr  bool
	verbose   bool
	quiet     bool
	debug     bool
)

func initUI() {
	stdoutTTY = isTTY(os.Stdout)
	stderrTTY = isTTY(os.Stderr)
	colorOut, colorErr = stdoutTTY, stderrTTY
	if os.Getenv("HOMEBREW_COLOR") != "" {
		colorOut, colorErr = true, true
	}
	if os.Getenv("NO_COLOR") != "" || os.Getenv("HOMEBREW_NO_COLOR") != "" {
		colorOut, colorErr = false, false
	}
	verbose = os.Getenv("HOMEBREW_VERBOSE") != ""
	debug = os.Getenv("HOMEBREW_DEBUG") != ""
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func paint(on bool, code, s string) string {
	if !on {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

// ohai prints a blue "==>" header like Homebrew.
func ohai(format string, a ...any) {
	title := fmt.Sprintf(format, a...)
	fmt.Println(paint(colorOut, "34", "==>") + " " + paint(colorOut, "1", title))
}

// oh1 prints a green "==>" header (used for success summaries).
func oh1(format string, a ...any) {
	title := fmt.Sprintf(format, a...)
	fmt.Println(paint(colorOut, "32", "==>") + " " + paint(colorOut, "1", title))
}

func opoo(format string, a ...any) {
	fmt.Fprintln(os.Stderr, paint(colorErr, "4;33", "Warning")+": "+fmt.Sprintf(format, a...))
}

func onoe(format string, a ...any) {
	fmt.Fprintln(os.Stderr, paint(colorErr, "4;31", "Error")+": "+fmt.Sprintf(format, a...))
}

// exitError carries an exit code up to main without calling os.Exit deep in the stack.
type exitError struct{ code int }

func (e exitError) Error() string { return "exit " + strconv.Itoa(e.code) }

func odie(format string, a ...any) {
	onoe(format, a...)
	panic(exitError{1})
}

func exit(code int) { panic(exitError{code}) }

func odebug(format string, a ...any) {
	if debug || verbose {
		fmt.Fprintln(os.Stderr, paint(colorErr, "2", fmt.Sprintf(format, a...)))
	}
}

func check() string        { return paint(colorOut, "32", "✔") }
func cross() string        { return paint(colorOut, "31", "✘") }
func bold(s string) string { return paint(colorOut, "1", s) }

func termWidth() int {
	if v, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && v > 0 {
		return v
	}
	type winsize struct{ Row, Col, X, Y uint16 }
	var ws winsize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(), uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if errno == 0 && ws.Col > 0 {
		return int(ws.Col)
	}
	return 80
}

// visibleLen ignores ANSI escapes.
func visibleLen(s string) int {
	n, esc := 0, false
	for _, r := range s {
		switch {
		case esc:
			if r == 'm' {
				esc = false
			}
		case r == '\033':
			esc = true
		default:
			n++
		}
	}
	return n
}

// putsColumns prints items in columns on a TTY, one per line otherwise (like Homebrew).
func putsColumns(items []string) {
	if len(items) == 0 {
		return
	}
	if !stdoutTTY {
		for _, it := range items {
			fmt.Println(it)
		}
		return
	}
	width := termWidth()
	maxLen := 0
	for _, it := range items {
		if l := visibleLen(it); l > maxLen {
			maxLen = l
		}
	}
	colW := maxLen + 2
	cols := width / colW
	if cols < 1 {
		cols = 1
	}
	rows := (len(items) + cols - 1) / cols
	for r := 0; r < rows; r++ {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			i := c*rows + r
			if i >= len(items) {
				break
			}
			it := items[i]
			b.WriteString(it)
			if c < cols-1 && (c+1)*rows+r < len(items) {
				b.WriteString(strings.Repeat(" ", colW-visibleLen(it)))
			}
		}
		fmt.Println(b.String())
	}
}

func humanSize(n int64) string {
	f := float64(n)
	switch {
	case f >= 1<<30:
		return trimFloat(f/(1<<30)) + "GB"
	case f >= 1<<20:
		return trimFloat(f/(1<<20)) + "MB"
	case f >= 1<<10:
		return trimFloat(f/(1<<10)) + "KB"
	}
	return strconv.FormatInt(n, 10) + "B"
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	if strings.HasSuffix(word, "a") { // formula -> formulae
		return fmt.Sprintf("%d %se", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func padRight(s string, n int) string {
	if l := utf8.RuneCountInString(s); l < n {
		return s + strings.Repeat(" ", n-l)
	}
	return s
}
