# Deployment and update lifecycle

## Using the images in an egg

Point the egg's image list at the tags (both panels use the egg's
`docker_images`):

```json
"docker_images": {
  "Java 25": "ghcr.io/asphyxia-mods/ctyl:java_25",
  "Java 21": "ghcr.io/asphyxia-mods/ctyl:java_21",
  "Java 17": "ghcr.io/asphyxia-mods/ctyl:java_17",
  "Java 11": "ghcr.io/asphyxia-mods/ctyl:java_11",
  "Java 8":  "ghcr.io/asphyxia-mods/ctyl:java_8"
}
```

No other egg change is needed for the startup patterns in
`compatibility.md`. Install scripts keep running in the egg's installer image
(e.g. `ghcr.io/pelican-eggs/installers:debian`): they need `curl` and a
shell, and this runtime deliberately has neither.

Two reference styles:

* **Mutable tag** (`:java_21`): updates arrive on the next server start.
  Simple, but the running digest is only visible on the node.
* **Pinned digest** (`:java_21@sha256:…`): exact, auditable and rollback
  friendly, but each release requires updating the egg's image list.
  `ctyl-deploy` switches servers between pinned entries that the egg offers.

## The three stages, measured separately

| Stage | Where it's recorded |
|---|---|
| 1. Upstream patch available | `upstream_latest_ga_date` (OpenJDK GA tag time) in `release-state/state/java_N.json`; Wolfi package build time in the lock |
| 2. Verified image published | `published_at` and `latency_hours` in the state file and `state/latency.jsonl` |
| 3. Running servers updated | `ctyl-deploy rollout --report results.json` (`finished_at`, deployed digest) |

## How the daemons pick up images (verified in their source)

* **Pterodactyl Wings** removes and recreates the container on **every
  start** (`OnBeforeStart`) and pulls the image first. If the pull fails but
  a local image with that tag exists, it silently uses the stale local image
  (`ensureImageExists`). An image name prefixed with `~` is never pulled.
* **Calagopus Wings** also recreates on start, but caches pulls: the same
  image is not re-pulled within `docker.registry_image_fetch_cache.duration`
  (default 300 s). A trailing `~` disables pulling.
* Neither restarts a running server when a tag moves.

So publishing does **not** update running servers. A restart within five
minutes on Calagopus, or a failed pull on either daemon, can keep the old
image. `ctyl-deploy` pre-pulls the verified digest on the node and checks the
digest of the recreated container, which closes both gaps.

## `ctyl-deploy`

Build it with `go build ./cmd/ctyl-deploy`. The tool uses only the panels'
**client APIs** (`/api/client`), with an API key of a user who has access to
the servers. Keep the key in a file or in `CTYL_PANEL_TOKEN`, never on the
command line.

```sh
STATE=https://raw.githubusercontent.com/asphyxia-mods/ctyl/release-state/state

# On a node: which containers run outdated digests, and why it matters
ctyl-deploy status   --repo ghcr.io/asphyxia-mods/ctyl --state-url $STATE
# On a node: pull and tag the verified digests ahead of a maintenance window
ctyl-deploy prefetch --repo ghcr.io/asphyxia-mods/ctyl --state-url $STATE \
    --verify-identity '^https://github.com/asphyxia-mods/ctyl/.github/workflows/'

# Roll out (asks for confirmation; one server at a time; stops at the first failure)
ctyl-deploy rollout --panel pterodactyl --url https://panel.example.com \
    --token-file /etc/ctyl/panel.key --repo ghcr.io/asphyxia-mods/ctyl --state-url $STATE \
    --node --backup --window "Sat,Sun 04:00-06:00" --timezone Europe/Berlin
```

For every server that uses an image from the repository, `rollout` does the
following:

1. **Plan.** Compare the running digest (pinned reference, or the container
   on the node with `--node`) with the tag's current verified digest.
   Classify the difference: `java-security` (the target ships a different
   OpenJDK version than the running image, by each image's recorded
   `runtime_version`), `security` (the running digest was recorded as not
   verified, is unknown, or has no recorded Java version), `maintenance`
   (packages or launcher), or `current`.
   `--only-security` skips maintenance updates. With `--node`, servers whose
   container is not on this node are listed as `other-node` and left alone;
   run the rollout on each node for its own servers.
2. **Refuse** if the target's recorded status is not `verified` and promoted
   (override: `--allow-unverified` is deliberately not exposed on the CLI),
   if the release state disagrees with the registry, or, with
   `--verify-identity`, if `cosign verify` fails.
3. **Pre-pull** `repo@digest` on the node and point the tag at it (`--node`).
4. **Back up** through the panel (`--backup`, Pterodactyl). For Calagopus,
   use the panel's backup schedules.
5. **Recheck the window** (`--window`): if pulling or backing up ran past
   its end, skip the server before any downtime.
