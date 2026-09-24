# Kue Preorder

Platform preorder kue dengan perhitungan bahan otomatis dari pesanan.

- Desain lengkap: [`docs/architecture.md`](docs/architecture.md)
- Aturan untuk AI agent (Antigravity dan Claude Code): [`.agents/rules/`](.agents/rules/) dan [`CLAUDE.md`](CLAUDE.md)
- Alur kerja agent: [`.agents/workflows/`](.agents/workflows/)

Status: M0 (fondasi) sedang berjalan. M0.1 (skeleton + `platform` + `/healthz`) selesai.

## Kebutuhan

| Tool | Versi | Untuk |
|---|---|---|
| Go | 1.26 (`go.mod` memasang toolchain go1.26.8 otomatis) | build dan test |
| golangci-lint | v2.13.2 | lint |
| goose | v3.28.0 | migrasi (mulai M0.2) |
| buf | v1.73.0 | kontrak API (mulai M0.3) |
| sqlc | menyusul di M0.2 | query bertipe |
| gcc | apa saja | `go test -race` (race detector butuh cgo) |
| Docker | apa saja | integration test (testcontainers) |

Pasang tool Go dengan versi yang sama seperti CI:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
go install github.com/bufbuild/buf/cmd/buf@v1.73.0
```

## Menjalankan

Salin `.env.example` menjadi `.env`, isi nilainya, lalu set variabelnya di shell (Go tidak membaca `.env` sendiri).

```powershell
# PowerShell
$env:APP_ENV = "development"
$env:DATABASE_URL = "postgres://..."
go run ./cmd/api      # http://localhost:8080/healthz
go run ./cmd/worker
```

`/healthz` hanya mengecek proses hidup. `/readyz` juga mengecek database dan menjawab 503 selama database belum bisa dihubungi.

## Quality gate

```sh
golangci-lint run
go vet ./...
go test -race ./...
```

Batas modul (§5) ditegakkan oleh `depguard`/`forbidigo` di `.golangci.yml` dan oleh `internal/archtest`.
