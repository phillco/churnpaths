package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type manifestFixture struct {
	t            *testing.T
	root, source string
}

func newManifestFixture(t *testing.T) *manifestFixture {
	t.Helper()
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return &manifestFixture{t, root, filepath.Join(root, "manifest.txt")}
}
func (f *manifestFixture) folder(relative string) string {
	f.t.Helper()
	p := filepath.Join(f.root, relative)
	if e := os.MkdirAll(p, 0700); e != nil {
		f.t.Fatal(e)
	}
	return p
}
func (f *manifestFixture) file(relative, content string) string {
	f.t.Helper()
	p := filepath.Join(f.root, relative)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		f.t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(content), 0600); e != nil {
		f.t.Fatal(e)
	}
	return p
}
func (f *manifestFixture) resolve(text string) Result {
	f.t.Helper()
	f.file("manifest.txt", text)
	r, e := Resolve(f.source, "alpha")
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *manifestFixture) reject(text string) {
	f.t.Helper()
	f.file("manifest.txt", text)
	if _, e := Resolve(f.source, "alpha"); e == nil {
		f.t.Fatalf("expected rejection: %q", text)
	}
}
func (f *manifestFixture) link(relative, target string) string {
	f.t.Helper()
	p := filepath.Join(f.root, relative)
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		f.t.Fatal(e)
	}
	if e := os.Symlink(target, p); e != nil {
		f.t.Fatal(e)
	}
	return p
}
func wantPaths(t *testing.T, actual []string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	sort.Strings(want)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("got %q, want %q", actual, want)
	}
}

func TestManifestHostScopesCasefoldWildcardAndSpaces(t *testing.T) {
	f := newManifestFixture(t)
	shared := f.folder("shared space")
	alpha := f.folder("alpha")
	other := f.folder("other")
	wild := f.folder("wildcard")
	r := f.resolve("# comment\npath " + shared + "\n@ALPHA path " + alpha + "\n@laptop path " + other + "\n@* path " + wild + "\n")
	wantPaths(t, r.Paths, shared, alpha, wild)
}
func TestManifestMissingExactAndUnmatchedGlobRemainDistinct(t *testing.T) {
	f := newManifestFixture(t)
	missing := filepath.Join(f.root, "missing")
	pattern := filepath.Join(f.root, "job-*")
	r := f.resolve("path " + missing + "\nglob " + pattern + "\n")
	wantPaths(t, r.Paths)
	wantPaths(t, r.Missing, missing)
	wantPaths(t, r.Unmatched, pattern)
}
func TestManifestGlobOnlyFindsImmediateChildren(t *testing.T) {
	f := newManifestFixture(t)
	direct := f.folder("job-a")
	f.folder("parent/job-b")
	f.file("job-not-a-folder.json", "{}")
	wantPaths(t, f.resolve("glob "+f.root+"/job-*\n").Paths, direct)
}
func TestManifestRecursiveOrParentComponentGlobsRejected(t *testing.T) {
	for _, pattern := range []string{"**/cache", "job-*/cache", "**", "?/cache", "[ab]/cache"} {
		t.Run(pattern, func(t *testing.T) { f := newManifestFixture(t); f.reject("glob " + f.root + "/" + pattern + "\n") })
	}
}
func TestManifestBroadRelativeAndParentPathsRejected(t *testing.T) {
	f := newManifestFixture(t)
	for _, raw := range []string{"/", "/Users", "/Volumes", "/Volumes/Disk1", "/System/Volumes/Data", homeDir(), "relative", f.root + "/../cache"} {
		f.reject("path " + raw + "\n")
	}
}
func TestManifestGlobHitCannotBypassHomeFolderGuard(t *testing.T) {
	f := newManifestFixture(t)
	home := f.folder("homes/someone")
	t.Setenv("HOME", home)
	f.reject("glob " + filepath.Dir(home) + "/*\n")
}
func TestManifestExactFileIsNotAFolder(t *testing.T) {
	f := newManifestFixture(t)
	file := f.file("file", "data")
	f.reject("path " + file + "\n")
}
func TestManifestNamedVirtualenvRequiresEnvironmentEvidence(t *testing.T) {
	f := newManifestFixture(t)
	cfg := f.folder("a/.venv")
	f.file("a/.venv/pyvenv.cfg", "")
	activation := f.folder("b/venv/bin")
	f.file("b/venv/bin/activate", "")
	f.folder("c/venv")
	f.folder("d/.venv")
	r := f.resolve("name .venv " + f.root + "\nname venv " + f.root + "\n")
	wantPaths(t, r.Paths, cfg, filepath.Dir(activation))
}
func TestManifestNamedWalkDoesNotFollowChildSymlinksOrGit(t *testing.T) {
	f := newManifestFixture(t)
	outside := f.folder("outside/node_modules")
	scan := f.folder("scan")
	f.link("scan/link", filepath.Dir(outside))
	f.folder("scan/.git/node_modules")
	f.folder("scan/.jj/node_modules")
	wantPaths(t, f.resolve("name node_modules "+scan+"\n").Paths)
}
func TestManifestNameRootSymlinkIsNotWalked(t *testing.T) {
	f := newManifestFixture(t)
	outside := f.folder("outside/node_modules")
	link := f.link("linked-root", filepath.Dir(outside))
	f.reject("name node_modules " + link + "\n")
}
func TestManifestExactSymlinkAndSymlinkAncestorAreRejected(t *testing.T) {
	f := newManifestFixture(t)
	outside := f.folder("outside/cache")
	link := f.link("link", filepath.Dir(outside))
	for _, rule := range []string{"path " + link, "path " + link + "/cache", "glob " + link + "/ca*"} {
		f.reject(rule + "\n")
	}
}
func TestManifestSymlinkedGlobHitIsSkippedNotFatal(t *testing.T) {
	f := newManifestFixture(t)
	real := f.folder("src/job-real")
	target := f.folder("elsewhere/job-target")
	link := f.link("src/job-alias", target)
	r := f.resolve("glob " + f.root + "/src/job-*\n")
	wantPaths(t, r.Paths, real)
	wantPaths(t, r.Skipped, link)
	wantPaths(t, r.Unmatched)
}
func TestManifestGlobMatchingOnlySymlinksIsNotUnmatched(t *testing.T) {
	f := newManifestFixture(t)
	target := f.folder("elsewhere/job-target")
	f.link("src/job-alias", target)
	r := f.resolve("glob " + f.root + "/src/job-*\n")
	wantPaths(t, r.Paths)
	wantPaths(t, r.Unmatched)
	if len(r.Skipped) != 1 {
		t.Fatal(r)
	}
}
func TestManifestNameDiscoveryStopsAfterFourLevels(t *testing.T) {
	f := newManifestFixture(t)
	included := f.folder("a/b/c/node_modules")
	f.folder("a/b/c/d/node_modules")
	wantPaths(t, f.resolve("name node_modules "+f.root+"\n").Paths, included)
}