6. **Stop gracefully** with the panel's stop action, which runs the egg's
   stop command (`stop` for Minecraft), and wait for `offline`. If the server
   does not stop within `--stop-timeout`, the rollout aborts **without
   killing it**. A forced kill during a save is how worlds get corrupted.
7. Switch the image if the server is pinned, then **start**, and wait for
   `running`. Both daemons set `running` only once the egg's "done" line has
   appeared.
8. **Verify** that the new container's digest is the target (`--node`).
9. On failure, **roll back**: stop gracefully, restore the previous image
   (pinned mode), or switch to `--rollback-image` (tag mode). Then start and
   wait. Without a rollback image the server is stopped gracefully (even if
   it started and only the digest check failed) and left stopped for
   inspection, and the rollout halts.

The default is interactive approval. Unattended security deployments are an
explicit choice:

```cron
# Security updates only, inside the window, no prompt
*/30 * * * * ctyl-deploy rollout --yes --only-security --window "04:00-06:00" ... >> /var/log/ctyl-deploy.log 2>&1
```

What is verified: the planning, rollback, never-kill and window logic against
a simulated panel (`internal/deploy/rollout_test.go`). The HTTP contracts of
both panels are tested against fake servers built from their source code and
documentation. Node discovery, pre-pull and digest verification were run
against a real Docker daemon and a local registry. **Not verified:** a
rollout against a live Pterodactyl or Calagopus installation. Run
`ctyl-deploy status` and a single-server `rollout --servers NAME` against a
staging server before scheduling anything.

## Hardening settings

What both daemons already apply to server containers (from their source):
read-only rootfs; `/tmp` as `tmpfs rw,exec,nosuid,size=<tmpfs_size>`;
`no-new-privileges`; dropped `setpcap mknod audit_write net_raw dac_override
fowner fsetid net_bind_service sys_chroot setfcap`, plus `sys_ptrace` on
Calagopus; a PID limit (Wings default 512); memory limits with overhead;
TTY with stdin; user = the daemon's system user (default 988:988).

The images also work with the stricter settings below, all covered by
`tests/imagetest`:

| Setting | Wings (`config.yml`) | Calagopus (`config.yml`) | plain Docker |
|---|---|---|---|
| user | `system.user` (default `pterodactyl`, 988) | `system.user` | `--user 988:988` (image default) |
| rootless | `system.user.rootless` (container UID 0 in a user namespace is accepted) | same | rootless Docker |
| generated `/etc/passwd` | `system.passwd.enabled: true` (gives other UIDs a passwd entry) | — | — |
| capabilities | fixed list above | fixed list above | `--cap-drop ALL` (works) |
| seccomp | Docker default | `docker.container_apply_seccomp` + per-server removals | default, or stricter |
| AppArmor/SELinux | Docker default profile | `docker.container_apparmor_profile` | `--security-opt apparmor=…` |
| /tmp | `docker.tmpfs_size` (MB) | same | `--tmpfs /tmp:rw,noexec,nosuid,nodev,size=256m` (Java works with `noexec`; some native-library mods extract to /tmp and need `exec`) |
| PIDs | `docker.container_pid_limit` | per-server limits | `--pids-limit 256` |
| network | `docker.network` (bridge `pterodactyl0`) | same | publish only the game port |
| userns | `docker.userns_mode` | `docker.userns_mode` | `--userns-remap` on the daemon |

A Docker example matching what the tests run:

```sh
docker run -d --name mc --read-only --tmpfs /tmp:rw,exec,nosuid,size=256m \
  --cap-drop ALL --security-opt no-new-privileges --pids-limit 512 \
  --memory 4g -p 25565:25565 -v /srv/mc:/home/container -it \
  -e 'STARTUP=java -Xms128M -XX:MaxRAMPercentage=95.0 -jar {{SERVER_JARFILE}}' \
  -e SERVER_JARFILE=server.jar ghcr.io/asphyxia-mods/ctyl:java_21@sha256:…
docker stop -t 120 mc   # SIGTERM: the JVM shutdown hook saves the world (tested)
```

Optional egress restriction: Minecraft needs outbound HTTPS to
`sessionserver.mojang.com` and `api.minecraftservices.com` for online-mode
authentication and profiles, plus whatever plugins integrate with. Calagopus
Wings has a per-server firewall. On Wings, use host `iptables`/`nftables` on
`pterodactyl0`. Blocking all egress breaks online mode.

## Operational responsibilities that remain

* Host OS, kernel and microcode updates (container escapes are kernel bugs).
* Docker/containerd/runc updates and daemon configuration (seccomp,
  AppArmor/SELinux, userns-remap, log limits).
* Wings / Calagopus Wings and the panel itself: updates, API key hygiene, TLS.
* Server software, plugins and mods. The image never updates them.
* Restarting servers to apply image updates: `ctyl-deploy` or the panel.
* Backups. A graceful stop is not a backup.
