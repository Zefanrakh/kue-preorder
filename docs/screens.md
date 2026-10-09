# Rencana layar M5

Daftar layar untuk tiga area M5: PWA ibu, storefront, dan CMS. Setiap layar dipetakan ke RPC yang sudah ada di `api/proto`. Yang belum disediakan Go ditandai **CELAH** dan dikumpulkan di bagian akhir.

Dokumen ini hanya membahas **isi** layar: data, aksi, keadaan, dan teks. Tampilannya diatur di Design System dan mockup (tautan di §26.1 `docs/architecture.md`). Arah gaya dan batasan ada di `docs/design/brief.md`.

Aturan yang berlaku di semua layar (`.agents/rules/web-frontend.md`):
- Semua data dan aksi lewat client Connect hasil codegen. Frontend tidak menghitung harga, total, DP, biaya admin, atau jumlah bahan; angka-angka itu datang dari Go.
- Uang ditampilkan `Rp150.000`, waktu dalam WIB.
- Penolakan `FailedPrecondition` membawa pesan berbahasa Indonesia dari Go; tampilkan apa adanya. `InvalidArgument` membawa `FieldErrors`; tampilkan di samping kolomnya.
- `Unauthenticated` → kembali ke layar masuk. `PermissionDenied` → "Akun ini tidak punya akses ke halaman ini."

## Label status

Dipakai sama di ketiga area.

| Jenis | Nilai | Label |
|---|---|---|
| Order | `AWAITING_DP` | Menunggu DP |
| | `CONFIRMED` | Dikonfirmasi |
| | `EXPIRED` | Kedaluwarsa |
| | `IN_PRODUCTION` | Sedang dibuat |
| | `READY` | Siap diambil |
| | `OUT_FOR_DELIVERY` | Sedang diantar (M6) |
| | `COMPLETED` | Selesai |
| | `CANCELLED` | Dibatalkan |
| Pembayaran | `UNPAID` | Belum dibayar |
| | `DP_PAID` | DP masuk |
| | `PAID_IN_FULL` | Lunas |
| | `FORFEITED` | DP hangus |
| | `REFUNDED` | Dikembalikan |
| Batch | `OPEN` | Masih menerima pesanan |
| | `LOCKED` | Sudah lewat batas belanja |
| | `IN_PRODUCTION` | Sedang produksi |
| | `DONE` | Produksi selesai |
| Baris belanja | `NEEDED` | Perlu dibeli |
| | `ORDERED` | Sudah dipesan |
| | `RECEIVED` | Sudah datang |
| Lot stok | `AVAILABLE` | Ada |
| | `EXHAUSTED` | Habis |
| | `DISCARDED` | Dibuang |
| | `EXPIRED` | Kedaluwarsa, buang |
| Pesanan pembelian | `ORDERED` | Menunggu datang |
| | `RECEIVED` | Sudah diterima |
| | `CANCELLED` | Dibatalkan |

Status selalu ditampilkan dengan kata (dan ikon), tidak hanya warna.

---

## A. PWA ibu

Peran: `owner` dan `kitchen`. Dipasang di HP sebagai PWA. Takaran gaya: sedikit (brief).

**Navigasi bawah** (4 tab, selalu terlihat, terjangkau ibu jari): **Hari ini** · **Belanja** · **Produksi** · **Stok**.

**Offline** (berlaku di semua layar PWA):
- Data terakhir tetap tampil, dengan keterangan "Terakhir diperbarui 07.42".
- Spanduk di atas: "Sedang offline. Yang Ibu tandai akan dikirim saat sinyal kembali."
- Aksi yang boleh diantre saat offline: `CheckLot`, `ReceiveOrder`, `AdvanceOrder`, `CreateOrder` (pembelian), `CountStock`, dan `ReceiveStock` (setelah **CELAH 6** ditutup). Baris yang masih di antrean diberi tanda "Menunggu sinyal".
- `CompleteBatch` ("Produksi selesai") tidak diantre: perlu online dan konfirmasi.
- Kalau aksi antrean ditolak server saat dikirim, aksi itu pindah ke daftar "Perlu dilihat lagi" di layar Hari ini, dengan pesan dari server. Penolakan yang berarti "sudah terjadi" (`item_closed`, `lot_empty` setelah buang, `nothing_to_order`) dianggap berhasil.

