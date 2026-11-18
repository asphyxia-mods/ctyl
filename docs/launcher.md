# `ctyl-launch`: startup semantics

`ctyl-launch` is the image entrypoint (`/usr/bin/ctyl-launch`). It is a
statically linked Go program with no dependencies outside the Go standard
library. It turns the panel's startup template into an argument vector and
replaces itself with the server process through `execve(2)`.

There is **no shell** in the image and the launcher never invokes one. It
does not fork and does not supervise: after `execve` the JVM is PID 1, so
signals, stdin, the TTY and the exit status go directly to and from the
server, exactly as they did with yolks' `exec env ${PARSED}`.

The behaviour is pinned by `internal/startup/startup_test.go` (table tests
and a fuzz target) and `internal/launch/*_test.go`, which execute the real
binary.

## What the panel provides

Both Pterodactyl and Calagopus pass the egg's startup command **unexpanded**
in `STARTUP` (`'invocation' => $server->startup` in Pterodactyl's
`ServerConfigurationStructureService`, `invocation: self.startup` in
Calagopus). Variable values arrive as separate environment variables. The
launcher can therefore tokenize the trusted template first and substitute
values afterwards. A value can never introduce quotes, operators, command
substitutions or glob patterns.

## Modes

| Invocation | Behaviour |
|---|---|
| `ctyl-launch` with `STARTUP` set | Parse `STARTUP` as described below and exec it. |
| `ctyl-launch CMD ARGS...` (e.g. `docker run IMAGE java -version`) | Exec `CMD ARGS...` verbatim. `STARTUP` is ignored. |
| `ctyl-launch --print-plan [CMD...]` | Print the resolved path, argv and environment as JSON and exit. Use this to check an egg. |
| `ctyl-launch --version` | Print the launcher version. |

## Template language

The language is a deliberately small subset of POSIX shell word syntax.
Anything outside it is **rejected** with a byte offset and an explanation,
never approximated.

### Words, quoting, escapes

