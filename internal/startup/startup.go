// Package startup turns a Pterodactyl/Calagopus-style startup template into an
// argument vector without invoking a shell.
//
// The accepted language is a deliberately small, shell-compatible subset; the
// exact semantics are specified in docs/launcher.md and pinned by the tests in
// this package. Anything outside the subset is rejected with a *SyntaxError
// instead of being approximated.
package startup

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxTemplateBytes = 64 << 10
	MaxArgs          = 4096
	// MaxArgBytes mirrors Linux MAX_ARG_STRLEN; execve rejects longer strings.
	MaxArgBytes = 128 << 10
	// MaxFileBytes bounds files read by the `cat FILE` compatibility idiom.
	MaxFileBytes  = 64 << 10
	maxIdiomDepth = 2
)

// Options controls expansion. The zero value is not usable: Lookup is required.
type Options struct {
	// Lookup returns the value of an environment variable.
	Lookup func(name string) (string, bool)
	// Dir is the directory relative paths (globs, file tests, cat) resolve against.
	Dir string
	// Strict disables field splitting of unquoted variables, except for the
	// variables named in ArgListVars.
	Strict      bool
	ArgListVars map[string]bool
	// AllowUnset makes references to undefined variables expand to "" instead
	// of failing.
	AllowUnset bool
}

// Command is the result of parsing a template.
type Command struct {
	// Assignments are leading NAME=value words, applied to the environment of
	// the executed program (env(1) semantics).
	Assignments []Assignment
	Argv        []string
	// Warnings describe shell-compatible but suspicious situations, such as a
	// glob that matched nothing and was kept literally.
	Warnings []string
}

type Assignment struct {
	Name, Value string
}

// SyntaxError reports a template the launcher refuses to interpret.
type SyntaxError struct {
	Offset int // byte offset into the template
	Msg    string
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("startup command, byte %d: %s", e.Offset, e.Msg)
}

// ExpansionError reports a template that is well formed but cannot be
// expanded in the current environment (missing variable, ambiguous glob...).
type ExpansionError struct {
	Msg string
}

func (e *ExpansionError) Error() string { return "startup command: " + e.Msg }

// Parse tokenizes and expands template.
func Parse(template string, opts Options) (*Command, error) {
	if opts.Lookup == nil {
		return nil, errors.New("startup: Options.Lookup is required")
	}
	if len(template) > MaxTemplateBytes {
		return nil, &SyntaxError{0, fmt.Sprintf("template is %d bytes, limit is %d", len(template), MaxTemplateBytes)}
	}
	if !utf8.ValidString(template) {
		return nil, &SyntaxError{firstInvalidUTF8(template), "template is not valid UTF-8"}
	}
	if off, c, ok := findControl(template); ok {
		return nil, &SyntaxError{off, fmt.Sprintf("control character 0x%02x is not allowed", c)}
	}
	p := &parser{src: template, opts: opts}
	if err := p.run(); err != nil {
		return nil, err
	}
	if len(p.cmd.Argv) == 0 {
		return nil, &ExpansionError{"no command to execute (template is empty after expansion)"}
	}
	return &p.cmd, nil
}

// findControl locates C0/DEL control characters other than tab, CR and LF.
func findControl(s string) (int, byte, bool) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 0x20 && c != '\t' && c != '\n' && c != '\r') || c == 0x7f {
			return i, c, true
		}
	}
	return 0, 0, false
}

func firstInvalidUTF8(s string) int {
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			return i
		}
		i += size
	}
	return 0
}

// piece is a run of characters inside one field. glob marks characters that
// came from unquoted template text, the only place where *, ? and [ are
// pattern metacharacters. Substituted values are never globbed.
type piece struct {
	text string
	glob bool
}

type field struct {
	pieces  []piece
	present bool // distinguishes an empty argument ("") from no argument
}

func (f *field) add(text string, glob bool) {
	f.present = true
	if text == "" {
		return
	}
	if n := len(f.pieces); n > 0 && f.pieces[n-1].glob == glob {
		f.pieces[n-1].text += text
		return
	}
	f.pieces = append(f.pieces, piece{text, glob})
}

func (f *field) literal() string {
	var b strings.Builder
	for _, pc := range f.pieces {
		b.WriteString(pc.text)
	}
	return b.String()
}

func isIFS(c byte) bool { return c == ' ' || c == '\t' || c == '\n' }

func isNameStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }

func validName(s string) bool {
	if s == "" || !isNameStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNameChar(s[i]) {
			return false
		}
	}
	return true
}
