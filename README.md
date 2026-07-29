# mos-motd

A message-of-the-day generator for [MOS](https://github.com/mos-nas/mos-releases), ported from
[dkaser/unraid-motd](https://github.com/dkaser/unraid-motd) (itself forked from
[cosandr/go-motd](https://github.com/cosandr/go-motd)).

Shows a compact system summary (OS/kernel/uptime/CPU/RAM, CPU and GPU temps, Docker container and
VM status, system + user drive usage, service status, network interfaces, SnapRAID parity sync
status, and last login) whenever you open an interactive shell on the box.

## MOS-specific additions

Modules added beyond the original unraid-motd feature set, since they're particularly relevant to a
homelab NAS:

- **SnapRAID** (`snapraid`): reports how long since the last parity sync (warns after 2 days,
  critical after 7 by default -- configurable via `warn_hours`/`crit_hours`). MOS doesn't use a
  classic `snapraid.conf` at all -- it has its own `mos-snapraid` wrapper around `snapraid` that
  keeps content files directly under `/var/snapraid/<pool>/parity<N>/.snapraid.content`. So this
  first tries a classic config (`config_path`, defaulting to `/etc/snapraid.conf`, then
  `/boot/config/snapraid.conf` and `/etc/snapraid/snapraid.conf`, for anyone running vanilla
  upstream snapraid), and if none of those exist, falls back to the newest `.snapraid.content` file
  found under `content_root` (defaults to `/var/snapraid`, override if your setup keeps them
  somewhere else) -- doesn't depend on your pool/array name either way. `check_errors` (default
  `true`) controls whether a best-effort `snapraid status` also runs to check for reported errors on
  top of the age-based status; set to `false` to skip it (e.g. on a very large array where it's
  slow). Shows "Unavailable" if `snapraid` isn't installed or no config/content file can be found.
- **GPU** (`gpu`): a colored "GPU Temp" box with the same `warn`/`crit` thresholds as CPU Temp
  (default 70/90), plus a best-effort utilization percentage shown alongside the temp (uncolored --
  only temperature drives the warn/crit coloring). A plain, uncolored `GPU` summary line is also
  added directly into the top sysinfo block, right under `CPU` (before `Load`/`RAM`), mirroring how
  CPU has both a plain usage line up top and a separately-colored per-core temp box further down --
  it's only added when a GPU is actually detected, unlike the always-present rows around it.
  Detection tries `nvidia-smi` first (proprietary NVIDIA driver, common for hardware transcoding,
  gives both temp and utilization together, and already reports every card it finds -- multiple
  NVIDIA GPUs each get their own `GPUn` entry with no extra config needed). If `nvidia-smi` isn't
  found, falls back to hwmon sensors from open-source drivers (`amdgpu`, `radeon`, `nouveau`) for
  temperature, pairing in a utilization reading from amdgpu's `gpu_busy_percent` sysfs attribute when
  there's exactly one GPU (no equivalent exists for radeon/nouveau). Multiple AMD/open-source GPUs
  aren't correlated card-by-card in the hwmon fallback -- doing that reliably needs real multi-card
  hwmon sensor-key data to verify against, which isn't available yet; each card's temp sensor still
  shows, just not guaranteed matched to the right utilization reading if there's more than one. Intel
  integrated GPUs aren't covered at all -- there's no reliably standard thermal sensor path for
  `i915` across kernel versions.
- **CPU Temp** (`cpu`): on multi-socket boards, handles a coretemp quirk where core sensor numbering
  restarts at 0 per physical package/socket, and (on at least one real box tested) core IDs aren't
  even contiguous within a socket (e.g. `0-4, 8-12`, skipping fused-off core slots) -- and gopsutil
  can report a handful of core readings again before the socket's own `coretemp_package_id_N` marker
  shows up. Readings are tracked per (package, core) pair rather than by core number alone, so a
  second socket's cores never overwrite the first's, and a re-reported core collapses back into one
  row instead of showing up twice. Rows are labeled `PkgN Core M` once more than one package is
  detected; single-socket boxes still show the plain `Core M` they always have.
- **VMs** (`vms`): libvirt VM status via `virsh` (not the `libvirt-go` CGO bindings, to keep the
  existing plain `go build` cross-compilation working for both `amd64`/`arm64` without needing
  target sysroots) -- same shape as the Docker module, with its own `ignore` list. A VM that's
  "shut off" isn't treated as a problem, same reasoning as Docker containers below: something you
  turned off on purpose isn't a failure. Shows "Unavailable" if `virsh` isn't found.
- **Last Login** (`last-login`): shows the previous login for the current user (host and
  timestamp), excluding the session currently running the MOTD. This reads `/var/log/wtmp` directly
  in Go (a stable, documented binary format) rather than shelling out to the `last` command, so it
  works with no extra packages installed -- it only needs whatever's already writing login records
  to wtmp (standard on Debian/Devuan with OpenSSH + PAM's `pam_lastlog`). `install`/`plugin_update`
  create `/var/log/wtmp` if it's missing (harmless if it already exists), since on some minimal
  installs nothing ever creates that initial file even though session logging would otherwise use
  it once it's there. If it's still "Unavailable" after that, session accounting itself likely isn't
  enabled -- check `UsePAM` in `/etc/ssh/sshd_config` and whether `pam_lastlog` is wired into
  `/etc/pam.d/sshd`.

If any of these shows "Unavailable", run `/usr/bin/plugins/motd --debug` over SSH and look for a
`[snapraid]`/`[gpu]`/`[last-login]` debug line -- it logs the specific reason.

## What changed from the Unraid version

Unraid-specific bits were swapped for MOS/Devuan equivalents:

- **Distro detection** now reads `/etc/os-release` (`PRETTY_NAME`) instead of
  `/etc/unraid-version`, with an optional MOS version marker appended if found at
  `/etc/mos-release` or `/boot/config/mos-version`.
- **Service checks** now run `/etc/init.d/<service> status` (Devuan/sysvinit) instead of Unraid's
  `/etc/rc.d/rc.<service> status` (Slackware), and the default service list matches Debian/Devuan
  init script names (`nginx`, `smbd`, `nfs-kernel-server`, `ssh`, `docker`) rather than
  Unraid's (`nginx`, `samba`, `tailscale`, `nfsd`, `sshd`, `docker`). The old enable/disable
  gating against Unraid's `ident.cfg`/`share.cfg`/`docker.cfg` was replaced with a best-effort
  check against MOS's own `/boot/config/*.json` files, falling back to "assume enabled."
- **Default config path** moved from `/boot/config/plugins/motd/config.yaml` (Unraid boot flash
  layout) to `/boot/optional/plugins/motd/config.yaml` (MOS boot layout).
- **Packaging**: the Unraid `.plg`/Slackware package was replaced with a MOS plugin (`page/`,
  `functions`, `settings.json`). MOS Hub installs plugins from a **Debian package** built and
  attached to a GitHub release -- not a raw downloaded binary -- so `.github/workflows/release.yml`
  cross-compiles the Go binary per architecture, builds the settings page with Vite, and packages
  both into `mos-motd_<version>_<arch>.deb` (+ a `.deb.md5` sidecar) for `amd64` and `arm64`. This
  layout was reverse-engineered from [ich777/mos-backup](https://github.com/ich777/mos-backup), a
  working MOS Hub plugin, since the published
  [MOS Plugin Development Guide](https://github.com/ich777/mos-docs/blob/master/docs/Plugins/MOS-Plugin-Development-Guide.md)
  doesn't document the `.deb` requirement, the Vite/Module Federation build for `page/`, or the
  real API paths.
- **Settings page**: `page/` is a Vite + Vue 3 project built with
  [`@originjs/vite-plugin-federation`](https://github.com/originjs/vite-plugin-federation) --
  `npm run build` in `page/` produces `page/dist/motd/remoteEntry.js`, which the MOS frontend loads
  at runtime as a remote module. It isn't a static `.vue` file MOS interprets directly. API calls
  use `/api/v1/mos/plugins/settings/motd` and `/api/v1/mos/plugins/query` (the docs omit the
  `/api/v1` prefix) with `Authorization: Bearer <token>` read from `localStorage.getItem("authToken")`.

Everything else — Docker container status (via the Docker API), drive usage (via `gopsutil`/ZFS),
CPU temps, and network interfaces — was already OS-agnostic and needed no changes.

## Installing

Add this repository in **MOS Hub → Settings → Add Repository**, then install the "MOTD" plugin
from the Hub. MOS Hub downloads the `.deb` matching your architecture from this repo's latest
GitHub release and installs the `motd` binary to `/usr/bin/plugins/motd` and the settings page to
`/var/www/mos-plugins/motd/`. The plugin's `install` function (in `functions`) then drops a login
hook at `/etc/profile.d/motd.sh`, fetches the optional `figurine` header renderer, and writes a
starter `config.yaml` to `/boot/optional/plugins/motd/config.yaml` (only if one doesn't already
exist).

**Updating** never overwrites your `config.yaml` -- `plugin_update` only writes a default config if
one is somehow missing, and otherwise leaves your existing config untouched.

**Uninstalling** removes everything the plugin added, including `/boot/optional/plugins/motd`
(config and settings) -- nothing is left behind. Use the settings page's **Reset to Default**
button instead if you just want to discard your config without uninstalling.

A release only exists once a tag is pushed and `.github/workflows/release.yml` runs -- see
[Releasing](#releasing) below.

### Releasing

Push a bare version tag (no `v` prefix, e.g. `0.1.0`) to trigger the release workflow, or run it
manually via **Actions → Build and Release → Run workflow** with a version input. The workflow
cross-builds the Go binary for `amd64`/`arm64`, builds `page/` with Vite, packages each into a
`.deb`, and publishes them as release assets that the `functions` install hook and MOS Hub expect.

## Configuration

Configuration lives at `/boot/optional/plugins/motd/config.yaml`, editable either directly or via
the plugin's settings page in the MOS web UI. **Save** persists the textarea and applies it to
`config.yaml`; **Preview MOTD** now does the same before running the binary, so it always reflects
what's currently in the box, not whatever was last saved; **Reset to Default** discards your config
entirely and re-dumps the binary's built-in defaults. `--dump-config` always writes fresh, code-level
defaults regardless of any existing config file — it does not merge with or preserve values already
on disk, which is what makes Reset actually reset. Run `motd --dump-config` to print the full set of
options to stdout, or `motd --dump-config --dump-config-path <file>` to write them to a file. See the
original [unraid-motd README](https://github.com/dkaser/unraid-motd#configuration) for the option
reference — the config schema itself didn't change.

The Reset to Default button doesn't use a confirmation popup/dialog (MOS's settings page runs inside
a remote-module iframe, where `window.confirm()`/`alert()` can silently throw instead of showing
anything). Instead, the button itself changes: a first click arms a 2-second countdown, and only a
second click after that actually performs the reset. If the second click doesn't come within a few
seconds of arming, it automatically disarms back to normal.

A few defaults worth knowing:

- `global.enabled` (default `true`) is an on/off switch for the automatic login hook only --
  setting it to `false` (or flipping the "Show MOTD at login" switch in the settings page, which
  takes effect immediately, no separate Save needed) silences `/etc/profile.d/motd.sh`, but running
  `/usr/bin/plugins/motd` directly and the settings page's Preview MOTD button both keep working
  regardless, so you can still check your config before turning it back on.
- `services.monitor` defaults to `[nginx, smbd, ssh, docker]` -- `nfs-kernel-server` is
  intentionally left out since NFS shares are opt-in and MOS's enabled/disabled state for it can't
  currently be verified reliably (see caveats below). Add it back in your config if you use NFS.
- Any Docker container in the "exited" state is shown as "stopped" (informational, not a
  failure) regardless of its exit code -- exit codes turned out too inconsistent across different
  apps' signal handling to reliably tell "you stopped this on purpose" from "this crashed" (e.g.
  plenty of apps still exit `1`, not `0`/`137`/`143`, on a completely normal, deliberate
  `docker stop`). The exit code is still shown alongside the label (`stopped (exit N)`) when it
  isn't one of the common stop-related codes, purely for visibility. A container stuck
  restarting/dead is still flagged as a problem.
- `snapraid` is looked up by absolute path (`/usr/bin`, `/usr/sbin`, `/sbin`, `/bin`,
  `/usr/local/{s,}bin`) rather than relying on `$PATH`, since the environment MOS's plugin query API
  spawns the binary in isn't guaranteed to have the same `$PATH` as an interactive shell.
- `last-login` has no such dependency at all -- it reads `/var/log/wtmp` directly rather than
  shelling out to anything, so there's nothing to look up on `$PATH` for it.

## Testing from a terminal

SSH into the box and run the binary directly:

```
/usr/bin/plugins/motd
```

It also runs automatically on every interactive shell login via `/etc/profile.d/motd.sh`. Useful
flags: `--debug` for verbose logging, `--hide-unavailable` to hide modules that errored (e.g. no
Docker socket), `--config <path>` to point at a different config file.

Note: with no flags and no controlling terminal (e.g. output piped, or run from a script/cron),
`motd` intentionally stays silent so it doesn't spam non-interactive shells like `scp`/`ssh host
cmd`. Passing any flag (even just `--hide-unavailable`) overrides this and always produces output
-- this is also how the MOS web UI's "Preview MOTD" button gets output back, since it runs the
binary through an API call where stdin isn't a real terminal.

## Caveats / things to verify on real MOS

This was ported and build-tested (`go build ./...`) in a sandbox without access to a live MOS
install, so a few things are worth double-checking on your box before relying on it:

- Devuan/MOS service script names — confirm `smbd` / `nfs-kernel-server` / `ssh` match what's
  actually installed (`ls /etc/init.d/`), and adjust `services.monitor` in `config.yaml` if not.
- The `page/` build, `.deb` packaging, `/api/v1/mos/plugins/...` paths, and `authToken` auth were
  all confirmed by inspecting [ich777/mos-backup](https://github.com/ich777/mos-backup)'s actual
  source and release assets (the published plugin dev guide is incomplete/outdated on all of
  these), but this plugin itself still hasn't been installed on a real MOS box. Once you can, check
  that the settings page actually loads/renders as a remote module and that save/load/"Preview
  MOTD" round-trip correctly — the `/api/v1/mos/plugins/query` response shape (`{success, output}`)
  was inferred from `mos-backup`'s `Plugin.vue`, not confirmed against a live response.
- `isServiceEnabled()` in `datasources/services.go` guesses at `/boot/config/docker.json` and
  `/boot/config/shares.json` having an `"enabled"` boolean — worth checking against your actual
  files and adjusting the key name if MOS uses something else.
- The `.deb`'s `DEBIAN/postinst`/`postrm` are no-ops (matching `mos-backup`'s pattern) since
  `functions install()`/`uninstall()` handle setup -- worth confirming MOS actually calls
  `functions install()` *after* the `.deb` is unpacked (so `/usr/bin/plugins/motd` already exists
  when it runs), not before.
