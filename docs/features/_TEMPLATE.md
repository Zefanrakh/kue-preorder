# <modul>

<!--
Cara memakai template ini:
- Salin ke docs/features/<modul>.md saat modul itu pertama kali disentuh.
- Handoff paling banyak 10 baris. Ditimpa setiap PR yang menyentuh modul ini,
  bukan ditambah di bawahnya: isinya keadaan sekarang, bukan riwayat.
- Riwayat masuk Changelog (satu baris per PR, terbaru di atas).
- Desain tetap di docs/architecture.md. Bagian lain cukup merujuk (§N) dan
  menambahkan yang tidak ada di sana: peta kode, cara verifikasi, jebakan.
- Jebakan teknis yang perlu lebih dari satu baris ditulis di Traps; Handoff
  hanya menunjuk ke sana.
- Hapus komentar ini dari salinan.
-->

## Handoff

- **Posisi:** <apa yang sudah jalan dan di-merge; PR terakhir>
- **Berikutnya:** <potongan kerja berikutnya untuk modul ini, dengan nomor issue>
- **Jebakan:** <satu baris per jebakan; detail di Traps>
- **Menunggu keputusan:** <keputusan bisnis atau desain yang belum diambil, atau "tidak ada">

## Tujuan

<Satu paragraf: masalah apa yang diselesaikan modul ini. Rujuk §N architecture.md.>

## Data

<Tabel, kolom penting, dan aturan datanya. Rujuk §9.x; tulis di sini hanya yang tidak ada di sana.>

## Alur

<Langkah utama dari input sampai efek, termasuk siapa memanggil siapa.>

## Akses

<Siapa yang boleh memanggil apa: peran staf, pelanggan, job, webhook. Endpoint Connect bila ada.>

## Peta kode

| File | Isi |
|---|---|
| `internal/<modul>/...` | |

**Dipakai oleh:** <modul lain dan simbol yang dipakainya>

## Verifikasi

<Test yang menjaga invariant modul ini dan perintah menjalankannya.>

## Celah

<Keterbatasan yang diketahui dan sengaja dibiarkan, beserta alasannya.>

## Traps

<Jebakan teknis yang sudah pernah menggigit atau hampir menggigit, masing-masing dengan akar masalahnya.>

## Changelog

<!-- Terbaru di atas. Satu baris per PR: tanggal · PR/commit · apa yang berubah. -->

- YYYY-MM-DD · #N · <ringkasan>
