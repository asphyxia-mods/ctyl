package startup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxGlobMatches = 64

// glob expands a field whose unquoted template text contains *, ? or [.
// Matching follows bash defaults (dotfiles need an explicit leading '.',
// results are sorted) with two deliberate differences: substituted values are
// never treated as patterns, and a pattern matching more than one path is an
// error instead of silently becoming several arguments.
func (p *parser) glob(f field) ([]string, error) {
	literal := f.literal()
	var pattern strings.Builder
	hasMeta := false
	for _, pc := range f.pieces {
		if pc.glob {
			if strings.ContainsAny(pc.text, "*?[") {
				hasMeta = true
			}
			pattern.WriteString(pc.text)
			continue
		}
		for _, r := range pc.text {
			if strings.ContainsRune(`*?[\`, r) {
				pattern.WriteByte('\\')
			}
			pattern.WriteRune(r)
		}
	}
	if !hasMeta {
		return []string{literal}, nil
	}
	matches, err := globMatches(p.opts.Dir, pattern.String())
	if err != nil {
		// bash keeps malformed patterns such as an unclosed '[' literally.
		return []string{literal}, nil
	}
	switch len(matches) {
	case 0:
		p.cmd.Warnings = append(p.cmd.Warnings, fmt.Sprintf("pattern %q matched no files and was passed literally", literal))
		return []string{literal}, nil
	case 1:
		return matches, nil
	}
	shown := matches
	if len(shown) > 5 {
		shown = append(shown[:5:5], "...")
	}
	return nil, &ExpansionError{fmt.Sprintf("pattern %q is ambiguous: it matches %d files (%s); make the startup command or file layout select exactly one", literal, len(matches), strings.Join(shown, ", "))}
}

func globMatches(dir, pattern string) ([]string, error) {
	prefixes := []string{""}
	rest := pattern
	if strings.HasPrefix(pattern, "/") {
		prefixes = []string{"/"}
		rest = strings.TrimLeft(pattern, "/")
	}
	components := strings.Split(rest, "/")
	for ci, comp := range components {
		last := ci == len(components)-1
		if comp == "" {
			continue
		}
		var next []string
		if !componentHasMeta(comp) {
			lit := unescape(comp)
			for _, pre := range prefixes {
				next = append(next, pre+lit+sepIf(!last))
			}
			prefixes = next
			continue
		}
		if _, err := filepath.Match(comp, ""); err != nil {
			return nil, err
		}
		explicitDot := strings.HasPrefix(comp, ".")
		for _, pre := range prefixes {
			entries, err := os.ReadDir(join(dir, pre))
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, ".") && !explicitDot {
					continue
				}
				if ok, _ := filepath.Match(comp, name); !ok {
					continue
				}
				if !last && !isDir(join(dir, pre+name)) {
					continue
				}
				next = append(next, pre+name+sepIf(!last))
				if len(next) > maxGlobMatches {
					return nil, fmt.Errorf("too many matches")
				}
			}
		}
		prefixes = next
	}
	var out []string
	for _, m := range prefixes {
		if _, err := os.Lstat(join(dir, m)); err == nil {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out, nil
}

func componentHasMeta(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '*', '?', '[':
			return true
		}
	}
	return false
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func sepIf(b bool) string {
	if b {
		return "/"
	}
	return ""
}

func join(dir, rel string) string {
	if rel == "" {
		rel = "."
	}
	if filepath.IsAbs(rel) || dir == "" {
		return rel
	}
	return filepath.Join(dir, rel)
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