### A1 · Masuk
- **Tujuan**: masuk sekali, lalu tetap masuk di HP itu.
- **Tampil**: logo, kolom email dan kata sandi, tombol "Masuk".
- **RPC**: Supabase Auth (cara masuk staf: lihat **CELAH 4**), lalu `IdentityService.WhoAmI` untuk peran.
- **Keadaan**: akun tanpa peran staf → "Akun ini bukan akun staf toko." Salah kata sandi → pesan dari Supabase.

### A2 · Hari ini
- **Tujuan**: dalam 3 detik ibu tahu apa yang harus dikerjakan sekarang.
- **Tampil**:
  - Sapaan dan tanggal: "Selamat pagi, Bu · Kamis, 8 Oktober".
  - **Daftar tugas** berurutan menurut waktu, masing-masing satu kartu besar yang membuka layarnya:
    - "Cek sisa bahan · 4 bahan" (kalau `ListLotsToCheck` tidak kosong) → A3
    - "Belanja untuk Sabtu · 5 bahan · ±Rp86.000" (batch yang `lines_to_buy` > 0) → A4
    - "Produksi hari ini · 12 pesanan" → A6
    - "Siap diambil · 3 pesanan" → A7
  - **Kartu peringatan** kalau ada batch dengan `error`: "Daftar belanja Sabtu gagal dihitung: <pesan>. Kabari owner." Daftar belanja yang tampil adalah hasil terakhir yang berhasil.
  - "Perlu dilihat lagi" (aksi offline yang ditolak), kalau ada.
- **RPC**: `BatchService.ListBatches` (hari ini sampai 7 hari ke depan), `InventoryService.ListLotsToCheck`, `OrderAdminService.ListOrders` (hari ini; status Dikonfirmasi sampai Siap diambil).
- **Keadaan**: tidak ada tugas → ilustrasi kecil dan "Tidak ada yang perlu dikerjakan sekarang."

### A3 · Cek sisa bahan
- **Tujuan**: sebelum belanja, tandai bahan mudah rusak yang masih bagus atau dibuang (§12).
- **Tampil**: dua kelompok.
  - "Perlu dicek": nama bahan, sisa (`balance` + satuan), tanggal datang, tanggal kedaluwarsa.
  - "Sudah kedaluwarsa, buang" (`expired`): hanya tombol Buang.
- **Aksi**:
  - **Masih bagus** (tombol besar, hijau) → `CheckLot` `OK`. Berlaku 24 jam.
  - **Buang** (merah) → lembar bawah dengan alasan: Bau, Berjamur, Kedaluwarsa, Lainnya (catatan wajib) → `CheckLot` `DISCARD`.
- **RPC**: `InventoryService.ListLotsToCheck`, `CheckLot`.
- **Keadaan**:
  - Kosong → "Semua bahan sudah dicek. Hasil cek berlaku 24 jam."
  - `lot_empty`, `lot_expired` → pesan dari server.
  - Offline → bisa diantre.
- **Teks**: "Bahan yang belum dicek tidak dihitung sebagai stok."

### A4 · Belanja (per tanggal produksi)
- **Tujuan**: tahu apa yang harus dibeli, memesannya per toko, dan mencentang yang datang.
- **Tampil**:
  - **Pilih tanggal**: deretan chip 7 hari ke depan; tanggal tanpa batch diredupkan.
  - **Kepala**: status batch, "Dihitung 07.42", perkiraan biaya (`cost_idr`), dan "2 bahan belum ada harganya" (`unpriced`).
  - **Batch `LOCKED`** → spanduk "Sudah lewat batas belanja. Pesanan baru tidak masuk ke tanggal ini."
  - **Baris belanja dikelompokkan per supplier**, sesuai urutan dari Go; "Belanja sendiri" di akhir. Setiap baris berisi:
    - nama bahan dan status baris
    - "Perlu 1.000 g · dari stok 600 g · dipesan 0 · datang 0"
    - yang harus dibeli: "400 g → 1 × 1 kg · Rp14.000"
  - **Pesanan pembelian tanggal itu** (`ProcurementService.ListOrders`): supplier, status, biaya, jumlah item.
