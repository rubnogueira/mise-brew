package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// service describes a formula's launchd plist / systemd unit shipped in its keg.
type service struct {
	Formula string
	Label   string
	Source  string // file inside the keg
}

func findService(name string) *service {
	opt := filepath.Join(optDir(), name)
	if runtime.GOOS == "darwin" {
		for _, label := range []string{"sh.brew." + name, "homebrew.mxcl." + name} {
			p := filepath.Join(opt, label+".plist")
			if fileExists(p) {
				return &service{name, label, p}
			}
		}
		return nil
	}
	for _, label := range []string{"sh.brew." + name, "homebrew." + name} {
		p := filepath.Join(opt, label+".service")
		if fileExists(p) {
			return &service{name, label, p}
		}
	}
	return nil
}

func allServices() []*service {
	var out []*service
	for _, n := range installedFormulae() {
		if s := findService(n); s != nil {
			out = append(out, s)
		}
	}
	return out
}

func isRoot() bool { return os.Geteuid() == 0 }

func launchDomain() string {
	if isRoot() {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

func installDir() string {
	if runtime.GOOS == "darwin" {
		if isRoot() {
			return "/Library/LaunchDaemons"
		}
		return filepath.Join(homeDir(), "Library", "LaunchAgents")
	}
	if isRoot() {
		return "/usr/lib/systemd/system"
	}
	return filepath.Join(homeDir(), ".config", "systemd", "user")
}

func (s *service) installedPath() string {
	ext := ".plist"
	if runtime.GOOS != "darwin" {
		ext = ".service"
	}
	return filepath.Join(installDir(), s.Label+ext)
}

type serviceState struct {
	Loaded   bool
	Running  bool
	PID      int
	ExitCode *int
}

var pidRe = regexp.MustCompile(`(?m)^\s*pid = (\d+)`)
var exitRe = regexp.MustCompile(`(?m)^\s*last exit code = (-?\d+)`)
var stateRe = regexp.MustCompile(`(?m)^\s*state = (\w+)`)

func (s *service) state() serviceState {
	var st serviceState
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("launchctl", "print", launchDomain()+"/"+s.Label).Output()
		if err != nil {
			return st
		}
		st.Loaded = true
		txt := string(out)
		if m := stateRe.FindStringSubmatch(txt); m != nil && m[1] == "running" {
			st.Running = true
		}
		if m := pidRe.FindStringSubmatch(txt); m != nil {
			st.PID, _ = strconv.Atoi(m[1])
		}
		if m := exitRe.FindStringSubmatch(txt); m != nil {
			c, _ := strconv.Atoi(m[1])
			st.ExitCode = &c
		}
		return st
	}
	args := systemctlArgs("show", s.Label, "--property=ActiveState,MainPID,ExecMainStatus,LoadState")
	out, err := exec.Command("systemctl", args...).Output()
	if err != nil {
		return st
	}
	props := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			props[k] = v
		}
	}
	st.Loaded = props["LoadState"] == "loaded" && props["ActiveState"] != "inactive"
	st.Running = props["ActiveState"] == "active"
	st.PID, _ = strconv.Atoi(props["MainPID"])
	if c, err := strconv.Atoi(props["ExecMainStatus"]); err == nil && props["ActiveState"] == "failed" {
		st.ExitCode = &c
	}
	return st
}

func systemctlArgs(args ...string) []string {
	if isRoot() {
		return args
	}
	return append([]string{"--user"}, args...)
}

func (s *service) status() (string, *int) {
	st := s.state()
	switch {
	case st.Running:
		return "started", nil
	case st.Loaded && st.ExitCode != nil && *st.ExitCode != 0:
		return "error", st.ExitCode
	case st.Loaded:
		return "scheduled", nil
	case fileExists(s.installedPath()):
		return "stopped", nil
	}
	return "none", nil
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return strconv.Itoa(os.Getuid())
}

