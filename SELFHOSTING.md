# Self-Hosting Guestbooks

This guide walks you through running your own instance of Guestbooks. The
app is a single Go binary backed by a SQLite database — there are no other
moving parts.

## Prerequisites

You need **one** of:

- **Go 1.25+** and a C compiler (for CGO sqlite). On Debian/Ubuntu:
  `apt install build-essential`.
- **Docker** (uses the included `Dockerfile`).

For a public deployment you'll also want a reverse proxy (Caddy, nginx,
Traefik) to terminate TLS and forward `X-Forwarded-For`.

## Quick start — binary

```bash
git clone https://codeberg.org/meadowingc/guestbooks.git
cd guestbooks

# Create your config from the template and edit it.
cp config.example.yaml config.yaml
$EDITOR config.yaml      # at minimum, set server.public_url

go build -tags release -o guestbooks .
./guestbooks
```

The server listens on `:6235` by default and prints its public URL on
startup. For a standalone binary behind a proxy on the same machine, set
`server.bind_host: "127.0.0.1"` and explicitly trust the proxy as described
below. Invalid configuration or an occupied listen port now prevents startup.

### Create systemd service

In order to keep the binary running in the background, and to 
automatically start it at boot, a systemd service has to be created.

In `/etc/systemd/system/guestbooks.service`, create the following file:

```systemd
[Unit]
Description=Guestbooks service
After=network.target

[Service]
User=nameofuser
Group=groupofuser

WorkingDirectory=/path/to/guestbooks/repo
ExecStart=/path/to/guestbooks/binary

Restart=always
RestartSec=3
TimeoutStopSec=40

StandardOutput=journal
StandardError=journal
SyslogIdentifier=guestbooks

[Install]
WantedBy=multi-user.target
```

Then, refresh systemd and enable the service:

```bash
sudo systemctl daemon-reload && \
sudo systemctl enable guestbooks && \
sudo systemctl start guestbooks
```

Check the status of the service:

```bash
sudo systemctl status guestbooks
```

To check the logs and to see if everything is working correctly, 
use journalctl:

```bash
sudo journalctl -u guestbooks -f
```

## Quick start — Docker

```bash
git clone https://codeberg.org/meadowingc/guestbooks.git
cd guestbooks

cp config.example.yaml config.yaml
$EDITOR config.yaml

docker build -t guestbooks .
docker run -d --name guestbooks \
  --stop-timeout 40 \
  -p 6235:6235 \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v guestbooks-data:/app/data \
  guestbooks
```

The SQLite database lives in the `guestbooks-data` named volume so it
survives container rebuilds. Inside the container, leave `server.bind_host`
empty (or use `0.0.0.0`), not `127.0.0.1`. When Caddy runs on the Docker host,
publish with `-p 127.0.0.1:6235:6235` to prevent direct public access. Determine
the actual Docker bridge peer before configuring trusted proxy CIDRs; do not
trust every private address as a shortcut.

Keep the container stop grace period at least 40 seconds. In Compose, use
`stop_grace_period: 40s`. Docker's default 10 seconds can kill the process before
its 30-second HTTP/mail drain finishes during a stop or restart.

`./guestbooks healthcheck` probes `/healthz` using the configured bind address
and port without starting another server or running migrations. Docker uses
the same command, so a nondefault internal port is supported. Update the
published port mapping separately.

## Configuration reference

All keys are documented inline in [`config.example.yaml`](./config.example.yaml).
Summary:

| Key                          | Default                  | Purpose                                                                 |
|------------------------------|--------------------------|-------------------------------------------------------------------------|
| `server.public_url`          | `http://localhost:PORT`  | Public origin used in emails and CSRF checks. **Set this in production.** |
| `server.port`                | `6235`                   | HTTP listen port.                                                       |
| `server.bind_host`           | `""`                     | Listen interface; use loopback behind a same-host proxy. |
| `server.trusted_proxies`     | `[]`                     | Explicit peer CIDRs allowed to supply forwarded client IPs. |
| `admin.allow_signups`        | `true`                   | When false, `/admin/signup` returns 404 (no new accounts can register). |
| `branding.show_credits`      | `false`                  | Show maintainer footer credit, ko-fi link, "About" card.               |
| `branding.support_url`       | `""`                     | Contact URL appended to notification emails.                            |
| `templates.extra_head_html`  | `""`                     | Raw HTML injected into `<head>` on every page (analytics, fonts, …).   |
| `mailer.mailer_name`         | `none`                   | `smtp`, `azure_communication_service`, or `none`.                       |
| `mailer.smtp.*`              | —                        | SMTP host/port/credentials/from address.                                |