- **Aksi**:
  - **"Pesan ke Toko Sinar · Rp28.000"** di setiap kelompok supplier → `CreateOrder` dengan `supplier_id`. Untuk kelompok tanpa supplier → `CreateOrder` tanpa supplier ("Belanja sendiri").
  - **Terima** (buka pesanan pembelian): checklist per item.
    - Jumlah datang sudah terisi sesuai pesanan dan bisa diubah dengan tombol − / +; "Tidak datang" = 0.
    - "Simpan yang datang" → `ReceiveOrder`.
    - Item yang tidak dicentang tetap menunggu.
  - **Batalkan pesanan** → alasan wajib → `CancelOrder`.
  - **Catat belanja masuk** (tombol mengambang, untuk belanja di luar pesanan) → formulir: bahan (cari), jumlah dan satuan, tanggal (bawaan: sekarang), catatan toko → `InventoryService.ReceiveStock`.
  - **Hitung ulang** (menu ⋯) → `BatchService.RecomputeBatch`.
- **RPC**: `BatchService.GetBatch` / `RecomputeBatch`, `ProcurementService.CreateOrder` / `ListOrders` / `GetOrder` / `ReceiveOrder` / `CancelOrder`, `InventoryService.ReceiveStock`.
- **Keadaan**:
  - Belum ada pesanan untuk tanggal itu (`NotFound`) → "Belum ada pesanan untuk tanggal ini."
  - Pesan dari server: `nothing_to_order`, `batch_done`, `order_closed`, `item_closed`, `broken_recipe`.

### A5 · Stok
- **Tujuan**: melihat sisa bahan dan membetulkan angka kalau berbeda dengan kenyataan.
- **Tampil**:
  - Kolom cari.
  - Daftar bahan: nama, total, "bisa dipakai hari ini" (`usable_today`), tanda "mudah rusak", dan tanda "perlu dicek" (menuju A3).
  - Ketuk bahan → daftar lotnya, terlama dulu: sisa, tanggal datang, kedaluwarsa, cek terakhir, asal (belanja masuk / pesanan / hitung ulang).
- **Aksi**: **Hitung ulang stok** (opname) per bahan → masukkan jumlah yang ada sekarang dan alasan (wajib) → `CountStock`.
  - Layar menampilkan "Tercatat 600 g" di samping kolom isian. Selisihnya tidak dihitung di frontend; hasil dari server langsung ditampilkan.
- **RPC**: `InventoryService.ListStock`, `CountStock`.
- **Keadaan**: belum ada stok → "Belum ada stok tercatat. Catat belanja masuk dari tab Belanja."

### A6 · Produksi (per tanggal, bawaan hari ini)
- **Tujuan**: tahu apa yang harus dibuat, menandai pesanan, dan menutup hari.
- **Tampil**:
  - **Total adonan** dari batch: "Adonan donut · 3 porsi" (`components`).
  - **Jumlah per varian**, misalnya "12 donut coklat, 8 donut keju" → **CELAH 1**. Sampai celah ini ditutup, bagian ini tidak ditampilkan.
  - **Daftar pesanan** menurut jam ambil: kode, nama pelanggan, isi singkat ("Donut coklat +2 lainnya"), jam ambil, status, dan status bayar.
  - Ketuk pesanan → detail (`GetOrder`):
    - isi lengkap dengan jumlahnya
    - **catatan pelanggan disorot**, karena sering berisi tulisan di kue
    - status bayar