func cmdServices(args []string) {
	o := parseArgs(args, []string{"file", "sudo-service-user", "max-wait"}, nil)
	o.applyGlobal()
	if v, ok := o.Val("max-wait"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			stopWait = time.Duration(n) * time.Second
		}
	}
	if o.Has("no-wait") {
		stopWait = 0
	}
	sub := "list"
	pos := o.Pos
	if len(pos) > 0 {
		sub, pos = pos[0], pos[1:]
	}
	targets := func() []*service {
		if o.Has("all") {
			return allServices()
		}
		if len(pos) == 0 {
			odie("Formula(e) missing, please provide a formula name or use `--all`.")
		}
		var out []*service
		for _, n := range pos {
			r := resolveInstalledFormula(n)
			if r == "" {
				odie("Formula `%s` is not installed.", n)
			}
			s := findService(r)
			if s == nil {
				odie("Formula `%s` has not implemented #plist, #service or provided a locatable service file.", r)
			}
			out = append(out, s)
		}
		return out
	}
	switch sub {
	case "list", "ls":
		servicesList(o.Has("json"))
	case "info", "i":
		servicesInfo(targets(), o.Has("json"))
	case "run":
		for _, s := range targets() {
			serviceRun(s)
		}
	case "start", "launch", "load", "s":
		for _, s := range targets() {
			serviceStart(s, o)
		}
	case "stop", "unload", "terminate", "term", "t", "u":
		for _, s := range targets() {
			serviceStop(s, o.Has("keep"))
		}
	case "kill", "k":
		for _, s := range targets() {
			serviceKill(s)
		}
	case "restart", "relaunch", "reload", "r":
		for _, s := range targets() {
			serviceStop(s, false)
			serviceStart(s, o)
		}
	case "cleanup", "clean", "cl", "rm":
		servicesCleanup()
	default:
		odie("Unknown `brew services` subcommand: %s", sub)
	}
}

func servicesList(asJSON bool) {
	svcs := allServices()
	type row struct {
		Name     string  `json:"name"`
		Status   string  `json:"status"`
		User     *string `json:"user"`
		File     *string `json:"file"`
		ExitCode *int    `json:"exit_code"`
	}
	var rows []row
	for _, s := range svcs {
		st, code := s.status()
		r := row{Name: s.Formula, Status: st, ExitCode: code}
		if st != "none" {
			u, f := currentUser(), s.installedPath()
			if st == "started" && !fileExists(f) {
				f = s.Source
			}
			r.User, r.File = &u, &f
		}
		rows = append(rows, r)
	}
	if asJSON {
		if rows == nil {
			rows = []row{}
		}
		printJSON(rows)
		return
	}
	if len(rows) == 0 {
		fmt.Println("No services available to control with `brew services`")
		return
	}
	w := 4
	for _, r := range rows {
		if len(r.Name) > w {
			w = len(r.Name)
		}
	}
	fmt.Println(bold(padRight("Name", w) + " Status    User File"))
	for _, r := range rows {
		st := r.Status
		color := map[string]string{"started": "32", "error": "31", "scheduled": "33", "stopped": "33"}[st]
		disp := padRight(st, 9)
		if r.ExitCode != nil {
			disp = padRight(fmt.Sprintf("%s  %d", st, *r.ExitCode), 9)
		}
		if color != "" {
			disp = paint(colorOut, color, disp)
		}
		user, file := "", ""
		if r.User != nil {
			user = *r.User
		}
		if r.File != nil {
			file = tildify(*r.File)
		}
		fmt.Printf("%s %s %s %s\n", padRight(r.Name, w), disp, user, file)
	}
}

