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

When feedback is enabled, the default hosted theme highlights confirmations and errors.
Custom themes and JavaScript embeds keep their own styling; use
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