### Mail setup

For most self-hosters, plain SMTP with any provider (Fastmail, Mailgun,
Postmark, your own postfix, …) is the right choice:

```yaml
mailer:
  mailer_name: smtp
  smtp:
    host: smtp.fastmail.com
    port: 587
    username: you@example.com
    password: app-specific-password
    from_email: "Guestbooks <noreply@example.com>"
```

If you don't want email at all, set `mailer_name: none`. Email verification
and password reset won't work, so it's strongly recommended to also set
`admin.allow_signups: false` after creating your account.

Account settings and moderated guestbooks display notification setup status.
Owners need a saved, verified email address and notifications enabled.
Changing an account email requires verification again. The resend button
uses the saved address. All verification attempts, including address changes,
share one persisted attempt per account per minute;
delivery-request failures are shown rather than reported as sent. Check
the spam folder and your configured provider if verification does not arrive.
An enabled status describes configuration, not guaranteed delivery.
Changing an address during cooldown still saves the new address, but explicitly
does not send an email; use Resend after the cooldown. Failed provider attempts
also consume the cooldown. ACS accepts a full HTTPS resource origin or an
existing bare hostname; no public URL, API key, recipient, or message body
should be pasted into diagnostic output.
SMTP requires STARTTLS (normally port 587), or uses implicit TLS on port 465.
Provider requests have a 15-second deadline. A provider accepting an email
request is not proof of final delivery; delivery failures are logged without
recipient addresses, credentials, or bodies. Retry an ambiguous send with
care to avoid duplicate mail.

When the instance mailer is `none`, the UI explicitly reports that email
delivery is unavailable. Debug builds log that notifications were skipped,
without logging their bodies,
instead of sending them; use the documented release build for deployment.

Optional private visitor email collection is independent of outbound mail.
It stores addresses in SQLite for the guestbook owner's dashboard, not in
public APIs or notification emails. These addresses are not encrypted from
the hosting operator and are included in database backups. Disabling collection
does not erase old addresses. Existing message soft-deletion and backup
retention remain unchanged; protect database files and backups accordingly.

### Bootstrapping your first user

1. Start the server with `admin.allow_signups: true`.
2. Visit `/admin/signup` and create your account.
3. Edit `config.yaml`, set `admin.allow_signups: false`, restart the server.

## Reverse proxy

The app ignores forwarded IP headers by default. Configure only the actual
proxy connection peers as trusted. For a same-host standalone deployment:

```yaml
server:
  public_url: "https://guestbooks.example.com"
  port: 6235
  bind_host: "127.0.0.1"
  trusted_proxies: ["127.0.0.1/32"]
```

If using IPv6 loopback, configure `::1` and `::1/128` consistently. Forwarded
chains are parsed right-to-left through trusted peers, independent of comma
spacing. Do not assume the leftmost value was set by a trusted proxy.

### Caddy

```caddy
guestbooks.example.com {
    reverse_proxy 127.0.0.1:6235
}
```

Caddy sets `X-Forwarded-For` automatically and normally ignores incoming
forwarding values from untrusted peers.

### Cloudflare before Caddy

Without explicit handling, Caddy can forward a Cloudflare edge IP instead of
the visitor IP, grouping unrelated visitors into one rate-limit bucket.
Configure both trust boundaries: Cloudflare to Caddy and Caddy to the app.

For a Caddy instance shared with other applications, the following change is
scoped to the guestbook site. Preserve that site's existing TLS directives;
do not change unrelated sites or global trust rules.

First fetch the official IPv4 and IPv6 lists from
<https://www.cloudflare.com/ips/> and create an operator-reviewed Caddy snippet
at `/etc/caddy/guestbooks-cloudflare-peers.caddy` containing **one `remote_ip`
matcher with those CIDRs**. For example, generate a candidate for review:

```bash
curl --fail --silent --show-error https://www.cloudflare.com/ips-v4 > /tmp/guestbooks-cf-v4.txt
curl --fail --silent --show-error https://www.cloudflare.com/ips-v6 > /tmp/guestbooks-cf-v6.txt
{
  printf 'remote_ip '
  tr '\n' ' ' < /tmp/guestbooks-cf-v4.txt
  printf ' '
  tr '\n' ' ' < /tmp/guestbooks-cf-v6.txt
  printf '\n'
} > /tmp/guestbooks-cloudflare-peers.caddy
```

Check that both downloads succeeded, contain the published nonempty CIDR
lists, and include no unexpected content before installing the candidate.
Keep the last validated list until its replacement has been reviewed.