func servicesInfo(svcs []*service, asJSON bool) {
	type info struct {
		Name        string `json:"name"`
		Label       string `json:"service_name"`
		Running     bool   `json:"running"`
		Loaded      bool   `json:"loaded"`
		Schedulable bool   `json:"schedulable"`
		PID         *int   `json:"pid"`
		ExitCode    *int   `json:"exit_code"`
		User        string `json:"user"`
		Status      string `json:"status"`
		File        string `json:"file"`
		Registered  bool   `json:"registered"`
	}
	var out []info
	for _, s := range svcs {
		st := s.state()
		status, _ := s.status()
		i := info{Name: s.Formula, Label: s.Label, Running: st.Running, Loaded: st.Loaded, ExitCode: st.ExitCode,
			User: currentUser(), Status: status, File: s.Source, Registered: fileExists(s.installedPath())}
		if st.PID > 0 {
			p := st.PID
			i.PID = &p
		}
		if i.Registered {
			i.File = s.installedPath()
		}
		out = append(out, i)
	}
	if asJSON {
		printJSON(out)
		return
	}
	for idx, i := range out {
		if idx > 0 {
			fmt.Println()
		}
		fmt.Printf("%s (%s)\n", bold(i.Name), i.Label)
		fmt.Printf("Running: %s\n", yesNo(i.Running))
		fmt.Printf("Loaded: %s\n", yesNo(i.Loaded))
		fmt.Printf("Schedulable: %s\n", yesNo(false))
		if i.PID != nil {
			fmt.Printf("PID: %d\n", *i.PID)
		}
		if i.ExitCode != nil {
			fmt.Printf("Exit code: %d\n", *i.ExitCode)
		}
		fmt.Printf("User: %s\nFile: %s\n", i.User, tildify(i.File))
	}
}

