package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type cliFixture struct {
	t                              *testing.T
	root, folder, list, xml, state string
	app                            *App
	out, err                       bytes.Buffer
}

func newCLI(t *testing.T) *cliFixture {
	t.Helper()
	f := &cliFixture{t: t, root: realTemp(t)}
	f.folder = filepath.Join(f.root, "scratch")
	f.list = filepath.Join(f.root, "list.txt")
	f.xml = filepath.Join(f.root, "absent.xml")
	f.state = filepath.Join(f.root, "state", "spotlight.json")
	mustMkdir(t, f.folder)
	mustWrite(t, f.list, "path "+f.folder+"\n")
	t.Setenv("CHURNPATHS_CONFIG", filepath.Join(f.root, "none.toml"))
	t.Setenv("CHURNPATHS_SOURCE", "")
	t.Setenv("CHURNPATHS_STATE", "")
	f.app = newApp(&f.out, &f.err)
	f.app.Platform = "darwin"
	f.app.Host = "test-host"
	f.app.LookPath = func(string) (string, error) { return "/fake/tmutil", nil }
	// Every native and shell entry point fails in tests unless explicitly faked.
	f.app.Run = func(string, []string, time.Duration) (string, string, error) {
		t.Fatal("unexpected shell-out")
		return "", "", errors.New("unexpected shell-out")
	}
	f.app.ReadSpotlight = func() ([]string, error) {
		t.Fatal("unexpected native read")
		return nil, errors.New("unexpected native read")
	}
	f.app.WriteSpotlight = func(string, bool) error {
		t.Fatal("unexpected native write")
		return errors.New("unexpected native write")
	}
	f.app.WriteBackup = func(string, bool) error {
		t.Fatal("unexpected native write")
		return errors.New("unexpected native write")
	}
	return f
}
func realTemp(t *testing.T) string {
	t.Helper()
	p, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if e := os.MkdirAll(p, 0755); e != nil {
		t.Fatal(e)
	}
}
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
}
func (f *cliFixture) run(args ...string) (int, string, string) {
	f.out.Reset()
	f.err.Reset()
	base := []string{"-s", f.list, "-x", f.xml, "--state", f.state, "--state-dir", filepath.Join(f.root, "bb-state")}
	code := f.app.RunCLI(append(base, args...))
	return code, f.out.String(), f.err.String()
}
func (f *cliFixture) fakePlans(plans map[string]Plan) {
	f.app.Planners = map[string]func(Options, Result) (Plan, error){}
	for name, p := range plans {
		p := p
		if p.Add == nil {
			p.Add = []string{}
		}
		if p.Remove == nil {
			p.Remove = []string{}
		}
		p.Detail = "x"
		f.app.Planners[name] = func(Options, Result) (Plan, error) { return p, nil }
	}
}
func requireContains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}
func TestCliListJSONIsTheBareResolverResult(t *testing.T) {
	f := newCLI(t)
	code, out, err := f.run("-l", "-j", "-m", "backblaze")
	if code != 0 {
		t.Fatal(code, err)
	}
	var result map[string]any
	if e := json.Unmarshal([]byte(out), &result); e != nil {
		t.Fatal(e)
	}
	if len(result) != 4 || result["paths"] == nil || result["missing"] == nil || result["unmatched"] == nil || result["skipped"] == nil {
		t.Fatal(result)
	}
}
func TestCliDefaultSkipsUnavailableBackblazeWithANote(t *testing.T) {
	f := newCLI(t)
	f.fakePlans(map[string]Plan{"spotlight": {InSync: true}, "timemachine": {InSync: true}})
	code, out, err := f.run()
	if code != 0 {
		t.Fatal(code, err)
	}
	for _, s := range []string{"backblaze: skipped (Backblaze not installed", "spotlight [dry run]", "timemachine [dry run]"} {
		requireContains(t, out, s)
	}
}
func TestCliExplicitUnavailableTargetIsAnError(t *testing.T) {
	f := newCLI(t)
	code, _, err := f.run("-t", "backblaze")
	if code != 2 {
		t.Fatal(code)
	}
	requireContains(t, err, "backblaze is unavailable")
}
func TestCliTargetIsRepeatableAndAllMeansEverything(t *testing.T) {
	for _, tc := range []struct{ args, want []string }{{[]string{"-t", "backblaze", "-t", "spotlight"}, []string{"spotlight", "backblaze"}}, {[]string{"-t", "all"}, targets}, {nil, targets}} {
		o, e := parseOptions(tc.args, map[string]any{}, "host")
		if e != nil {
			t.Fatal(e)
		}
		got, _ := selected(o)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, tc.want)
		}
	}
}
func TestCliCheckExitsOneOnDriftInAnyTarget(t *testing.T) {
	f := newCLI(t)
	f.fakePlans(map[string]Plan{"spotlight": {InSync: true}, "timemachine": {InSync: false}})
	if code, _, _ := f.run("-c", "-t", "spotlight", "-t", "timemachine"); code != 1 {
		t.Fatal(code)
	}
}
func TestCliApplyForAnotherHostIsRefused(t *testing.T) {
	f := newCLI(t)
	code, _, err := f.run("-a", "-H", "not-this-host", "-t", "spotlight")
	if code != 2 {
		t.Fatal(code)
	}
	requireContains(t, err, "hostname must match")
}
func TestCliDryRunConflictsWithApplyAndListWithModes(t *testing.T) {
	for _, args := range [][]string{{"-n", "-a"}, {"-l", "-a"}, {"status", "-a"}, {"-c", "-a"}, {"status", "-c"}, {"status", "-n"}, {"-l", "-c"}, {"-l", "status"}} {
		f := newCLI(t)
		if code, _, _ := f.run(args...); code != 2 {
			t.Fatal(args, code)
		}
	}
}
func TestCliOneFailingTargetDoesNotStopTheOthers(t *testing.T) {
	f := newCLI(t)
	f.fakePlans(map[string]Plan{"timemachine": {InSync: true}})
	f.app.Planners["spotlight"] = func(Options, Result) (Plan, error) { return Plan{}, errors.New("no backend") }
	code, out, err := f.run("-t", "spotlight", "-t", "timemachine")
	if code != 2 {
		t.Fatal(code)
	}
	requireContains(t, err, "spotlight: no backend")
	requireContains(t, out, "timemachine [dry run]")
}
func TestCliBackblazePreviewSaysDryRunAndWritesNothing(t *testing.T) {
	f := newCLI(t)
	original := "<bzexclusions>\n</bzexclusions>\n"
	mustWrite(t, f.xml, original)
	code, out, err := f.run("-t", "backblaze")
	if code != 0 {
		t.Fatal(code, err)
	}
	requireContains(t, out, "backblaze [dry run]")
	requireContains(t, out, "nothing is written to Backblaze without -a")
	data, e := os.ReadFile(f.xml)
	if e != nil || string(data) != original {
		t.Fatal(string(data), e)
	}
	if _, e = os.Stat(filepath.Join(f.root, "bb-state")); !os.IsNotExist(e) {
		t.Fatal("preview created state", e)
	}
}
func TestCliStatusShowsBackblazeStateAsJSON(t *testing.T) {
	f := newCLI(t)
	mustWrite(t, f.xml, "<bzexclusions>\n</bzexclusions>\n")
	code, out, err := f.run("status", "-j", "-t", "backblaze")
	if code != 0 {
		t.Fatal(code, err)
	}
	var got map[string]map[string]map[string]string
	if e := json.Unmarshal([]byte(out), &got); e != nil {
		t.Fatal(e)
	}
	if got["targets"]["backblaze"][f.folder] != "included" {
		t.Fatal(got)
	}
}
func TestXMLCliDryRunApplyAndCheck(t *testing.T) {
	f := newCLI(t)
	original := "<bzexclusions>\n</bzexclusions>\n"
	mustWrite(t, f.xml, original)
	mustWrite(t, f.list, "path "+f.folder+"\npath "+filepath.Join(f.root, "gone")+"\n")
	for _, step := range []struct {
		mode string
		want int
	}{{"", 0}, {"-c", 1}, {"-a", 0}, {"-c", 0}} {
		args := []string{"-t", "backblaze"}
		if step.mode != "" {
			args = append(args, step.mode)
		}
		code, _, err := f.run(args...)
		if code != step.want {
			t.Fatal(step, code, err)
		}
		if step.mode == "" {
			data, _ := os.ReadFile(f.xml)
			if string(data) != original {
				t.Fatal("preview wrote XML")
			}
		}
	}
	p := &XMLPolicy{Path: f.xml, StateDir: filepath.Join(f.root, "bb-state")}
	owned, e := p.Owned()
	if e != nil || len(owned) != 2 {
		t.Fatal(owned, e)
	}
	mustWrite(t, f.list, "")
	if code, _, err := f.run("-t", "backblaze", "-a"); code != 0 {
		t.Fatal(code, err)
	}
	owned, e = p.Owned()
	if e != nil || len(owned) != 0 {
		t.Fatal(owned, e)
	}
}

