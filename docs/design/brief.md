# Brief desain — Zielfan Cake & Bakery

Dokumen ini adalah titik awal desain M5 (§26.1 `docs/architecture.md`). Isinya: untuk siapa yang dirancang, arah gaya, dan batasan yang wajib. Rencana layar ada di `docs/screens.md`. Design System dan mockup ada di artifact yang tautannya dicatat di §26.1.

## Toko

- **Nama**: Zielfan Cake & Bakery, sesuai tulisan di logo. Nama file logo semula menyebut "Coffee & Bakery"; yang dipakai adalah tulisan di logo.
- **Yang dijual**: kue preorder (contoh di data uji: donut, bolu). Pelanggan mengambil sendiri sampai M6; delivery lewat Biteship menyusul.
- **Logo**: `docs/design/brand/logo.png`, 1254 × 1254 px, latar transparan.
  - Lambang: huruf Z yang sekaligus cangkir beruap, dengan roti di lengkung bawahnya.
  - Tulisan "Zielfan" memakai serif yang hangat dan membulat; tagline "Cake & Bakery" memakai sans-serif berjarak lebar.
  - Warna dari piksel logo: coklat tua `#512914`, karamel `#BF843A`, coklat roti `#C49765`.
  - Belum ada versi SVG, versi lambang saja, atau versi untuk latar gelap. Ikon PWA dan favicon sementara dipotong dari lambangnya.

## Pengguna

| Pengguna | Perangkat | Keadaan saat memakai | Yang paling penting |
|---|---|---|---|
| **Ibu** (`kitchen`) | HP, dipasang sebagai PWA | Di dapur: tangan bertepung, satu tangan memegang sesuatu, sering terburu-buru, sinyal kadang hilang | Langsung tahu apa yang harus dikerjakan hari ini; tombol besar; checklist tetap jalan saat offline |
| **Pelanggan** | HP, lewat browser (sering dibuka dari tautan WhatsApp) | Santai, memilih kue untuk acara | Kuenya menggugah, harga dan DP jelas, tahu kapan bisa diambil |
| **Owner** | Laptop, kadang HP | Mengatur katalog, resep, harga, uang | Data padat tapi rapi; tidak salah klik untuk urusan uang |

**Asumsi perangkat** (diganti kalau ternyata lain):
- HP ibu: Android kelas menengah, layar sekitar 6,5 inci. PWA tetap diuji di iPhone (Safari), karena di sana instal dan offline lebih terbatas.
- CMS: dirancang untuk laptop (lebar 1280 px), tetapi tetap bisa dipakai di HP (375 px) tanpa scroll ke samping.

## Arah gaya: buku cerita klasik

Suasana yang dituju adalah **membuka buku cerita lama yang hangat**: ilustrasi pensil dan cat air, kertas krem, madu, pagi yang tenang. Inspirasinya suasana buku Winnie-the-Pooh klasik (A.A. Milne, ilustrasi E.H. Shepard), **bukan** versi Disney.

| Unsur | Wujudnya |
|---|---|
| Latar | Kertas krem dengan butiran halus, seperti halaman buku tua |
| Garis | Tinta coklat yang sedikit tidak rata, seperti digambar tangan |
| Warna | Sapuan cat air yang lembut dan pucat: madu, hijau sage, biru langit, merah bata pucat. Coklat tinta dari logo untuk teks. |
| Huruf | Serif yang hangat untuk judul (mirip tulisan "Zielfan" di logo); sans-serif yang ramah dan mudah dibaca untuk teks |
| Hiasan | Pembatas berornamen kecil, huruf awal besar (*drop cap*) di cerita toko, ilustrasi kecil: lebah, tetesan madu, gandum, uap, irisan kue |
| Nada bahasa | Hangat dan sopan, seperti toko kue keluarga. Tidak berlebihan dan tidak memakai bahasa gaul. |

### Takaran per area (disetujui)

| Area | Takaran | Artinya |
|---|---|---|
| **Storefront** | Penuh | Latar kertas, hiasan, ilustrasi, langkah checkout sebagai "bab" buku cerita |
| **PWA ibu** | Sedikit | Palet warna dan huruf yang sama, tanpa hiasan yang mengganggu. Yang utama: jelas, besar, cepat |
| **CMS** | Netral | Rapi dan padat: tabel, formulir, grafik. Huruf serif hanya untuk judul halaman. Ada mode gelap. |

## Batasan yang wajib

- **Hanya suasananya, bukan karakternya.** Dilarang memakai:
  - Pooh dan teman-temannya, atau beruang yang mirip dengannya;
  - nama "Pooh", "Winnie", atau "Hundred Acre Wood";
  - guci madu bertulisan "HUNNY";
  - gambar yang meniru ilustrasi Shepard.

  Alasannya: ilustrasi Shepard masih dilindungi hak cipta di Indonesia sampai 2046 (seumur hidup ilustrator ditambah 70 tahun, UU 28/2014; Shepard wafat 1976), dan Disney memegang merek dagang atas nama dan karakternya. Untuk toko yang berjualan, pelanggaran bisa berujung listing diturunkan atau ganti desain setelah kemasan dicetak. Semua ilustrasi dibuat sendiri.
- **Foto produk wajib foto kue asli**, bukan buatan AI (§26.1). Gambar lain (banner, ilustrasi) boleh dibuat dengan model gambar.
- **Gambar referensi** di `docs/design/moodboard/` tidak di-commit (folder itu ada di `.gitignore`), karena biasanya milik orang lain.

## Aksesibilitas

- Kontras teks minimal WCAG AA: 4,5:1 untuk teks biasa, 3:1 untuk teks besar dan batas elemen (garis input, ikon).
- Karamel dan coklat roti dari logo **tidak dipakai untuk teks** di atas krem (kontrasnya 2,96 dan 2,45). Keduanya untuk isian, hiasan, dan latar tombol bertulisan besar.
- Area sentuh minimal 48 × 48 px; di PWA ibu 56 px.
- Teks PWA ibu minimal 17 px; teks storefront dan CMS minimal 16 px.
- Status tidak hanya dibedakan dengan warna, tetapi juga dengan ikon atau kata.
- Gerakan dan animasi mengikuti `prefers-reduced-motion`.

## Bahasa

- Semua teks bahasa Indonesia, dengan kalimat biasa (bukan Title Case): "Pesan sekarang", bukan "Pesan Sekarang".
- Uang: `Rp150.000` (format `id-ID`, tanpa desimal). Waktu: WIB, misalnya "Kamis, 8 Okt · 10.00 WIB".
- Pesan penolakan dari Go (`Precondition`) ditampilkan apa adanya, karena sudah ditulis untuk dibaca manusia.