- **Aksi**:
  - Per pesanan: **Mulai dibuat** → **Siap diambil** (`AdvanceOrder` `IN_PRODUCTION` / `READY`). Ketukan ganda aman.
  - Pesanan yang belum lunas tetap tampil dengan tanda "Belum lunas". Kalau tombolnya ditekan, server menolak (`not_paid_in_full`) dan pesannya ditampilkan.
  - **Produksi selesai** (di bawah, hanya pada hari produksi atau sesudahnya) → konfirmasi "Stok dikurangi sesuai resep. Tidak bisa dibatalkan." → `CompleteBatch` → hasilnya per bahan:
    - "terpakai 950 g"
    - kalau ada `missing`: "kurang tercatat 120 g, betulkan lewat Hitung ulang stok"
- **RPC**: `BatchService.GetBatch` / `CompleteBatch`, `OrderAdminService.ListOrders` / `GetOrder` / `AdvanceOrder`.
- **Keadaan**: tidak ada pesanan → "Tidak ada pesanan untuk tanggal ini." Pesan dari server: `not_production_day`, `batch_done`.

### A7 · Serah terima
- **Tujuan**: menyerahkan kue kepada pelanggan yang datang, tanpa salah orang dan tanpa yang belum lunas.
- **Tampil**:
  - Kolom cari besar: kode order, nama, atau nomor HP (`ListOrders` `query`).
  - Daftar "Siap diambil hari ini".
  - Kartu pesanan:
    - kode dalam huruf besar ("K7M3QX")
    - nama dan isi pesanan
    - status bayar besar: **Lunas** (hijau, centang), atau **Belum lunas, jangan diserahkan** (merah)
- **Aksi**: **Sudah diambil** → `AdvanceOrder` `COMPLETED`.
- **RPC**: `OrderAdminService.ListOrders` / `GetOrder` / `AdvanceOrder`.
- **Keadaan**: tidak ketemu → "Tidak ada pesanan dengan kode atau nama itu." Pesan dari server: `not_paid_in_full`, `illegal_transition`.

---

## B. Storefront

Peran: siapa saja. Masuk (OTP WhatsApp) hanya saat memesan dan melihat pesanan sendiri (§8). Takaran gaya: penuh.

**Alur sebagai buku cerita**. Langkahnya ditulis sebagai bab, dengan penanda kecil di atas: "Bab satu dari lima". Nomor bab ditulis dengan kata, karena di huruf buku cerita angka "1" terbaca seperti "I".

**Keranjang** disimpan di browser pelanggan; isinya hanya varian dan jumlah. Harga dan total selalu dari `QuoteOrder`.

### B1 · Beranda dan katalog ("Pilih kue")
- **Tampil**:
  - Logo, satu paragraf cerita toko dengan huruf awal besar (isi dari owner: **CELAH 3**).
  - Kartu produk:
    - foto (`image_path`), atau ilustrasi pengganti bertulisan "Foto menyusul"
    - nama
    - "mulai Rp25.000" (varian termurah; Go sudah mengurutkan termurah dulu)
    - "Pesan paling lambat 2 hari sebelumnya" (`min_notice_hours` varian termurah)
- **RPC**: `StorefrontService.ListShopProducts` (tanpa login; di-cache CDN 60 detik).
- **Keadaan**: katalog kosong → "Toko sedang menyiapkan menu."

### B2 · Detail produk
- **Tampil**: foto besar, deskripsi, pilihan varian (rasa, ukuran: `options`), harga varian, jumlah (− / +).
- **Aksi**: **Masukkan ke keranjang**.
- **RPC**: `StorefrontService.GetShopProduct` (berdasarkan slug).
- **Keadaan**: produk tidak dijual lagi (`NotFound`) → "Kue ini sedang tidak tersedia" dengan tautan ke katalog.

### B3 · Keranjang dan waktu ambil (Bab 1 · "Kapan diambil?")
- **Tampil**:
  - Isi keranjang dengan tombol − / +.
  - Pilihan tanggal dan jam ambil. Hari libur dan jam di luar jam ambil seharusnya diredupkan sejak awal (**CELAH 2**). Sampai celah ditutup, pemeriksaan dilakukan lewat `QuoteOrder`.
  - **Ringkasan dari `QuoteOrder`**:
    - per item, subtotal, total
    - DP yang dibayar sekarang (`dp_required_idr`)
    - jadwal: dibuat tanggal (`production_date`), batas bayar DP, batas pelunasan
