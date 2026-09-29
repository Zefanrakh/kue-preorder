# Template WhatsApp

Daftar template pesan WhatsApp yang dikirim sistem ke pelanggan. Setiap template harus diajukan dan disetujui Meta sebelum bisa dipakai. Dokumen ini adalah acuan bersama:
- **Pemilik** memakainya untuk mengajukan template di WhatsApp Manager.
- **Kode M2.7b** memakai nama template dan urutan variabel yang sama persis.

Kalau teks diubah saat pengajuan, ubah juga dokumen ini di commit yang sama.

## Ringkasan

| # | Nama template | Kategori | Dikirim saat | Tombol |
|---|---|---|---|---|
| 0 | `kode_masuk` | Authentication | Pelanggan minta kode masuk (§8) | Salin kode |
| 1 | `pesanan_diterima` | Utility | Order dibuat, menunggu pembayaran pertama | Bayar sekarang |
| 2 | `pesanan_dikonfirmasi` | Utility | DP masuk | Lunasi sekarang |
| 3 | `pengingat_pelunasan` | Utility | H-2, H-1, dan 3 jam sebelum tenggat pelunasan, hanya kalau belum lunas | Lunasi sekarang |
| 4 | `pesanan_lunas` | Utility | Order lunas: bayar penuh di awal, atau pelunasan masuk | — |
| 5 | `pesanan_siap` | Utility | Ibu menandai order "siap diambil" | — |
| 6 | `pesanan_kedaluwarsa` | Utility | Pembayaran pertama tidak masuk sampai tenggat | — |
| 7 | `pesanan_batal_dp_hangus` | Utility | Pelunasan tidak masuk sampai tenggat; DP hangus | — |
| 8 | `pesanan_dibatalkan_refund` | Utility | Toko membatalkan order dan mengembalikan dana | — |

Template permintaan maaf untuk jadwal ulang ada di §16 `architecture.md`, dan baru diajukan menjelang M6.

Semua template memakai bahasa **Indonesian** (`id`). Variabelnya **bernomor**, `{{1}}`, `{{2}}`, dan seterusnya: pilih "Number" saat membuat template.

## Aturan Meta yang perlu dijaga

