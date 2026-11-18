package startup

import (
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

const maxArithDepth = 32

// Operators bash accepts in $((...)) that the launcher rejects. Assignments and
// increments have side effects; the rest are simply not needed by eggs.
var (
	unsupportedArithPairs = []string{"++", "--", "**", "+=", "-=", "*=", "/=", "%="}
	unsupportedArithChars = "<>=!~&|^?:,"
)

// arithmetic evaluates $((EXPR)) at p.pos without a shell. EXPR is restricted
// to decimal integers, variables, + - * / %, unary + -, and parentheses.
//
// Unlike bash, variable values are never evaluated as expressions: a value
// must itself be a decimal integer. This is what makes bash arithmetic
// injectable (X='a[$(id)]' runs id in $((X))) and it cannot happen here.
// Overflow and division by zero are errors instead of wrapping or crashing.
func (p *parser) arithmetic() (string, error) {
	start := p.pos
	p.pos += 3
	p.arithDepth = 0
	v, err := p.arithSum(start)
	if err != nil {
		return "", err
	}
	p.arithSkipSpace()
	if !strings.HasPrefix(p.src[p.pos:], "))") {
		return "", p.arithUnexpected(start, "an operator or '))'")
	}
	p.pos += 2
	return v.String(), nil
}

func (p *parser) arithSum(start int) (*big.Int, error) {
	x, err := p.arithProduct(start)
	for err == nil {
		var op byte
		if op, err = p.arithOperator("+-"); err != nil || op == 0 {
			break
		}
		var y *big.Int
		if y, err = p.arithProduct(start); err == nil {
			if op == '+' {
				x.Add(x, y)
			} else {
				x.Sub(x, y)
			}
			err = arithFits(x)
		}
	}
	return x, err
}

func (p *parser) arithProduct(start int) (*big.Int, error) {
	x, err := p.arithUnary(start)
	for err == nil {
		var op byte
		if op, err = p.arithOperator("*/%"); err != nil || op == 0 {
			break
		}
		var y *big.Int
		if y, err = p.arithUnary(start); err == nil {
			err = arithApply(op, x, y)
		}
	}
	return x, err
}

func arithApply(op byte, x, y *big.Int) error {
	switch {
	case op == '*':
		x.Mul(x, y)
	case y.Sign() == 0:
		return &ExpansionError{"arithmetic expansion: division by zero"}
	case op == '/':
		x.Quo(x, y)
	default:
		x.Rem(x, y)
	}
	return arithFits(x)
}

func (p *parser) arithUnary(start int) (*big.Int, error) {
	if p.arithDepth >= maxArithDepth {
		return nil, p.syntaxErr(p.pos, "arithmetic expansion nested too deeply")
	}
	op, err := p.arithOperator("+-")
	if err != nil {
		return nil, err
	}
	if op == 0 {
		return p.arithOperand(start)
	}
	p.arithDepth++
	x, err := p.arithUnary(start)
	p.arithDepth--
	if err != nil || op == '+' {
		return x, err
	}
	return x.Neg(x), arithFits(x)
}

func (p *parser) arithOperand(start int) (*big.Int, error) {
	p.arithSkipSpace()
	rest := p.src[p.pos:]
	switch {
	case rest == "":
		return nil, p.syntaxErr(start, "unterminated $((")
	case rest[0] >= '0' && rest[0] <= '9':
		return p.arithLiteral()
	case rest[0] == '(':
		p.pos++
		p.arithDepth++
		x, err := p.arithSum(start)
		p.arithDepth--
		if err != nil {
			return nil, err
		}
		p.arithSkipSpace()
		if !strings.HasPrefix(p.src[p.pos:], ")") {
			return nil, p.arithUnexpected(start, "an operator or ')'")
		}
		p.pos++
		return x, nil
	case strings.HasPrefix(rest, "{{"):
		name, err := p.braceVar()
		if err != nil {
			return nil, err
		}
		return p.arithVariable(name)
	case strings.HasPrefix(rest, "$("):
		return nil, p.syntaxErr(p.pos, "command substitution and nested $((...)) are not supported inside $((...))")
	case rest[0] == '$':
		exp, literal, err := p.dollar(false)
		if err != nil {
			return nil, err
		}
		if literal {
			return nil, p.arithUnexpected(start, "a variable name after '$'")
		}
		return arithValue(exp.name, exp.pieces[0].text)
	case isNameStart(rest[0]):
		end := 1
		for end < len(rest) && isNameChar(rest[end]) {
			end++
		}
		p.pos += end
		return p.arithVariable(rest[:end])
	}
	return nil, p.arithUnexpected(start, "a number, variable or '('")
}

func (p *parser) arithLiteral() (*big.Int, error) {
	start := p.pos
	end := p.pos
	for end < len(p.src) && (isNameChar(p.src[end]) || p.src[end] == '#') {
		end++
	}
	text := p.src[start:end]
	if !isDecimal(text) {
		return nil, p.syntaxErr(start, "arithmetic expansion: %q is not a decimal integer (octal, hex and base# numbers are not supported)", text)
	}
	p.pos = end
	x, _ := new(big.Int).SetString(text, 10)
	if !x.IsInt64() {
		return nil, p.syntaxErr(start, "arithmetic expansion: %s does not fit in a 64-bit integer", text)
	}
	return x, nil
}

func (p *parser) arithVariable(name string) (*big.Int, error) {
	v, err := p.lookup(name)
	if err != nil {
		return nil, err
	}
	return arithValue(name, v)
}

func arithValue(name, value string) (*big.Int, error) {
	if !isDecimal(strings.TrimPrefix(value, "-")) {
		return nil, &ExpansionError{fmt.Sprintf("variable %s is %q, but $((...)) needs a decimal integer (digits with an optional leading '-', no leading zeros)", name, value)}
	}
	x, _ := new(big.Int).SetString(value, 10)
	if !x.IsInt64() {
		return nil, &ExpansionError{fmt.Sprintf("variable %s (%s) does not fit in a 64-bit integer", name, value)}
	}
	return x, nil
}

func isDecimal(s string) bool {
	if s == "" || (s[0] == '0' && len(s) > 1) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func arithFits(x *big.Int) error {
	if x.IsInt64() {
		return nil
	}
	return &ExpansionError{"arithmetic expansion: result does not fit in a 64-bit integer"}
}

// arithOperator consumes the next operator if it is one of allowed and
// returns 0 if the next token is not an operator.
func (p *parser) arithOperator(allowed string) (byte, error) {
	p.arithSkipSpace()
	rest := p.src[p.pos:]
	for _, op := range unsupportedArithPairs {
		if strings.HasPrefix(rest, op) {
			return 0, p.arithUnsupportedOperator(op)
		}
	}
	if rest == "" {
		return 0, nil
	}
	if strings.IndexByte(unsupportedArithChars, rest[0]) >= 0 {
		return 0, p.arithUnsupportedOperator(rest[:1])
	}
	if strings.IndexByte(allowed, rest[0]) >= 0 {
		p.pos++
		return rest[0], nil
	}
	return 0, nil
}

func (p *parser) arithUnsupportedOperator(op string) error {
	return p.syntaxErr(p.pos, "arithmetic operator %q is not supported; only + - * / %% and parentheses are", op)
}

func (p *parser) arithUnexpected(start int, want string) error {
	if p.pos >= len(p.src) {
		return p.syntaxErr(start, "unterminated $((")
	}
	r, _ := utf8.DecodeRuneInString(p.src[p.pos:])
	return p.syntaxErr(p.pos, "arithmetic expansion: unexpected %q, expected %s", r, want)
}

func (p *parser) arithSkipSpace() {
	for p.pos < len(p.src) && (isIFS(p.src[p.pos]) || p.src[p.pos] == '\r') {
		p.pos++
	}
}
