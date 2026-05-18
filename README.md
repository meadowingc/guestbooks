# Guestbooks

A small, embeddable guestbook service. Create a guestbook, drop a `<script>`
tag on your site, and visitors can leave messages.

The canonical hosted instance lives at <https://guestbooks.meadow.cafe>.

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
