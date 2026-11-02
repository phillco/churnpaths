package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"
)

const defaultXML = "/Library/Backblaze.bzpkg/bzdata/bzexcluderules_editable.xml"
const xmlBegin = "<!-- churnpaths managed folders v1 BEGIN -->"
const xmlEnd = "<!-- churnpaths managed folders v1 END -->"

var xmlFields = []string{"plat", "osVers", "ruleIsOptional", "skipFirstCharThenStartsWith", "contains_1", "contains_2", "doesNotContain", "endsWith", "hasFileExtension"}

func xmlAttributes(directory string) (map[string]string, error) {
	home, _ := os.UserHomeDir()
	unsafe := !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || directory == "/Users" || directory == "/Volumes" || directory == home || strings.ContainsAny(directory, "*?[]\x00\r\n")
	for _, part := range strings.Split(directory, "/") {
		if part == ".." {
			unsafe = true
		}
	}
	if unsafe {
		return nil, fmt.Errorf("unsafe exact folder prefix: %s", directory)
	}
	prefix := directory[1:] + "/"
	if len([]rune(prefix)) < 4 {
		return nil, fmt.Errorf("folder prefix is too short: %s", directory)
	}
	return map[string]string{"plat": "mac", "osVers": "*", "ruleIsOptional": "t", "skipFirstCharThenStartsWith": prefix, "contains_1": "*", "contains_2": "*", "doesNotContain": "*", "endsWith": "*", "hasFileExtension": "*"}, nil
}

func xmlKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseXML validates the entire document while retaining its original bytes for rendering.
func parseXML(data []byte) ([]string, error) {
	if bytes.Contains(data, []byte("<!DOCTYPE")) || bytes.Contains(data, []byte("<!ENTITY")) {
		return nil, errors.New("document types/entities are not accepted")
	}
	if bytes.Count(data, []byte("</bzexclusions>")) != 1 {
		return nil, errors.New("expected one closing bzexclusions tag")
	}
	begin, end := bytes.Count(data, []byte(xmlBegin)), bytes.Count(data, []byte(xmlEnd))
	if !((begin == 0 && end == 0) || (begin == 1 && end == 1)) {
		return nil, errors.New("managed block markers are missing or duplicated")
	}
	d := xml.NewDecoder(bytes.NewReader(data))
	depth, roots := 0, 0
	inside, ended := false, false
	owned := map[string]bool{}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("editable exclusions XML is malformed")
		}
		switch node := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				roots++
				if roots != 1 || node.Name.Space != "" || node.Name.Local != "bzexclusions" || len(node.Attr) != 0 {
					return nil, errors.New("unexpected editable exclusions root")
				}
				continue
			}
			if depth != 2 || node.Name.Space != "" || node.Name.Local != "excludefname_rule" {
				return nil, errors.New("unexpected editable exclusion rule")
			}
			attrs := map[string]string{}
			for _, a := range node.Attr {
				if a.Name.Space != "" || attrs[a.Name.Local] != "" || a.Value == "" {
					return nil, errors.New("unexpected editable exclusion rule")
				}
				attrs[a.Name.Local] = a.Value
			}
			for _, key := range xmlFields {
				if attrs[key] == "" {
					return nil, errors.New("unexpected editable exclusion rule")
				}
			}
			if len(attrs) > len(xmlFields)+1 || (len(attrs) == len(xmlFields)+1 && attrs["bzmergeblock"] == "") {
				return nil, errors.New("unexpected editable exclusion rule")
			}
			if p := attrs["plat"]; p != "mac" && p != "win" && p != "*" {
				return nil, errors.New("unexpected editable exclusion rule")
			}
			if p := attrs["ruleIsOptional"]; p != "t" && p != "f" {
				return nil, errors.New("unexpected editable exclusion rule")
			}
			prefix := attrs["skipFirstCharThenStartsWith"]
			if prefix != "*" && len([]rune(prefix)) < 4 {
				return nil, errors.New("editable exclusion prefix is too short")
			}
			if inside {
				if !strings.HasSuffix(prefix, "/") {
					return nil, errors.New("managed folder prefix must end with /")
				}
				directory := "/" + strings.TrimSuffix(prefix, "/")
				wanted, err := xmlAttributes(directory)
				if err != nil {
					return nil, err
				}
				if !reflect.DeepEqual(attrs, wanted) || owned[directory] {
					return nil, errors.New("managed XML rule was changed or duplicated")
				}
				owned[directory] = true
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if len(bytes.TrimSpace(node)) > 0 {
				return nil, errors.New("unexpected text in editable exclusions")
			}
		case xml.Comment:
			if depth == 2 {
				return nil, errors.New("unexpected editable exclusion rule")
			}
			if depth != 1 {
				continue
			}
			switch strings.TrimSpace(string(node)) {
			case "churnpaths managed folders v1 BEGIN":
				if inside || ended || begin != 1 || end != 1 {
					return nil, errors.New("unexpected managed block start")
				}
				inside = true
			case "churnpaths managed folders v1 END":
				if !inside {
					return nil, errors.New("unexpected managed block end")
				}
				inside = false
				ended = true
			default:
				if inside {
					return nil, errors.New("unexpected comment inside managed block")
				}
			}
		case xml.Directive:
			return nil, errors.New("document types/entities are not accepted")
		}
	}
	if roots != 1 || depth != 0 {
		return nil, errors.New("editable exclusions XML is malformed")
	}
	if inside || (begin == 1 && !ended) {
		return nil, errors.New("managed block must be directly inside bzexclusions")
	}
	return xmlKeys(owned), nil
}

