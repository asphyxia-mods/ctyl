package startup

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// token is produced only in idiom mode, where the parser tokenizes the body of
// a command substitution instead of building an argument vector.
type token struct {
	op     string // "&&" or "||"; empty for words
	raw    string // exact source text of a word, used for keyword matching
	pieces []piece
}

func (t token) value() string {
	var b strings.Builder
	for _, pc := range t.pieces {
		b.WriteString(pc.text)
	}
	return b.String()
}

type parser struct {
	src  string
	pos  int
	opts Options
	cmd  Command

	inWord         bool
	wordStart      int
	cur            field
	split          []field // fields completed by splitting inside the current word
	assign         string  // name of the assignment the current word defines
	commandStarted bool

	// idiom mode: tokens are collected instead of argv; depth bounds nesting.
	idiom  bool
	tokens []token
	depth  int
}

func (p *parser) syntaxErr(off int, format string, args ...any) error {
	return &SyntaxError{Offset: off, Msg: fmt.Sprintf(format, args...)}
}

func (p *parser) run() error {
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		if isIFS(c) || c == '\r' {
			if err := p.endWord(); err != nil {
				return err
			}
			p.pos++
			continue
		}
		if !p.inWord {
			if err := p.startWord(); err != nil {
				return err
			}
			continue
		}
		if err := p.step(); err != nil {
			return err
		}
	}
	return p.endWord()
}

func (p *parser) startWord() error {
	p.inWord = true
	p.wordStart = p.pos
	switch p.src[p.pos] {
	case '#':
		return p.syntaxErr(p.pos, "'#' comments are not supported; quote the '#' if it is part of an argument")
	case '~':
		return p.syntaxErr(p.pos, "tilde expansion is not supported; use {{HOME}} or an explicit path")
	}
	if p.idiom || p.commandStarted {
		return nil
	}
	if name, ok := assignmentPrefix(p.src[p.pos:]); ok {
		p.assign = name
		p.pos += len(name) + 1
		return nil
	}
	p.commandStarted = true
	return nil
}

// assignmentPrefix reports whether s starts with an unquoted NAME=.
func assignmentPrefix(s string) (string, bool) {
	i := 0
	for i < len(s) && isNameChar(s[i]) {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != '=' || !isNameStart(s[0]) {
		return "", false
	}
	return s[:i], true
}

func (p *parser) endWord() error {
	if !p.inWord {
		return nil
	}
	p.inWord = false
	fields := append(p.split, p.cur)
	p.split, p.cur = nil, field{}

	if p.assign != "" {
		name := p.assign
		p.assign = ""
		var value strings.Builder
		for _, f := range fields {
			value.WriteString(f.literal())
		}
		p.cmd.Assignments = append(p.cmd.Assignments, Assignment{name, value.String()})
		return nil
	}

	if p.idiom {
		if fields[0].present {
			p.tokens = append(p.tokens, token{raw: p.src[p.wordStart:p.pos], pieces: fields[0].pieces})
		}
		return nil
	}

	for _, f := range fields {
		if !f.present {
			continue
		}
		args, err := p.glob(f)
		if err != nil {
			return err
		}
		for _, a := range args {
			if strings.IndexByte(a, 0) >= 0 {
				return &ExpansionError{fmt.Sprintf("argument %d contains a NUL byte", len(p.cmd.Argv))}
			}
			if len(a) > MaxArgBytes {
				return &ExpansionError{fmt.Sprintf("argument %d is %d bytes, limit is %d", len(p.cmd.Argv), len(a), MaxArgBytes)}
			}
			if len(p.cmd.Argv) >= MaxArgs {
				return &ExpansionError{fmt.Sprintf("more than %d arguments", MaxArgs)}
			}
			p.cmd.Argv = append(p.cmd.Argv, a)
		}
	}
	return nil
}

// step consumes one construct in unquoted context.
func (p *parser) step() error {
	start := p.pos
	c := p.src[p.pos]
	switch c {
	case '\'':
		text, err := p.singleQuoted()
		if err != nil {
			return err
		}
		p.cur.add(text, p.idiom)
	case '"':
		pieces, err := p.doubleQuoted()
		if err != nil {
			return err
		}
		p.cur.add("", false)
		for _, pc := range pieces {
			p.cur.add(pc.text, pc.glob && p.idiom)
		}
	case '\\':
		if p.pos+1 >= len(p.src) {
			return p.syntaxErr(start, "trailing backslash")
		}
		if n := lineContinuation(p.src[p.pos+1:]); n > 0 {
			p.pos += 1 + n
			return nil
		}
		_, size := utf8.DecodeRuneInString(p.src[p.pos+1:])
		p.cur.add(p.src[p.pos+1:p.pos+1+size], false)
		p.pos += 1 + size
	case '{':
		if !strings.HasPrefix(p.src[p.pos:], "{{") {
			return p.syntaxErr(start, "brace expansion is not supported; quote '{' if it is part of an argument")
		}
		name, err := p.braceVar()
		if err != nil {
			return err
		}
		return p.unquotedVar(name)
	case '}':
		return p.syntaxErr(start, "unexpected '}'; quote it if it is part of an argument")
	case '$':
		return p.unquotedDollar()
	case '`':
		pieces, err := p.backtick()
		if err != nil {
			return err
		}
		return p.unquotedSplit(pieces)
	case ';', '&', '|', '<', '>', '(', ')':
		return p.operator()
	default:
		if c < 0x20 || c == 0x7f {
			return p.syntaxErr(start, "control character 0x%02x is not allowed", c)
		}
		end := p.pos
		for end < len(p.src) && !isSpecial(p.src[end]) {
			end++
		}
		p.cur.add(p.src[p.pos:end], true)
		p.pos = end
	}
	return nil
}

func isSpecial(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\'', '"', '\\', '{', '}', '$', '`', ';', '&', '|', '<', '>', '(', ')':
		return true
	}
	return c < 0x20 || c == 0x7f
}

