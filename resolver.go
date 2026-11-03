package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

type Result struct {
	Paths     []string `json:"paths"`
	Missing   []string `json:"missing"`
	Unmatched []string `json:"unmatched"`
	Skipped   []string `json:"skipped"`
}

func homeDir() string { h, _ := os.UserHomeDir(); return h }
func expandUser(raw string) string {
	if raw == "~" {
		return homeDir()
	}
	if strings.HasPrefix(raw, "~/") {
		return filepath.Join(homeDir(), raw[2:])
	}
	if strings.HasPrefix(raw, "~") {
		name, rest, _ := strings.Cut(raw[1:], "/")
		if u, err := user.Lookup(name); err == nil {
			return filepath.Join(u.HomeDir, rest)
		}
	}
	return raw
}
func loadConfig() (map[string]any, error) {
	path := os.Getenv("CHURNPATHS_CONFIG")
	if path == "" {
		path = filepath.Join(homeDir(), ".config/churnpaths/config.toml")
	}
	config := map[string]any{}
	if !isFile(path) {
		return config, nil
	}
	if _, err := toml.DecodeFile(path, &config); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, key := range []string{"source", "state"} {
		if value, ok := config[key]; ok {
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("%s: %s must be a string", path, key)
			}
		}
	}
	return config, nil
}
func defaultSource(config map[string]any) string {
	raw := os.Getenv("CHURNPATHS_SOURCE")
	if raw == "" {
		raw, _ = config["source"].(string)
	}
	if raw != "" {
		return expandUser(raw)
	}
	return filepath.Join(homeDir(), ".config/churnpaths/churn-paths.txt")
}
func defaultState(config map[string]any) string {
	raw := os.Getenv("CHURNPATHS_STATE")
	if raw == "" {
		raw, _ = config["state"].(string)
	}
	if raw != "" {
		return expandUser(raw)
	}
	return filepath.Join(homeDir(), ".local/state/churnpaths/spotlight.json")
}
func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
func isDir(path string) bool { info, err := os.Stat(path); return err == nil && info.IsDir() }
func isLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }
func deviceOf(info os.FileInfo) uint64 {
	if s, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(s.Dev)
	}
	return 0
}
func isMount(path string) bool {
	a, err := os.Stat(path)
	if err != nil {
		return false
	}
	b, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return false
	}
	return deviceOf(a) != deviceOf(b) || os.SameFile(a, b)
}
func normalized(raw string, allowSymlink bool) (string, error) {
	path := expandUser(raw)
	if !filepath.IsAbs(path) || hasParentPart(path) {
		return "", fmt.Errorf("expected an absolute path or ~/path: %s", raw)
	}
	path = filepath.Clean(path)
	for _, broad := range []string{"/", "/Users", "/Volumes", homeDir(), "/System/Volumes", "/System/Volumes/Data"} {
		if path == broad {
			return "", fmt.Errorf("refusing broad folder exclusion: %s", raw)
		}
	}
	if parent := filepath.Dir(path); parent == "/Users" || parent == "/Volumes" {
		return "", fmt.Errorf("whole homes/volumes belong in mechanism-specific policy: %s", raw)
	}
	if !allowSymlink {
		for p := path; ; p = filepath.Dir(p) {
			if isLink(p) {
				return "", fmt.Errorf("refusing symlink folder or ancestor: %s", raw)
			}
			if p == filepath.Dir(p) {
				break
			}
		}
	}
	if isMount(path) {
		return "", fmt.Errorf("mounted roots belong in mechanism-specific policy: %s", raw)
	}
	return path, nil
}
func hasParentPart(path string) bool {
	for _, part := range strings.Split(path, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
func splitPair(line string) (string, string, error) {
	index := strings.IndexFunc(line, unicode.IsSpace)
	if index < 0 {
		return "", "", fmt.Errorf("not enough values to unpack (expected 2, got 1)")
	}
	return line[:index], strings.TrimLeftFunc(line[index:], unicode.IsSpace), nil
}
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func splitLines(data []byte) []string {
	text := strings.NewReplacer("\r\n", "\n", "\r", "\n", "\v", "\n", "\f", "\n", "\x1c", "\n", "\x1d", "\n", "\x1e", "\n", "\u0085", "\n", "\u2028", "\n", "\u2029", "\n").Replace(string(data))
	return strings.Split(text, "\n")
}
func lineParts(data []byte) []string { return splitLines(data) }

// Python glob treats backslashes literally, excludes leading dots unless the
// pattern starts with a dot, and accepts unmatched '[' as a literal.
func globRegex(pattern string) (*regexp.Regexp, error) {
	chars := []rune(pattern)
	var out strings.Builder
	out.WriteString("(?s)^")
	for i := 0; i < len(chars); i++ {
		c := chars[i]
		switch c {
		case '*':
			out.WriteString(".*")
		case '?':
			out.WriteString(".")
		case '[':
			j := i + 1
			if j < len(chars) && chars[j] == '!' {
				j++
			}
			if j < len(chars) && chars[j] == ']' {
				j++
			}
			for j < len(chars) && chars[j] != ']' {
				j++
			}
			if j == len(chars) {
				out.WriteString(`\[`)
				continue
			}
			stuff := chars[i+1 : j]
			i = j
			out.WriteString(globClass(stuff))
		default:
			out.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	out.WriteByte('$')
	return regexp.Compile(out.String())
}

// Match fnmatch's treatment of invalid ranges and literal set punctuation.
func globClass(stuff []rune) string {
	if strings.ContainsRune(string(stuff), '-') {
		var chunks [][]rune
		start, k := 0, 1
		if stuff[0] == '!' {
			k = 2
		}
		for k < len(stuff) {
			next := -1
			for n := k; n < len(stuff); n++ {
				if stuff[n] == '-' {
					next = n
					break
				}
			}
			if next < 0 {
				break
			}
			chunks = append(chunks, append([]rune{}, stuff[start:next]...))
			start = next + 1
			k = next + 3
		}
		if start < len(stuff) {
			chunks = append(chunks, append([]rune{}, stuff[start:]...))
		} else if len(chunks) > 0 {
			chunks[len(chunks)-1] = append(chunks[len(chunks)-1], '-')
		}
		for k := len(chunks) - 1; k > 0; k-- {
			left, right := chunks[k-1], chunks[k]
			if len(left) > 0 && len(right) > 0 && left[len(left)-1] > right[0] {
				chunks[k-1] = append(left[:len(left)-1], right[1:]...)
				chunks = append(chunks[:k], chunks[k+1:]...)
			}
		}
		var escaped strings.Builder
		for k, chunk := range chunks {
			if k > 0 {
				escaped.WriteByte('-')
			}
			for _, r := range chunk {
				if strings.ContainsRune(`\-[]^`, r) {
					escaped.WriteByte('\\')
				}
				escaped.WriteRune(r)
			}
		}
		value := escaped.String()
		if value == "" {
			return `[^\s\S]`
		}
		if value == "!" {
			return "."
		}
		if strings.HasPrefix(value, "!") {
			value = "^" + value[1:]
		}
		return "[" + value + "]"
	}
	if len(stuff) == 0 {
		return `[^\s\S]`
	}
	var out strings.Builder
	out.WriteByte('[')
	if stuff[0] == '!' {
		out.WriteByte('^')
		stuff = stuff[1:]
	}
	for _, r := range stuff {
		if strings.ContainsRune(`\[]^`, r) {
			out.WriteByte('\\')
		}
		out.WriteRune(r)
	}
	out.WriteByte(']')
	return out.String()
}

func globFolders(path string) []string {
	parent, pattern := filepath.Dir(path), filepath.Base(path)
	matcher, err := globRegex(pattern)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil
	}
	var hits []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
			continue
		}
		if matcher.MatchString(name) {
			p := filepath.Join(parent, name)
			if isDir(p) {
				hits = append(hits, p)
			}
		}
	}
	return hits
}

var discoveryMaxDirectories = 100000
var discoveryTimeout = 30 * time.Second
var discoveryNow = time.Now

// discoveryReadDir is injectable so tests can remove folders mid-walk.
var discoveryReadDir = os.ReadDir

func Resolve(source, hostname string) (Result, error) {
	result := Result{Paths: []string{}, Missing: []string{}, Unmatched: []string{}, Skipped: []string{}}
	data, err := os.ReadFile(source)
	if err != nil {
		return result, err
	}
	if !utf8.Valid(data) {
		return result, fmt.Errorf("%s: invalid UTF-8 list", source)
	}
	type rule struct{ kind, rest string }
	var rules []rule
	var includes []string
	for index, raw := range splitLines(data) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "@") {
			selector, rest, e := splitPair(line)
			if e != nil {
				return result, e
			}
			line = rest
			if selector != "@*" && foldHost(selector[1:]) != foldHost(hostname) {
				continue
			}
		}
		directive, rest, e := splitPair(line)
		if e != nil {
			return result, e
		}
		if directive == "include" {
			includes = append(includes, rest)
			continue
		}
		if directive != "path" && directive != "glob" && directive != "name" {
			return result, fmt.Errorf("%s:%d: unknown directive %s", source, index+1, directive)
		}
		rules = append(rules, rule{directive, rest})
	}
	paths, missing, skipped := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, raw := range includes {
		listing := expandUser(raw)
		if !isFile(listing) {
			continue
		}
		content, e := os.ReadFile(listing)
		if e != nil {
			return result, e
		}
		if !utf8.Valid(content) {
			return result, fmt.Errorf("%s: invalid UTF-8 list", listing)
		}
		for _, rawline := range lineParts(content) {
			line := strings.TrimSpace(rawline)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			directive, rest, _ := strings.Cut(line, " ")
			rest = strings.TrimSpace(rest)
			path, e := normalized(rest, false)
			if directive != "path" || e != nil || isLink(path) || (exists(path) && !isDir(path)) {
				if rest != "" {
					skipped[rest] = true
				} else {
					skipped[line] = true
				}
				continue
			}
			if isDir(path) {
				paths[path] = true
			}
		}
	}
	byRoot := map[string]map[string]bool{}
	var roots []string
	for _, rule := range rules {
		if rule.kind == "name" {
			name, root, e := splitPair(rule.rest)
			if e != nil {
				return result, e
			}
			if strings.Contains(name, "/") || name == "." || name == ".." {
				return result, fmt.Errorf("invalid folder name: %s", name)
			}
			root, e = normalized(root, false)
			if e != nil {
				return result, e
			}
			if byRoot[root] == nil {
				byRoot[root] = map[string]bool{}
				roots = append(roots, root)
			}
			byRoot[root][name] = true
			continue
		}
		path, e := normalized(rule.rest, false)
		if e != nil {
			return result, e
		}
		hits := []string{path}
		if rule.kind == "glob" {
			if strings.ContainsAny(filepath.Dir(path), "*?[") || strings.Contains(path, "**") {
				return result, fmt.Errorf("globs must match one immediate child folder")
			}
			hits = globFolders(path)
			linkCount := 0
			regular := make([]string, 0, len(hits))
			for _, hit := range hits {
				if isLink(hit) {
					skipped[hit] = true
					linkCount++
				} else {
					regular = append(regular, hit)
				}
			}
			hits = regular
			if len(hits) == 0 && linkCount == 0 {
				result.Unmatched = append(result.Unmatched, path)
			}
		} else if !exists(path) {
			missing[path] = true
		}
		for _, hit := range hits {
			hit, e = normalized(hit, false)
			if e != nil {
				return result, e
			}
			if isDir(hit) {
				paths[hit] = true
			} else if exists(hit) {
				return result, fmt.Errorf("not a directory: %s", hit)
			}
		}
	}
	deadline := discoveryNow().Add(discoveryTimeout)
	visited := 0
	for _, root := range roots {
		if !exists(root) {
			continue
		}
		info, e := os.Stat(root)
		if e != nil {
			return result, e
		}
		device := deviceOf(info)
		wanted := byRoot[root]
		var walk func(string, int) error
		walk = func(parent string, depth int) error {
			visited++
			if visited > discoveryMaxDirectories || discoveryNow().After(deadline) {
				return fmt.Errorf("dependency discovery exceeded limit under %s at %s; narrow name roots", root, parent)
			}
			// os.walk enumerates each directory before its caller prunes it.
			entries, e := discoveryReadDir(parent)
			if e != nil {
				// Worktrees and build folders come and go under source roots: one that
				// vanished between listing and descent is simply gone. Other errors fail.
				if parent != root && errors.Is(e, fs.ErrNotExist) {
					return nil
				}
				return e
			}
			if paths[parent] || depth >= 4 {
				return nil
			}
			var descend []string
			for _, entry := range entries {
				name := entry.Name()
				if name == ".git" || name == ".jj" || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				child := filepath.Join(parent, name)
				info, e := entry.Info()
				if e != nil {
					if errors.Is(e, fs.ErrNotExist) {
						continue // vanished mid-walk
					}
					return e
				}
				if !info.IsDir() || deviceOf(info) != device {
					continue
				}
				match := wanted[name]
				if name == "venv" || name == ".venv" {
					match = match && (isFile(filepath.Join(child, "pyvenv.cfg")) || isFile(filepath.Join(child, "bin/activate")))
				}
				if name == "target" {
					match = match && (isFile(filepath.Join(child, ".rustc_info.json")) || isDir(filepath.Join(child, "debug/deps")) || isDir(filepath.Join(child, "release/deps")))
				}
				if match {
					paths[child] = true
				} else {
					descend = append(descend, child)
				}
			}
			for _, child := range descend {
				if e := walk(child, depth+1); e != nil {
					return e
				}
			}
			return nil
		}
		if e := walk(root, 0); e != nil {
			return result, e
		}
	}
	candidates := sortedKeys(paths)
	sort.Slice(candidates, func(i, j int) bool {
		a, b := strings.Count(candidates[i], "/"), strings.Count(candidates[j], "/")
		if a == b {
			return candidates[i] < candidates[j]
		}
		return a < b
	})
	for _, p := range candidates {
		covered := false
		for _, parent := range result.Paths {
			if strings.HasPrefix(p, parent+"/") {
				covered = true
				break
			}
		}
		if !covered {
			result.Paths = append(result.Paths, p)
		}
	}
	sort.Strings(result.Paths)
	result.Missing = sortedKeys(missing)
	result.Skipped = sortedKeys(skipped)
	return result, nil
}