func xmlEscapeAttribute(v string) string {
	v = strings.ReplaceAll(v, "&", "&amp;")
	v = strings.ReplaceAll(v, "<", "&lt;")
	v = strings.ReplaceAll(v, ">", "&gt;")
	v = strings.ReplaceAll(v, "\"", "&quot;")
	v = strings.ReplaceAll(v, "\t", "&#09;")
	return v
}

func renderXML(data []byte, directories []string) ([]byte, error) {
	if _, err := parseXML(data); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, p := range directories {
		set[p] = true
	}
	var block strings.Builder
	block.WriteString(xmlBegin)
	for _, directory := range xmlKeys(set) {
		attrs, err := xmlAttributes(directory)
		if err != nil {
			return nil, err
		}
		block.WriteString("\n    <excludefname_rule")
		for _, key := range xmlFields {
			fmt.Fprintf(&block, " %s=\"%s\"", key, xmlEscapeAttribute(attrs[key]))
		}
		block.WriteString(" />")
	}
	block.WriteString("\n" + xmlEnd)
	start := bytes.Index(data, []byte(xmlBegin))
	var output []byte
	if start >= 0 {
		stop := bytes.Index(data, []byte(xmlEnd)) + len(xmlEnd)
		output = append(output, data[:start]...)
		output = append(output, block.String()...)
		output = append(output, data[stop:]...)
	} else {
		point := bytes.Index(data, []byte("</bzexclusions>"))
		output = append(output, data[:point]...)
		output = append(output, block.String()...)
		output = append(output, '\n')
		output = append(output, data[point:]...)
	}
	actual, err := parseXML(output)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(actual, xmlKeys(set)) {
		return nil, errors.New("generated managed rules did not verify")
	}
	return output, nil
}

type xmlSignature struct {
	Dev, Ino    uint64
	Size, Mtime int64
	UID, GID    uint32
	Mode        os.FileMode
}

func xmlSig(info os.FileInfo) xmlSignature {
	st := info.Sys().(*syscall.Stat_t)
	return xmlSignature{uint64(st.Dev), uint64(st.Ino), info.Size(), info.ModTime().UnixNano(), st.Uid, st.Gid, info.Mode()}
}
func xmlPerm(mode os.FileMode) os.FileMode {
	return mode & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
}

// XMLPolicy mutates only its marked block. Hooks permit tests to inject races and fake chown.
type XMLPolicy struct {
	Path, StateDir string
	AfterSnapshot  func()
	BeforeReplace  func()
	Run            func([]string) error
}