// A folder listed by its parent can be removed (e.g. an agent worktree) before
// it is examined or descended into; the walk skips it instead of failing.
func TestManifestNameWalkSkipsVanishedFolders(t *testing.T) {
	f := newManifestFixture(t)
	kept := f.folder("stays/node_modules")
	f.folder("worktree-gone/deeper")
	f.folder("listed-then-gone")
	old := discoveryReadDir
	defer func() { discoveryReadDir = old }()
	discoveryReadDir = func(dir string) ([]os.DirEntry, error) {
		entries, e := old(dir)
		if dir == f.root {
			// Both vanish after the root's listing: one before its Info(), one before descent.
			os.RemoveAll(filepath.Join(f.root, "worktree-gone"))
			os.RemoveAll(filepath.Join(f.root, "listed-then-gone"))
		}
		return entries, e
	}
	wantPaths(t, f.resolve("name node_modules "+f.root+"\n").Paths, kept)
}

// Losing the configured root mid-walk must fail, not publish a partial result.
func TestManifestNameWalkFailsWhenRootVanishes(t *testing.T) {
	f := newManifestFixture(t)
	scan := f.folder("scan")
	f.folder("scan/stays/node_modules")
	f.folder("scan/gone")
	old := discoveryReadDir
	defer func() { discoveryReadDir = old }()
	discoveryReadDir = func(dir string) ([]os.DirEntry, error) {
		entries, e := old(dir)
		if dir == scan {
			os.Rename(scan, filepath.Join(f.root, "scan-moved"))
		}
		return entries, e
	}
	f.file("manifest.txt", "name node_modules "+scan+"\n")
	if _, e := Resolve(f.source, "alpha"); !errors.Is(e, fs.ErrNotExist) {
		t.Fatalf("want root-vanished error, got %v", e)
	}
}
func TestManifestNameUnreadableRootFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permissions")
	}
	f := newManifestFixture(t)
	outer := f.folder("outer")
	scan := f.folder("outer/scan")
	if e := os.Chmod(outer, 0); e != nil {
		t.Fatal(e)
	}
	defer os.Chmod(outer, 0700)
	f.file("manifest.txt", "name node_modules "+scan+"\n")
	if _, e := Resolve(f.source, "alpha"); !errors.Is(e, fs.ErrPermission) {
		t.Fatalf("want permission error for unreadable root, got %v", e)
	}
}
func TestManifestNameWalkStillFailsOnOtherErrors(t *testing.T) {
	f := newManifestFixture(t)
	f.folder("stays/node_modules")
	locked := f.folder("locked")
	old := discoveryReadDir
	defer func() { discoveryReadDir = old }()
	discoveryReadDir = func(dir string) ([]os.DirEntry, error) {
		if dir == locked {
			return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrPermission}
		}
		return old(dir)
	}
	f.file("manifest.txt", "name node_modules "+f.root+"\n")
	if _, e := Resolve(f.source, "alpha"); !errors.Is(e, fs.ErrPermission) {
		t.Fatalf("want permission error, got %v", e)
	}
}
func TestManifestMatchingNamePrunesNestedMatches(t *testing.T) {
	f := newManifestFixture(t)
	parent := f.folder("project/node_modules")
	f.folder("project/node_modules/nested/node_modules")
	wantPaths(t, f.resolve("name node_modules "+f.root+"\n").Paths, parent)
}
func TestManifestRustTargetRequiresGeneratedBuildEvidence(t *testing.T) {
	f := newManifestFixture(t)
	generated := f.folder("rust/target/debug/deps")
	f.folder("source/target")
	wantPaths(t, f.resolve("name target "+f.root+"\n").Paths, filepath.Dir(filepath.Dir(generated)))
}
func TestManifestExactParentCoversNestedGlobAndNamedHits(t *testing.T) {
	f := newManifestFixture(t)
	parent := f.folder("job")
	f.folder("job/node_modules")
	wantPaths(t, f.resolve("path "+parent+"\nglob "+parent+"/node_*\nname node_modules "+f.root+"\n").Paths, parent)
}
func TestIncludeAddsExactFoldersAndSkipsBadEntries(t *testing.T) {
	f := newManifestFixture(t)
	cache := f.folder("work/cache")
	link := f.link("link", cache)
	listing := f.file("generated.txt", "# generated\npath "+cache+"\npath "+f.root+"/gone\npath "+link+"\nglob "+f.root+"/*\npath relative\n")
	r := f.resolve("include " + listing + "\ninclude " + f.root + "/absent.txt\n")
	wantPaths(t, r.Paths, cache)
	wantPaths(t, r.Missing)
	wantPaths(t, r.Skipped, link, f.root+"/*", "relative")
}
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CHURNPATHS_CONFIG", "CHURNPATHS_SOURCE", "CHURNPATHS_STATE"} {
		t.Setenv(key, "")
	}
}
func TestConfigDefaultSourceWithoutConfig(t *testing.T) {
	f := newManifestFixture(t)
	clearConfigEnv(t)
	t.Setenv("CHURNPATHS_CONFIG", f.root+"/none.toml")
	config, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	if got, want := defaultSource(config), filepath.Join(homeDir(), ".config/churnpaths/churn-paths.txt"); got != want {
		t.Fatalf("%q != %q", got, want)
	}
}
func TestConfigFileSetsSourceAndState(t *testing.T) {
	f := newManifestFixture(t)
	clearConfigEnv(t)
	configPath := f.file("config.toml", "source = \"~/lists/mine.txt\"\nstate = \"/tmp/x/state.json\"\n")
	t.Setenv("CHURNPATHS_CONFIG", configPath)
	config, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	if defaultSource(config) != filepath.Join(homeDir(), "lists/mine.txt") || defaultState(config) != "/tmp/x/state.json" {
		t.Fatal(config)
	}
}
func TestConfigEnvironmentBeatsConfigAndFlagBeatsEnvironment(t *testing.T) {
	f := newManifestFixture(t)
	clearConfigEnv(t)
	p := f.file("config.toml", "source = \"/from/config\"\n")
	t.Setenv("CHURNPATHS_CONFIG", p)
	t.Setenv("CHURNPATHS_SOURCE", "/from/env")
	config, e := loadConfig()
	if e != nil {
		t.Fatal(e)
	}
	if defaultSource(config) != "/from/env" {
		t.Fatal(defaultSource(config))
	} // CLI explicit-source precedence is covered by TestParityConfig.
}
func TestConfigBadConfigIsAnError(t *testing.T) {
	f := newManifestFixture(t)
	clearConfigEnv(t)
	for _, content := range []string{"source = 3\n", "state = false\n", "source = [\n"} {
		p := f.file("config.toml", content)
		t.Setenv("CHURNPATHS_CONFIG", p)
		if _, e := loadConfig(); e == nil {
			t.Fatal("accepted bad config", content)
		}
	}
}
func TestConfigUnknownDirectiveAndBadIncludeLine(t *testing.T) {
	f := newManifestFixture(t)
	f.reject("bogus /x\n")
}
func TestManifestDiscoveryBudgetsFailClosed(t *testing.T) {
	f := newManifestFixture(t)
	f.folder("scan/a")
	limit := discoveryMaxDirectories
	defer func() { discoveryMaxDirectories = limit }()
	discoveryMaxDirectories = 1
	f.reject("name node_modules " + f.root + "\n")
}
func TestManifestDiscoveryDeadlineFailsClosed(t *testing.T) {
	f := newManifestFixture(t)
	old := discoveryNow
	defer func() { discoveryNow = old }()
	calls := 0
	discoveryNow = func() time.Time { calls++; return time.Unix(int64(calls*60), 0) }
	f.reject("name node_modules " + f.root + "\n")
}
func TestManifestPythonGlobSemantics(t *testing.T) {
	f := newManifestFixture(t)
	a := f.folder("job-a")
	b := f.folder("job-b")
	f.folder(".job-hidden")
	f.folder("job-[")
	for _, test := range []struct {
		pattern string
		want    []string
	}{{"job-[!b[]", []string{a}}, {"job-[ab]", []string{a, b}}, {"job-[", []string{f.root + "/job-["}}, {"job-?", []string{a, b, f.root + "/job-["}}} {
		r := f.resolve("glob " + f.root + "/" + test.pattern + "\n")
		wantPaths(t, r.Paths, test.want...)
	}
}
func TestManifestMalformedLinesFailClosed(t *testing.T) {
	f := newManifestFixture(t)
	for _, text := range []string{"path\n", "@alpha\n", "name node_modules\n", "name ../bad " + f.root + "\n"} {
		f.reject(text)
	}
}
func TestManifestUnicodePathsAndRepeatedUnmatched(t *testing.T) {
	f := newManifestFixture(t)
	p := f.folder("café with spaces")
	r := f.resolve("path " + p + "\nglob " + f.root + "/absent*\nglob " + f.root + "/absent*\n")
	wantPaths(t, r.Paths, p)
	if len(r.Unmatched) != 2 || !strings.Contains(r.Unmatched[0], "absent*") {
		t.Fatal(r)
	}
}