| Syntax | Meaning |
|---|---|
| space, tab, newline, CR | separate words |
| `'text'` | literal; nothing is expanded. `{{X}}` inside single quotes is an error, because it is almost certainly a mistake. |
| `"text"` | one word; `{{X}}`, `${X}`, `$X` and the idioms below are expanded without field splitting. `\"` `\\` `\$` `` \` `` and backslash-newline are escapes; any other backslash is literal. |
| `\c` (unquoted) | the literal character `c`; backslash-newline is a line continuation |
| `""` or `''` | an empty argument |

### Variables

`{{NAME}}`, `${NAME}` and `$NAME` are equivalent. `NAME` must match
`[A-Za-z_][A-Za-z0-9_]*`.

* **Unset variables are an error** (exit 2), because a typo would otherwise
  silently become an empty argument. Set `CTYL_ALLOW_UNSET=1` to expand them
  to the empty string instead.
* **Inside double quotes**, a variable is part of exactly one argument.
* **Unquoted**, a variable's value is split on spaces, tabs and newlines into
  separate arguments (shell field splitting). This keeps eggs such as
  `java {{JVM_FLAGS}} -jar {{SERVER_JARFILE}}` working. Splitting only ever
  produces arguments: quotes, `;`, `|`, `$(...)` and `*` inside a value stay
  literal characters. An unquoted variable that is empty produces no
  argument.
* To keep a value with spaces as one argument, quote it:
  `-jar "{{SERVER_JARFILE}}"`.
* **Strict mode** (`CTYL_STRICT_VARIABLES=1`): unquoted variables are never
  split, except those listed in `CTYL_ARGLIST_VARIABLES` (comma separated,
  e.g. `JVM_FLAGS,EXTRA_ARGS`). This makes "one variable = one argument" the
  rule and argument lists explicit.

Unsupported expansions are errors: `${X:-default}`, `${#X}`, `$1`, `$@`, `$?`,
and `{{a b}}` or `{{server.x}}`.

### Arithmetic

`$((EXPR))` is evaluated by the launcher, so eggs can size the heap from the
panel's memory limit:

```
java -Xms$(({{SERVER_MEMORY}}*80/100))M -Xmx$(({{SERVER_MEMORY}}*80/100))M -jar {{SERVER_JARFILE}}
```

`EXPR` may contain only:

* decimal integer literals;
* variables, written `{{NAME}}`, `${NAME}`, `$NAME` or bare `NAME`;
* binary `+ - * / %` and unary `+ -`, with the usual precedence;
* parentheses and whitespace.

The rules differ from bash where bash would be unsafe or surprising:

* **Values are never evaluated as expressions.** A variable must hold a
  decimal integer (`-?[0-9]+`, no leading zeros), otherwise the template is
  rejected. In bash, `$((X))` with `X='a[$(id)]'` runs `id`, and `X='2+3'`
  is evaluated as an expression; here both are errors.
* Unset variables are an error, as everywhere else. With
  `CTYL_ALLOW_UNSET=1` they become empty strings, which are still not
  integers, so the template is still rejected.
* Literals with a leading zero (`010`, octal in bash), `0x`, and `base#n`
  are rejected rather than read differently from bash.
* Assignments (`=`, `+=`…), `++`, `--`, `**`, comparisons, bitwise and
  logical operators, `?:`, `,`, array subscripts, quotes, `$(...)` and
  nested `$((...))` are rejected.
* Arithmetic uses 64-bit integers. Division and remainder truncate toward
  zero, as in bash. Overflow and division by zero are errors instead of
  wrapping or crashing.
* Parentheses and unary operators can be nested at most 32 deep.

The result is a decimal integer. It is never split or globbed, so it can be
used anywhere in a word, in double quotes or in an assignment value.

On Pterodactyl and Calagopus, `SERVER_MEMORY=0` means *unlimited*. The
example above then yields `-Xmx0M` and the JVM refuses to start, exactly as
it would under a shell. `-XX:MaxRAMPercentage=80` reads the container's
memory limit instead and also works when memory is unlimited.

### Leading assignments

Leading `NAME=value` words set environment variables for the server process,
as with `env(1)`: `MALLOC_ARENA_MAX=2 java -jar server.jar`. Assignment values
are never split. `NAME=value` after the command is an ordinary argument.

### Globs

`*`, `?` and `[...]` in **unquoted template text** match file names relative
to the working directory, following bash rules: `*` does not match a leading
`.`, and results are sorted. Two differences from bash are deliberate:

* exactly **one** match: replaced by that path;
* **no** match: kept literally, with a warning (bash behaviour, which is
  what `java -cp libs/*` relies on);
* **more than one** match: an error. `java -jar forge-*.jar` with both the
  server jar and the installer jar present would otherwise silently run the
  installer (it sorts first).

Values of variables and the output of `cat` are never treated as patterns.

### Compatibility idioms (no shell is run)

Common Forge, NeoForge and Technic eggs embed small shell programs. The
launcher recognizes these exact, side-effect-free forms and evaluates them
itself:

```
$( [[ [!] -f|-e|-d|-s PATH ]] && printf %s "TEXT" [ || printf %s "TEXT" ] )
$( [ [!] -f|-e|-d|-s PATH ] && printf %s "TEXT" [ || printf %s "TEXT" ] )
$(cat PATH)        `cat PATH`
```

* File tests are `stat(2)` calls relative to the working directory.
* `TEXT` is expanded like a double-quoted string. The result is then field
  split and globbed like the output of an unquoted shell command
  substitution, so `printf %s "-jar {{SERVER_JARFILE}}"` yields two arguments.
* `cat PATH` reads a regular file **inside the working directory** (no
  absolute paths, no `..`), at most 64 KiB, without NUL bytes. Trailing
  newlines are removed and the content is split on whitespace when unquoted.
  It is never globbed or re-parsed.
* Nesting depth is limited to 2. All referenced variables must be set, even
  in a branch that is not taken.
* Any other command substitution or backtick content is an error.

These cover the official Forge egg
(`$( [[ ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" || printf %s "@unix_args.txt" )`),
the user-JVM-args variant (`$([[ -f user_jvm_args.txt ]] && printf %s "@user_jvm_args.txt")`)
and `` -jar `cat .serverjar` ``.

### Always rejected

| Input | Error |
|---|---|
| `;` `&&` `\|\|` `\|` `&` | command lists, pipelines and background jobs need a shell |
| `>` `>>` `<` `2>&1` | redirections need a shell |
| `(` `)` | subshells |
| `{a,b}` and a stray `{` or `}` | brace expansion |
| `~/x` at the start of a word | tilde expansion (use `{{HOME}}`) |
| `#` at the start of a word | comments |
| control characters other than tab, CR and LF; NUL; invalid UTF-8 | |
| unterminated quotes, `$(` or `{{` | |

Limits: a 64 KiB template, 4096 arguments, and 128 KiB per argument (the
Linux `MAX_ARG_STRLEN`).

## Command resolution

A command containing `/` is used as given, relative to the working
directory. Otherwise it is looked up in `PATH`, and **relative `PATH` entries
are ignored**, so files in the writable server directory cannot shadow
`java`. The image's `PATH` is `/usr/bin:/bin`. Only programs shipped in the
image or placed in the server directory can run.

## Environment

The server process receives the container environment, the template's
leading assignments, and:

* `TZ=UTC` if `TZ` is unset or empty (as yolks did);
* `INTERNAL_IP`, the container's outbound source address, if unset. Yolks
  computed this with `ip route`; the launcher connects a UDP socket, which
  sends no packets.

`HOME=/home/container`, `LANG=C.UTF-8`, `JAVA_HOME=/usr/lib/jvm/default-jvm`
and `PATH` are image defaults. Nothing else is injected. In particular the
launcher never adds or rewrites JVM options.

## Identity checks

The launcher refuses to run as UID 0 in the initial user namespace (exit 3).
Root inside a user namespace is allowed: rootless Docker, `userns-remap`, and
the Wings rootless mode that uses container UID 0. Set `CTYL_ALLOW_ROOT=1` to
override.

If the UID has no `/etc/passwd` entry, the launcher prints a note. Java 17
and older then report `user.name` and `user.home` as `?`; Java 19 and newer
fall back to `$HOME`. The image's user is `container` (988:988), which is
also Wings' default UID.

## Output

Unless `CTYL_QUIET=1`, the launcher prints three lines before exec:

```
[ctyl] image java_21 (wolfi)
[ctyl] java 21.0.13.1+-wolfi-r0 (wolfi openjdk-21-21.0.13.1-r0, Linux/x86_64)
[ctyl] exec java -Xms128M -XX:MaxRAMPercentage=95.0 -jar server.jar
```

The `exec` line is shell-quoted, so it shows argument boundaries exactly.
Warnings and errors go to stderr, prefixed `ctyl-launch:`.

## Exit codes

| Code | Meaning |
|---|---|
| exit status of the server | after a successful exec |
| 2 | startup template rejected or not expandable |
| 3 | refused to run as root |
| 126 | command found but not executable |
| 127 | command not found |

## Options summary

| Variable | Effect |
|---|---|
| `STARTUP` | startup template (set by the panel) |
| `CTYL_STRICT_VARIABLES=1` | do not field-split unquoted variables… |
| `CTYL_ARGLIST_VARIABLES=A,B` | …except these |
| `CTYL_ALLOW_UNSET=1` | unset variables expand to "" |
| `CTYL_ALLOW_ROOT=1` | allow real root |
| `CTYL_QUIET=1` | no banner |