Dari [Template review](https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/template-review) dan [Utility templates](https://developers.facebook.com/documentation/business-messaging/whatsapp/templates/utility-templates/utility-templates):
- **Teks tidak boleh diawali atau diakhiri variabel.** Semua teks di bawah sudah memenuhinya.
- **Variabel harus berurutan**, tanpa loncat nomor.
- **Setiap variabel wajib diberi contoh nilai** saat pengajuan. Contohnya tercantum di tiap template.
- **Jangan terlalu banyak variabel** dibanding panjang teks.
- **Template Utility tidak boleh berisi promosi.** Kalau ada promosi, Meta memindahkannya ke kategori Marketing, yang lebih mahal. Jangan menambahkan diskon atau ajakan membeli produk lain.
- Batas panjang: isi maksimal 1.024 karakter, footer 60 karakter, label tombol 25 karakter.

## Tombol link bayar

Template 1, 2, dan 3 punya tombol **URL dinamis**. Alamat dasarnya tetap, dan sistem mengisi ujungnya:

```
https://DOMAIN/bayar/{{1}}
```

`{{1}}` adalah ID pembayaran (acak dan tidak bisa ditebak). Halaman itu langsung mengarahkan pelanggan ke halaman bayar Midtrans untuk tagihan yang sedang terbuka, tanpa perlu login.

**Kenapa tidak langsung ke link Midtrans?**
- Format link Midtrans bisa berubah, dan template yang sudah disetujui tidak bisa diubah sembarangan.
- Kalau nanti ada tombol "ganti cara bayar", link yang sama tetap berlaku.
- Kalau harus login dulu di halaman pesanan, pelanggan perlu kode OTP, yang juga berbayar.

**Akibatnya, template 1–3 baru bisa diajukan setelah domain dibeli**, karena alamat dasar tombol harus diisi saat pengajuan. Ganti `DOMAIN` dengan domain toko. Template 4–8 bisa diajukan kapan saja.

## Isi template

Contoh nilai di bawah memakai pesanan Sari, 20 donut, Rp160.000.

### 0. `kode_masuk` (Authentication)

Teks template Authentication ditetapkan Meta, jadi tidak ditulis sendiri. Saat membuat template ini, pilih:
- Pengiriman kode: **Copy code** (tombol "Salin kode").
- Nyalakan **tambahan keamanan** ("Demi keamanan, jangan bagikan kode ini").
- Nyalakan **masa berlaku kode: 5 menit**.

Nama template ini diisi ke `WHATSAPP_OTP_TEMPLATE` (§8).

### 1. `pesanan_diterima`

**Isi:**
```
Halo {{1}}, terima kasih sudah memesan di {{2}}.

Pesanan {{3}} sudah kami terima dengan total {{4}}. Supaya pesanan dikonfirmasi, mohon bayar {{5}} sebelum {{6}}.

Kalau pembayaran belum masuk sampai waktu itu, pesanan otomatis batal tanpa biaya.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Nama toko | Kue Ibu |
| `{{3}}` | Kode pesanan | K7M3QX |
| `{{4}}` | Total pesanan | Rp160.000 |
| `{{5}}` | Yang dibayar sekarang: DP, atau total kalau bayar penuh | Rp80.000 |
| `{{6}}` | Tenggat pembayaran pertama | Senin, 5 Oktober 2026 pukul 13.00 WIB |

**Tombol:** URL dinamis, label `Bayar sekarang`, alamat `https://DOMAIN/bayar/{{1}}`, contoh `https://DOMAIN/bayar/3f2a9c1e-8b4d-4e6f-9a1b-2c3d4e5f6a7b`.

### 2. `pesanan_dikonfirmasi`

**Isi:**
```
Halo {{1}}, DP {{2}} untuk pesanan {{3}} sudah kami terima. Pesanan dikonfirmasi untuk diambil {{4}}.

Sisa pembayaran {{5}} mohon dilunasi paling lambat {{6}}. Tombol di bawah membuka halaman pelunasan.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | DP yang diterima | Rp80.000 |
| `{{3}}` | Kode pesanan | K7M3QX |
| `{{4}}` | Waktu ambil | Kamis, 8 Oktober 2026 pukul 09.00 WIB |
| `{{5}}` | Sisa pembayaran | Rp80.000 |
| `{{6}}` | Tenggat pelunasan | Rabu, 7 Oktober 2026 pukul 19.30 WIB |

**Tombol:** URL dinamis, label `Lunasi sekarang`, alamat `https://DOMAIN/bayar/{{1}}`.

### 3. `pengingat_pelunasan`

**Isi:**
```
Halo {{1}}, ini pengingat untuk pesanan {{2}} yang akan diambil {{3}}.

Sisa pembayaran {{4}} jatuh tempo {{5}}. Kalau belum lunas sampai waktu itu, pesanan dibatalkan dan DP tidak dapat dikembalikan sesuai syarat dan ketentuan.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Waktu ambil | Kamis, 8 Oktober 2026 pukul 09.00 WIB |
| `{{4}}` | Sisa pembayaran | Rp80.000 |
| `{{5}}` | Tenggat pelunasan | Rabu, 7 Oktober 2026 pukul 19.30 WIB |

**Tombol:** URL dinamis, label `Lunasi sekarang`, alamat `https://DOMAIN/bayar/{{1}}`.

### 4. `pesanan_lunas`

**Isi:**
```
Halo {{1}}, pembayaran pesanan {{2}} sudah lunas. Terima kasih!

Pesanan bisa diambil {{3}} di {{4}}. Sebutkan kode pesanan saat mengambil.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Waktu ambil | Kamis, 8 Oktober 2026 pukul 09.00 WIB |
| `{{4}}` | Alamat pengambilan | Jl. Melati No. 12, Bandung |

### 5. `pesanan_siap`

**Isi:**
```
Halo {{1}}, pesanan {{2}} sudah siap diambil di {{3}}. Sampai jumpa!
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Alamat pengambilan | Jl. Melati No. 12, Bandung |

### 6. `pesanan_kedaluwarsa`

**Isi:**
```
Halo {{1}}, pesanan {{2}} kami batalkan karena pembayaran belum kami terima sampai {{3}}. Tidak ada biaya yang dikenakan.

Silakan pesan lagi kapan saja.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Tenggat pembayaran pertama | Senin, 5 Oktober 2026 pukul 13.00 WIB |

### 7. `pesanan_batal_dp_hangus`

**Isi:**
```
Halo {{1}}, pesanan {{2}} dibatalkan karena pelunasan belum kami terima sampai {{3}}. Sesuai syarat dan ketentuan, DP sebesar {{4}} tidak dapat dikembalikan.

Kalau ada pertanyaan, hubungi kami di {{5}}.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Tenggat pelunasan | Rabu, 7 Oktober 2026 pukul 19.30 WIB |
| `{{4}}` | Uang yang ditahan | Rp80.000 |
| `{{5}}` | Kontak toko | 0812-3456-7890 |

### 8. `pesanan_dibatalkan_refund`

**Isi:**
```
Halo {{1}}, mohon maaf, pesanan {{2}} terpaksa kami batalkan. Seluruh pembayaran sebesar {{3}} sudah kami kembalikan dengan nomor referensi {{4}}.

Kalau ada pertanyaan, hubungi kami di {{5}}.
```

| Variabel | Isi | Contoh |
|---|---|---|
| `{{1}}` | Nama pelanggan | Sari |
| `{{2}}` | Kode pesanan | K7M3QX |
| `{{3}}` | Jumlah yang dikembalikan | Rp160.000 |
| `{{4}}` | Nomor referensi transfer balik | BCA 0412 7788 |
| `{{5}}` | Kontak toko | 0812-3456-7890 |

Alasan pembatalan yang diisi owner di CMS **tidak** dikirim ke pelanggan, karena alasan itu catatan internal untuk audit.

## Perkiraan biaya

Meta menagih per pesan template yang terkirim.
- **Utility dan Authentication di Indonesia**: sekitar **Rp356,65 + PPN 11% ≈ Rp396 per pesan** (tarif per 1 Juli 2026, menurut [ChatMaxima](https://chatmaxima.com/whatsapp-api-pricing/indonesia/) dan [EngageLab](https://www.engagelab.com/blog/whatsapp-business-api-pricing)). Cek tarif resminya di WhatsApp Manager sebelum go-live.
- **Template Utility yang terkirim saat jendela layanan pelanggan masih terbuka gratis** menurut [halaman harga Meta](https://developers.facebook.com/documentation/business-messaging/whatsapp/pricing). Jendela itu terbuka 24 jam setelah pelanggan terakhir mengirim pesan.
- Beberapa sumber pihak ketiga menyebut aturan pesan di dalam jendela berubah mulai 1 Oktober 2026. Halaman resmi Meta belum menyebutkannya, jadi cek lagi sebelum go-live.

| Jenis order | Pesan | Biaya per order |
|---|---|---|
| Bayar penuh di awal | diterima, lunas, siap = 3 | ≈ Rp1.200 |
| Dengan DP, lunas sebelum pengingat | diterima, dikonfirmasi, lunas, siap = 4 | ≈ Rp1.600 |
| Dengan DP, dengan ketiga pengingat | 4 + 3 pengingat = 7 | ≈ Rp2.800 |
| Login pelanggan | 1 kode per login | ≈ Rp400 |

Untuk 100 order sebulan, perkiraannya sekitar Rp150.000–Rp300.000, ditambah biaya kode login.

## Langkah pengajuan

1. Buka **WhatsApp Manager** → **Message templates** → **Create template**.
2. Pilih kategori sesuai tabel di atas: **Utility**, atau **Authentication** untuk `kode_masuk`.
3. Isi **nama** persis seperti di tabel (huruf kecil dan garis bawah) dan bahasa **Indonesian**.
4. Pilih jenis variabel **Number**. Salin isi template, lalu isi **contoh nilai** setiap variabel dari tabelnya.
5. Untuk template 1–3, tambahkan tombol **Visit website** → **Dynamic**, isi label dan alamat dasarnya, lalu isi contoh URL.
6. Ajukan. Hasilnya biasanya keluar dalam hitungan menit sampai 24 jam.
7. Kalau ditolak, baca alasannya, perbaiki teksnya, lalu ubah dokumen ini supaya kode ikut menyesuaikan.

## Yang masih perlu diputuskan

1. **Domain toko**, untuk alamat tombol bayar (template 1–3).
2. **Kontak toko** di template 7 dan 8: nomor mana yang dihubungi pelanggan?
3. **Nomor WhatsApp toko dipakai di mana.** Nomor yang terdaftar di Cloud API tidak bisa dipakai chat biasa di aplikasi WhatsApp, kecuali memakai fitur *coexistence*, yaitu aplikasi WhatsApp Business dan Cloud API pada nomor yang sama. Pilihannya menentukan apakah pelanggan bisa membalas pesan otomatis ini dan dibalas ibu dari HP.
4. **Profil toko di sistem**: nama toko, alamat pengambilan, dan kontak. Isinya dipakai template 1, 4, 5, 7, dan 8. Setelan ini dibuat di M2.7b.
