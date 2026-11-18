package startup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// evalIdiom evaluates the body of $(...) or `...`. Only a fixed set of
// side-effect-free forms found in widely used eggs is recognised:
//
//	[[ [!] -f|-e|-d|-s PATH ]] && printf %s TEXT [|| printf %s TEXT]
//	[ [!] -f|-e|-d|-s PATH ] && printf %s TEXT [|| printf %s TEXT]
//	cat PATH
//
// Nothing is ever executed: tests are stat(2) calls and cat is a bounded read
// of a file below the working directory.
func (p *parser) evalIdiom(body string, offset int) ([]piece, error) {
	if p.depth >= maxIdiomDepth {
		return nil, p.syntaxErr(offset, "command substitutions nested too deeply")
	}
	sub := &parser{src: body, opts: p.opts, idiom: true, depth: p.depth + 1}
	if err := sub.run(); err != nil {
		var se *SyntaxError
		if errors.As(err, &se) {
			return nil, &SyntaxError{Offset: offset + 2 + se.Offset, Msg: se.Msg}
		}
		return nil, err
	}
	pieces, err := p.matchIdiom(sub.tokens, offset)
	if err != nil {
		return nil, err
	}
	return trimTrailingNewlines(pieces), nil
}

func (p *parser) matchIdiom(toks []token, offset int) ([]piece, error) {
	unsupported := func(why string) error {
		return p.syntaxErr(offset, "unsupported command substitution (%s); only `[[ -f FILE ]] && printf %%s \"...\" [|| printf %%s \"...\"]` and `cat FILE` are supported without a shell", why)
	}
	isWord := func(i int, raw string) bool {
		return i < len(toks) && toks[i].op == "" && toks[i].raw == raw
	}
	if len(toks) == 0 {
		return nil, unsupported("empty")
	}

	if isWord(0, "cat") {
		if len(toks) != 2 || toks[1].op != "" {
			return nil, unsupported("cat takes exactly one file")
		}
		content, err := p.readFile(toks[1].value())
		if err != nil {
			return nil, err
		}
		return []piece{{content, false}}, nil
	}

	var closer string
	switch {
	case isWord(0, "[["):
		closer = "]]"
	case isWord(0, "["):
		closer = "]"
	default:
		return nil, unsupported(fmt.Sprintf("command %q", toks[0].raw+toks[0].op))
	}
	i := 1
	negate := isWord(i, "!")
	if negate {
		i++
	}
	if i+2 >= len(toks) || toks[i].op != "" || toks[i+1].op != "" {
		return nil, unsupported("malformed test")
	}
	testOp := toks[i].raw
	path := toks[i+1].value()
	i += 2
	if !isWord(i, closer) {
		return nil, unsupported("test must be a single file test closed by " + closer)
	}
	i++
	result, err := p.fileTest(testOp, path)
	if err != nil {
		return nil, unsupported(err.Error())
	}
	result = result != negate

	if i >= len(toks) || toks[i].op != "&&" {
		return nil, unsupported("test must be followed by && printf %s")
	}
	thenText, i, ok := printfAt(toks, i+1)
	if !ok {
		return nil, unsupported("expected printf %s TEXT after &&")
	}
	var elseText []piece
	if i < len(toks) {
		if toks[i].op != "||" {
			return nil, unsupported("expected || printf %s TEXT")
		}
		elseText, i, ok = printfAt(toks, i+1)
		if !ok || i != len(toks) {
			return nil, unsupported("expected printf %s TEXT after ||")
		}
	}
	if result {
		return thenText, nil
	}
	return elseText, nil
}

func printfAt(toks []token, i int) ([]piece, int, bool) {
	if i+2 >= len(toks) {
		return nil, i, false
	}
	if toks[i].op != "" || toks[i].raw != "printf" || toks[i+1].op != "" || toks[i+1].value() != "%s" || toks[i+2].op != "" {
		return nil, i, false
	}
	return toks[i+2].pieces, i + 3, true
}

func (p *parser) fileTest(op, path string) (bool, error) {
	if path == "" {
		return false, errors.New("empty path in file test")
	}
	fi, err := os.Stat(p.resolve(path))
	switch op {
	case "-e":
		return err == nil, nil
	case "-f":
		return err == nil && fi.Mode().IsRegular(), nil
	case "-d":
		return err == nil && fi.IsDir(), nil
	case "-s":
		return err == nil && fi.Mode().IsRegular() && fi.Size() > 0, nil
	}
	return false, fmt.Errorf("file test %q (supported: -e -f -d -s)", op)
}

func (p *parser) resolve(path string) string {
	if filepath.IsAbs(path) || p.opts.Dir == "" {
		return path
	}
	return filepath.Join(p.opts.Dir, path)
}

// readFile implements `cat PATH`: a regular file below the working directory,
// at most MaxFileBytes long.
func (p *parser) readFile(path string) (string, error) {
	clean := filepath.Clean(path)
	if path == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &ExpansionError{fmt.Sprintf("cat %q: only relative paths inside the working directory are allowed", path)}
	}
	f, err := os.Open(p.resolve(clean))
	if err != nil {
		return "", &ExpansionError{fmt.Sprintf("cat %q: %v", path, err)}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", &ExpansionError{fmt.Sprintf("cat %q: not a regular file", path)}
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return "", &ExpansionError{fmt.Sprintf("cat %q: %v", path, err)}
	}
	if len(data) > MaxFileBytes {
		return "", &ExpansionError{fmt.Sprintf("cat %q: file larger than %d bytes", path, MaxFileBytes)}
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return "", &ExpansionError{fmt.Sprintf("cat %q: file contains NUL bytes", path)}
	}
	return string(data), nil
}

func trimTrailingNewlines(pieces []piece) []piece {
	for len(pieces) > 0 {
		last := &pieces[len(pieces)-1]
		last.text = strings.TrimRight(last.text, "\n")
		if last.text != "" {
			break
		}
		pieces = pieces[:len(pieces)-1]
	}
	return pieces
}