func lineContinuation(s string) int {
	switch {
	case strings.HasPrefix(s, "\n"):
		return 1
	case strings.HasPrefix(s, "\r\n"):
		return 2
	}
	return 0
}

func (p *parser) operator() error {
	start := p.pos
	op := p.src[p.pos : p.pos+1]
	if p.pos+1 < len(p.src) {
		if two := p.src[p.pos : p.pos+2]; two == "&&" || two == "||" || two == ">>" || two == "<<" || two == "&>" || two == ";;" {
			op = two
		}
	}
	if p.idiom && (op == "&&" || op == "||") {
		if err := p.endWord(); err != nil {
			return err
		}
		p.tokens = append(p.tokens, token{op: op})
		p.pos += 2
		return nil
	}
	var what string
	switch op {
	case ";", ";;":
		what = "command lists"
	case "&&", "||":
		what = "conditional command lists"
	case "&":
		what = "background jobs"
	case "|":
		what = "pipelines"
	case "(", ")":
		what = "subshells"
	default:
		what = "redirections"
	}
	return p.syntaxErr(start, "shell operator %q is not supported (%s need a shell); quote it if it is part of an argument", op, what)
}

func (p *parser) singleQuoted() (string, error) {
	start := p.pos
	end := strings.IndexByte(p.src[p.pos+1:], '\'')
	if end < 0 {
		return "", p.syntaxErr(start, "unterminated single quote")
	}
	text := p.src[p.pos+1 : p.pos+1+end]
	if i := strings.Index(text, "{{"); i >= 0 && strings.Contains(text[i:], "}}") {
		return "", p.syntaxErr(start, "{{...}} inside single quotes is never substituted; use double quotes to substitute, or escape the braces to keep them literally")
	}
	p.pos += end + 2
	return text, nil
}