- **Aksi**:
  - Setiap perubahan isi keranjang atau waktu → `QuoteOrder` lagi.
  - Kalau waktu ditolak (`rejection`): tampilkan pesannya, ditambah tombol **"Pakai Sabtu, 10 Okt · 10.00"** (`suggested_pickup_at`).
  - **Lanjut** → B4, atau langsung B5 kalau sudah masuk.
- **RPC**: `CheckoutService.QuoteOrder` (tanpa login; dibatasi per alamat, `ResourceExhausted` → "Terlalu banyak percobaan, coba lagi sebentar").
- **Keadaan**: `FieldErrors` pada `items` atau `pickup_at` ditampilkan di tempatnya. Keranjang kosong → tautan ke katalog.

### B4 · Masuk dengan WhatsApp (Bab 2 · "Kenalan dulu")
- **Tampil**: kolom nomor HP (boleh `0812-…`), lalu kolom kode 6 digit.
- **Aksi**:
  - **Kirim kode lewat WhatsApp**.
  - Kirim ulang tersedia setelah 60 detik.
  - Kode berlaku 5 menit (§8).
- **RPC**: Supabase Auth (masuk dengan nomor HP).
- **Keadaan**: pesan dari hook ditampilkan apa adanya ("Nomor ini tidak bisa menerima pesan WhatsApp", "Coba lagi sebentar").
- **Catatan**: belum bisa dipakai sungguhan sebelum template Meta disetujui. Untuk development, pakai nomor test Supabase.

### B5 · Checkout (Bab 3 · "Cara bayar")
- **Tampil**:
  - nama (wajib), email (opsional), catatan untuk kue (≤500 karakter, misalnya tulisan di kue)
  - **DP atau lunas**, kalau DP diizinkan. Kalau tidak diizinkan, alasannya ditampilkan (`full_payment_reason`):
    - pesanan kecil → "Pesanan di bawah Rp150.000 dibayar lunas sekaligus."
    - jadwal terlalu dekat → "Waktu ambil terlalu dekat untuk DP, jadi dibayar lunas sekarang."
  - **Pilihan cara bayar** dari `payment_options`, QRIS di atas: "QRIS · biaya admin Rp0", "Transfer bank · biaya admin Rp4.440", dan seterusnya.
  - **Syarat & Ketentuan DP**, dengan kotak centang. Teksnya belum ditulis (§27 "Masih terbuka" no. 2).
- **Aksi**: **Pesan dan bayar** → `PlaceOrder`, dengan `idempotency_key` yang dibuat sekali per checkout dan dikirim ulang saat mencoba lagi.
- **RPC**: `CheckoutService.QuoteOrder` (angka terbaru), `CustomerOrderService.PlaceOrder`.
- **Keadaan**:
  - `too_many_unpaid` → "Masih ada 2 pesanan yang menunggu DP. Bayar atau tunggu salah satunya kedaluwarsa dulu."
  - `phone_required` → kembali ke B4.
  - `FieldErrors` ditampilkan di kolomnya.

### B6 · Bayar (Bab 4 · "Bayar")
- **Tampil**:
  - kode order besar ("K7M3QX") dengan tombol salin
  - yang dibayar sekarang dan biaya admin, terpisah
  - batas waktu bayar (`expires_at` / `dp_due_at`) dengan hitung mundur
- **Aksi**: **Bayar sekarang** → `checkout_url` milik Midtrans.
- **RPC**: `CustomerOrderService.GetMyOrder`. Kalau `checkout_url` masih kosong → "Sedang menyiapkan tagihan…", lalu panggil `GetMyOrder` lagi.
- **Catatan**: menunggu M2.6b (akun Midtrans).

