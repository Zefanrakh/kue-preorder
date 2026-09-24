# Kue Preorder

Platform preorder kue dengan perhitungan bahan otomatis dari pesanan.

- Desain lengkap: [`docs/architecture.md`](docs/architecture.md)
- Aturan untuk AI agent (Antigravity dan Claude Code): [`.agents/rules/`](.agents/rules/) dan [`CLAUDE.md`](CLAUDE.md)
- Alur kerja agent: [`.agents/workflows/`](.agents/workflows/)

Status: M0 (fondasi) sedang berjalan. M0.1 (skeleton + `platform` + `/healthz`), M0.2 (migrasi awal + sqlc + integration test), M0.3 (kontrak buf + ConnectRPC + verifikasi JWT Supabase), dan M0.4 (CI) selesai. Berikutnya M0.5 (observability).

## Kebutuhan

| Tool | Versi | Untuk |
|---|---|---|
| Go | 1.26 (`go.mod` memasang toolchain go1.26.8 otomatis) | build dan test |
| golangci-lint | v2.13.2 | lint |
| goose | v3.28.0 | migrasi |
| buf | v1.73.0 | kontrak API (mulai M0.3) |
| sqlc | v1.31.1 | query bertipe (butuh gcc untuk dipasang) |
| gcc | apa saja | `go test -race` dan sqlc (keduanya butuh cgo) |
| Docker | apa saja | integration test (testcontainers) |
| Node.js | 22 | pengecekan client TS (`api/`) |

Pasang tool Go dengan versi yang sama seperti CI:

```sh
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
go install github.com/pressly/goose/v3/cmd/goose@v3.28.0
go install github.com/bufbuild/buf/cmd/buf@v1.73.0
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
```

### Windows

- **gcc di folder bernama spasi gagal saat linking** (mis. `C:\Users\Nama Anda\...`). Salin folder `mingw64` WinLibs ke path tanpa spasi, lalu arahkan Go ke sana:
  ```powershell
  go env -w CC=C:\winlibs\mingw64\bin\gcc.exe CGO_ENABLED=1
  ```
- **Docker Desktop** butuh WSL2 dan fitur *Virtual Machine Platform*: `wsl --install --no-distribution` di terminal admin, lalu **Restart** (bukan *Shut down*, karena Fast Startup tidak benar-benar me-reboot).

## Menjalankan

Salin `.env.example` menjadi `.env`, isi nilainya, lalu set variabelnya di shell (Go tidak membaca `.env` sendiri).

```powershell
# PowerShell
$env:APP_ENV = "development"
$env:DATABASE_URL = "postgres://..."
$env:SUPABASE_URL = "https://<project-ref>.supabase.co"
go run ./cmd/api      # http://localhost:8080/healthz
go run ./cmd/worker
```

`/healthz` hanya mengecek proses hidup. `/readyz` juga mengecek database dan menjawab 503 selama database belum bisa dihubungi. `api` menolak start kalau migrasi belum dijalankan (tenant default belum ada).

## Kontrak API

```sh
buf lint
buf generate                                  # api/gen/go + api/gen/ts, lalu commit hasilnya
buf breaking --against '.git#branch=main'
```

Coba `WhoAmI` dengan token user sungguhan. Ambil token lewat endpoint Auth memakai publishable key (`sb_publishable_…`, aman dibagikan):

```sh
TOKEN=$(curl -s "$SUPABASE_URL/auth/v1/token?grant_type=password" \
  -H "apikey: $SUPABASE_PUBLISHABLE_KEY" -H "Content-Type: application/json" \
  -d '{"email":"...","password":"..."}' | jq -r .access_token)

curl -s http://localhost:8080/kuepreorder.identity.v1.IdentityService/WhoAmI \
  -H "Content-Type: application/json" -H "Authorization: Bearer $TOKEN" -d '{}'
```

## Staf pertama

Peran `owner` pertama diberikan lewat Supabase SQL editor, setelah user itu mendaftar di Supabase Auth:

```sql
insert into staff_roles (tenant_id, auth_user_id, role)
select '00000000-0000-0000-0000-000000000001', id, 'owner'
from auth.users
where email = 'email-ibu@example.com';
```

## Database

```sh
goose -dir db/migrations postgres "$DATABASE_URL" up   # terapkan migrasi
sqlc generate                                         # setelah mengubah migrasi atau db/queries
```

Migrasi yang sudah di-merge tidak boleh diedit; tambahkan file baru. Setiap tabel baru wajib punya `tenant_id` (kecuali tabel global) dan `enable row level security`, dan integration test skema akan gagal kalau lupa.

## Quality gate

```sh
golangci-lint run
go vet -tags=integration ./...
go test -race ./...                      # unit test
go test -race -tags=integration ./...    # + integration test (butuh Docker)
sqlc diff                                # hasil sqlc sudah ter-commit
buf lint && buf format --diff --exit-code && buf breaking --against '.git#branch=main'
(cd api && npm ci && npm run typecheck)  # client TS lolos tsc strict
```

## CI

`.github/workflows/ci.yml` menjalankan semua perintah di atas pada setiap PR dan push ke `main`, dengan tiga job: `go`, `codegen`, dan `ts`. Kalau CI merah, jalankan perintah yang sama di lokal.

Proteksi `main` (sekali saja, di GitHub → Settings → Branches → *Add branch ruleset* untuk `main`):
- *Require a pull request before merging*
- *Require status checks to pass*: pilih `Go (tidy, vet, lint, test)`, `Codegen (sqlc, buf, workflows)`, `TypeScript client`
- *Block force pushes*

Dependabot membuka PR pembaruan dependency tiap Senin pagi (Go, npm, Actions; masing-masing dikelompokkan). Versi tool di `ci.yml` (golangci-lint, sqlc, buf, actionlint) dan plugin `protoc-gen-es` di `buf.gen.yaml` diperbarui manual, bersama tabel di atas.

Batas modul (§5) ditegakkan oleh `depguard`/`forbidigo` di `.golangci.yml` dan oleh `internal/archtest`.