// doubleQuoted parses "..." starting at the opening quote. Returned pieces
// have glob=true for characters that came from template text, so that idiom
// output can be globbed like a shell would; callers outside idioms clear it.
func (p *parser) doubleQuoted() ([]piece, error) {
	start := p.pos
	p.pos++
	var out field
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		switch c {
		case '"':
			p.pos++
			return out.pieces, nil
		case '\\':
			if p.pos+1 >= len(p.src) {
				return nil, p.syntaxErr(start, "unterminated double quote")
			}
			if n := lineContinuation(p.src[p.pos+1:]); n > 0 {
				p.pos += 1 + n
				continue
			}
			switch n := p.src[p.pos+1]; n {
			case '$', '`', '"', '\\':
				out.add(string(n), false)
				p.pos += 2
			default:
				out.add(`\`, true)
				p.pos++
			}
		case '{':
			if !strings.HasPrefix(p.src[p.pos:], "{{") {
				out.add("{", true)
				p.pos++
				continue
			}
			name, err := p.braceVar()
			if err != nil {
				return nil, err
			}
			v, err := p.lookup(name)
			if err != nil {
				return nil, err
			}
			out.add(v, false)
		case '$':
			exp, literal, err := p.dollar(true)
			if err != nil {
				return nil, err
			}
			if literal {
				out.add("$", true)
				continue
			}
			for _, pc := range exp.pieces {
				out.add(pc.text, pc.glob)
			}
		case '`':
			pieces, err := p.backtick()
			if err != nil {
				return nil, err
			}
			for _, pc := range pieces {
				out.add(pc.text, pc.glob)
			}
		default:
			if (c < 0x20 && c != '\t' && c != '\n' && c != '\r') || c == 0x7f {
				return nil, p.syntaxErr(p.pos, "control character 0x%02x is not allowed", c)
			}
			end := p.pos + 1
			for end < len(p.src) && !strings.ContainsRune("\"\\{$`", rune(p.src[end])) && p.src[end] >= 0x20 {
				end++
			}
			out.add(p.src[p.pos:end], true)
			p.pos = end
		}
	}
	return nil, p.syntaxErr(start, "unterminated double quote")
}

// braceVar parses {{NAME}} at p.pos.
func (p *parser) braceVar() (string, error) {
	start := p.pos
	end := strings.Index(p.src[p.pos+2:], "}}")
	if end < 0 {
		return "", p.syntaxErr(start, "unterminated {{ variable reference")
	}
	name := p.src[p.pos+2 : p.pos+2+end]
	if !validName(name) {
		return "", p.syntaxErr(start, "invalid variable reference {{%s}}; expected {{NAME}} with NAME matching [A-Za-z_][A-Za-z0-9_]*", name)
	}
	p.pos += end + 4
	return name, nil
}

type expansion struct {
	pieces []piece
	name   string // set for variable expansions
}

// dollar parses a $-construct. literal reports a lone '$' that shells keep
// verbatim (followed by whitespace, end of input, or a closing quote).
func (p *parser) dollar(quoted bool) (exp expansion, literal bool, err error) {
	start := p.pos
	rest := p.src[p.pos+1:]
	switch {
	case rest == "" || isIFS(rest[0]) || rest[0] == '\r' || (quoted && rest[0] == '"'):
		p.pos++
		return expansion{}, true, nil
	case strings.HasPrefix(rest, "(("):
		return exp, false, p.syntaxErr(start, "arithmetic expansion $((...)) is not supported")
	case strings.HasPrefix(rest, "("):
		body, err := p.substitutionBody()
		if err != nil {
			return exp, false, err
		}
		pieces, err := p.evalIdiom(body, start)
		return expansion{pieces: pieces}, false, err
	case strings.HasPrefix(rest, "{"):
		end := strings.IndexByte(rest, '}')
		if end < 0 {
			return exp, false, p.syntaxErr(start, "unterminated ${")
		}
		name := rest[1:end]
		if !validName(name) {
			return exp, false, p.syntaxErr(start, "unsupported parameter expansion ${%s}; only ${NAME} is supported", name)
		}
		p.pos += end + 2
		v, err := p.lookup(name)
		return expansion{pieces: []piece{{v, false}}, name: name}, false, err
	case isNameStart(rest[0]):
		end := 1
		for end < len(rest) && isNameChar(rest[end]) {
			end++
		}
		name := rest[:end]
		p.pos += end + 1
		v, err := p.lookup(name)
		return expansion{pieces: []piece{{v, false}}, name: name}, false, err
	}
	return exp, false, p.syntaxErr(start, "unsupported expansion $%c; only $NAME, ${NAME} and {{NAME}} are supported", rest[0])
}

func (p *parser) unquotedDollar() error {
	exp, literal, err := p.dollar(false)
	if err != nil {
		return err
	}
	if literal {
		p.cur.add("$", false)
		return nil
	}
	if exp.name != "" {
		return p.addVarValue(exp.name, exp.pieces[0].text)
	}
	return p.unquotedSplit(exp.pieces)
}

func (p *parser) unquotedVar(name string) error {
	v, err := p.lookup(name)
	if err != nil {
		return err
	}
	return p.addVarValue(name, v)
}

func (p *parser) addVarValue(name, value string) error {
	if p.assign != "" {
		p.cur.addNonEmpty(value, false)
		return nil
	}
	if p.idiom {
		if strings.ContainsAny(value, " \t\n") {
			return &ExpansionError{fmt.Sprintf("variable %s contains whitespace and is used unquoted inside a command substitution; quote it", name)}
		}
		p.cur.addNonEmpty(value, false)
		return nil
	}
	if p.opts.Strict && !p.opts.ArgListVars[name] {
		p.cur.addNonEmpty(value, false)
		return nil
	}
	return p.unquotedSplit([]piece{{value, false}})
}

// unquotedSplit applies shell field splitting (IFS = space, tab, newline) to
// the result of an unquoted expansion.
func (p *parser) unquotedSplit(pieces []piece) error {
	if p.assign != "" || p.idiom {
		for _, pc := range pieces {
			p.cur.addNonEmpty(pc.text, pc.glob && p.idiom)
		}
		return nil
	}
	for _, pc := range pieces {
		text := pc.text
		for text != "" {
			i := strings.IndexAny(text, " \t\n")
			if i < 0 {
				p.cur.add(text, pc.glob)
				break
			}
			if i > 0 {
				p.cur.add(text[:i], pc.glob)
			}
			if p.cur.present {
				p.split = append(p.split, p.cur)
				p.cur = field{}
			}
			text = text[i+1:]
		}
	}
	return nil
}

func (f *field) addNonEmpty(text string, glob bool) {
	if text != "" {
		f.add(text, glob)
	}
}

func (p *parser) lookup(name string) (string, error) {
	if v, ok := p.opts.Lookup(name); ok {
		return v, nil
	}
	if p.opts.AllowUnset {
		return "", nil
	}
	return "", &ExpansionError{fmt.Sprintf("variable %s is not set (set it in the egg/server variables, or set CTYL_ALLOW_UNSET=1 to expand unset variables to empty strings)", name)}
}

// substitutionBody returns the text between "$(" and its matching ")".
func (p *parser) substitutionBody() (string, error) {
	start := p.pos
	i := p.pos + 2
	depth := 1
	for i < len(p.src) {
		switch p.src[i] {
		case '\\':
			i += 2
			continue
		case '\'':
			end := strings.IndexByte(p.src[i+1:], '\'')
			if end < 0 {
				return "", p.syntaxErr(i, "unterminated single quote")
			}
			i += end + 2
			continue
		case '"':
			j := i + 1
			for j < len(p.src) && p.src[j] != '"' {
				if p.src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(p.src) {
				return "", p.syntaxErr(i, "unterminated double quote")
			}
			i = j + 1
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				body := p.src[p.pos+2 : i]
				p.pos = i + 1
				return body, nil
			}
		}
		i++
	}
	return "", p.syntaxErr(start, "unterminated $(")
}

// backtick parses `...` at p.pos and evaluates it as an idiom.
func (p *parser) backtick() ([]piece, error) {
	start := p.pos
	var body strings.Builder
	i := p.pos + 1
	for i < len(p.src) {
		c := p.src[i]
		if c == '\\' && i+1 < len(p.src) && strings.IndexByte("`\\$", p.src[i+1]) >= 0 {
			body.WriteByte(p.src[i+1])
			i += 2
			continue
		}
		if c == '`' {
			p.pos = i + 1
			return p.evalIdiom(body.String(), start)
		}
		body.WriteByte(c)
		i++
	}
	return nil, p.syntaxErr(start, "unterminated backtick")
}
