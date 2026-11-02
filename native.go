package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Runner is the only subprocess seam used by the native readers.
type Runner func(name string, args []string, timeout time.Duration) (stdout, stderr string, err error)

const batchSize = 50

// spotlightBridge owns only Copy/Create handles. Borrowed bytes are copied before release.
// The function seam permits lifetime and failure tests without loading any framework.
type spotlightBridge struct {
	copyList     func() uintptr
	createData   func(uintptr) uintptr
	dataBytes    func(uintptr) ([]byte, error)
	release      func(uintptr)
	createString func(string) uintptr
	set          func(uintptr, bool) int
}

func (b spotlightBridge) exclusions() ([]string, error) {
	array := b.copyList()
	if array == 0 {
		return nil, errors.New("Search Privacy returned no exclusion list")
	}
	defer b.release(array)
	data := b.createData(array)
	if data == 0 {
		return nil, errors.New("Search Privacy list is not a property list")
	}
	defer b.release(data)
	raw, err := b.dataBytes(data)
	if err != nil {
		return nil, err
	}
	value, err := parsePlist(raw)
	if err != nil {
		return nil, errors.New("Search Privacy returned malformed list data")
	}
	arrayValue, ok := value.([]any)
	if !ok {
		return nil, errors.New("Search Privacy returned unexpected exclusion types")
	}
	paths := make([]string, 0, len(arrayValue))
	for _, item := range arrayValue {
		path, ok := item.(string)
		if !ok {
			return nil, errors.New("Search Privacy returned unexpected exclusion types")
		}
		paths = append(paths, path)
	}
	return paths, nil
}
func (b spotlightBridge) setExcluded(path string, excluded bool) error {
	str := b.createString(path)
	if str == 0 {
		return errors.New("Could not encode the exclusion path")
	}
	defer b.release(str)
	status := b.set(str, excluded)
	if status != 0 {
		return fmt.Errorf("Search Privacy rejected %q (status %d)", path, status)
	}
	return nil
}

func readSpotlight() ([]string, error) {
	b, err := loadSpotlightBridge()
	if err != nil {
		return nil, err
	}
	return b.exclusions()
}
func checkedSpotlightWrite(path string, excluded bool, load func() (spotlightBridge, error)) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return errors.New("An absolute path without NUL bytes is required")
	}
	b, err := load()
	if err != nil {
		return err
	}
	if err = b.setExcluded(path, excluded); err != nil {
		return err
	}
	paths, err := b.exclusions()
	if err != nil {
		return err
	}
	found := false
	for _, p := range paths {
		if p == path {
			found = true
		}
	}
	if found != excluded {
		return fmt.Errorf("Search Privacy readback did not confirm %q", path)
	}
	return nil
}
func writeSpotlight(path string, excluded bool) error {
	return checkedSpotlightWrite(path, excluded, loadSpotlightBridge)
}
func setBackupExcluded(path string, excluded bool) error {
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return errors.New("An absolute filesystem path without NUL bytes is required")
	}
	return nativeBackupWrite(path, excluded)
}