```caddy
guestbooks.example.com {
    # Keep any TLS directive already required by your deployment here.
    @cloudflare {
        import /etc/caddy/guestbooks-cloudflare-peers.caddy
        header CF-Connecting-IP *
    }
    handle @cloudflare {
        reverse_proxy 127.0.0.1:6235 {
            header_up X-Forwarded-For {http.request.header.CF-Connecting-IP}
        }
    }
    handle {
        reverse_proxy 127.0.0.1:6235 {
            header_up X-Forwarded-For {http.request.remote.host}
        }
    }
}
```

Only Cloudflare connection peers can supply the visitor header. Direct
requests, including ones with forged `CF-Connecting-IP`, use the actual peer.
A missing Cloudflare visitor header also falls back to the actual peer.
Never forward that header unconditionally. The app trusts only local Caddy,
not Cloudflare addresses directly.

Validate with the installed Caddy version before reloading:
`caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile`.
Review updated Cloudflare ranges periodically using the same process. Check
the installed Caddy version, any Cloudflare Workers/header transforms, and
the origin firewall during rollout; the example does not alter them.
Confirm externally that port 6235 is inaccessible. Do not apply a blanket
firewall rule affecting other VPS services.

### nginx

```nginx
server {
    listen 443 ssl http2;
    server_name guestbooks.example.com;
    # ssl_certificate ...;

    location / {
        proxy_pass http://127.0.0.1:6235;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## Backups

The entire state lives in `guestbook.db` (plus `-wal` and `-shm` files
while running). Two options:

**Cold backup** (simple):

```bash
systemctl stop guestbooks
cp guestbook.db* /path/to/backups/
systemctl start guestbooks
```

**Hot backup** (no downtime):

```bash
sqlite3 guestbook.db ".backup '/path/to/backups/guestbook-$(date +%F).db'"
```

For Docker, run a one-off container with the sqlite CLI installed:

```bash
docker run --rm -v guestbooks-data:/data -v "$(pwd):/backup" alpine:3.20 \
    sh -c "apk add --no-cache sqlite >/dev/null && \
           sqlite3 /data/guestbook.db \".backup '/backup/snapshot.db'\""
```

## Upgrading

### Managed systemd VPS

For an existing installation using the release layout below, deploy from your
Linux workstation with:

```bash
python3 scripts/deploy_vps.py meadow-ubuntu-8gb-hel1-1 \
  --public-url https://guestbooks.meadow.cafe
