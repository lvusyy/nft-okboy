# okboy

**nftables-based dynamic firewall allowlist manager** — authorized clients
authenticate and their IP is automatically registered into nftables, switched
seamlessly when it changes, with clean, traceable rules. Written in Go, shipped
as a **single static binary** with no runtime dependencies.

English | [简体中文](README.md)

> This is the Go + nftables rewrite of [ufw-okboy](https://github.com/lvusyy/UFW-OkBoy)
> (Python + UFW): it faithfully preserves all of the authentication / authorization
> / security semantics and swaps the data plane to native nftables and the artifact
> to a single static binary.

---

## Architecture

```
Client (browser / Python / shell)
    │  HTTPS + HMAC-SHA256 signature
    ▼
Nginx (TLS termination, passes X-Real-IP)
    │  HTTP 127.0.0.1:5000
    ▼
okboy (Go: HTTP API + CLI + auth + throttle)
    │  nft -j -f -  (JSON transaction, no shell)
    ▼
nftables (dedicated `inet okboy` table, accept-only, coexists with k8s/host)
    │
    ▼
SQLite (pure-Go modernc; users/groups/membership/audit)
```

## Why

- **Single static binary**: `CGO_ENABLED=0` + pure-Go SQLite (modernc) → one file
  runs on any Linux host, no Python / venv / gunicorn / libc dependency.
- **Native nftables**: modern distros default to nftables; okboy drives it via the
  `nft` JSON interface, with rules commented `okboy:<user>:<group>`, deleted
  precisely by handle, and reconciled atomically in one transaction.
- **Coexists with k8s**: okboy owns its own `inet okboy` table — hook input,
  priority -150, **policy accept, accept-only rules** — so it can only *open* access
  for specific IP/port pairs, never drops, and is never flushed by
  Calico/Cilium/kube-proxy (table isolation).

## Security (parity with the Python version, plus hardening)

| Feature | Description |
|---------|-------------|
| **Stateless HMAC-SHA256 auth** | Secret never on the wire; timestamp window; all failures recorded |
| **Admin TOTP step-up** | Every admin write requires a 6-digit code (RFC 6238) once TOTP is enrolled |
| **TOTP replay protection** | A used code is rejected on reuse within the window (atomic last-counter consume) |
| **Per-IP throttle** | Too many failures from one IP → 429 (keyed on IP, not username — no lockout DoS) |
| **Name allowlist (SR-1)** | Usernames/groups `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$` — closes nft injection |
| **Anti-IP-spoofing (H-9)** | X-Forwarded-For uses the **rightmost** hop, trusted only from `trusted_proxies` |
| **Revoke + forced re-auth** | `revoke`: close ports + clear state + rotate secret (old credential dies instantly) |
| **Injection-safe writes** | All writes go via `nft -j -f -` on stdin (encoding/json escaped, no shell, no argv) |

## Quick start

**Build the single static binary:**

```bash
make static          # → dist/okboy-linux-amd64 (CGO_ENABLED=0, static)
```

> Multi-arch: `make release-bins` builds **amd64 / arm64 / armv7** in one go (pure Go, free cross-compile — no C toolchain); the [Releases](https://github.com/lvusyy/nft-okboy/releases) page ships all three prebuilt binaries plus `SHA256SUMS`.

**Configure and run:**

```bash
cp config.example.yaml config.yaml
./okboy gen-secret alice                 # generate a user secret
./okboy -c config.yaml user-add alice    # create the user (or edit users: in config.yaml)
./okboy -c config.yaml group-add ssh 22  # create a group (bind port 22)
./okboy -c config.yaml user-join alice ssh
sudo ./okboy -c config.yaml serve        # start (needs root or CAP_NET_ADMIN)
```

**Client**: open `https://your-server/` → enter username + secret → Connect. Or
reuse ufw-okboy's `knock.py` / `knock.sh` (the API contract is identical).

## Testing

```bash
make test          # unit tests (any platform: auth RFC vectors + firewall Mock reconcile + db primitives)
make integration   # real nftables integration test (Linux+root, runs in an isolated netns — zero host/k8s impact)
```

`internal/firewall/mock.go` lets the core reconcile/auth logic be unit-tested on a
non-Linux dev box; `nft_integration_test.go` (`-tags integration`) validates the
**real** `nft` inside an isolated network namespace: EnsureBase idempotence, add,
handle-based list, precise cross-group delete, IPv6, ListManaged. `scripts/e2e-test.sh`
runs the full server + HMAC knock + real nftables end to end.

## Deploy

```bash
install -Dm755 dist/okboy-linux-amd64 /opt/okboy/okboy
install -Dm600 config.yaml /etc/okboy/config.yaml
install -Dm644 deploy/okboy.service /etc/systemd/system/okboy.service
systemctl enable --now okboy
# nginx: see deploy/nginx-okboy.conf (MUST set proxy_set_header X-Real-IP $remote_addr)
```

## CLI commands

```
serve [--debug]          start the API server
gen-secret [user]        generate a secret
user-add <name> [--admin] / user-del / user-list
group-add <name> <port> [--proto tcp] / group-del / group-list
user-join <user> <group> / user-leave <user> <group>
admin-add <user>         grant admin
revoke <user> [--no-rotate]   force offline + rotate secret
list                     list managed nftables rules
cleanup [--max-age <days>]    purge stale rules
backup [--dir <path>]    checksummed online backup
--version
```

## Project structure

```
cmd/okboy/            main: subcommand dispatch
internal/config/      YAML config loading
internal/db/          SQLite layer (schema + migrations + CRUD + atomic IP write + backup)
internal/auth/        HMAC verify + TOTP + per-IP throttle
internal/firewall/    FirewallBackend interface + nftables impl + Mock + Manager (reconcile)
internal/server/      stdlib net/http routes (mirror every app.py endpoint)
internal/static/      go:embed single-file web UI
deploy/               systemd unit + nginx example
```

## License

MIT