### B7 · Pesanan saya dan lacak pesanan (Bab 5 · "Menunggu kue")
- **Tampil**:
  - **Daftar pesanan** (`ListMyOrders`): kode, isi singkat, jam ambil, status.
  - **Detail** (`GetMyOrder`):
    - **jalan cerita** bergaya ilustrasi: Dipesan → DP masuk → Lunas → Sedang dibuat → Siap diambil → Selesai
    - isi pesanan, total, riwayat pembayaran
    - batas pelunasan
    - **alamat dan jam ambil toko** (**CELAH 3**)
- **Aksi**:
  - **Lunasi sekarang** (tagihan pelunasan, `checkout_url`).
  - **Hubungi toko** lewat WhatsApp (**CELAH 3**).
- **RPC**: `CustomerOrderService.ListMyOrders`, `GetMyOrder`.
- **Keadaan**: belum ada pesanan → ilustrasi dan "Belum ada pesanan. Yuk pilih kue dulu." `NotFound` → "Pesanan tidak ditemukan."

---

## C. CMS

Peran: `owner` mengubah. `kitchen` melihat semuanya, dan mengubah sebagian (lihat tabel §8). Takaran gaya: netral, dengan mode gelap. Tata letak: menu samping di laptop, menu bawah di HP.

Tombol yang tidak boleh dipakai peran itu **disembunyikan**, tidak hanya dimatikan. Go tetap menolak (`PermissionDenied`) sebagai pengaman terakhir.

### C1 · Beranda
- **Tampil**:
  - pesanan hari ini dan besok per status
  - batch 7 hari ke depan dengan biaya belanja
  - batch yang `error`
  - tanggal libur yang "On hold"
- **RPC**: `OrderAdminService.ListOrders`, `BatchService.ListBatches`, `ScheduleAdminService.ListClosedDates`.

### C2 · Order
- **Tampil**:
  - **Tabel**: kode, pelanggan, nomor HP, tanggal produksi, jam ambil, isi singkat, total, status, status bayar.
  - **Saringan**: rentang tanggal (≤92 hari), status (bisa beberapa), dan cari (kode / nama / HP).
  - **Detail**:
    - isi pesanan
    - catatan pelanggan
    - jadwal: ambil, produksi, batas DP, batas pelunasan
    - **riwayat pembayaran** (ledger), termasuk bukti manual: referensi, catatan, siapa
- **Aksi**:
  - Semua staf: langkah produksi (`AdvanceOrder`).
  - **Hanya owner**:
    - **Catat pembayaran manual**: nominal (paling banyak sisa tagihan), nomor referensi (wajib), catatan (wajib) → `RecordManualPayment`. Foto bukti → **CELAH 5**.
    - **Batalkan pesanan**: pilih cara, dengan penjelasan masing-masing.
      - "Belum bayar" (`UNPAID`)
      - "DP hangus" (`FORFEIT`, hanya setelah tenggat pelunasan lewat)
      - "Refund" (`REFUND`, transfer dulu, lalu isi referensi transfer balik)

      Alasan wajib → `CancelOrder`.
- **RPC**: `OrderAdminService` (semua).
- **Keadaan**: semua alasan `Precondition` (§13) ditampilkan apa adanya.

### C3 · Produk dan varian
- **Tampil**:
  - **Produk**: foto, nama, slug, deskripsi, aktif.
  - **Varian**: nama, pilihan (rasa / ukuran), SKU, harga, menit produksi, waktu pesan minimal (jam), aktif.
- **Aksi** (owner):
  - tambah/ubah produk dan varian
  - upload foto (**CELAH 5**)
  - **Ubah harga** → dialog berisi harga lama, harga baru, dan alasan (wajib) → `ChangeVariantPrice`
- **RPC**: `CatalogAdminService` (`ListProducts`, `CreateProduct`, `UpdateProduct`, `ListVariants`, `CreateVariant`, `UpdateVariant`, `ChangeVariantPrice`).
- **Kitchen**: hanya melihat.