// parsePlist accepts XML property lists, rejecting duplicate dictionary keys and trailing data.
func parsePlist(raw []byte) (any, error) {
	d := xml.NewDecoder(strings.NewReader(string(raw)))
	next := func() (xml.Token, error) {
		for {
			t, e := d.Token()
			if e != nil {
				return nil, e
			}
			switch x := t.(type) {
			case xml.CharData:
				if strings.TrimSpace(string(x)) != "" {
					return nil, errors.New("unexpected property-list text")
				}
			case xml.Comment, xml.ProcInst, xml.Directive:
				continue
			default:
				return t, nil
			}
		}
	}
	t, err := next()
	if err != nil {
		return nil, err
	}
	start, ok := t.(xml.StartElement)
	if !ok || start.Name.Local != "plist" || start.Name.Space != "" {
		return nil, errors.New("expected a property list")
	}
	t, err = next()
	if err != nil {
		return nil, err
	}
	start, ok = t.(xml.StartElement)
	if !ok {
		return nil, errors.New("missing property-list value")
	}
	leaf := func(s xml.StartElement) (string, error) {
		var out strings.Builder
		for {
			token, err := d.Token()
			if err != nil {
				return "", err
			}
			switch t := token.(type) {
			case xml.CharData:
				out.Write(t)
			case xml.Comment:
				continue
			case xml.EndElement:
				if t.Name != s.Name {
					return "", errors.New("invalid property-list scalar end")
				}
				return out.String(), nil
			default:
				return "", errors.New("nested element in property-list scalar")
			}
		}
	}
	var read func(xml.StartElement) (any, error)
	read = func(s xml.StartElement) (any, error) {
		if s.Name.Space != "" {
			return nil, errors.New("unexpected property-list namespace")
		}
		switch s.Name.Local {
		case "string", "integer", "real", "data", "date":
			value, err := leaf(s)
			if err != nil {
				return nil, err
			}
			switch s.Name.Local {
			case "integer":
				n, e := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
				return n, e
			case "real":
				n, e := strconv.ParseFloat(strings.TrimSpace(value), 64)
				return n, e
			case "string":
				return value, nil
			default:
				return struct{ Kind, Value string }{s.Name.Local, value}, nil
			}
		case "true", "false":
			value, err := leaf(s)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(value) != "" {
				return nil, errors.New("invalid boolean")
			}
			return s.Name.Local == "true", nil
		case "array":
			out := []any{}
			for {
				t, e := next()
				if e != nil {
					return nil, e
				}
				if end, ok := t.(xml.EndElement); ok {
					if end.Name != s.Name {
						return nil, errors.New("invalid array end")
					}
					return out, nil
				}
				child, ok := t.(xml.StartElement)
				if !ok {
					return nil, errors.New("invalid array")
				}
				v, e := read(child)
				if e != nil {
					return nil, e
				}
				out = append(out, v)
			}
		case "dict":
			out := map[string]any{}
			for {
				t, e := next()
				if e != nil {
					return nil, e
				}
				if end, ok := t.(xml.EndElement); ok {
					if end.Name != s.Name {
						return nil, errors.New("invalid dictionary end")
					}
					return out, nil
				}
				key, ok := t.(xml.StartElement)
				if !ok || key.Name.Local != "key" || key.Name.Space != "" {
					return nil, errors.New("missing dictionary key")
				}
				ks, e := leaf(key)
				if e != nil {
					return nil, e
				}
				if _, ok := out[ks]; ok {
					return nil, errors.New("duplicate dictionary key")
				}
				t, e = next()
				if e != nil {
					return nil, e
				}
				child, ok := t.(xml.StartElement)
				if !ok {
					return nil, errors.New("missing dictionary value")
				}
				v, e := read(child)
				if e != nil {
					return nil, e
				}
				out[ks] = v
			}
		default:
			return nil, fmt.Errorf("unexpected property-list element: %s", s.Name.Local)
		}
	}
	value, err := read(start)
	if err != nil {
		return nil, err
	}
	t, err = next()
	if err != nil {
		return nil, err
	}
	end, ok := t.(xml.EndElement)
	if !ok || end.Name.Local != "plist" {
		return nil, errors.New("invalid property-list end")
	}
	_, err = next()
	if err != io.EOF {
		return nil, errors.New("trailing property-list data")
	}
	return value, nil
}

func canonicalPath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// Resolve existing ancestors as Path.resolve(strict=False) does.
	suffix := []string{}
	base := path
	for {
		resolved, e := filepath.EvalSymlinks(base)
		if e == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(e) {
			return "", e
		}
		parent := filepath.Dir(base)
		if parent == base {
			return "", e
		}
		suffix = append(suffix, filepath.Base(base))
		base = parent
	}
}
func exclusionStates(paths []string, run Runner) (map[string]bool, error) {
	canonical := map[string]string{}
	unique := []string{}
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		if _, ok := canonical[p]; ok {
			continue
		}
		c, e := canonicalPath(p)
		if e != nil {
			return nil, e
		}
		canonical[p] = c
		if !seen[c] {
			seen[c] = true
			unique = append(unique, c)
		}
	}
	states := map[string]bool{}
	for offset := 0; offset < len(unique); offset += batchSize {
		end := offset + batchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[offset:end]
		out, stderr, e := run("tmutil", append([]string{"isexcluded", "-X"}, batch...), 120*time.Second)
		if e != nil {
			detail := strings.TrimSpace(stderr)
			if detail == "" {
				detail = strings.TrimSpace(out)
			}
			return nil, fmt.Errorf("cannot check exclusions: %s", detail)
		}
		value, e := parsePlist([]byte(out))
		if e != nil {
			return nil, fmt.Errorf("invalid tmutil exclusion output: %w", e)
		}
		rows, ok := value.([]any)
		if !ok {
			return nil, errors.New("invalid tmutil exclusion output: expected a property-list array")
		}
		wanted := map[string]bool{}
		for _, p := range batch {
			wanted[p] = true
		}
		found := map[string]bool{}
		for _, v := range rows {
			row, ok := v.(map[string]any)
			if !ok {
				return nil, errors.New("invalid tmutil exclusion output: missing per-item Path")
			}
			path, ok := row["Path"].(string)
			if !ok {
				return nil, errors.New("invalid tmutil exclusion output: missing per-item Path")
			}
			path = filepath.Clean(path)
			valid := true
			excluded := false
			switch flag := row["IsExcluded"].(type) {
			case bool:
				excluded = flag
			case int64:
				valid = flag == 0 || flag == 1
				excluded = flag == 1
			default:
				valid = false
			}
			if !filepath.IsAbs(path) || !valid {
				return nil, errors.New("invalid tmutil exclusion output: invalid per-item exclusion state")
			}
			if _, exists := found[path]; !wanted[path] || exists {
				return nil, fmt.Errorf("invalid tmutil exclusion output: unexpected or duplicate item: %s", path)
			}
			found[path] = excluded
		}
		if len(found) != len(batch) {
			return nil, errors.New("invalid tmutil exclusion output: partial exclusion output")
		}
		for p, v := range found {
			states[p] = v
		}
	}
	result := map[string]bool{}
	for p, c := range canonical {
		result[p] = states[c]
	}
	return result, nil
}
