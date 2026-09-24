---
trigger: always_on
---

# Kue Preorder — aturan proyek (selalu aktif)

Platform preorder kue: storefront (guest + member), recipe engine non-linear,
agregasi batch → daftar belanja bahan, stok berbasis ledger, DP + pelunasan,
jadwal ulang massal, pengiriman Biteship, procurement via WhatsApp.

**Sumber kebenaran desain: `docs/architecture.md`.** Sebelum mengerjakan fitur,
baca bagian yang relevan di dokumen itu. Kalau kode dan dokumen bertentangan,
berhenti dan tanyakan; jangan menebak. Kalau desain berubah, perbarui dokumen
di commit yang sama.

## Stack (sudah diputuskan, jangan diganti tanpa diskusi)

- Backend: Go 1.26+, modular monolith. Binary `cmd/api` dan `cmd/worker`.
- Kontrak API: ConnectRPC + buf. Sumber di `api/proto`, hasil di `api/gen/go` dan `api/gen/ts`.
- DB: Supabase Postgres, akses lewat pgx + sqlc. Migrasi goose di `db/migrations`.
- Job: River. Rumus user: expr-lang/expr (sandbox).
- Auth: Supabase Auth; Go memverifikasi JWT lewat JWKS. Peran di tabel `staff_roles`.
- Frontend: Next.js di `web/`, memakai client TS hasil codegen.
- Integrasi: Xendit, Biteship, WhatsApp Cloud API, Resend.
- Observability: slog (JSON), OpenTelemetry, Sentry.

## Aturan yang tidak boleh dilanggar

**Satu otak**
- Semua logika bisnis ada di Go. `web/` hanya menampilkan dan memanggil Go.
- `web/` tidak boleh mengakses database secara langsung.

**Batas modul** (`internal/`)
- `recipe` adalah engine murni: tidak mengimpor paket `internal/` lain, tanpa DB, tanpa HTTP.
- `aggregation` hanya bergantung ke `recipe` dan interface modul lain.
- Modul tidak pernah mengimpor paket `postgres/` milik modul lain. Lintas modul lewat interface service.

**Uang dan angka**
- Uang selalu `int64` rupiah. Tidak pernah `float`.
- Total dihitung ulang di server; angka dari client tidak dipercaya.
- Hasil rumus dibulatkan per komponen, dijumlahkan sebagai `int64`, dibulatkan ke `pack_size` paling akhir.

**Resep dan agregasi**
- Resep dua tingkat: varian → komponen (linear, `units_per_item`) → bahan (non-linear).
- Rumus non-linear diterapkan ke **total unit komponen per batch**, tidak per order, tidak per varian.
- Recompute batch harus idempoten dan berjalan dalam satu transaksi dengan lock baris batch.
- Baris `batch_requirements` berstatus `ordered`/`received` tidak ditimpa diam-diam.
- Resep gagal dievaluasi → batch ditandai error dan alert. Jangan menebak.

**Stok**
- Stok = jumlah `stock_movements` (append-only). Tidak ada kolom saldo yang di-update.
- Bahan mudah rusak hanya dihitung kalau sudah dicek "masih bagus" dalam 24 jam. Belum dicek = tidak dihitung.

**Pembayaran dan order**
- Tidak ada COD/tempo. `in_production`, `out_for_delivery`, `completed` wajib `paid_in_full`.
- Order masuk agregasi sejak `confirmed` (DP terbayar).
- Transisi status hanya lewat fungsi domain (`order.Transition`) yang menolak transisi ilegal.
- DP dan tenggat pelunasan dikunci saat checkout.

**Integrasi**
- Semua webhook: verifikasi → insert `webhook_events` (dedupe) → efek + outbox, dalam satu transaksi.
- Webhook Biteship tidak punya signature: token rahasia di path + ambil ulang data dari API Biteship sebelum dipercaya.
- Efek lintas sistem ditulis ke `outbox` dalam transaksi yang sama dengan perubahan data.

**Waktu**
- Selalu lewat `platform/clock`, jangan `time.Now()` langsung.
- Simpan `timestamptz` (UTC), tampilkan WIB. Semua jadwal job memakai `Asia/Jakarta`.

**Multi-tenant**
- Setiap tabel milik tenant punya `tenant_id` dan setiap query di-scope dengannya.

## Cara bekerja

1. Kerjakan per milestone (lihat §26 `docs/architecture.md`). Satu PR = satu potongan kecil yang bisa di-review.
2. Untuk `recipe`, `aggregation`, `inventory`, `payments`, `scheduling`: tulis test dulu atau bersamaan. Invariant di dokumen harus punya test.
3. Perubahan skema: migrasi goose baru, lalu `sqlc generate`. Jangan edit migrasi yang sudah di-merge.
4. Perubahan kontrak: edit `.proto`, jalankan `buf generate`, pastikan `buf breaking` lulus.
5. Sebelum menyatakan selesai, jalankan quality gate:
   - `golangci-lint run`
   - `go vet ./...`
   - `go test -race ./...`
   - kode hasil generate (`buf`, `sqlc`) sudah ter-commit
6. Jangan menambah dependency baru tanpa menyebutkan alasannya.
7. Jangan menyimpan secret di repo. Semua lewat environment variable (§28).

## Bahasa

- Kode, nama identifier, dan komentar kode: bahasa Inggris.
- Teks yang dilihat pelanggan dan ibu (UI, notifikasi, template WA): bahasa Indonesia.
- Penjelasan ke developer di chat: bahasa Indonesia.