// foldHost implements Unicode's full case folding for host selectors. The
// exceptions are Unicode mappings that expand or differ from simple lowercase.
func foldHost(value string) string {
	var out strings.Builder
	for _, r := range value {
		if expanded, ok := hostFoldExceptions[r]; ok {
			out.WriteString(expanded)
		} else {
			out.WriteRune(unicode.ToLower(r))
		}
	}
	return out.String()
}

var hostFoldExceptions = map[rune]string{
	'µ': "μ",
	'ß': "ss",
	'İ': "i̇",
	'ŉ': "ʼn",
	'ſ': "s",
	'ǰ': "ǰ",
	'ͅ': "ι",
	'ΐ': "ΐ",
	'ΰ': "ΰ",
	'ς': "σ",
	'ϐ': "β",
	'ϑ': "θ",
	'ϕ': "φ",
	'ϖ': "π",
	'ϰ': "κ",
	'ϱ': "ρ",
	'ϵ': "ε",
	'և': "եւ",
	'Ꭰ': "Ꭰ",
	'Ꭱ': "Ꭱ",
	'Ꭲ': "Ꭲ",
	'Ꭳ': "Ꭳ",
	'Ꭴ': "Ꭴ",
	'Ꭵ': "Ꭵ",
	'Ꭶ': "Ꭶ",
	'Ꭷ': "Ꭷ",
	'Ꭸ': "Ꭸ",
	'Ꭹ': "Ꭹ",
	'Ꭺ': "Ꭺ",
	'Ꭻ': "Ꭻ",
	'Ꭼ': "Ꭼ",
	'Ꭽ': "Ꭽ",
	'Ꭾ': "Ꭾ",
	'Ꭿ': "Ꭿ",
	'Ꮀ': "Ꮀ",
	'Ꮁ': "Ꮁ",
	'Ꮂ': "Ꮂ",
	'Ꮃ': "Ꮃ",
	'Ꮄ': "Ꮄ",
	'Ꮅ': "Ꮅ",
	'Ꮆ': "Ꮆ",
	'Ꮇ': "Ꮇ",
	'Ꮈ': "Ꮈ",
	'Ꮉ': "Ꮉ",
	'Ꮊ': "Ꮊ",
	'Ꮋ': "Ꮋ",
	'Ꮌ': "Ꮌ",
	'Ꮍ': "Ꮍ",
	'Ꮎ': "Ꮎ",
	'Ꮏ': "Ꮏ",
	'Ꮐ': "Ꮐ",
	'Ꮑ': "Ꮑ",
	'Ꮒ': "Ꮒ",
	'Ꮓ': "Ꮓ",
	'Ꮔ': "Ꮔ",
	'Ꮕ': "Ꮕ",
	'Ꮖ': "Ꮖ",
	'Ꮗ': "Ꮗ",
	'Ꮘ': "Ꮘ",
	'Ꮙ': "Ꮙ",
	'Ꮚ': "Ꮚ",
	'Ꮛ': "Ꮛ",
	'Ꮜ': "Ꮜ",
	'Ꮝ': "Ꮝ",
	'Ꮞ': "Ꮞ",
	'Ꮟ': "Ꮟ",
	'Ꮠ': "Ꮠ",
	'Ꮡ': "Ꮡ",
	'Ꮢ': "Ꮢ",
	'Ꮣ': "Ꮣ",
	'Ꮤ': "Ꮤ",
	'Ꮥ': "Ꮥ",
	'Ꮦ': "Ꮦ",
	'Ꮧ': "Ꮧ",
	'Ꮨ': "Ꮨ",
	'Ꮩ': "Ꮩ",
	'Ꮪ': "Ꮪ",
	'Ꮫ': "Ꮫ",
	'Ꮬ': "Ꮬ",
	'Ꮭ': "Ꮭ",
	'Ꮮ': "Ꮮ",
	'Ꮯ': "Ꮯ",
	'Ꮰ': "Ꮰ",
	'Ꮱ': "Ꮱ",
	'Ꮲ': "Ꮲ",
	'Ꮳ': "Ꮳ",
	'Ꮴ': "Ꮴ",
	'Ꮵ': "Ꮵ",
	'Ꮶ': "Ꮶ",
	'Ꮷ': "Ꮷ",
	'Ꮸ': "Ꮸ",
	'Ꮹ': "Ꮹ",
	'Ꮺ': "Ꮺ",
	'Ꮻ': "Ꮻ",
	'Ꮼ': "Ꮼ",
	'Ꮽ': "Ꮽ",
	'Ꮾ': "Ꮾ",
	'Ꮿ': "Ꮿ",
	'Ᏸ': "Ᏸ",
	'Ᏹ': "Ᏹ",
	'Ᏺ': "Ᏺ",
	'Ᏻ': "Ᏻ",
	'Ᏼ': "Ᏼ",
	'Ᏽ': "Ᏽ",
	'ᏸ': "Ᏸ",
	'ᏹ': "Ᏹ",
	'ᏺ': "Ᏺ",
	'ᏻ': "Ᏻ",
	'ᏼ': "Ᏼ",
	'ᏽ': "Ᏽ",
	'ᲀ': "в",
	'ᲁ': "д",
	'ᲂ': "о",
	'ᲃ': "с",
	'ᲄ': "т",
	'ᲅ': "т",
	'ᲆ': "ъ",
	'ᲇ': "ѣ",
	'ᲈ': "ꙋ",
	'ẖ': "ẖ",
	'ẗ': "ẗ",
	'ẘ': "ẘ",
	'ẙ': "ẙ",
	'ẚ': "aʾ",
	'ẛ': "ṡ",
	'ẞ': "ss",
	'ὐ': "ὐ",
	'ὒ': "ὒ",
	'ὔ': "ὔ",
	'ὖ': "ὖ",
	'ᾀ': "ἀι",
	'ᾁ': "ἁι",
	'ᾂ': "ἂι",
	'ᾃ': "ἃι",
	'ᾄ': "ἄι",
	'ᾅ': "ἅι",
	'ᾆ': "ἆι",
	'ᾇ': "ἇι",
	'ᾈ': "ἀι",
	'ᾉ': "ἁι",
	'ᾊ': "ἂι",
	'ᾋ': "ἃι",
	'ᾌ': "ἄι",
	'ᾍ': "ἅι",
	'ᾎ': "ἆι",
	'ᾏ': "ἇι",
	'ᾐ': "ἠι",
	'ᾑ': "ἡι",
	'ᾒ': "ἢι",
	'ᾓ': "ἣι",
	'ᾔ': "ἤι",
	'ᾕ': "ἥι",
	'ᾖ': "ἦι",
	'ᾗ': "ἧι",
	'ᾘ': "ἠι",
	'ᾙ': "ἡι",
	'ᾚ': "ἢι",
	'ᾛ': "ἣι",
	'ᾜ': "ἤι",
	'ᾝ': "ἥι",
	'ᾞ': "ἦι",
	'ᾟ': "ἧι",
	'ᾠ': "ὠι",
	'ᾡ': "ὡι",
	'ᾢ': "ὢι",
	'ᾣ': "ὣι",
	'ᾤ': "ὤι",
	'ᾥ': "ὥι",
	'ᾦ': "ὦι",
	'ᾧ': "ὧι",
	'ᾨ': "ὠι",
	'ᾩ': "ὡι",
	'ᾪ': "ὢι",
	'ᾫ': "ὣι",
	'ᾬ': "ὤι",
	'ᾭ': "ὥι",
	'ᾮ': "ὦι",
	'ᾯ': "ὧι",
	'ᾲ': "ὰι",
	'ᾳ': "αι",
	'ᾴ': "άι",
	'ᾶ': "ᾶ",
	'ᾷ': "ᾶι",
	'ᾼ': "αι",
	'ι': "ι",
	'ῂ': "ὴι",
	'ῃ': "ηι",
	'ῄ': "ήι",
	'ῆ': "ῆ",
	'ῇ': "ῆι",
	'ῌ': "ηι",
	'ῒ': "ῒ",
	'ΐ': "ΐ",
	'ῖ': "ῖ",
	'ῗ': "ῗ",
	'ῢ': "ῢ",
	'ΰ': "ΰ",
	'ῤ': "ῤ",
	'ῦ': "ῦ",
	'ῧ': "ῧ",
	'ῲ': "ὼι",
	'ῳ': "ωι",
	'ῴ': "ώι",
	'ῶ': "ῶ",
	'ῷ': "ῶι",
	'ῼ': "ωι",
	'ꭰ': "Ꭰ",
	'ꭱ': "Ꭱ",
	'ꭲ': "Ꭲ",
	'ꭳ': "Ꭳ",
	'ꭴ': "Ꭴ",
	'ꭵ': "Ꭵ",
	'ꭶ': "Ꭶ",
	'ꭷ': "Ꭷ",
	'ꭸ': "Ꭸ",
	'ꭹ': "Ꭹ",
	'ꭺ': "Ꭺ",
	'ꭻ': "Ꭻ",
	'ꭼ': "Ꭼ",
	'ꭽ': "Ꭽ",
	'ꭾ': "Ꭾ",
	'ꭿ': "Ꭿ",
	'ꮀ': "Ꮀ",
	'ꮁ': "Ꮁ",
	'ꮂ': "Ꮂ",
	'ꮃ': "Ꮃ",
	'ꮄ': "Ꮄ",
	'ꮅ': "Ꮅ",
	'ꮆ': "Ꮆ",
	'ꮇ': "Ꮇ",
	'ꮈ': "Ꮈ",
	'ꮉ': "Ꮉ",
	'ꮊ': "Ꮊ",
	'ꮋ': "Ꮋ",
	'ꮌ': "Ꮌ",
	'ꮍ': "Ꮍ",
	'ꮎ': "Ꮎ",
	'ꮏ': "Ꮏ",
	'ꮐ': "Ꮐ",
	'ꮑ': "Ꮑ",
	'ꮒ': "Ꮒ",
	'ꮓ': "Ꮓ",
	'ꮔ': "Ꮔ",
	'ꮕ': "Ꮕ",
	'ꮖ': "Ꮖ",
	'ꮗ': "Ꮗ",
	'ꮘ': "Ꮘ",
	'ꮙ': "Ꮙ",
	'ꮚ': "Ꮚ",
	'ꮛ': "Ꮛ",
	'ꮜ': "Ꮜ",
	'ꮝ': "Ꮝ",
	'ꮞ': "Ꮞ",
	'ꮟ': "Ꮟ",
	'ꮠ': "Ꮠ",
	'ꮡ': "Ꮡ",
	'ꮢ': "Ꮢ",
	'ꮣ': "Ꮣ",
	'ꮤ': "Ꮤ",
	'ꮥ': "Ꮥ",
	'ꮦ': "Ꮦ",
	'ꮧ': "Ꮧ",
	'ꮨ': "Ꮨ",
	'ꮩ': "Ꮩ",
	'ꮪ': "Ꮪ",
	'ꮫ': "Ꮫ",
	'ꮬ': "Ꮬ",
	'ꮭ': "Ꮭ",
	'ꮮ': "Ꮮ",
	'ꮯ': "Ꮯ",
	'ꮰ': "Ꮰ",
	'ꮱ': "Ꮱ",
	'ꮲ': "Ꮲ",
	'ꮳ': "Ꮳ",
	'ꮴ': "Ꮴ",
	'ꮵ': "Ꮵ",
	'ꮶ': "Ꮶ",
	'ꮷ': "Ꮷ",
	'ꮸ': "Ꮸ",
	'ꮹ': "Ꮹ",
	'ꮺ': "Ꮺ",
	'ꮻ': "Ꮻ",
	'ꮼ': "Ꮼ",
	'ꮽ': "Ꮽ",
	'ꮾ': "Ꮾ",
	'ꮿ': "Ꮿ",
	'ﬀ': "ff",
	'ﬁ': "fi",
	'ﬂ': "fl",
	'ﬃ': "ffi",
	'ﬄ': "ffl",
	'ﬅ': "st",
	'ﬆ': "st",
	'ﬓ': "մն",
	'ﬔ': "մե",
	'ﬕ': "մի",
	'ﬖ': "վն",
	'ﬗ': "մխ",
}