func yesNo(b bool) string {
	if b {
		return check()
	}
	return cross()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func runQuiet(name string, args ...string) error {
	odebug("%s %s", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	if verbose || debug {
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	}
	return cmd.Run()
}

func serviceStart(s *service, o Opts) {
	src := s.Source
	if f, ok := o.Val("file"); ok && f != "" {
		src = f
	}
	if st := s.state(); st.Running {
		fmt.Printf("Service `%s` already started, use `brew services restart %s` to restart.\n", s.Formula, s.Formula)
		return
	}
	dst := s.installedPath()
	if err := copyFile(src, dst); err != nil {
		odie("could not install %s: %v", dst, err)
	}
	if u, ok := o.Val("sudo-service-user"); ok && u != "" {
		if !isRoot() {
			odie("--sudo-service-user requires running as root (sudo brew services ...).")
		}
		if runtime.GOOS == "darwin" {
			data, _ := os.ReadFile(dst)
			txt := string(data)
			if i := strings.LastIndex(txt, "</dict>"); i >= 0 && !strings.Contains(txt, "<key>UserName</key>") {
				txt = txt[:i] + "\t<key>UserName</key>\n\t<string>" + u + "</string>\n" + txt[i:]
				_ = os.WriteFile(dst, []byte(txt), 0o644)
			}
		} else {
			data, _ := os.ReadFile(dst)
			txt := strings.Replace(string(data), "[Service]", "[Service]\nUser="+u, 1)
			_ = os.WriteFile(dst, []byte(txt), 0o644)
		}
	}
	unit := s.Label
	if runtime.GOOS != "darwin" {
		if timer := strings.TrimSuffix(s.Source, ".service") + ".timer"; fileExists(timer) {
			tdst := strings.TrimSuffix(dst, ".service") + ".timer"
			if err := copyFile(timer, tdst); err != nil {
				odie("%v", err)
			}
			unit = s.Label + ".timer"
		}
	}
	if runtime.GOOS == "darwin" {
		_ = runQuiet("launchctl", "enable", launchDomain()+"/"+s.Label)
		if err := runQuiet("launchctl", "bootstrap", launchDomain(), dst); err != nil {
			odie("Failure while executing; `/bin/launchctl bootstrap %s %s` exited with %v.", launchDomain(), dst, err)
		}
	} else {
		_ = runQuiet("systemctl", systemctlArgs("daemon-reload")...)
		if err := runQuiet("systemctl", systemctlArgs("enable", "--now", unit)...); err != nil {
			odie("Failure while executing `systemctl enable --now %s`: %v", unit, err)
		}
	}
	oh1("Successfully started `%s` (label: %s)", s.Formula, s.Label)
}

func serviceRun(s *service) {
	if st := s.state(); st.Running {
		fmt.Printf("Service `%s` already running, use `brew services restart %s` to restart.\n", s.Formula, s.Formula)
		return
	}
	if runtime.GOOS == "darwin" {
		if err := runQuiet("launchctl", "bootstrap", launchDomain(), s.Source); err != nil {
			odie("Failure while executing `launchctl bootstrap %s %s`: %v", launchDomain(), s.Source, err)
		}
	} else {
		dst := s.installedPath()
		if err := copyFile(s.Source, dst); err != nil {
			odie("%v", err)
		}
		_ = runQuiet("systemctl", systemctlArgs("daemon-reload")...)
		if err := runQuiet("systemctl", systemctlArgs("start", s.Label)...); err != nil {
			odie("Failure while executing `systemctl start %s`: %v", s.Label, err)
		}
	}
	oh1("Successfully ran `%s` (label: %s)", s.Formula, s.Label)
}

// stopWait is set from --max-wait / --no-wait.
var stopWait = 60 * time.Second

func waitStopped(s *service) {
	if stopWait <= 0 {
		return
	}
	deadline := time.Now().Add(stopWait)
	for time.Now().Before(deadline) {
		if !s.state().Running {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	opoo("Service `%s` is still running after %s", s.Formula, stopWait)
}

func serviceStop(s *service, keep bool) {
	st := s.state()
	if !st.Loaded && !fileExists(s.installedPath()) {
		opoo("Service `%s` is not started.", s.Formula)
		return
	}
	fmt.Printf("Stopping `%s`... (might take a while)\n", s.Formula)
	if runtime.GOOS == "darwin" {
		if st.Loaded {
			_ = runQuiet("launchctl", "bootout", launchDomain()+"/"+s.Label)
		}
	} else {
		verb := "stop"
		if !keep {
			verb = "disable"
		}
		a := []string{verb}
		if !keep {
			a = append(a, "--now")
		}
		_ = runQuiet("systemctl", systemctlArgs(append(a, s.Label)...)...)
	}
	waitStopped(s)
	if !keep {
		_ = os.Remove(s.installedPath())
		_ = os.Remove(strings.TrimSuffix(s.installedPath(), ".service") + ".timer")
		if runtime.GOOS != "darwin" {
			_ = runQuiet("systemctl", systemctlArgs("daemon-reload")...)
		}
	}
	oh1("Successfully stopped `%s` (label: %s)", s.Formula, s.Label)
}

func serviceKill(s *service) {
	if !s.state().Running {
		opoo("Service `%s` is not started.", s.Formula)
		return
	}
	var err error
	if runtime.GOOS == "darwin" {
		err = runQuiet("launchctl", "kill", "SIGTERM", launchDomain()+"/"+s.Label)
	} else {
		err = runQuiet("systemctl", systemctlArgs("kill", s.Label)...)
	}
	if err != nil {
		odie("Unable to kill `%s`: %v", s.Formula, err)
	}
	oh1("Successfully killed `%s` (label: %s)", s.Formula, s.Label)
}

// servicesCleanup removes registered service files whose formula is gone.
func servicesCleanup() {
	entries, _ := os.ReadDir(installDir())
	var removed []string
	for _, e := range entries {
		n := e.Name()
		var label, formula string
		for _, p := range []string{"sh.brew.", "homebrew.mxcl.", "homebrew."} {
			if strings.HasPrefix(n, p) {
				label = strings.TrimSuffix(strings.TrimSuffix(n, ".plist"), ".service")
				formula = strings.TrimPrefix(label, p)
				break
			}
		}
		if label == "" || isFormulaInstalled(formula) {
			continue
		}
		if runtime.GOOS == "darwin" {
			_ = runQuiet("launchctl", "bootout", launchDomain()+"/"+label)
		} else {
			_ = runQuiet("systemctl", systemctlArgs("disable", "--now", label)...)
		}
		_ = os.Remove(filepath.Join(installDir(), n))
		removed = append(removed, formula)
	}
	sort.Strings(removed)
	if len(removed) == 0 {
		fmt.Println("All user-space services OK, nothing cleaned...")
		return
	}
	for _, r := range removed {
		fmt.Printf("Removing unused service file: %s\n", r)
	}
}
