---
description: Kerjakan satu milestone atau potongan milestone dari docs/architecture.md secara bertahap
---

# Implement milestone

Input: nama milestone atau potongannya (mis. "M1 recipe: model + fit").

1. Baca §26 (roadmap) di `docs/architecture.md` dan semua bagian yang dirujuk milestone ini.
   Kalau milestone punya **Prasyarat** di §26, cek dulu. Kalau belum terpenuhi, berhenti dan ingatkan pengguna; jangan lanjut menulis kode.
2. Tulis rencana singkat: file yang dibuat/diubah, migrasi, perubahan `.proto`, dan daftar test (termasuk invariant dari dokumen). Tunggu persetujuan sebelum menulis kode.
3. Kerjakan dalam urutan: migrasi → query sqlc → domain + service → test → handler Connect → frontend (kalau ada).
4. Untuk modul inti (`recipe`, `aggregation`, `inventory`, `payments`, `scheduling`), test ditulis sebelum atau bersamaan dengan implementasi.
5. Jalankan quality gate:
   - `golangci-lint run`
   - `go vet ./...`
   - `go test -race ./...`
   - `buf lint` dan `buf breaking` kalau `.proto` berubah
   - `sqlc generate` tanpa perubahan tersisa
6. Kalau implementasi menuntut perubahan desain, perbarui `docs/architecture.md` di commit yang sama dan sebutkan bagiannya.
7. Ringkas hasil: apa yang selesai, test apa yang ditambahkan, apa yang tersisa untuk potongan berikutnya.
