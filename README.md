# Kue Preorder

Platform preorder kue dengan perhitungan bahan otomatis dari pesanan.

- Desain lengkap: [`docs/architecture.md`](docs/architecture.md)
- Aturan untuk AI agent (Antigravity dan Claude Code): [`.agents/rules/`](.agents/rules/) dan [`CLAUDE.md`](CLAUDE.md)
- Alur kerja agent: [`.agents/workflows/`](.agents/workflows/)

Status: **M0 (fondasi) selesai**: skeleton + `platform`, migrasi awal + sqlc, kontrak buf + ConnectRPC + verifikasi JWT Supabase, CI, dan observability (OpenTelemetry + Sentry). Berikutnya M1 (engine: `catalog` + `recipe`).

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

## Observability

Tracing dan Sentry mati selama env-nya kosong, jadi tidak perlu akun untuk development.

- **Tracing:** isi `OTEL_EXPORTER_OTLP_ENDPOINT` (dan `OTEL_EXPORTER_OTLP_HEADERS` untuk autentikasi) ke backend OTLP mana pun, misalnya Grafana Cloud atau Honeycomb. Setiap RPC dan query DB menjadi span; log di dalam span membawa `trace_id`.
- **Sentry:** isi `SENTRY_DSN`. Setiap log **ERROR** menjadi event Sentry, termasuk panic yang tertangkap. Karena itu, pakai level ERROR hanya untuk hal yang perlu ditindaklanjuti.

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
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...          # celah keamanan yang sudah dikenal
(cd api && npm ci && npm audit signatures && npm run typecheck)  # client TS lolos tsc strict
```

## CI

`.github/workflows/ci.yml` menjalankan semua perintah di atas pada setiap PR dan push ke `main`, dengan tiga job: `go`, `codegen`, dan `ts`. Kalau CI merah, jalankan perintah yang sama di lokal.

**Aturan merge:** hanya merge PR kalau ketiga job hijau. Repo ini private di paket GitHub Free, yang tidak menyediakan proteksi branch maupun *required status checks*, jadi aturan ini dijaga dengan disiplin, bukan dipaksa oleh GitHub. Kalau repo pindah ke paket berbayar atau menjadi public, aktifkan ruleset untuk `main`: *Require a pull request*, *Require status checks* (ketiga job di atas), dan *Block force pushes*.

Kuota Actions paket Free (2.000 menit/bulan untuk repo private) jauh cukup: satu run sekitar 8 menit. Semua budget billing di-set $0, jadi kelebihan pemakaian dihentikan, bukan ditagih.

## Dependency dan supply chain

Pengaman yang sudah terpasang:
- **Dependabot** membuka PR tiap Senin pagi (Go, npm, Actions; masing-masing dikelompokkan) dengan **cooldown 7 hari** (14 hari untuk versi mayor): versi baru baru diusulkan setelah berumur seminggu. Rilis dari akun maintainer yang dibajak biasanya ketahuan dan ditarik dalam hitungan jam atau hari. Update keamanan tidak ikut ditunda.
- **`api/.npmrc`** mematikan *lifecycle scripts* (`preinstall`/`postinstall`), jalur utama worm npm berjalan saat install, di laptop maupun di CI.
- **CI** memeriksa tanda tangan registry dan provenance npm (`npm audit signatures`), celah yang dikenal di kode Go (`govulncheck`), dan menjalankan action yang dikunci ke commit SHA dengan token hanya-baca dan tanpa secret.
- Modul Go tidak punya script install, dan setiap versi dikunci `go.sum` serta dicocokkan dengan checksum database publik Go.

Aturan untuk PR Dependabot:
1. **Jangan pernah aktifkan auto-merge.**
2. Merge hanya kalau CI hijau.
3. Baca changelog atau release notes versi barunya.
4. Periksa diff `package-lock.json` / `go.sum`: curigai dependency baru yang tidak dijelaskan di changelog, dan paket dengan `"hasInstallScript": true`.
5. Jangan mencoba PR yang mencurigakan dengan `npm install` di laptop.

Pakai `npm ci` untuk memasang dependency; `npm install <paket>` hanya saat memang menambah dependency, dan sebutkan alasannya di PR.

**Kalau ada kabar paket yang kita pakai disusupi:** jangan merge PR-nya (atau revert kalau sudah), lalu ganti semua kredensial di mesin yang sempat memasang versi itu: token GitHub, SSH key, npm token, dan key Supabase.

Versi tool di `ci.yml` (golangci-lint, sqlc, buf, actionlint, govulncheck) dan plugin `protoc-gen-es` di `buf.gen.yaml` tidak diurus Dependabot; perbarui manual bersama tabel di atas.

Batas modul (§5) ditegakkan oleh `depguard`/`forbidigo` di `.golangci.yml` dan oleh `internal/archtest`.