func (p *XMLPolicy) paths() (string, string) {
	path, state := p.Path, p.StateDir
	if path == "" {
		path = defaultXML
	}
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local/state/churnpaths")
	}
	return path, state
}
func (p *XMLPolicy) snapshot() ([]byte, os.FileInfo, error) {
	path, _ := p.paths()
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, nil, errors.New("editable exclusions must be an existing regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || xmlSig(info) != xmlSig(after) {
		return nil, nil, errors.New("editable exclusions changed while reading; retry")
	}
	if _, err = parseXML(data); err != nil {
		return nil, nil, err
	}
	return data, info, nil
}
func (p *XMLPolicy) Owned() ([]string, error) {
	path, _ := p.paths()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return []string{}, nil
	}
	data, _, err := p.snapshot()
	if err != nil {
		return nil, err
	}
	return parseXML(data)
}
func (p *XMLPolicy) log(event map[string]any) error {
	_, state := p.paths()
	if err := os.MkdirAll(state, 0755); err != nil {
		return err
	}
	event["time"] = float64(time.Now().UnixNano()) / 1e9
	f, err := os.OpenFile(filepath.Join(state, "xml-changes.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(event); err != nil {
		return err
	}
	return f.Sync()
}
func xmlWriteSync(f *os.File, data []byte) error {
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
func (p *XMLPolicy) unchanged(original []byte, info os.FileInfo) bool {
	path, _ := p.paths()
	now, err := os.Lstat(path)
	if err != nil || !now.Mode().IsRegular() || xmlSig(now) != xmlSig(info) {
		return false
	}
	data, err := os.ReadFile(path)
	return err == nil && bytes.Equal(data, original)
}
func (p *XMLPolicy) SetMany(directories []string, excluded bool) error {
	dirs := map[string]bool{}
	for _, directory := range directories {
		if _, err := xmlAttributes(directory); err != nil {
			return err
		}
		dirs[directory] = true
	}
	if len(dirs) == 0 {
		return nil
	}
	path, state := p.paths()
	original, info, err := p.snapshot()
	if err != nil {
		return err
	}
	if p.AfterSnapshot != nil {
		p.AfterSnapshot()
	}
	old, err := parseXML(original)
	if err != nil {
		return err
	}
	updated := map[string]bool{}
	for _, s := range old {
		updated[s] = true
	}
	for s := range dirs {
		if excluded {
			updated[s] = true
		} else {
			delete(updated, s)
		}
	}
	wanted := xmlKeys(updated)
	if reflect.DeepEqual(old, wanted) {
		return nil
	}
	output, err := renderXML(original, wanted)
	if err != nil {
		return err
	}
	backups := filepath.Join(state, "xml-backups")
	if err = os.MkdirAll(backups, 0700); err != nil {
		return err
	}
	backup, err := os.CreateTemp(backups, "editable-*.xml")
	if err != nil {
		return err
	}
	err = xmlWriteSync(backup, original)
	closeErr := backup.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".churnpaths-")
	if err != nil {
		return err
	}
	defer os.Remove(staged.Name())
	err = xmlWriteSync(staged, output)
	if err == nil {
		err = staged.Chmod(xmlPerm(info.Mode()))
	}
	closeErr = staged.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	stageInfo, err := os.Stat(staged.Name())
	if err != nil {
		return err
	}
	beforeSig, stageSig := xmlSig(info), xmlSig(stageInfo)
	if beforeSig.UID != stageSig.UID || beforeSig.GID != stageSig.GID {
		command := []string{"/usr/bin/sudo", "-n", "/usr/sbin/chown", fmt.Sprintf("%d:%d", beforeSig.UID, beforeSig.GID), staged.Name()}
		if err = p.log(map[string]any{"action": "chown staged editable exclusions", "command": command}); err != nil {
			return err
		}
		run := p.Run
		if run == nil {
			run = func(cmd []string) error {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				return exec.CommandContext(ctx, cmd[0], cmd[1:]...).Run()
			}
		}
		if err = run(command); err != nil {
			return errors.New("could not preserve editable exclusions owner/group")
		}
	}
	stageInfo, err = os.Stat(staged.Name())
	if err != nil {
		return err
	}
	stageSig = xmlSig(stageInfo)
	if beforeSig.UID != stageSig.UID || beforeSig.GID != stageSig.GID || xmlPerm(beforeSig.Mode) != xmlPerm(stageSig.Mode) {
		return errors.New("staged exclusions permissions do not match original")
	}
	if !p.unchanged(original, info) {
		return errors.New("editable exclusions changed before replacement; retry")
	}
	beforeHash, afterHash := sha256.Sum256(original), sha256.Sum256(output)
	event := map[string]any{"path": path, "directories": xmlKeys(dirs), "excluded": excluded, "backup": backup.Name(), "before_sha256": fmt.Sprintf("%x", beforeHash), "after_sha256": fmt.Sprintf("%x", afterHash)}
	if len(dirs) == 1 {
		event["directory"] = xmlKeys(dirs)[0]
	}
	event["action"] = "replace intent"
	if err = p.log(event); err != nil {
		return err
	}
	if p.BeforeReplace != nil {
		p.BeforeReplace()
	}
	if !p.unchanged(original, info) {
		return errors.New("editable exclusions changed before replacement; retry")
	}
	if err = os.Rename(staged.Name(), path); err != nil {
		return err
	}
	actual, actualInfo, err := p.snapshot()
	if err != nil {
		return err
	}
	actualOwned, err := parseXML(actual)
	if err != nil {
		return err
	}
	actualSig := xmlSig(actualInfo)
	if !bytes.Equal(actual, output) || !reflect.DeepEqual(actualOwned, wanted) || actualSig.UID != beforeSig.UID || actualSig.GID != beforeSig.GID || xmlPerm(actualSig.Mode) != xmlPerm(beforeSig.Mode) {
		return errors.New("editable exclusions readback did not match replacement")
	}
	event["action"] = "replace verified"
	return p.log(event)
}
