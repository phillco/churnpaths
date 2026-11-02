package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var targets = []string{"spotlight", "timemachine", "backblaze"}

const tmNote = "Time Machine exclusions are sticky: removing a list entry never re-includes a folder; undo with `tmutil removeexclusion PATH`"
const bbNote = "dry run is the default; nothing is written to Backblaze without -a"

type Options struct {
	Source, Hostname, XML, StateDir, State, Command string
	Targets                                         []string
	Apply, Check, DryRun, List, JSON, Help          bool
}

type Plan struct {
	Add     []string `json:"add"`
	Remove  []string `json:"remove"`
	InSync  bool     `json:"in_sync"`
	Applied bool     `json:"applied"`
	Failed  *int     `json:"failed,omitempty"`
	Detail  string   `json:"detail"`
}

type App struct {
	Out, Err       io.Writer
	Platform, Host string
	Run            Runner
	LookPath       func(string) (string, error)
	ReadSpotlight  func() ([]string, error)
	WriteSpotlight func(string, bool) error
	WriteBackup    func(string, bool) error
	WriteJSON      func(string, any) error
	Planners       map[string]func(Options, Result) (Plan, error)
}

func localHost() string { h, _ := os.Hostname(); return strings.SplitN(h, ".", 2)[0] }
func commandRun(name string, args []string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errout bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errout
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return out.String(), errout.String(), err
}
func newApp(out, err io.Writer) *App {
	return &App{Out: out, Err: err, Platform: runtime.GOOS, Host: localHost(), Run: commandRun, LookPath: exec.LookPath, ReadSpotlight: readSpotlight, WriteSpotlight: writeSpotlight, WriteBackup: setBackupExcluded, WriteJSON: writeJSON}
}

// Set by the release build with -ldflags "-X main.version=<tag>".
var version = "dev"

func main() { os.Exit(newApp(os.Stdout, os.Stderr).RunCLI(os.Args[1:])) }

func parseOptions(argv []string, config map[string]any, host string) (Options, error) {
	o := Options{Source: defaultSource(config), State: defaultState(config), Hostname: host, XML: defaultXML}
	values := map[string]*string{"s": &o.Source, "source": &o.Source, "H": &o.Hostname, "hostname": &o.Hostname, "x": &o.XML, "xml": &o.XML, "state-dir": &o.StateDir, "state": &o.State}
	flags := map[string]*bool{"a": &o.Apply, "apply": &o.Apply, "c": &o.Check, "check": &o.Check, "n": &o.DryRun, "dry-run": &o.DryRun, "l": &o.List, "list": &o.List, "j": &o.JSON, "json": &o.JSON, "h": &o.Help, "help": &o.Help}
	mechanism := ""
	isValue := func(key string) bool {
		return values[key] != nil || key == "t" || key == "target" || key == "m" || key == "mechanism"
	}
	setValue := func(key, val string) error {
		if ptr := values[key]; ptr != nil {
			*ptr = val
			return nil
		}
		if !contains(targets, val) && !(val == "all" && (key == "t" || key == "target")) {
			return fmt.Errorf("argument --%s: invalid choice: '%s'", key, val)
		}
		if key == "m" || key == "mechanism" {
			mechanism = val
		} else {
			o.Targets = append(o.Targets, val)
		}
		return nil
	}
	nextValue := func(i *int, key string) (string, error) {
		if *i+1 >= len(argv) {
			return "", fmt.Errorf("argument --%s: expected one argument", key)
		}
		value := argv[*i+1]
		if strings.HasPrefix(value, "-") && value != "-" {
			if _, err := strconv.ParseFloat(value, 64); err != nil {
				return "", fmt.Errorf("argument --%s: expected one argument", key)
			}
		}
		*i = *i + 1
		return value, nil
	}
	longNames := []string{"source", "hostname", "xml", "state-dir", "state", "target", "mechanism", "apply", "check", "dry-run", "list", "json", "help"}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if arg == "--" {
			for _, v := range argv[i+1:] {
				if v != "status" || o.Command != "" {
					return o, fmt.Errorf("unrecognized arguments: %s", v)
				}
				o.Command = v
			}
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if arg != "status" || o.Command != "" {
				return o, fmt.Errorf("invalid command: %s", arg)
			}
			o.Command = arg
			continue
		}
		if strings.HasPrefix(arg, "--") {
			key, val, has := strings.Cut(arg[2:], "=")
			if !isValue(key) && flags[key] == nil {
				matches := []string{}
				for _, name := range longNames {
					if strings.HasPrefix(name, key) {
						matches = append(matches, name)
					}
				}
				if len(matches) != 1 {
					return o, fmt.Errorf("unrecognized or ambiguous argument: %s", arg)
				}
				key = matches[0]
			}
			if f := flags[key]; f != nil {
				if has {
					return o, fmt.Errorf("argument --%s: ignored explicit argument", key)
				}
				*f = true
				if key == "help" {
					return o, nil
				}
				continue
			}
			if !has {
				var e error
				val, e = nextValue(&i, key)
				if e != nil {
					return o, e
				}
			}
			if e := setValue(key, val); e != nil {
				return o, e
			}
			continue
		}
		cluster := arg[1:]
		for j := 0; j < len(cluster); j++ {
			key := cluster[j : j+1]
			if f := flags[key]; f != nil {
				*f = true
				if key == "h" {
					return o, nil
				}
				continue
			}
			if !isValue(key) {
				return o, fmt.Errorf("unrecognized arguments: %s", arg)
			}
			val := cluster[j+1:]
			inline := len(val) > 0
			if strings.HasPrefix(val, "=") {
				val = val[1:]
			}
			if !inline {
				var e error
				val, e = nextValue(&i, key)
				if e != nil {
					return o, e
				}
			}
			if e := setValue(key, val); e != nil {
				return o, e
			}
			break
		}
	}
	if mechanism != "" {
		o.Targets = append(o.Targets, mechanism)
	}
	modes := 0
	if o.Apply {
		modes++
	}
	if o.Check {
		modes++
	}
	if o.DryRun {
		modes++
	}
	if modes > 1 {
		return o, errors.New("-a, -c and -n are mutually exclusive")
	}
	if o.Command == "status" && modes > 0 {
		return o, errors.New("status takes no -a/-c/-n")
	}
	if o.List && (o.Apply || o.Check || o.Command != "") {
		return o, errors.New("-l only resolves; it cannot be combined with -a, -c or status")
	}
	return o, nil
}

