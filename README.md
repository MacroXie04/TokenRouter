# TokenRouter

An AI API gateway with provider adapters, durable usage accounting, a dashboard
control plane, and an embedded React application.

## Develop locally

Requires Go 1.26 and Node/npm. Build the embedded assets before compiling Go:

```sh
cd web
npm ci --no-audit --no-fund
npm run build
cd ..
go run ./cmd/tokenrouter
```

The server defaults to port 3000 and SQLite. Create the first administrator using
the setup wizard. See `.env.example` for configuration; no default password is
provided.

- [Architecture and ownership](docs/architecture/README.md)
- [Developer commands and validation](docs/development/README.md)
- [Reorganization and validation record](docs/development/reorganization.md)
- [Runtime and operations](docs/operations/README.md)
- [Historical compatibility evidence](docs/parity/FINAL_REPORT.md)

`cmd/tokenrouter` is the entry point, `internal` contains the backend, `web/src`
contains the frontend, and `protocolkit` is independently tested. The root Go
test command does not include the separate protocol module.
