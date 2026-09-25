---
trigger: glob
globs: "**/*.go"
---

# Aturan kode Go

- Error dibungkus dengan konteks: `fmt.Errorf("recompute batch %s: %w", date, err)`. Tidak ada `panic` di jalur normal.
- Sentinel error didefinisikan di paket domain (`var ErrInvalidResult = errors.New(...)`).
- `context.Context` selalu jadi parameter pertama untuk fungsi yang melakukan I/O.
- Struktur per modul: `domain.go`, `service.go`, `repo.go` (interface), `postgres/` (implementasi), `connect/` (handler).
- Handler Connect hanya validasi input + panggil service. Tidak ada logika bisnis di handler.
- Transaksi lewat helper `platform/db` (`db.InTx(ctx, func(tx) error)`), bukan `Begin` manual di banyak tempat.
- Query ditulis di `db/queries/*.sql` dan di-generate sqlc. Tidak ada SQL string bebas di kode Go, kecuali di `platform`.
- Uang: `int64` rupiah. Gram hasil agregasi: `int64`. Hasil model resep: `float64` hanya di dalam `recipe`.
- Waktu: ambil dari `clock.Clock` yang di-inject. Test memakai jam palsu.
- Logging: `slog` dengan field terstruktur (`slog.String("order_id", id)`), selalu lewat method `...Context(ctx, ...)` supaya correlation id dan trace id ikut.
- Level log punya arti: **ERROR = alert** (otomatis menjadi event Sentry). Pakai ERROR hanya untuk kegagalan yang perlu ditindaklanjuti manusia; gangguan yang lumrah (token kedaluwarsa, input salah, retry yang masih berjalan) pakai WARN/INFO. Jangan pernah me-log token, password, atau secret.
- Test: table-driven. Integration test yang butuh DB memakai testcontainers dan build tag `integration`.
- Nama test menjelaskan perilaku: `TestRecomputeBatch_SharedComponentUsesCombinedUnits`.