### C4 · Resep
- **Tampil**:
  - Per varian: komponen yang dipakai dan jumlahnya per buah (`units_per_item`).
  - Per komponen: baris resep per bahan, yang berisi:
    - model (Lurus `a + b·u`, Pangkat `a·u^b`, Titik ukur, Rumus)
    - parameter
    - faktor susut
    - titik ukur
    - versi
- **Aksi** (owner dan kitchen):
  - atur komponen varian (`SetVariantComponents`, semuanya sekaligus)
  - tambah/ubah/hapus baris resep (`SetRecipeLine`, `RemoveRecipeLine`)
  - **Coba cocokkan** dari titik ukur → `FitPreview` → grafik dan tabel kecocokan:
    - R², selisih per titik, selisih terbesar
    - alasan gagal per model
  - Pilih satu model, lalu simpan.
- **Grafik fit** menampilkan titik ukur dan kurva tiap model. Kurva butuh titik sampel dari Go (**CELAH 7**). Sampai celah itu ditutup, yang digambar hanya titik ukur dan nilai hasil cocok di titik-titik itu (titik ukur + selisih).
- **Keadaan**: rumus ditolak engine → `FieldErrors` pada `params_json` atau `points`, ditampilkan di samping isiannya.
- **RPC**: `CatalogAdminService` (`ListComponents`, `CreateComponent`, `UpdateComponent`, `ListVariantComponents`, `SetVariantComponents`, `ListRecipeLines`, `SetRecipeLine`, `RemoveRecipeLine`, `FitPreview`).

### C5 · Bahan, supplier, kemasan
- **Tampil**:
  - **Bahan**: nama, satuan dasar, mudah rusak, umur simpan, aturan sisa.
  - **Supplier**: nama, nomor WhatsApp, cara pesan (manual / WhatsApp).
  - **Kemasan** per bahan: supplier, isi, satuan, harga, bawaan.
- **Aksi**:
  - Bahan: owner dan kitchen.
  - Supplier dan kemasan: owner. **Jadikan bawaan** → `SetDefaultPack`.
- **RPC**: `CatalogAdminService` (`ListIngredients`, `CreateIngredient`, `UpdateIngredient`, `ListSuppliers`, `CreateSupplier`, `UpdateSupplier`, `ListPacks`, `CreatePack`, `UpdatePack`, `SetDefaultPack`).

### C6 · Batch, belanja, dan stok
- Isi dan aksinya sama dengan A4, A5, dan A6, ditampilkan sebagai tabel lebar untuk laptop. Komponen tampilannya dipakai bersama dengan PWA.
- **RPC**: sama dengan A4 sampai A6.

### C7 · Setelan
- **Pembayaran** (owner mengubah, kitchen melihat):
  - aturan DP: persen minimal (`dp_min_percent`), DP menutup biaya bahan, tenggat pelunasan (jam sebelum produksi), masa berlaku tagihan DP, batas pesanan kecil yang wajib lunas (`dp_min_total_idr`), minimum order
  - per cara bayar: aktif, tarif biaya admin, **contoh biaya dari server** untuk Rp50.000 / Rp150.000 / Rp500.000
  - setiap perubahan wajib alasan; tombol "Kembalikan ke tarif bawaan"
  - cara bayar terakhir yang aktif tidak bisa dimatikan (`last_method`)
- **Jadwal** (owner mengubah, kitchen melihat):
  - waktu belanja sebelum produksi (jam)
  - jam ambil
  - kapasitas harian, dengan keterangan "disimpan, belum dipakai" (§27)
- **Tanggal libur** (owner dan kitchen):
  - daftar dengan label untuk pelanggan ("Libur Lebaran")
  - **On hold** kalau masih ada order aktif: "On hold · 3 order perlu dipindah"
  - tambah dan buka lagi
- **RPC**: `PaymentSettingsService` (semua), `ScheduleAdminService` (semua).

### C8 · Staf
- **Tampil**: daftar staf: nama/email, peran (owner / kitchen), sejak kapan.
- **Aksi** (owner): undang staf, ubah peran, cabut akses. Owner terakhir tidak bisa dicabut.
- **RPC**: **CELAH 4**.

