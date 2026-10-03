# Guestbooks

A small, embeddable guestbook service. Create a guestbook, drop a `<script>`
tag on your site, and visitors can leave messages.

The canonical hosted instance lives at <https://guestbooks.meadow.cafe>.

## Guestbook options

In your guestbook's **Edit settings** page:

- **After Submission** can show your own plain-text confirmation (in any
  language, up to 2,000 characters), or redirect visitors to an HTTP(S)
  thank-you page. Both are opt-in, including for new guestbooks. If you
  moderate messages, use the confirmation to explain that approval is pending.
- **Private Visitor Email** optionally collects an email address, visible
  only in your authenticated dashboard with a link to your email app.
  Collection is off by default; visitors never have to supply an address.
  Customize the label/help in your own language. Website stays optional.

Hosted pages and iframe embeds update automatically. For custom JavaScript
embeds, copy the optional email field from **Get embed code** into your site's
HTML; it is never injected into your existing layout. Disabling collection
discards newly submitted email values but retains previously collected ones.
Remove the field from copied HTML when you turn collection off.

### Updating an existing JavaScript embed

The standard embed loads its script from Guestbooks; the form HTML belongs
to your website. You do not need to replace the entire embed when changing
these settings:

| Change in the dashboard | Update copied HTML? |
| --- | --- |
| Enable, edit, or disable a confirmation message | No. The next submission uses the saved setting; a notice is inserted only when there is text to show. |
| Enable, change, or disable a redirect | No. The next submission uses the saved setting, unless a nonempty `redirect_to_url` input overrides it. |
| Enable private email | Yes. Add the optional email field from the updated snippet; existing forms without it still work. |
| Change the email label or help | Yes. Update that field's copied HTML. |
| Disable private email | Remove the field from your HTML. Storage stops immediately even if an old form still sends an address. |

Hosted pages and iframe forms pick up field/label changes on their next load.
Self-hosted copies of the JavaScript source, rather than a script URL pointing
to the updated Guestbooks service, must be updated separately.

When feedback is enabled, hosted pages and iframes using the default or a
built-in theme group the submit button and feedback in a themed footer:
side by side on wider screens and stacked on mobile. Confirmation and error
panels have distinct visual markers, and owner-written text stays unchanged.
Custom CSS and JavaScript embeds keep their own styling; use
`#guestbooks___success-message` and `#guestbooks___error-message` to style feedback.
Without opting in, existing hosted form spacing and successful JavaScript
submission layouts are unchanged.

The existing hidden `redirect_to_url` input overrides the guestbook's
post-submit setting, for both JavaScript and normal form submissions.
Redirects inside an iframe stay inside that iframe; the destination must
allow embedding. Redirect URLs are limited to 2,048 bytes.

**Compatibility note:** a pre-existing `redirect_to_url` input now performs
an actual browser navigation in JavaScript embeds. Previously it was followed
only inside the background request. Remove that input if you want to keep
visitors on the page.

In **Settings → Email & Notifications**, add and verify your account email,
then enable notifications. Moderated guestbooks show whether this setup is
complete. You can resend verification once per minute. Visitor email
addresses are not included in notification emails or public APIs.

## Self-hosting

See [SELFHOSTING.md](./SELFHOSTING.md) for a full guide (binary install,
Docker, reverse proxy snippets, backup/upgrade, etc.).

Existing managed systemd VPS installations can use
[`scripts/deploy_vps.py`](./SELFHOSTING.md#managed-systemd-vps) for backed-up,
versioned updates with public HTTPS form checks and guarded rollback.

Quick start:

```bash
git clone https://codeberg.org/meadowingc/guestbooks.git
cd guestbooks
cp config.example.yaml config.yaml   # edit server.public_url
go build -tags release -o guestbooks .
./guestbooks
```

## Development

```bash
task run       # go run .
task test      # unit/handler, Chromium browser, and maintenance-script tests
task test-unit # no browser required
task test-browser # explicit browser coverage
task lint      # go vet + staticcheck + exhaustive
task dev       # air live-reload
```

Unit tests use temporary SQLite databases. Browser tests require Chromium
(Rod can download its browser when first needed) and are enabled explicitly
with `go test -tags browser ./...`. Use `-tags browser,release` for production
behavior. `task release` also validates both build modes before building.
For reproducible browser checks, explicitly select an installed Chrome or
Chromium binary. Both complete build-mode suites are validated with Chrome
for Testing 145.0.7632.6. Rod's older automatic Chromium 128 fallback showed
intermittent renderer hangs in our environment; those hangs are not resolved.

```bash
CHROME=/path/to/chrome
go test -tags browser -count=1 -timeout 5m ./... -args -rod=bin="$CHROME"
go test -tags browser,release -count=1 -timeout 5m ./... -args -rod=bin="$CHROME"
```

The script tests need Python 3.8+ and the same bcrypt dependency as
the offline password-reset helper:

```bash
python3 -m venv .venv
.venv/bin/pip install -r scripts/requirements.txt
source .venv/bin/activate
task test-scripts
```

## Reliability and compatibility notes

Admin sessions now expire after 30 days, and upgrading requires one fresh
sign-in. Logout and password reset revoke the previous session. Password
changes rotate the session and invalidate outstanding reset links.
Admin forms and automated admin mutations require a CSRF token and cookie;
public cross-origin guestbook submissions remain supported.

Messages and replies accept up to 2,500 Unicode characters, visitor names up
to 200, and website values up to 2,048 bytes. Empty required fields and bodies
over 64 KiB are rejected regardless of content type. New message/reply writes
normalize CRLF and CR newlines to LF before counting and storage, so browser
form serialization does not change the character limit. PoW embeds require
HTTPS (or a browser-recognized secure localhost context); unsupported
browsers/pages show an explicit error.

Both public API versions retain their response shapes. Pagination now counts
root messages rather than replies, which appear only beneath approved,
active parents. Missing/deleted guestbooks are unavailable, invalid IDs return
client errors, and deleted content cannot reappear through cached responses.
Historical orphan replies are hidden from direct admin editing as well as
moderation lists. Books without an active owner cannot serve embeds or issue
PoW challenges. Multi-statement SQLite writes reserve the writer before
checking ancestry; read-only API snapshots remain concurrent with writers.

Custom CSS is delivered as a stylesheet rather than trusted inline HTML.
Valid custom styles and permitted HTTPS fonts remain supported; unsafe saved
styles require owner repair. The unreliable optional Format button has been
removed. Existing hosted, iframe, and service-hosted JavaScript embeds receive
the fixes without replacing their copied form markup.
Valid nested conditional styles remain supported, and built-in theme settings
can be saved even if the editor's theme download is slow or fails. Reloading
the embed script after replacing either its form or messages container
reinitializes the embed.
Built-in themes also wrap long website titles, visitor names, and unbroken
message/reply text instead of forcing horizontal scrolling.

## License

See [LICENSE](./LICENSE).
