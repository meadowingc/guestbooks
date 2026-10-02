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

The default hosted theme highlights submission confirmations and errors.
Custom themes and JavaScript embeds keep their own styling; use
`#guestbooks___success-message` and `#guestbooks___error-message` to style feedback.

The existing hidden `redirect_to_url` input overrides the guestbook's
post-submit setting, for both JavaScript and normal form submissions.
Redirects inside an iframe stay inside that iframe; the destination must
allow embedding. Redirect URLs are limited to 2,048 bytes.

In **Settings → Email & Notifications**, add and verify your account email,
then enable notifications. Moderated guestbooks show whether this setup is
complete. You can resend verification once per minute. Visitor email
addresses are not included in notification emails or public APIs.

## Self-hosting

See [SELFHOSTING.md](./SELFHOSTING.md) for a full guide (binary install,
Docker, reverse proxy snippets, backup/upgrade, etc.).

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
task test      # go test
task lint      # go vet + staticcheck + exhaustive
task dev       # air live-reload
```

## License

See [LICENSE](./LICENSE).
