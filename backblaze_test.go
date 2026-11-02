package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const xmlUserRule = `<excludefname_rule plat="mac" osVers="*" ruleIsOptional="t" skipFirstCharThenStartsWith="users/" contains_1="/itunes/" contains_2="*" doesNotContain="*" endsWith="*" hasFileExtension="*" bzmergeblock="custom-v1" />`

var xmlOriginal = []byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<bzexclusions>\n<!-- Keep me. -->\n  " + xmlUserRule + "\n</bzexclusions>\n")

func xmlFixture(t *testing.T) *XMLPolicy {
	t.Helper()
	root := t.TempDir()
	p := &XMLPolicy{Path: filepath.Join(root, "editable.xml"), StateDir: filepath.Join(root, "state")}
	if err := os.WriteFile(p.Path, xmlOriginal, 0640); err != nil {
		t.Fatal(err)
	}
	return p
}
func xmlRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func xmlOwned(t *testing.T, p *XMLPolicy) []string {
	t.Helper()
	s, err := p.Owned()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestXMLAddPreservesUnownedBytesAndBacksUp(t *testing.T) {
	p := xmlFixture(t)
	if err := p.SetMany([]string{"/opt/cache one", "/opt/a&b"}, true); err != nil {
		t.Fatal(err)
	}
	changed := xmlRead(t, p.Path)
	start := bytes.Index(changed, []byte(xmlBegin))
	end := bytes.Index(changed, []byte(xmlEnd)) + len(xmlEnd)
	restored := append(append([]byte{}, changed[:start]...), changed[end+1:]...)
	if !bytes.Equal(restored, xmlOriginal) {
		t.Fatalf("unowned bytes changed: %s", restored)
	}
	if got := xmlOwned(t, p); !reflect.DeepEqual(got, []string{"/opt/a&b", "/opt/cache one"}) {
		t.Fatal(got)
	}
	info, err := os.Stat(p.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	backups, err := os.ReadDir(filepath.Join(p.StateDir, "xml-backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	if !bytes.Equal(xmlRead(t, filepath.Join(p.StateDir, "xml-backups", backups[0].Name())), xmlOriginal) {
		t.Fatal("backup differs")
	}
	info, err = os.Stat(filepath.Join(p.StateDir, "xml-backups", backups[0].Name()))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup not private")
	}
	log := xmlRead(t, filepath.Join(p.StateDir, "xml-changes.ndjson"))
	if bytes.Count(log, []byte("\n")) != 2 || !bytes.Contains(log, []byte("replace verified")) {
		t.Fatal(string(log))
	}
}
func TestXMLRemoveRestoresOriginalBytesAroundBlock(t *testing.T) {
	p := xmlFixture(t)
	if err := p.SetMany([]string{"/opt/cache"}, true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMany([]string{"/opt/cache"}, false); err != nil {
		t.Fatal(err)
	}
	if len(xmlOwned(t, p)) != 0 {
		t.Fatal("still owned")
	}
	if !bytes.Contains(xmlRead(t, p.Path), []byte(xmlUserRule)) {
		t.Fatal("unowned rule missing")
	}
}
func TestXMLPrefixEndsWithSlashSoSiblingsDoNotMatch(t *testing.T) {
	attrs, err := xmlAttributes("/opt/cache")
	if err != nil {
		t.Fatal(err)
	}
	if attrs["skipFirstCharThenStartsWith"] != "opt/cache/" {
		t.Fatal(attrs)
	}
}
func TestXMLUnsafePrefixesRejected(t *testing.T) {
	for _, bad := range []string{"/", "/Users", "relative", "/opt/../x", "/opt/a*b", "/ab", "/opt/cache/", "/opt/a\x00b", "/opt/a\nb", "/opt/a?b", "/opt/a[b"} {
		t.Run(bad, func(t *testing.T) {
			if _, err := xmlAttributes(bad); err == nil {
				t.Fatal("accepted unsafe prefix")
			}
		})
	}
}
func TestXMLTamperedBlockIsRefused(t *testing.T) {
	p := xmlFixture(t)
	if err := p.SetMany([]string{"/opt/cache"}, true); err != nil {
		t.Fatal(err)
	}
	data := xmlRead(t, p.Path)
	start := bytes.Index(data, []byte(xmlBegin))
	tail := bytes.Replace(data[start:], []byte(`endsWith="*"`), []byte(`endsWith="x"`), 1)
	data = append(data[:start:start], tail...)
	if err := os.WriteFile(p.Path, data, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Owned(); err == nil {
		t.Fatal("tampered managed block accepted")
	}
}
func TestXMLDTDSymlinkAndMissingFileRefused(t *testing.T) {
	p := xmlFixture(t)
	if err := os.WriteFile(p.Path, []byte("<!DOCTYPE x [<!ENTITY a 'b'>]><bzexclusions/>"), 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Owned(); err == nil {
		t.Fatal("DTD accepted")
	}
	link := filepath.Join(filepath.Dir(p.Path), "link.xml")
	if err := os.Symlink(p.Path, link); err != nil {
		t.Fatal(err)
	}
	q := &XMLPolicy{Path: link, StateDir: p.StateDir}
	if _, err := q.Owned(); err == nil {
		t.Fatal("symlink accepted")
	}
	q.Path = filepath.Join(filepath.Dir(p.Path), "missing.xml")
	if got, err := q.Owned(); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
}
func TestXMLConcurrentChangeIsNotOverwritten(t *testing.T) {
	for _, phase := range []string{"snapshot", "logged"} {
		t.Run(phase, func(t *testing.T) {
			p := xmlFixture(t)
			raced := append(append([]byte{}, xmlOriginal...), '\n')
			hook := func() {
				if err := os.WriteFile(p.Path, raced, 0640); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "snapshot" {
				p.AfterSnapshot = hook
			} else {
				p.BeforeReplace = hook
			}
			if err := p.SetMany([]string{"/opt/cache"}, true); err == nil || !strings.Contains(err.Error(), "changed before replacement") {
				t.Fatal(err)
			}
			if !bytes.Equal(xmlRead(t, p.Path), raced) {
				t.Fatal("concurrent edit lost")
			}
			staged, _ := filepath.Glob(filepath.Join(filepath.Dir(p.Path), ".churnpaths-*"))
			if len(staged) != 0 {
				t.Fatal("staged files left", staged)
			}
		})
	}
}
func TestXMLParserRejectsMalformedAndUnexpectedDocuments(t *testing.T) {
	valid, err := renderXML(xmlOriginal, []string{"/opt/cache"})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"malformed":           []byte("<bzexclusions>"),
		"wrong_root":          []byte("<wrong></bzexclusions>"),
		"root_attributes":     []byte("<bzexclusions a='b'></bzexclusions>"),
		"nested_marker":       []byte("<bzexclusions><excludefname_rule>" + xmlBegin + xmlEnd + "</excludefname_rule></bzexclusions>"),
		"missing_marker":      bytes.Replace(valid, []byte(xmlEnd), nil, 1),
		"duplicate_marker":    bytes.Replace(valid, []byte(xmlBegin), []byte(xmlBegin+xmlBegin), 1),
		"inside_comment":      bytes.Replace(valid, []byte(xmlBegin), []byte(xmlBegin+"<!-- unknown -->"), 1),
		"unexpected_rule":     []byte("<bzexclusions><anything /></bzexclusions>"),
		"text":                []byte("<bzexclusions>bad</bzexclusions>"),
		"trailing_text":       append(append([]byte{}, xmlOriginal...), []byte("bad")...),
		"extra_root":          append(append([]byte{}, xmlOriginal...), []byte("<another/>")...),
		"duplicate_attribute": bytes.Replace(valid, []byte(`plat="mac"`), []byte(`plat="mac" plat="mac"`), 1),
		"extra_attribute":     bytes.Replace(valid, []byte(`plat="mac"`), []byte(`plat="mac" mystery="x"`), 1),
		"bad_prefix":          bytes.Replace(valid, []byte(`opt/cache/`), []byte(`opt/cache`), 1),
		"duplicate_rule":      bytes.Replace(valid, []byte(xmlEnd), append(bytes.Split(bytes.Split(valid, []byte(xmlBegin))[1], []byte(xmlEnd))[0], []byte(xmlEnd)...), 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseXML(data); err == nil {
				t.Fatalf("accepted %s", data)
			}
		})
	}
}
func TestXMLRenderMatchesPythonEscapingAndIsIdempotent(t *testing.T) {
	dirs := []string{"/opt/café", "/opt/a&b", "/opt/a\"b", "/opt/a\tb"}
	data, err := renderXML(xmlOriginal, dirs)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"opt/café/", "opt/a&amp;b/", "opt/a&quot;b/", "opt/a&#09;b/"} {
		if !bytes.Contains(data, []byte(s)) {
			t.Fatal(s)
		}
	}
	again, err := renderXML(data, dirs)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("render not idempotent", err)
	}
}
func TestXMLNoopDoesNotWriteOrCreateState(t *testing.T) {
	p := xmlFixture(t)
	if err := p.SetMany(nil, true); err != nil {
		t.Fatal(err)
	}
	if err := p.SetMany([]string{"/opt/absent"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.StateDir); !os.IsNotExist(err) {
		t.Fatal("state written")
	}
	if !bytes.Equal(xmlRead(t, p.Path), xmlOriginal) {
		t.Fatal("no-op changed bytes")
	}
}

func TestXMLUnsupportedEncodingsFailClosed(t *testing.T) {
	// The standard XML decoder deliberately has no extra charset dependency.
	// Even an ASCII-only document declaring another encoding must fail closed.
	for _, encoding := range []string{"ISO-8859-1", "UTF-16"} {
		t.Run(encoding, func(t *testing.T) {
			p := xmlFixture(t)
			data := bytes.Replace(xmlOriginal, []byte(`encoding="UTF-8"`), []byte(`encoding="`+encoding+`"`), 1)
			if err := os.WriteFile(p.Path, data, 0640); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Owned(); err == nil {
				t.Fatal("accepted unsupported encoding")
			}
			if err := p.SetMany([]string{"/opt/cache"}, true); err == nil {
				t.Fatal("applied unsupported encoding")
			}
			if !bytes.Equal(xmlRead(t, p.Path), data) {
				t.Fatal("unsupported document changed")
			}
			if _, err := os.Stat(p.StateDir); !os.IsNotExist(err) {
				t.Fatal("state written for unsupported document")
			}
		})
	}
}
