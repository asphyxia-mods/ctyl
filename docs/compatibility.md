# Compatibility

## Egg startup patterns

The patterns below were collected from the startup commands of all Java eggs
in `pelican-eggs/minecraft` and the eggs bundled with Pterodactyl Panel
(109 egg files, 32 distinct Java startup strings).

| Pattern (example) | Supported | Notes |
|---|---|---|
| `java -Xms128M -XX:MaxRAMPercentage=95.0 -Dterminal.jline=false -Dterminal.ansi=true -jar {{SERVER_JARFILE}}` (Paper, Vanilla, Purpur…) | yes | |
| `java -Xms128M -Xmx{{SERVER_MEMORY}}M -jar {{SERVER_JARFILE}}` | yes | variable inside a word |
| `… --add-modules=jdk.incubator.vector -jar …` | yes | tested with Paper 1.21.11 |
| `… -jar forge-*.jar`, `FTBserver-*.jar` (Technic, FTB) | yes | exactly one match required; zero matches stay literal |
| `$( [[ ! -f unix_args.txt ]] && printf %s "-jar {{SERVER_JARFILE}}" \|\| printf %s "@unix_args.txt" )` (Forge) | yes | evaluated without a shell |
| `java $([[ -f user_jvm_args.txt ]] && printf %s "@user_jvm_args.txt") … $([[ ! -f unix_args.txt ]] && printf %s "-jar \`cat .serverjar\`" \|\| …)` (Forge Enhanced) | yes | nested `` `cat` `` inside `printf` |
| `java … @unix_args.txt` (NeoForge) | yes | the egg symlinks `unix_args.txt` |
| `java -javaagent:log4jfix/Log4jPatcher-1.0.0.jar …` | yes | agents tested |
| `java --add-opens … -p modules -m gomint.server/…` | yes | plain arguments |
| startup strings that are shell programs: `;`, `&&` lists, pipes, redirects, `cd x && java`, `if`… | **no** | rejected with an explanation. None of the surveyed Minecraft Java eggs use them. |
| eggs that start non-Java programs (e.g. `./bedrock_server`) | not the target | a binary in the server directory can be exec'd, but the image only ships Java's runtime libraries |

## Real server matrix

Run on 2026-11-18 (amd64, 4 vCPU, 15 GB RAM; servers sequential), using the
Wings container settings for the first boot and the Calagopus settings for
the restarts:

* **Boot 1:** start, wait for the egg's done marker (`)! For help, type `),
  `say` and `list` through the console, `tick query`, then the egg's `stop`.
  Checks: exit 0, save logged, `world/level.dat` present, exactly one
  process, only port 25565 listening.
* **Boot 2:** restart on the same data, create a scoreboard objective, stop
  with **SIGTERM**, then check the objective was written to the world files.
* **Boot 3:** boot again (world loads after SIGTERM) and `stop`.

| Java | Server | First ready (s) | Restart ready (s) | RSS (MB) | Avg tick (idle) | Save logged on SIGTERM | World saved on SIGTERM |
|---|---|---|---|---|---|---|---|
| 8 | Forge 1.12.2 (14.23.5.2859), `forge-*.jar` glob | 11.0 | 6.9 | 976 | n/a | no | yes |
| 8 | Vanilla 1.16.5 | 22.7 | 13.6 | 702 | n/a | no | yes |
| 11 | Vanilla 1.16.5 | 24.6 | 13.6 | 730 | n/a | no | yes |
| 17 | Paper 1.20.4 | 19.7 | 7.1 | 658 | 1.5 ms | yes | yes |
| 17 | Forge 1.20.1 (47.4.28), `unix_args.txt` idiom | 33.2 | 14.7 | 1905 | n/a | yes | yes |
| 21 | Paper 1.21.11 + `--add-modules=jdk.incubator.vector` | 21.2 | 8.8 | 1303 | 0.2 ms | yes | yes |
| 21 | Fabric 1.21.11 + Fabric API, Lithium, spark | 20.0 | 9.0 | 731 | 0.2 ms | no | yes |
| 21 | NeoForge 21.1.267 (1.21.1), `@unix_args.txt` | 15.5 | 10.9 | 745 | 0.3 ms | no | yes |
| 25 | Vanilla 26.3 | 9.0 | 6.6 | 624 | 0.2 ms | yes | yes |
| 25 | Paper 26.3 | 14.9 | 7.9 | 993 | 0.2 ms | yes | yes |
| 25 | Fabric 26.3 + Fabric API | 14.2 | 10.1 | 694 | 0.2 ms | yes | yes |
| 27 | Vanilla 26.3 | 9.9 | 7.0 | 1399 | 0.1 ms | yes | yes |
| 27 | Paper 26.3 | 15.8 | 8.2 | 1325 | 0.2 ms | yes | yes |

"First ready" includes world generation. RSS depends mostly on heap sizing
(`MaxRAMPercentage=95` of a 1.5–3 GB limit) and is not a footprint
comparison. Tick times come from an empty server, so this is a smoke-level
measurement, not a benchmark. Installers (Forge, NeoForge) ran inside the
same images, as an install container would. Downloads were verified against
Mojang sha1, PaperMC sha256, Modrinth sha512 and Maven checksums.

The servers ran as UID 1000, which has no passwd entry in the image, under
the daemons' read-only/tmpfs/capability settings. The runtime probe
(`tests/imagetest`) additionally covers the image's default user, UID 4242,
`--cap-drop ALL`, `--network none`, noexec `/tmp` and a 128-process limit.

## Known limitations and behaviours

* **SIGTERM saves, but some servers don't log it.** On SIGTERM (what
  `docker stop` and host shutdowns send), the JVM's shutdown hook saves the
  world on every tested server (proven via the scoreboard check). Vanilla
  ≤ 1.16, Fabric 1.21.11 and NeoForge 1.21.1 lose the log lines because
  log4j shuts down in parallel. Panels stop servers with the egg's `stop`
  command anyway, and `ctyl-deploy` does too.
* **Arbitrary UIDs on Java ≤ 17.** Without a passwd entry, Java 17 and older
  report `user.home=?` and `user.name=?`. Java 19+ uses `$HOME`. The image
  user is 988, which is Wings' default. For other UIDs, enable Wings'
  `system.passwd`. The launcher prints a note.
* **No `jdk.attach` / `jdk.compiler`** (JRE). Runtime self-attach and
  in-process compilation are unavailable (`java-distributions.md`).
* **Native-library mods on `noexec` /tmp.** Wings and Calagopus mount `/tmp`
  with `exec`. If you harden it to `noexec`, mods that extract `.so` files to
  /tmp (spark's async-profiler, some performance mods) need
  `-Djava.io.tmpdir=/home/container/.tmp`.
* **No shell for egg-side debugging.** `docker exec … sh` does not work. Use
  the panel console, `ctyl-launch --print-plan`, or `docker cp`.
* **Java 8 fonts.** Wolfi's `openjdk-8-jre` pulls in no fontconfig and no
  font, so headless text rendering failed with an NPE. `java_8` therefore
  adds `libfontconfig1` and `ttf-dejavu` (`extra_packages` in
  `policy/java-lines.yaml`).
* **arm64:** Java 21 was built for arm64, passed hygiene and the gate, and
  passed the runtime probe, signal, console and no-shell tests under qemu
  user emulation. The Minecraft matrix and native arm64 runs are configured
  in CI (`ubuntu-24.04-arm`) but were not run here.