### C9 · Jadwal ulang massal (backend M6)
- **Tampil**:
  - pilih tanggal yang terdampak (misalnya hari libur dadakan)
  - **pratinjau dampak**:
    - jumlah order dan pelanggan
    - uang yang sudah masuk
    - tanggal pengganti yang ditawarkan
    - pratinjau pesan permintaan maaf WhatsApp
- **Aksi**: kirim tawaran ke semua pelanggan terdampak.
- **RPC**: belum ada; dibangun di M6 (§16). Layar ini didesain sekarang supaya alurnya sudah teruji di mockup.

---

## Celah API

Setiap celah di bawah dipastikan dengan membaca `api/proto` (Oktober 2026). Penutupnya dikerjakan sebagai potongan backend M5, dengan alur `implement-milestone`, sebelum layar yang membutuhkannya dibangun.

| No | Celah | Dipakai di | Usulan penutup |
|---|---|---|---|
| 1 | **Jumlah per varian per tanggal produksi.** `BatchDetail` hanya berisi total komponen; `StaffOrderSummary` hanya berisi item pertama dan jumlah item. | A6 | Tambah `variant_totals` (nama produk, nama varian, jumlah) ke `BatchDetail`, dihitung dari order yang masuk batch |
| 2 | **Jadwal publik.** Jam ambil dan tanggal libur hanya bisa dibaca staf (`ScheduleAdminService`), jadi kalender storefront tidak bisa meredupkan hari libur sejak awal. | B3 | `StorefrontService.GetShopSchedule`: jam ambil, tanggal libur beserta labelnya, dan hari yang sudah penuh, dalam rentang tanggal |
| 3 | **Profil toko publik**: cerita toko, alamat, tautan peta, nomor WhatsApp toko. | B1, B7 | Setelan profil toko (owner, di C7) dan `StorefrontService.GetShopProfile` |
| 4 | **Staf**: cara masuk staf belum ditetapkan; belum ada RPC untuk kelola staf (§8: "owner mengelola staf lewat CMS (M5)"). | A1, C8 | Usul: staf masuk dengan email dan kata sandi Supabase (tidak bergantung template WhatsApp). `IdentityService`: `ListStaff`, `InviteStaff`, `SetStaffRole`, `RemoveStaff`, dengan pengaman owner terakhir |
| 5 | **Foto.** `image_path` sudah ada di produk, tetapi bucket Storage dan aturan siapa boleh meng-upload belum ada. Foto bukti pembayaran manual belum punya tempat di `RecordManualPayment` (§14). | C2, C3 | Bucket `products` (publik baca, staf tulis) dan `payment-proofs` (privat); kolom `proof_path` di pembayaran manual |
| 6 | **Kirim ulang dari antrean offline.** `ReceiveStock` tidak punya kunci idempotensi, jadi belanja masuk yang terkirim dua kali menjadi dua lot. `CountStock` yang terkirim dua kali mencatat dua audit. | A4, A5 | `idempotency_key` di `ReceiveStockRequest` dan `CountStockRequest`, seperti `PlaceOrderRequest` |
| 7 | **Kurva grafik fit.** `FitPreview` mengembalikan parameter dan selisih per titik, tetapi tidak ada titik kurva. Frontend tidak boleh menghitung rumus sendiri. | C4 | Tambah `curve` (titik `units`, `amount` pada rentang yang sama dengan `recipe.Validate`) ke `FitResult` |
| 8 | **Ganti cara bayar pelunasan** (§27: M2.6b atau M5). | B7 | Bersama M2.6b |

Bukan celah API, tetapi menunggu keputusan atau isi dari pemilik:
- **Teks Syarat & Ketentuan DP** (§27 "Masih terbuka" no. 2), dipakai di B5.
- **Foto kue asli** untuk katalog (B1, B2).
- Logo versi SVG, versi lambang saja, dan versi untuk latar gelap (brief).
