# Keputusan arsitektur (ADR)

Folder ini mencatat **keputusan baru** beserta alternatif yang ditolak, supaya
alasan sebuah pilihan tidak hilang bersama riwayat chat.

## Keputusan lama tetap di §27

Keputusan yang diambil sebelum folder ini ada tetap di §27
`docs/architecture.md` ("Sudah ditetapkan", "Default yang dipakai", dan
pertimbangan 27.x). Isinya tidak dipindah ke sini. ADR hanya untuk keputusan
yang diambil setelah SDLC-4 (2026-10-26).

Kalau ADR baru mengubah keputusan di §27, perbarui baris §27 itu di PR yang
sama dan rujuk ADR-nya, supaya §27 tetap menjadi ringkasan yang benar.

## Kapan menulis ADR

Tulis ADR kalau keputusannya:

- mengubah batas modul, kontrak API, skema data, atau aturan di
  `.agents/rules/00-project.md`;
- memilih vendor, layanan berbayar, atau dependency besar;
- memilih satu dari beberapa desain yang sama-sama masuk akal, dan alasannya
  tidak terlihat dari kode.

Detail implementasi yang mudah diubah cukup ditandai `[Operator]` di badan PR.

## Aturan

- Nama file `NNNN-<slug>.md`, nomor naik dari `0001`. Mulai dari `_TEMPLATE.md`.
- ADR ditulis langsung berstatus **diterima**, di PR yang sama dengan perubahan
  yang ditimbulkannya; merge oleh Lead adalah penerimaannya. Status **usulan**
  hanya untuk ADR yang dibuka untuk dibahas dulu, tanpa perubahan lain.
- ADR berstatus **diterima** tidak pernah diedit. Kalau keputusannya berubah,
  tulis ADR baru yang menggantikannya; di ADR lama hanya baris Status yang
  diubah menjadi "digantikan oleh NNNN". Perubahan baris Status itu satu-satunya
  pengecualian, bukan pelanggaran.
- `docs/architecture.md` tetap sumber kebenaran desain. ADR menjelaskan
  *kenapa*; desain yang dihasilkan tetap ditulis di bagian architecture.md
  yang relevan.