func contains(xs []string, s string) bool {
	for _, v := range xs {
		if v == s {
			return true
		}
	}
	return false
}
func stringSet(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range xs {
		m[s] = true
	}
	return m
}
func setKeys(m map[string]bool) []string {
	xs := []string{}
	for s, b := range m {
		if b {
			xs = append(xs, s)
		}
	}
	sort.Strings(xs)
	return xs
}
func difference(a, b map[string]bool) []string {
	xs := []string{}
	for s := range a {
		if !b[s] {
			xs = append(xs, s)
		}
	}
	sort.Strings(xs)
	return xs
}
func (a *App) availability(name string, o Options) string {
	if a.Platform != "darwin" {
		return "macOS only"
	}
	if name == "timemachine" {
		if _, err := a.LookPath("tmutil"); err != nil {
			return "tmutil not found"
		}
	}
	if name == "backblaze" {
		if _, err := os.Stat(o.XML); err != nil {
			return fmt.Sprintf("Backblaze not installed (%s not found)", o.XML)
		}
	}
	return ""
}
func selected(o Options) ([]string, bool) {
	if len(o.Targets) == 0 || contains(o.Targets, "all") {
		return targets, false
	}
	xs := []string{}
	for _, n := range targets {
		if contains(o.Targets, n) {
			xs = append(xs, n)
		}
	}
	return xs, true
}
func (a *App) RunCLI(argv []string) int {
	if len(argv) == 1 {
		switch argv[0] {
		case "-V", "--version":
			fmt.Fprintln(a.Out, "churnpaths", version)
			return 0
		case "-h", "--help":
			fmt.Fprint(a.Out, helpText)
			return 0
		}
	}
	config, err := loadConfig()
	if err != nil {
		return a.fail(err)
	}
	o, err := parseOptions(argv, config, a.Host)
	if err != nil {
		return a.fail(err)
	}
	if o.Help {
		fmt.Fprint(a.Out, helpText)
		return 0
	}
	result, err := Resolve(expandUser(o.Source), o.Hostname)
	if err != nil {
		return a.fail(err)
	}
	if o.List {
		if o.JSON {
			a.json(result)
		} else {
			for _, kv := range []struct {
				k string
				v []string
			}{{"paths", result.Paths}, {"missing", result.Missing}, {"unmatched", result.Unmatched}, {"skipped", result.Skipped}} {
				for _, p := range kv.v {
					fmt.Fprintf(a.Out, "%s: %s\n", kv.k, p)
				}
			}
		}
		return 0
	}
	names, explicit := selected(o)
	if explicit {
		for _, n := range names {
			if reason := a.availability(n, o); reason != "" {
				return a.fail(fmt.Errorf("%s is unavailable: %s", n, reason))
			}
		}
	}
	if o.Apply && foldHost(o.Hostname) != foldHost(a.Host) {
		return a.fail(errors.New("--apply hostname must match this machine"))
	}
	report := map[string]any{}
	code := 0
	drift := false
	available := false
	for _, name := range names {
		if reason := a.availability(name, o); reason != "" {
			report[name] = map[string]string{"unavailable": reason}
			if !o.JSON {
				fmt.Fprintf(a.Out, "%s: skipped (%s)\n", name, reason)
			}
			continue
		}
		available = true
		if o.Command == "status" {
			rows, e := a.folderStates(name, o, result)
			if e != nil {
				fmt.Fprintf(a.Err, "churnpaths: %s: %s\n", name, e)
				code = 2
				continue
			}
			report[name] = rows
			if !o.JSON {
				fmt.Fprintf(a.Out, "%s:\n", name)
				for _, group := range []struct {
					paths  []string
					exists bool
				}{{result.Paths, true}, {result.Missing, false}} {
					for _, p := range group.paths {
						mark := "-"
						suffix := ""
						if strings.HasPrefix(rows[p], "excluded") {
							mark = "+"
						}
						if !group.exists {
							mark = "?"
							suffix = " (missing)"
						}
						fmt.Fprintf(a.Out, "  %s %s%s\n", mark, p, suffix)
					}
				}
			}
			continue
		}
		plan, e := a.plan(name, o, result)
		if e != nil {
			fmt.Fprintf(a.Err, "churnpaths: %s: %s\n", name, e)
			report[name] = map[string]string{"error": e.Error()}
			code = 2
			continue
		}
		report[name] = plan
		if plan.Failed != nil && *plan.Failed != 0 {
			code = 2
		}
		drift = drift || !plan.InSync
		if !o.JSON {
			a.printPlan(name, plan, o)
		}
	}
	if o.JSON {
		if o.Command == "status" {
			a.json(map[string]any{"targets": report})
		} else {
			a.json(struct {
				Result
				Targets map[string]any `json:"targets"`
			}{result, report})
		}
	} else if !available && o.Command != "status" {
		fmt.Fprintln(a.Err, "churnpaths: no target available on this machine")
	}
	if code != 0 {
		return code
	}
	if o.Check && drift {
		return 1
	}
	return 0
}
func (a *App) fail(err error) int { fmt.Fprintf(a.Err, "churnpaths: %s\n", err); return 2 }
func (a *App) json(v any) {
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
func (a *App) printPlan(name string, p Plan, o Options) {
	label := "dry run"
	if o.Apply {
		label = "applied"
	} else if o.Check {
		label = "check"
	}
	summary := ""
	if name == "timemachine" {
		word := "would exclude"
		if o.Apply {
			word = "excluded"
		}
		summary = fmt.Sprintf("%s %d", word, len(p.Add))
	} else {
		add, remove := "add", "remove"
		if o.Apply {
			add, remove = "added", "removed"
		}
		summary = fmt.Sprintf("%s %d, %s %d", add, len(p.Add), remove, len(p.Remove))
	}
	fmt.Fprintf(a.Out, "%s [%s]: %s, %s\n", name, label, summary, p.Detail)
	for _, s := range p.Add {
		fmt.Fprintf(a.Out, "  + %s\n", s)
	}
	for _, s := range p.Remove {
		fmt.Fprintf(a.Out, "  - %s\n", s)
	}
	if name == "timemachine" {
		fmt.Fprintf(a.Out, "  note: %s\n", tmNote)
	} else if name == "backblaze" && !o.Apply && !o.Check {
		fmt.Fprintf(a.Out, "  note: %s\n", bbNote)
	}
}
func (a *App) plan(name string, o Options, r Result) (Plan, error) {
	if f := a.Planners[name]; f != nil {
		return f(o, r)
	}
	switch name {
	case "spotlight":
		return a.planSpotlight(o, r)
	case "timemachine":
		return a.planTimeMachine(o, r)
	default:
		return a.planBackblaze(o, r)
	}
}
func (a *App) planBackblaze(o Options, r Result) (Plan, error) {
	p := &XMLPolicy{Path: o.XML, StateDir: o.StateDir}
	owned, err := p.Owned()
	if err != nil {
		return Plan{}, err
	}
	desired := stringSet(append(append([]string{}, r.Paths...), r.Missing...))
	add, remove := difference(desired, stringSet(owned)), difference(stringSet(owned), desired)
	applied := false
	if o.Apply && (len(add)+len(remove) > 0) {
		if err = p.SetMany(add, true); err != nil {
			return Plan{}, err
		}
		if err = p.SetMany(remove, false); err != nil {
			return Plan{}, err
		}
		after, e := p.Owned()
		if e != nil {
			return Plan{}, e
		}
		if strings.Join(after, "\x00") != strings.Join(setKeys(desired), "\x00") {
			return Plan{}, errors.New("readback differs from requested rules")
		}
		applied = true
	}
	return Plan{Add: add, Remove: remove, InSync: applied || len(add)+len(remove) == 0, Applied: applied, Detail: fmt.Sprintf("%d desired, %d owned before", len(desired), len(owned))}, nil
}
func (a *App) planTimeMachine(o Options, r Result) (Plan, error) {
	paths := append([]string{}, r.Paths...)
	sort.Strings(paths)
	states, err := exclusionStates(paths, a.Run)
	if err != nil {
		return Plan{}, err
	}
	pending := []string{}
	for _, p := range paths {
		if !states[p] {
			pending = append(pending, p)
		}
	}
	added := []string{}
	failures := 0
	if o.Apply {
		for start := 0; start < len(pending); start += 50 {
			end := start + 50
			if end > len(pending) {
				end = len(pending)
			}
			batch := pending[start:end]
			for _, p := range batch {
				if e := a.WriteBackup(p, true); e != nil {
					failures++
					fmt.Fprintf(a.Err, "failed: %s: %s\n", p, e)
				}
			}
			after, e := exclusionStates(batch, a.Run)
			if e != nil {
				return Plan{}, e
			}
			for _, p := range batch {
				if after[p] {
					added = append(added, p)
				} else {
					failures++
					fmt.Fprintf(a.Err, "failed: %s: still included after write\n", p)
				}
			}
		}
	} else {
		added = pending
	}
	sync := len(pending) == 0
	if o.Apply {
		sync = failures == 0
	}
	return Plan{Add: added, Remove: []string{}, InSync: sync, Applied: o.Apply && failures == 0, Failed: &failures, Detail: fmt.Sprintf("already excluded %d, missing %d", len(paths)-len(pending), len(r.Missing))}, nil
}
func (a *App) folderStates(name string, o Options, r Result) (map[string]string, error) {
	excluded := map[string]bool{}
	var err error
	switch name {
	case "spotlight":
		var xs []string
		xs, err = a.ReadSpotlight()
		excluded = stringSet(xs)
	case "timemachine":
		excluded, err = exclusionStates(r.Paths, a.Run)
	default:
		var xs []string
		xs, err = (&XMLPolicy{Path: o.XML, StateDir: o.StateDir}).Owned()
		excluded = stringSet(xs)
	}
	if err != nil {
		return nil, err
	}
	rows := map[string]string{}
	for _, p := range r.Paths {
		rows[p] = "included"
		if excluded[p] {
			rows[p] = "excluded"
		}
	}
	for _, p := range r.Missing {
		rows[p] = "included, missing"
		if excluded[p] {
			rows[p] = "excluded, missing"
		}
	}
	return rows, nil
}
func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".churnpaths-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (a *App) planSpotlight(o Options, r Result) (Plan, error) {
	statePath := expandUser(o.State)
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Plan{}, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "spotlight.lock"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Plan{}, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return Plan{}, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	owned := map[string]bool{}
	data, err := os.ReadFile(statePath)
	if err == nil {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil || string(raw["version"]) != "1" || len(raw["managed"]) == 0 || raw["managed"][0] != '[' {
			return Plan{}, errors.New("invalid Spotlight ownership state")
		}
		var xs []any
		if json.Unmarshal(raw["managed"], &xs) != nil {
			return Plan{}, errors.New("invalid Spotlight ownership state")
		}
		for _, v := range xs {
			s, ok := v.(string)
			if !ok {
				return Plan{}, errors.New("folder paths must be strings")
			}
			p, e := normalized(s, true)
			if e != nil {
				return Plan{}, e
			}
			owned[p] = true
		}
	} else if !os.IsNotExist(err) {
		return Plan{}, err
	}
	currentList, err := a.ReadSpotlight()
	if err != nil {
		return Plan{}, err
	}
	current := stringSet(currentList)
	for p := range owned {
		if !current[p] {
			delete(owned, p)
		}
	}
	desired := stringSet(r.Paths)
	declared := stringSet(append(append([]string{}, r.Paths...), r.Missing...))
	add, remove := difference(desired, current), difference(owned, declared)
	save := func() error {
		return a.WriteJSON(statePath, struct {
			Version int      `json:"version"`
			Managed []string `json:"managed"`
		}{1, setKeys(owned)})
	}
	if o.Apply {
		if foldHost(o.Hostname) != foldHost(a.Host) {
			return Plan{}, errors.New("--apply hostname must match this machine")
		}
		if err = save(); err != nil {
			return Plan{}, err
		}
		for _, step := range []struct {
			on    bool
			paths []string
		}{{true, add}, {false, remove}} {
			for _, p := range step.paths {
				before, e := a.ReadSpotlight()
				if e != nil {
					return Plan{}, e
				}
				sort.Strings(before)
				event := map[string]any{"time": float64(time.Now().UnixNano()) / 1e9, "path": p, "excluded": step.on, "before": before}
				entry, e := json.Marshal(event)
				if e != nil {
					return Plan{}, e
				}
				log, e := os.OpenFile(filepath.Join(dir, "changes.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					return Plan{}, e
				}
				_, e = log.Write(append(entry, '\n'))
				closeErr := log.Close()
				if e != nil {
					return Plan{}, e
				}
				if closeErr != nil {
					return Plan{}, closeErr
				}
				if step.on {
					owned[p] = true
				}
				if e = save(); e != nil {
					return Plan{}, e
				}
				if e = a.WriteSpotlight(p, step.on); e != nil {
					return Plan{}, e
				}
				if !step.on {
					delete(owned, p)
				}
				if e = save(); e != nil {
					return Plan{}, e
				}
			}
		}
		afterList, e := a.ReadSpotlight()
		if e != nil {
			return Plan{}, e
		}
		after := stringSet(afterList)
		if len(difference(desired, after)) > 0 {
			return Plan{}, errors.New("Spotlight privacy read-back differs from requested changes")
		}
		for _, p := range remove {
			if after[p] {
				return Plan{}, errors.New("Spotlight privacy read-back differs from requested changes")
			}
		}
	}
	return Plan{Add: add, Remove: remove, InSync: len(add)+len(remove) == 0, Applied: o.Apply, Detail: fmt.Sprintf("%d in Search Privacy", len(current))}, nil
}

const helpText = `usage: churnpaths [-h] [-V] [-s SOURCE] [-H HOSTNAME] [-t TARGET] [-j] [-l]
                  [-x XML] [--state-dir STATE_DIR] [--state STATE] [-a | -c | -n] [status]

One list of high-churn, regenerable folders for Spotlight, Time Machine and Backblaze.
Default: preview every available target. macOS only; list resolution also works on Linux.

  status              show each folder's state per target
  -h, --help          show help
  -V, --version       show version and exit
  -s, --source FILE   churn list file
  -H, --hostname HOST host selector for @host lines (default: this host)
  -t, --target TARGET spotlight, timemachine, backblaze or all; repeatable
  -j, --json          JSON output
  -l, --list          resolve folders only; touch no target
  -x, --xml FILE      Backblaze editable exclusions file
  --state-dir DIR     Backblaze backups and change log
  --state FILE        private Spotlight ownership state
  -a, --apply         write changes (default: preview)
  -c, --check         exit 1 when any target has drift
  -n, --dry-run       preview only (the default)
`