// Spotlight tests inject a stateful in-memory backend. The only writes are
// ownership files and change logs below this test's disposable directory.
type ownershipFixture struct {
	*cliFixture
	current map[string]bool
	calls   []string
	o       Options
}

func newOwnership(t *testing.T) *ownershipFixture {
	f := &ownershipFixture{cliFixture: newCLI(t), current: map[string]bool{}}
	f.o = Options{State: f.state, Apply: true, Hostname: f.app.Host}
	f.app.ReadSpotlight = func() ([]string, error) { return setKeys(f.current), nil }
	f.app.WriteSpotlight = func(p string, on bool) error {
		f.calls = append(f.calls, fmt.Sprintf("%s:%t", p, on))
		if on {
			f.current[p] = true
		} else {
			delete(f.current, p)
		}
		return nil
	}
	return f
}
func (f *ownershipFixture) save(paths ...string) {
	f.t.Helper()
	if err := writeJSON(f.state, map[string]any{"version": 1, "managed": paths}); err != nil {
		f.t.Fatal(err)
	}
}
func (f *ownershipFixture) managed() []string {
	f.t.Helper()
	data, e := os.ReadFile(f.state)
	if e != nil {
		f.t.Fatal(e)
	}
	var v struct{ Managed []string }
	if e = json.Unmarshal(data, &v); e != nil {
		f.t.Fatal(e)
	}
	return v.Managed
}
func (f *ownershipFixture) plan(paths, missing []string) (Plan, error) {
	return f.app.planSpotlight(f.o, Result{Paths: paths, Missing: missing})
}
func TestOwnershipPreservesUnrelatedAndPreexistingDesiredExclusions(t *testing.T) {
	f := newOwnership(t)
	existing, added, unrelated := filepath.Join(f.root, "existing"), filepath.Join(f.root, "added"), filepath.Join(f.root, "unrelated")
	f.current[existing] = true
	f.current[unrelated] = true
	if _, e := f.plan([]string{existing, added}, nil); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(setKeys(f.current), setKeys(stringSet([]string{existing, added, unrelated}))) || !reflect.DeepEqual(f.managed(), []string{added}) || !reflect.DeepEqual(f.calls, []string{added + ":true"}) {
		t.Fatal(f.current, f.managed(), f.calls)
	}
}
func TestOwnershipPreviewHasNoAPIOrOwnershipMutations(t *testing.T) {
	f := newOwnership(t)
	f.o.Apply = false
	p, e := f.plan([]string{f.folder}, nil)
	if e != nil || p.InSync || len(f.calls) != 0 {
		t.Fatal(p, e, f.calls)
	}
	if _, e = os.Stat(f.state); !os.IsNotExist(e) {
		t.Fatal("ownership exists", e)
	}
}
func TestOwnershipRemovesOnlyStaleOwnedExclusion(t *testing.T) {
	f := newOwnership(t)
	stale, other := filepath.Join(f.root, "stale"), filepath.Join(f.root, "unrelated")
	f.save(stale)
	f.current[stale] = true
	f.current[other] = true
	if _, e := f.plan([]string{f.folder}, nil); e != nil {
		t.Fatal(e)
	}
	if f.current[stale] || !f.current[other] || !f.current[f.folder] || !reflect.DeepEqual(f.managed(), []string{f.folder}) || len(f.calls) != 2 {
		t.Fatal(f.current, f.managed(), f.calls)
	}
}
func TestOwnershipOwnedFolderReplacedBySymlinkIsRemovedNotFatal(t *testing.T) {
	f := newOwnership(t)
	link := filepath.Join(f.root, "was-a-folder")
	if e := os.Symlink(f.folder, link); e != nil {
		t.Fatal(e)
	}
	f.save(link)
	f.current[link] = true
	if _, e := f.plan([]string{f.folder}, nil); e != nil {
		t.Fatal(e)
	}
	if f.current[link] || !f.current[f.folder] || len(f.calls) != 2 {
		t.Fatal(f.current, f.calls)
	}
}
func TestOwnershipMissingDeclaredPathRetainsOwnership(t *testing.T) {
	f := newOwnership(t)
	missing := filepath.Join(f.root, "missing")
	f.save(missing)
	f.current[missing] = true
	p, e := f.plan(nil, []string{missing})
	if e != nil || !p.InSync || len(f.calls) != 0 || !reflect.DeepEqual(f.managed(), []string{missing}) {
		t.Fatal(p, e, f.calls, f.managed())
	}
}
func TestOwnershipAbsentStalePathLosesOwnership(t *testing.T) {
	f := newOwnership(t)
	f.save(filepath.Join(f.root, "gone"))
	if _, e := f.plan(nil, nil); e != nil {
		t.Fatal(e)
	}
	if len(f.managed()) != 0 || len(f.calls) != 0 {
		t.Fatal(f.managed(), f.calls)
	}
}
func TestOwnershipInvalidStateFailsBeforeAnyAPIMutation(t *testing.T) {
	for _, state := range []string{"[]", "null", `{"version":99,"managed":[]}`, `{"version":1,"managed":[null]}`, `{"version":1,"managed":["/"]}`, `{"version":1,"managed":null}`} {
		f := newOwnership(t)
		mustMkdir(t, filepath.Dir(f.state))
		mustWrite(t, f.state, state)
		if _, e := f.plan([]string{f.folder}, nil); e == nil {
			t.Fatal("accepted", state)
		}
		if len(f.calls) != 0 {
			t.Fatal(f.calls)
		}
	}
}
func TestOwnershipWrongHostFailsBeforeMutation(t *testing.T) {
	f := newOwnership(t)
	f.o.Hostname = "a-different-host.invalid"
	if _, e := f.plan([]string{f.folder}, nil); e == nil {
		t.Fatal("accepted wrong host")
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
	if _, e := os.Stat(f.state); !os.IsNotExist(e) {
		t.Fatal(e)
	}
}
func TestOwnershipFailureBeforeAPINeverCreatesAnExclusion(t *testing.T) {
	f := newOwnership(t)
	f.app.WriteJSON = func(string, any) error { return errors.New("disk full") }
	if _, e := f.plan([]string{f.folder}, nil); e == nil {
		t.Fatal("did not fail")
	}
	if len(f.current) != 0 || len(f.calls) != 0 {
		t.Fatal(f.current, f.calls)
	}
}
func TestOwnershipAddThenReadbackFailureRetainsOwnershipForRecovery(t *testing.T) {
	f := newOwnership(t)
	write := f.app.WriteSpotlight
	f.app.WriteSpotlight = func(p string, b bool) error { _ = write(p, b); return errors.New("readback failed") }
	if _, e := f.plan([]string{f.folder}, nil); e == nil {
		t.Fatal("did not fail")
	}
	if !reflect.DeepEqual(f.managed(), []string{f.folder}) || !f.current[f.folder] {
		t.Fatal(f.managed(), f.current)
	}
	f.app.WriteSpotlight = write
	if _, e := f.plan(nil, nil); e != nil {
		t.Fatal(e)
	}
	if f.current[f.folder] || len(f.managed()) != 0 {
		t.Fatal(f.current, f.managed())
	}
}
func TestOwnershipRemoveThenFailureCanFinishOwnershipCleanup(t *testing.T) {
	f := newOwnership(t)
	f.save(f.folder)
	f.current[f.folder] = true
	write := f.app.WriteSpotlight
	f.app.WriteSpotlight = func(p string, b bool) error { _ = write(p, b); return errors.New("readback failed") }
	if _, e := f.plan(nil, nil); e == nil {
		t.Fatal("did not fail")
	}
	if !reflect.DeepEqual(f.managed(), []string{f.folder}) {
		t.Fatal(f.managed())
	}
	f.app.WriteSpotlight = write
	if _, e := f.plan(nil, nil); e != nil {
		t.Fatal(e)
	}
	if len(f.managed()) != 0 {
		t.Fatal(f.managed())
	}
}

func fakeTM(f *cliFixture, states map[string]bool) *[]string {
	writes := []string{}
	f.app.Run = func(name string, args []string, _ time.Duration) (string, string, error) {
		if name != "tmutil" || len(args) < 2 || args[0] != "isexcluded" || args[1] != "-X" {
			f.t.Fatalf("unexpected command %s %v", name, args)
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0"?><plist version="1.0"><array>`)
		for _, p := range args[2:] {
			fmt.Fprintf(&b, "<dict><key>Path</key><string>%s</string><key>IsExcluded</key><%t/></dict>", p, states[p])
		}
		b.WriteString("</array></plist>")
		return b.String(), "", nil
	}
	f.app.WriteBackup = func(p string, _ bool) error { writes = append(writes, p); return nil }
	return &writes
}
func tmFixture(t *testing.T) *cliFixture {
	f := newCLI(t)
	other := filepath.Join(f.root, "other")
	mustMkdir(t, other)
	mustWrite(t, f.list, "path "+f.folder+"\npath "+other+"\npath "+filepath.Join(f.root, "gone")+"\n")
	return f
}
func TestTimeMachineResolvesPresentAndMissing(t *testing.T) {
	f := tmFixture(t)
	r, e := Resolve(f.list, "any")
	if e != nil || len(r.Paths) != 2 || !reflect.DeepEqual(r.Missing, []string{filepath.Join(f.root, "gone")}) {
		t.Fatal(r, e)
	}
}
func TestTimeMachineDryRunWritesNothing(t *testing.T) {
	f := tmFixture(t)
	writes := fakeTM(f, nil)
	if code, _, err := f.run("-t", "timemachine", "-n"); code != 0 || len(*writes) != 0 {
		t.Fatal(code, err, *writes)
	}
}
func TestTimeMachineApplyOnlyWritesIncludedFoldersAndReadsBack(t *testing.T) {
	f := tmFixture(t)
	writes := fakeTM(f, map[string]bool{filepath.Join(f.root, "other"): true})
	code, _, err := f.run("-t", "timemachine", "-a")
	if code != 2 || !reflect.DeepEqual(*writes, []string{f.folder}) {
		t.Fatal(code, err, *writes)
	}
	requireContains(t, err, "still included after write")
}
func TestTimeMachineCheckReportsIncluded(t *testing.T) {
	f := tmFixture(t)
	states := map[string]bool{f.folder: true}
	fakeTM(f, states)
	if code, _, err := f.run("-t", "timemachine", "-c"); code != 1 {
		t.Fatal(code, err)
	}
	states[filepath.Join(f.root, "other")] = true
	if code, _, err := f.run("-t", "timemachine", "-c"); code != 0 {
		t.Fatal(code, err)
	}
}
func TestTimeMachineCheckAndApplyConflict(t *testing.T) {
	f := tmFixture(t)
	if code, _, _ := f.run("-c", "-a"); code != 2 {
		t.Fatal(code)
	}
}
func TestTimeMachineDefaultPreviewWritesNothingAndStatusReads(t *testing.T) {
	f := tmFixture(t)
	writes := fakeTM(f, map[string]bool{f.folder: true})
	for _, args := range [][]string{{"-t", "timemachine"}, {"-t", "timemachine", "status"}} {
		if code, _, err := f.run(args...); code != 0 || len(*writes) != 0 {
			t.Fatal(code, err, *writes)
		}
	}
}

func TestCliLastMechanismWinsAndTargetsAccumulate(t *testing.T) {
	for _, tc := range []struct{ args, want []string }{{[]string{"-m", "spotlight", "-m", "backblaze"}, []string{"backblaze"}}, {[]string{"-t", "timemachine", "-mspotlight", "-mbackblaze"}, []string{"timemachine", "backblaze"}}} {
		o, e := parseOptions(tc.args, map[string]any{}, "host")
		if e != nil {
			t.Fatal(e)
		}
		got, _ := selected(o)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, tc.want)
		}
	}
}
func TestCliBadConfigReturnsTwo(t *testing.T) {
	f := newCLI(t)
	config := filepath.Join(f.root, "bad.toml")
	mustWrite(t, config, "source = 3\n")
	t.Setenv("CHURNPATHS_CONFIG", config)
	if code, _, err := f.run("-l"); code != 2 {
		t.Fatal(code, err)
	}
}
func TestCliMissingOptionValueRejected(t *testing.T) {
	for _, args := range [][]string{{"-s", "-l"}, {"--source", "--json"}, {"-s"}, {"-t", "--list"}} {
		if _, err := parseOptions(args, map[string]any{}, "host"); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
