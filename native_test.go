package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeSpotlight struct {
	bridge   spotlightBridge
	raw      []byte
	releases []uintptr
	calls    int
	reads    int
	status   int
}

func newFakeSpotlight(raw string) *fakeSpotlight {
	f := &fakeSpotlight{raw: []byte(raw)}
	f.bridge = spotlightBridge{copyList: func() uintptr { f.reads++; return 10 }, createData: func(uintptr) uintptr { return 20 }, dataBytes: func(uintptr) ([]byte, error) { return f.raw, nil }, release: func(p uintptr) { f.releases = append(f.releases, p) }, createString: func(string) uintptr { return 30 }, set: func(uintptr, bool) int { f.calls++; return f.status }}
	return f
}
func (f *fakeSpotlight) load() (spotlightBridge, error) { return f.bridge, nil }
func TestPublicAdapterReadsWithoutMutation(t *testing.T) {
	f := newFakeSpotlight(`<plist><array><string>/existing</string></array></plist>`)
	paths, e := f.bridge.exclusions()
	if e != nil || !reflect.DeepEqual(paths, []string{"/existing"}) || f.calls != 0 {
		t.Fatalf("%v %v calls=%d", paths, e, f.calls)
	}
}
func TestPublicAdapterAddAndRemoveConfirmReadback(t *testing.T) {
	for _, excluded := range []bool{true, false} {
		raw := `<plist><array/></plist>`
		if excluded {
			raw = `<plist><array><string>/test</string></array></plist>`
		}
		f := newFakeSpotlight(raw)
		if e := checkedSpotlightWrite("/test", excluded, f.load); e != nil || f.calls != 1 || f.reads != 1 {
			t.Fatalf("%v calls=%d reads=%d", e, f.calls, f.reads)
		}
	}
}
func TestPublicAdapterReadbackMismatchIsAnError(t *testing.T) {
	f := newFakeSpotlight(`<plist><array/></plist>`)
	if e := checkedSpotlightWrite("/test", true, f.load); e == nil || !strings.Contains(e.Error(), "readback") {
		t.Fatal(e)
	}
}
func TestPublicAdapterAPIFailureDoesNotClaimSuccess(t *testing.T) {
	f := newFakeSpotlight(`<plist><array/></plist>`)
	f.status = -1
	if e := checkedSpotlightWrite("/test", true, f.load); e == nil || f.reads != 0 {
		t.Fatalf("%v reads=%d", e, f.reads)
	}
}
func TestPublicAdapterBadPathsNeverLoadFramework(t *testing.T) {
	for _, p := range []string{"relative", "/nul\x00path", ""} {
		loaded := false
		e := checkedSpotlightWrite(p, true, func() (spotlightBridge, error) { loaded = true; return spotlightBridge{}, nil })
		if e == nil || loaded {
			t.Fatalf("%q: %v loaded=%v", p, e, loaded)
		}
	}
}
func TestPublicAdapterUnsupportedPlatformIsActionable(t *testing.T) {
	e := errors.New("Search Privacy is only available on macOS")
	if got := checkedSpotlightWrite("/test", true, func() (spotlightBridge, error) { return spotlightBridge{}, e }); got != e {
		t.Fatal(got)
	}
}
func TestPublicAdapterMissingPrivateSymbolIsActionable(t *testing.T) {
	e := errors.New("The macOS Search Privacy API is unavailable")
	if got := checkedSpotlightWrite("/test", true, func() (spotlightBridge, error) { return spotlightBridge{}, e }); got != e {
		t.Fatal(got)
	}
}
func TestCoreFoundationUnicodePathsAndOwnedObjectsRelease(t *testing.T) {
	f := newFakeSpotlight(`<plist><array><string>/café/test</string><string>/with spaces</string></array></plist>`)
	paths, e := f.bridge.exclusions()
	if e != nil || !reflect.DeepEqual(paths, []string{"/café/test", "/with spaces"}) {
		t.Fatalf("%v %v", paths, e)
	}
	if !reflect.DeepEqual(f.releases, []uintptr{20, 10}) {
		t.Fatal(f.releases)
	}
}
func TestCoreFoundationInvalidTypesStillReleaseOwnedObjects(t *testing.T) {
	f := newFakeSpotlight(`<plist><dict><key>wrong</key><string>type</string></dict></plist>`)
	if _, e := f.bridge.exclusions(); e == nil {
		t.Fatal("accepted dictionary")
	}
	if !reflect.DeepEqual(f.releases, []uintptr{20, 10}) {
		t.Fatal(f.releases)
	}
}
func TestCoreFoundationMalformedXMLStillReleasesOwnedObjects(t *testing.T) {
	f := newFakeSpotlight(`<?xml version='1.0'?><plist><array>`)
	if _, e := f.bridge.exclusions(); e == nil || !strings.Contains(e.Error(), "malformed") {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(f.releases, []uintptr{20, 10}) {
		t.Fatal(f.releases)
	}
}
func TestCoreFoundationFailedSerializationReleasesArrayOnly(t *testing.T) {
	f := newFakeSpotlight("")
	f.bridge.createData = func(uintptr) uintptr { return 0 }
	if _, e := f.bridge.exclusions(); e == nil {
		t.Fatal("accepted failed serialization")
	}
	if !reflect.DeepEqual(f.releases, []uintptr{10}) {
		t.Fatal(f.releases)
	}
}
func TestCoreFoundationRejectedWriteReleasesString(t *testing.T) {
	f := newFakeSpotlight("")
	f.status = -1
	if e := f.bridge.setExcluded("/test", true); e == nil || !strings.Contains(e.Error(), "status -1") {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(f.releases, []uintptr{30}) || f.calls != 1 {
		t.Fatalf("%v calls=%d", f.releases, f.calls)
	}
}
func TestTimeMachinePartialTmutilOutputIsAnError(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, p := range []string{a, b} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	a, _ = canonicalPath(a)
	run := func(string, []string, time.Duration) (string, string, error) {
		return fmt.Sprintf(`<plist><array><dict><key>Path</key><string>%s</string><key>IsExcluded</key><true/></dict></array></plist>`, a), "", nil
	}
	if _, e := exclusionStates([]string{a, b}, run); e == nil || !strings.Contains(e.Error(), "partial") {
		t.Fatal(e)
	}
}
func TestTimeMachineMalformedAndUnexpectedOutputFailsClosed(t *testing.T) {
	root := t.TempDir()
	path, _ := canonicalPath(root)
	row := func(p, flag string) string {
		return fmt.Sprintf(`<dict><key>Path</key><string>%s</string><key>IsExcluded</key>%s</dict>`, p, flag)
	}
	for _, raw := range []string{`<plist><array>`, `<plist><dict/></plist>`, `<plist><array><string>oops</string></array></plist>`, `<plist><array>` + row("relative", `<true/>`) + `</array></plist>`, `<plist><array>` + row(path, `<integer>2</integer>`) + `</array></plist>`, `<plist><array>` + row(path, `<string>1</string>`) + `</array></plist>`, `<plist><array>` + row(path, `<true/>`) + row(path, `<true/>`) + `</array></plist>`, `<plist><array>` + row(path+"/unexpected", `<true/>`) + `</array></plist>`} {
		t.Run(raw, func(t *testing.T) {
			run := func(string, []string, time.Duration) (string, string, error) { return raw, "", nil }
			if _, e := exclusionStates([]string{root}, run); e == nil {
				t.Fatal("accepted invalid output")
			}
		})
	}
}
func TestTimeMachineCanonicalDedupAndBatching(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	link := filepath.Join(root, "link")
	if e := os.Mkdir(real, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(real, link); e != nil {
		t.Fatal(e)
	}
	paths := []string{real, link, real}
	for i := 0; i < 101; i++ {
		paths = append(paths, filepath.Join(root, fmt.Sprint(i)))
	}
	calls := 0
	run := func(name string, args []string, timeout time.Duration) (string, string, error) {
		calls++
		if name != "tmutil" || !reflect.DeepEqual(args[:2], []string{"isexcluded", "-X"}) || len(args) > 52 || timeout != 120*time.Second {
			t.Fatalf("%s %v %v", name, args, timeout)
		}
		var out strings.Builder
		out.WriteString(`<plist><array>`)
		for _, p := range args[2:] {
			fmt.Fprintf(&out, `<dict><key>Path</key><string>%s</string><key>IsExcluded</key><integer>1</integer></dict>`, p)
		}
		out.WriteString(`</array></plist>`)
		return out.String(), "", nil
	}
	states, e := exclusionStates(paths, run)
	if e != nil || calls != 3 || len(states) != 103 || !states[link] || !states[real] {
		t.Fatalf("err=%v calls=%d states=%v", e, calls, states)
	}
}
func TestTimeMachineCommandFailureIsActionable(t *testing.T) {
	run := func(string, []string, time.Duration) (string, string, error) {
		return "", "permission denied", errors.New("exit 1")
	}
	if _, e := exclusionStates([]string{t.TempDir()}, run); e == nil || !strings.Contains(e.Error(), "permission denied") {
		t.Fatal(e)
	}
}
func TestTimeMachineEmptyDoesNotRun(t *testing.T) {
	run := func(string, []string, time.Duration) (string, string, error) {
		t.Fatal("unexpected command")
		return "", "", nil
	}
	states, e := exclusionStates(nil, run)
	if e != nil || len(states) != 0 {
		t.Fatalf("%v %v", states, e)
	}
}
func TestNativeBackupBadPathsNeverLoadFramework(t *testing.T) {
	for _, p := range []string{"relative", "/nul\x00path", ""} {
		if e := setBackupExcluded(p, true); e == nil {
			t.Fatal("accepted invalid path")
		}
	}
}

func TestPlistKeysOnlyAllowedInDictionaryKeyPositions(t *testing.T) {
	for _, raw := range []string{
		`<plist><key>/opt/cache</key></plist>`,
		`<plist><array><key>/opt/cache</key></array></plist>`,
		`<plist><dict><key>Path</key><key>/opt/cache</key></dict></plist>`,
		`<plist><dict><key>nested</key><array><key>/opt/cache</key></array></dict></plist>`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parsePlist([]byte(raw)); err == nil {
				t.Fatal("accepted key outside dictionary key position")
			}
		})
	}
	value, err := parsePlist([]byte(`<plist><dict><key>Path</key><string>/opt/cache</string><key>nested</key><dict><key>value</key><true/></dict></dict></plist>`))
	expected := map[string]any{"Path": "/opt/cache", "nested": map[string]any{"value": true}}
	if err != nil || !reflect.DeepEqual(value, expected) {
		t.Fatalf("valid dictionary rejected: %v %v", value, err)
	}
}

func TestCoreFoundationKeyOutsideDictionaryIsMalformed(t *testing.T) {
	f := newFakeSpotlight(`<plist><array><key>/opt/cache</key></array></plist>`)
	if _, err := f.bridge.exclusions(); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.releases, []uintptr{20, 10}) {
		t.Fatal(f.releases)
	}
}

func TestTimeMachineKeyUsedAsPathIsMalformed(t *testing.T) {
	path, err := canonicalPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(string, []string, time.Duration) (string, string, error) {
		return fmt.Sprintf(`<plist><array><dict><key>Path</key><key>%s</key><key>IsExcluded</key><true/></dict></array></plist>`, path), "", nil
	}
	if _, err := exclusionStates([]string{path}, run); err == nil {
		t.Fatal("accepted key used as Path value")
	}
}