```

Replace the SSH alias and URL for another installation. The SSH destination
must log in as root, using your normal SSH configuration/default key. The
script asks you to type `yes`; `--yes` supplies confirmation for automation.
Commit and push your changes first. The updater refuses a dirty worktree and
builds an isolated archive of the local `HEAD`, not whatever happens to be in
the VPS checkout. It does not commit, push, pull, or install dependencies.

Prerequisites: Python 3.8+, Git, Go (the version required by `go.mod` or newer),
a native CGO/C compiler, SSH/SCP, and the Python dependencies in
`scripts/requirements.txt`. Both machines must use Linux and the same supported
CPU architecture (x86_64 or aarch64), with compatible native libraries. The VPS
needs Python 3.8+, systemd and `runuser`; it does not need Go or bcrypt.
The updater executes the new binary's non-mutating `healthcheck` command on the
VPS before stopping the running service, catching incompatible native builds.

The updater targets this **already provisioned** layout; it is not an initial
installer and does not apply to the basic single-directory or Docker setups:

| Item | Location |
| --- | --- |
| Service | `guestbooks.service`, enabled and running as `guestbooks` |
| Service executable | `/opt/guestbooks/current/guestbooks` |
| Versioned releases | `/opt/guestbooks/releases/<commit>-<binary-hash>/` |
| Working directory / live database | `/var/lib/guestbooks/guestbook.db` |
| Configuration | `/etc/guestbooks/config.yaml` |
| Private backups and logs | `/root/guestbooks-deploy-backups/update-*/` |

The working directory's `config.yaml` must link to the configuration above;
`assets` and `templates` must link through `/opt/guestbooks/current/`. The local
app must listen at `127.0.0.1:6235`, and the public origin must use HTTPS.
The existing service unit is `/etc/systemd/system/guestbooks.service` and the
existing Caddy configuration is `/etc/caddy/Caddyfile`. Neither is rewritten.

Each update runs non-browser Go tests in both build modes and the Python
script tests, builds with `-tags release`, and uploads matching assets/templates,
checksums and a source archive. Run the full browser release checks separately
as described in the README; the updater does not run Chromium.
It checks the existing public route, stops the service for a consistent SQLite
backup, atomically switches the release link, and restarts. Health and actual
HTTPS sign-in form POSTs are checked again, including CSRF rejection. Probes
use random invalid credentials: no production account/message is created and
no mail is sent. This is not a substitute for an authenticated browser check.
HTTP probes identify themselves as `GuestbooksDeploymentCheck/1.0` rather than
Python's default user agent, which some edge protections challenge.
Reload open admin forms after the restart.

Deployment is serialized with a lock and runs in a transient systemd unit.
If SSH disconnects, it continues on the VPS. The command prints the deployment
journal command and receipt path; inspect those before retrying. Deploying an
identical, already-active release checks health without restarting it.

**Recovery:** if activation fails and the database is logically unchanged,
the previous binary/assets/templates are restarted. If any data or schema
changed, the service is deliberately left **stopped** with a `needs_recovery`
receipt. Inspect the deployment journal and private `install.log`, preserve
the current database, and assess compatibility before restarting either
release. **The updater never restores an old database over current data.**
It also never runs orphan repair, purges records, changes Caddy/SMTP settings,
or deletes old releases/backups. Backups contain private data and credentials;
retain them securely and manage disk space deliberately.

The checkout and historical runtime files under `/root/guestbooks` are not
updated or used by this command. The exact committed source is retained as
`source.tar` alongside each deployment's backup. Obtain matching maintenance
scripts from that revision rather than assuming an old checkout is current.

### Basic single-directory installation

```bash
git pull
go build -tags release -o guestbooks .
# restart your service manager (systemd, docker, …)
```

The app runs `AutoMigrate` on startup so schema changes apply automatically.
Always back up `guestbook.db` first.

The session-expiry migration requires a fresh sign-in for existing sessions.
Sessions have a 30-day absolute server-side maximum. Logout and password
reset revoke old cookies; authenticated password changes rotate the cookie.
Changing the recovery email/password invalidates outstanding reset links.
Existing usernames are not renamed; exact legacy names remain usable and
ambiguous whitespace-normalized names are not guessed.

Admin automation must obtain a CSRF cookie and the `X-CSRF-Token` response
header from a protected GET and return both on mutations. Browser forms do
this automatically. After an application restart, reload an old form to
refresh its CSRF token. Visitor POSTs and public embeds remain cross-origin.

Deleting a guestbook now soft-deletes its messages; deleting a parent also
soft-deletes its replies. Historical orphaned content is hidden immediately
but is not automatically purged. Use the explicit repair workflow below.

For a production upgrade, preserve the previous binary/configuration and a
consistent database backup. Apply the loopback binding, explicit proxy trust,
and guestbook-only Caddy configuration together. Check health, fresh sign-in,
submission, moderation, and visitor-IP behavior using controlled requests.
Complete an actual sign-in POST through the public HTTPS URL; loading the
form alone does not verify CSRF handling across TLS termination. Keep
`server.public_url` set to that HTTPS origin even when Caddy's upstream hop
uses HTTP.
Use an operator-owned recipient only with explicit approval for a mail check.
Older binaries reintroduce the old security/visibility behavior: rollback
must consider data/schema compatibility and may require restoring the backup.

## Operational helpers

The `scripts/` directory has a couple of maintenance scripts:

- `reset_user_password.py` — reset a user's password directly in the DB.
- `hard_delete_all_data_for_username.py` — GDPR-style account purge.
- `repair_orphaned_data.py` — report historical orphaned descendants and,
  only when explicitly requested, soft-delete them.

Run them while the server is **stopped** to avoid SQLite lock contention.
The password-reset helper requires Python 3 and bcrypt; install
`scripts/requirements.txt` in a virtual environment rather than modifying
the operating system's Python packages.
Use `--database /absolute/path/to/guestbook.db` to avoid operating on the wrong
working-directory database. They open an existing database, not a new empty
one. Password resets revoke sessions and recovery links. Purges include
soft-deleted guestbooks and messages and require confirmation.

Hard deletion removes database records, not existing backups or guaranteed
forensic remnants in SQLite/WAL. Maintain a separate backup-retention policy.
Content left by a historical broken purge may have lost its ownership link;
do not assume it can still be attributed to a username.

With the service stopped and a consistent backup saved, preview historical
repair first:

```bash
python3 scripts/repair_orphaned_data.py --database /absolute/path/to/guestbook.db
```

Review the affected counts/IDs before using the command's `--apply` option
and confirming the change. Repair soft-deletes orphaned descendants; it does
not physically erase their records or modify existing backups.

## Reporting issues

Please file bugs (in the software, not your deployment) on the upstream
tracker: <https://codeberg.org/meadowingc/guestbooks/issues>.