func TestManifestFullUnicodeHostCasefold(t *testing.T) {
	f := newManifestFixture(t)
	folder := f.folder("matched")
	for _, pair := range [][2]string{{"Straße", "STRASSE"}, {"ΟΣ", "ος"}, {"ﬃ", "FFI"}, {"İ", "i̇"}, {"ᾈ", "ἀι"}} {
		f.file("manifest.txt", "@"+pair[0]+" path "+folder+"\n")
		result, err := Resolve(f.source, pair[1])
		if err != nil {
			t.Fatal(err)
		}
		wantPaths(t, result.Paths, folder)
	}
}
func TestManifestInvalidGlobRangesMatchPython(t *testing.T) {
	cases := []struct {
		pattern string
		yes, no []string
	}{
		{"[z-a]", nil, []string{"a", "z", "-"}},
		{"[!z-a]", []string{"a", "z", "-"}, nil},
		{"[-az]", []string{"-", "a", "z"}, []string{"b"}},
		{"[a-z-]", []string{"a", "b", "z", "-"}, []string{"_"}},
		{"[[]", []string{"["}, []string{"]"}},
		{"[]]", []string{"]"}, []string{"["}},
		{"[!]]", []string{"[", "a"}, []string{"]"}},
	}
	for _, test := range cases {
		t.Run(test.pattern, func(t *testing.T) {
			r, e := globRegex(test.pattern)
			if e != nil {
				t.Fatal(e)
			}
			for _, v := range test.yes {
				if !r.MatchString(v) {
					t.Fatalf("%q should match %q", test.pattern, v)
				}
			}
			for _, v := range test.no {
				if r.MatchString(v) {
					t.Fatalf("%q should not match %q", test.pattern, v)
				}
			}
		})
	}
}
func TestManifestUniversalLineSeparators(t *testing.T) {
	f := newManifestFixture(t)
	a := f.folder("a")
	b := f.folder("b")
	for _, sep := range []string{"\r", "\r\n", "\v", "\f", "\x1c", "\x1d", "\x1e", "\u0085", "\u2028", "\u2029"} {
		r := f.resolve("path " + a + sep + "path " + b + sep)
		wantPaths(t, r.Paths, a, b)
	}
}
func TestManifestNameRootFileFailsClosed(t *testing.T) {
	f := newManifestFixture(t)
	p := f.file("file", "text")
	f.reject("name node_modules " + p + "\n")
}

func TestManifestInvalidUTF8FailsClosed(t *testing.T) {
	f := newManifestFixture(t)
	f.file("manifest.txt", "path "+f.root+"/cache\xff\n")
	if _, err := Resolve(f.source, "alpha"); err == nil {
		t.Fatal("accepted non-UTF8 list")
	}
	listing := f.file("generated.txt", "path "+f.root+"/cache\xff\n")
	f.file("manifest.txt", "include "+listing+"\n")
	if _, err := Resolve(f.source, "alpha"); err == nil {
		t.Fatal("accepted non-UTF8 include")
	}
}
