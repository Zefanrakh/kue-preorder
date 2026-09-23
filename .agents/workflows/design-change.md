---
description: Catat perubahan desain atau keputusan baru ke docs/architecture.md sebelum menulis kode
---

# Design change

Input: perubahan yang diinginkan (mis. "refund lewat Xendit disbursement").

1. Cari bagian `docs/architecture.md` yang terdampak (model data, lifecycle, job, test, env var, keputusan §27).
2. Tulis dampaknya: tabel/kolom yang berubah, aturan penjaga, job baru, test baru, risiko ke data yang sudah ada.
3. Tunggu persetujuan.
4. Perbarui semua bagian yang terdampak di dokumen, termasuk tabel keputusan §27 (pindahkan dari "Masih terbuka" ke "Sudah ditetapkan" bila relevan).
5. Kalau aturan yang selalu berlaku ikut berubah, perbarui juga `.agents/rules/00-project.md` (maks. 12.000 karakter).
6. Commit dokumen terpisah dari kode: `docs: <ringkasan perubahan>`.
