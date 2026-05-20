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
startup.

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
  -p 6235:6235 \
  -v "$(pwd)/config.yaml:/app/config.yaml:ro" \
  -v guestbooks-data:/app/data \
  guestbooks
```

The SQLite database lives in the `guestbooks-data` named volume so it
survives container rebuilds.

## Configuration reference

All keys are documented inline in [`config.example.yaml`](./config.example.yaml).
Summary:

| Key                          | Default                  | Purpose                                                                 |
|------------------------------|--------------------------|-------------------------------------------------------------------------|
| `server.public_url`          | `http://localhost:PORT`  | Public origin used in emails and CSRF checks. **Set this in production.** |
| `server.port`                | `6235`                   | HTTP listen port.                                                       |
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

### Bootstrapping your first user

1. Start the server with `admin.allow_signups: true`.
2. Visit `/admin/signup` and create your account.
3. Edit `config.yaml`, set `admin.allow_signups: false`, restart the server.

## Reverse proxy

The app trusts `X-Forwarded-For` for client IP detection (used by the rate
limiter). Make sure your proxy sets it.

### Caddy

```caddy
guestbooks.example.com {
    reverse_proxy localhost:6235
}
```

Caddy sets `X-Forwarded-For` automatically.

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

```bash
git pull
go build -tags release -o guestbooks .
# restart your service manager (systemd, docker, …)
```

The app runs `AutoMigrate` on startup so schema changes apply automatically.
Always back up `guestbook.db` first.

## Operational helpers

The `scripts/` directory has a couple of maintenance scripts:

- `reset_user_password.py` — reset a user's password directly in the DB.
- `hard_delete_all_data_for_username.py` — GDPR-style account purge.

Run them while the server is **stopped** to avoid SQLite lock contention.

## Reporting issues

Please file bugs (in the software, not your deployment) on the upstream
tracker: <https://codeberg.org/meadowingc/guestbooks/issues>.
