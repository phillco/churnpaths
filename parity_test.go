package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

var parityBinary string

func TestMain(m *testing.M) {
	// Build the actual delivered executable once. Parity only invokes resolve or
	// preview/status/check of an XML file in a disposable directory, never apply.
	dir, err := os.MkdirTemp("", "churnpaths-parity-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	parityBinary = filepath.Join(dir, "churnpaths")
	cmd := exec.Command("go", "build", "-o", parityBinary, ".")
	if out, e := cmd.CombinedOutput(); e != nil {
		fmt.Fprintf(os.Stderr, "parity build: %s: %s\n", e, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type parityResult struct {
	code     int
	out, err string
}

func parityCommand(t *testing.T, cmd *exec.Cmd) parityResult {
	t.Helper()
	var out, err bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &err
	e := cmd.Run()
	code := 0
	if e != nil {
		if ee, ok := e.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatal(e)
		}
	}
	return parityResult{code, out.String(), err.String()}
}
func pythonParity(t *testing.T, args []string, forceMac bool, fakeNative ...bool) parityResult {
	t.Helper()
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Fatal("parity tests require Python 3.11+:", e)
	}
	// The harness forbids native reads/writes; only fixture XML may be read.
	script := `import importlib.machinery, importlib.util, sys
sys.path.insert(0, "python")
loader = importlib.machinery.SourceFileLoader("reference", "python/churnpaths")
spec = importlib.util.spec_from_loader(loader.name, loader)
m = importlib.util.module_from_spec(spec)
loader.exec_module(m)
def forbidden(*a, **kw): raise AssertionError("unexpected native operation in parity")
m.tm_exclusion_api.exclusion_states = forbidden
m.tm_exclusion_api.set_excluded = forbidden
import churnpaths_spotlight as s
s.exclusions = forbidden
s.set_excluded = forbidden
`
	if len(fakeNative) > 0 && fakeNative[0] {
		script += "m.tm_exclusion_api.exclusion_states = lambda paths: {p: False for p in paths}\ns.exclusions = lambda: []\nm.shutil.which = lambda name: '/fake/tmutil'\n"
	}
	if forceMac {
		script += "m.sys.platform = 'darwin'\n"
	}
	script += "raise SystemExit(m.main(sys.argv[1:]))\n"
	return parityCommand(t, exec.Command(python, append([]string{"-c", script}, args...)...))
}
func goParity(t *testing.T, f *cliFixture, args []string, forceMac bool) parityResult {
	t.Helper()
	if forceMac {
		f.out.Reset()
		f.err.Reset()
		code := f.app.RunCLI(args)
		return parityResult{code, f.out.String(), f.err.String()}
	}
	return parityCommand(t, exec.Command(parityBinary, args...))
}
func compareParity(t *testing.T, py, goResult parityResult, jsonOutput bool) {
	t.Helper()
	if py.code != goResult.code {
		t.Fatalf("exit mismatch Python=%+v Go=%+v", py, goResult)
	}
	if jsonOutput {
		var p, g any
		if e := json.Unmarshal([]byte(py.out), &p); e != nil {
			t.Fatal(py, e)
		}
		if e := json.Unmarshal([]byte(goResult.out), &g); e != nil {
			t.Fatal(goResult, e)
		}
		if !reflect.DeepEqual(p, g) {
			t.Fatalf("JSON mismatch\nPython: %s\nGo: %s", py.out, goResult.out)
		}
	} else if py.out != goResult.out {
		t.Fatalf("stdout mismatch\nPython: %q\nGo: %q", py.out, goResult.out)
	}
	if py.err != goResult.err {
		t.Fatalf("stderr mismatch\nPython: %q\nGo: %q", py.err, goResult.err)
	}
}
func parityFixture(t *testing.T) *cliFixture {
	f := newCLI(t)
	for _, p := range []string{"jobs/job-a", "jobs/job-b", "projects/a/node_modules", "projects/b/.venv/bin", "projects/c/venv", "projects/r/target/debug/deps", "projects/deep/a/b/node_modules", "projects/deep/a/b/c/node_modules", "projects/.git/node_modules", "projects/.jj/node_modules", "hidden/.cache", "outside/node_modules"} {
		mustMkdir(t, filepath.Join(f.root, p))
	}
	mustWrite(t, filepath.Join(f.root, "projects/b/.venv/bin/activate"), "")
	for link, target := range map[string]string{"jobs/job-alias": "scratch", "projects/link": "outside", "jobs/job-broken": "does-not-exist"} {
		if e := os.Symlink(filepath.Join(f.root, target), filepath.Join(f.root, link)); e != nil {
			t.Fatal(e)
		}
	}
	mustWrite(t, filepath.Join(f.root, "jobs/job-file"), "not a directory")
	include := filepath.Join(f.root, "generated.txt")
	mustWrite(t, include, "# generated\npath "+f.folder+"\npath relative\npath "+filepath.Join(f.root, "gone-include")+"\nglob "+filepath.Join(f.root, "jobs/*")+"\npath "+filepath.Join(f.root, "jobs/job-alias")+"\n")
	list := fmt.Sprintf("# fixture\n@ALPHA path %s\n@other path %s\n@* glob %s/jobs/job-*\npath %s/gone\nglob %s/absent-*\ninclude %s\nname node_modules %s/projects\nname .venv %s/projects\nname venv %s/projects\nname target %s/projects\n", f.folder, filepath.Join(f.root, "foreign"), f.root, f.root, f.root, include, f.root, f.root, f.root, f.root)
	mustWrite(t, f.list, list)
	mustWrite(t, f.xml, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<bzexclusions>\n<!-- user comment -->\n</bzexclusions>\n")
	return f
}
func TestParityResolveFixtures(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, host := range []string{"alpha", "other", "unknown"} {
			t.Run(fmt.Sprintf("%s/json=%t", host, jsonOutput), func(t *testing.T) {
				f := parityFixture(t)
				args := []string{"-s", f.list, "-H", host, "-l"}
				if jsonOutput {
					args = append(args, "-j")
				}
				compareParity(t, pythonParity(t, args, false), goParity(t, f, args, false), jsonOutput)
			})
		}
	}
}
func TestParityPreviewFixtures(t *testing.T) {
	for _, mode := range []string{"", "-n", "-c", "status"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", mode, jsonOutput), func(t *testing.T) {
				f := parityFixture(t)
				args := []string{"-s", f.list, "-H", "alpha", "-t", "backblaze", "-x", f.xml, "--state-dir", filepath.Join(f.root, "bb-state")}
				if mode != "" {
					args = append(args, mode)
				}
				if jsonOutput {
					args = append(args, "-j")
				}
				original, _ := os.ReadFile(f.xml)
				// On macOS compare the actual executable as well; elsewhere exercise the
				// same dispatcher with a fake availability platform for fixture XML only.
				forceMac := runtime.GOOS != "darwin"
				compareParity(t, pythonParity(t, args, forceMac), goParity(t, f, args, forceMac), jsonOutput)
				after, _ := os.ReadFile(f.xml)
				if !bytes.Equal(original, after) {
					t.Fatal("parity mutated XML")
				}
				if _, e := os.Stat(filepath.Join(f.root, "bb-state")); !os.IsNotExist(e) {
					t.Fatal("parity created backup state", e)
				}
			})
		}
	}
}
func TestParityConfig(t *testing.T) {
	f := parityFixture(t)
	config := filepath.Join(f.root, "config.toml")
	mustWrite(t, config, "source = "+fmt.Sprintf("%q", f.list)+"\nstate = "+fmt.Sprintf("%q", f.state)+"\n")
	t.Setenv("CHURNPATHS_CONFIG", config)
	for _, tc := range []struct {
		name, env string
		args      []string
	}{{"config", "", nil}, {"environment", f.list, nil}, {"flag", "/absent/from-env", []string{"-s", f.list}}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHURNPATHS_SOURCE", tc.env)
			args := append([]string{"-l", "-j", "-H", "alpha"}, tc.args...)
			compareParity(t, pythonParity(t, args, false), goParity(t, f, args, false), true)
		})
	}
}
func TestParityGlobCharacters(t *testing.T) {
	f := newCLI(t)
	for _, name := range []string{"a", "b", "c", "-", "[", "x]", ".hidden", "café", "with space", `a\b`} {
		mustMkdir(t, filepath.Join(f.root, "matches", name))
	}
	for _, pattern := range []string{"*", "?", "[ab]", "[!a]", "[z-a]", "[!z-a]", "[[]", "[", "[]a]", "[a-]", "[-a]", "[!]]", `a\b`, ".*"} {
		t.Run(pattern, func(t *testing.T) {
			mustWrite(t, f.list, "glob "+filepath.Join(f.root, "matches", pattern)+"\n")
			args := []string{"-s", f.list, "-l", "-j"}
			compareParity(t, pythonParity(t, args, false), goParity(t, f, args, false), true)
		})
	}
}
func TestParityOwnedXMLPreview(t *testing.T) {
	f := parityFixture(t)
	stale := filepath.Join(f.root, "stale")
	data, _ := os.ReadFile(f.xml)
	owned, e := renderXML(data, []string{stale, f.folder})
	if e != nil {
		t.Fatal(e)
	}
	mustWrite(t, f.xml, string(owned))
	for _, mode := range []string{"-n", "-c", "status"} {
		args := []string{"-s", f.list, "-H", "alpha", "-t", "backblaze", "-x", f.xml, mode}
		forceMac := runtime.GOOS != "darwin"
		compareParity(t, pythonParity(t, args, forceMac), goParity(t, f, args, forceMac), false)
	}
	after, _ := os.ReadFile(f.xml)
	if !bytes.Equal(owned, after) {
		t.Fatal("preview mutated owned XML")
	}
}
func TestParityLongAndAttachedFlags(t *testing.T) {
	f := parityFixture(t)
	for _, args := range [][]string{{"--source=" + f.list, "--hostname=alpha", "--list", "--json"}, {"-s" + f.list, "-Halpha", "-lj"},
		{"-ljtbackblaze", "-s" + f.list},
		{"-l", "-j", "-s=" + f.list, "-m=backblaze"},
		{"-ljs" + f.list},
		{"--sou", f.list, "--li", "--js"}, {"--source", f.list, "--list", "--json", "-mbackblaze"}} {
		compareParity(t, pythonParity(t, args, false), goParity(t, f, args, false), true)
	}
}
func TestParityUTF8Output(t *testing.T) {
	f := newCLI(t)
	name := filepath.Join(f.root, "café & <cache>")
	mustMkdir(t, name)
	mustWrite(t, f.list, "path "+name+"\n")
	args := []string{"-s", f.list, "-l", "-j"}
	compareParity(t, pythonParity(t, args, false), goParity(t, f, args, false), true)
	args = args[:len(args)-1]
	py, goResult := pythonParity(t, args, false), goParity(t, f, args, false)
	compareParity(t, py, goResult, false)
	if !strings.Contains(goResult.out, "café") {
		t.Fatal(goResult)
	}
}

func TestParityAllTargetsWithFakeNativeReaders(t *testing.T) {
	for _, mode := range []string{"", "-n", "-c", "status"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/json=%t", mode, jsonOutput), func(t *testing.T) {
				f := parityFixture(t)
				fakeTM(f, nil)
				f.app.ReadSpotlight = func() ([]string, error) { return []string{}, nil }
				args := []string{"-s", f.list, "-H", "alpha", "-x", f.xml, "--state", f.state, "--state-dir", filepath.Join(f.root, "bb-state")}
				if mode != "" {
					args = append(args, mode)
				}
				if jsonOutput {
					args = append(args, "-j")
				}
				original, _ := os.ReadFile(f.xml)
				compareParity(t, pythonParity(t, args, true, true), goParity(t, f, args, true), jsonOutput)
				after, _ := os.ReadFile(f.xml)
				if !bytes.Equal(original, after) {
					t.Fatal("read-only parity wrote XML")
				}
				if _, e := os.Stat(f.state); !os.IsNotExist(e) {
					t.Fatal("read-only parity wrote ownership", e)
				}
			})
		}
	}
}
