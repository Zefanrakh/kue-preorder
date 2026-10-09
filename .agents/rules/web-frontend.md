---
trigger: glob
globs: "web/**"
---

# Aturan frontend (Next.js di `web/`)

- `web/` hanya tampilan. Semua data dan aksi lewat client Connect hasil codegen di `api/gen/ts`.
- Dilarang: akses database langsung, Supabase client untuk data bisnis, atau menghitung harga/total/bahan di frontend. Supabase di frontend hanya untuk login dan upload foto ke Storage.
- JWT Supabase dikirim di header setiap panggilan ke Go.
- Angka uang diterima sebagai integer rupiah dan hanya diformat untuk tampilan (`Intl.NumberFormat('id-ID', { style: 'currency', currency: 'IDR', maximumFractionDigits: 0 })`).
- Waktu ditampilkan dalam WIB (`Asia/Jakarta`).
- Teks UI dalam bahasa Indonesia, kalimat biasa (bukan Title Case).
- PWA dashboard ibu: tombol besar, bisa dipakai satu tangan, tetap bisa checklist saat offline lalu sinkron.
- Checkout delivery wajib pin lokasi di peta (koordinat dibutuhkan kurir instan Biteship).
- Tampilan mengikuti Design System Zielfan (tautan di §26.1 `docs/architecture.md`): warna, huruf, spasi, dan sudut dari token-nya, bukan nilai yang ditulis langsung. Isi tiap layar mengikuti `docs/screens.md`.
- Takaran gaya per area: storefront penuh (kertas, hiasan, sudut gambar tangan), PWA ibu sedikit (palet dan huruf saja; tombol 56 px, teks minimal 17 px), CMS netral dengan tema gelap.
- Ilustrasi hanya yang dibuat sendiri (`docs/design/illustrations/`). Dilarang memakai karakter, nama, atau gambar dari buku cerita mana pun (lihat `docs/design/brief.md`). Foto produk wajib foto kue asli.
- Grafik data memakai palet grafik tervalidasi (biru, oranye, aqua), bukan warna buku cerita.
