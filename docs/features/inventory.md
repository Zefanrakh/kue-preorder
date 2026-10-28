# inventory

## Handoff

- **Posisi:** stok berbasis ledger (M3.2), pemakaian FIFO saat produksi selesai (M3.3), penerimaan dari procurement (M4.1). `ReceiveStock` dan `CountStock` punya `idempotency_key` untuk antrean offline PWA (M5.1, #45).
- **Berikutnya:** tidak ada potongan terjadwal. Layar PWA A4/A5 (M5, menunggu desain disetujui) harus membuat satu kunci per isian dan mengirim ulang kunci yang sama.
- **Jebakan:** kunci diklaim sebelum kunci bahan (Traps 1). Body kiriman dibandingkan sebelum default diisi (Traps 2).
- **Menunggu keputusan:** tidak ada.

## Tujuan

Mencatat stok dapur: lot per bahan, ledger pergerakan yang append-only, dan cek "masih bagus" untuk bahan mudah rusak. Stok selalu jumlah `stock_movements`; tidak ada kolom saldo. Desain: §9.4 dan §12 `docs/architecture.md`.

## Data

Tabel di §9.4 (migrasi `00012_inventory.sql`, `00013_procurement.sql` untuk `procurement_item_id`, `00014_inventory_requests.sql`).

`inventory_requests` (M5.1): satu baris per `(tenant_id, rpc, idempotency_key)`. `rpc` adalah `receive` atau `count`. `request` adalah field kiriman pertama sebagai JSON kanonik, dan `result_id` adalah lot baru (receive) atau bahannya (count).

## Alur

1. **Belanja masuk** (`Service.Receive`): validasi → transaksi: klaim kunci → kunci bahan → lot + movement `receive` + (bahan `confirm`) cek ok saat tiba + audit + outbox `inventory.stock_changed`.
2. **Cek stok** (`Service.Check`): "Masih bagus" mencatat cek; "Buang" menulis movement `waste` dan lot menjadi `discarded`.
3. **Stok opname** (`Service.Count`): transaksi: klaim kunci → kunci bahan → selisih keluar dari lot tertua atau masuk ke lot terbaru (atau lot `adjustment` baru) → audit + outbox. Selisih nol tidak menulis apa pun selain klaim.
4. **Kiriman ulang**: kunci yang sudah diklaim dengan body sama tidak menimbulkan efek; `Receive` mengembalikan lot pertama, `Count` mengembalikan stok saat ini. Body berbeda → `ErrKeyReused` (FailedPrecondition `idempotency_key_reused`).
5. **Produksi selesai** (`Consumer.Consume`, dipanggil `aggregation`): FIFO, kunci bahan urut id, yang kurang dilaporkan sebagai `Missing`.
6. **Procurement** (`Receiver.ReceiveOrdered`): seperti belanja masuk, di transaksi procurement, tanpa kunci idempotensi (procurement menjaga state item-nya sendiri).

## Akses

`kuepreorder.inventory.v1.InventoryService`, peran `owner` dan `kitchen` (§8). `Reader`, `Consumer`, dan `Receiver` dipakai in-process tanpa cek peran; pemanggilnya yang sudah mengotorisasi.

## Peta kode

| File | Isi |
|---|---|
| `internal/inventory/domain.go` | Tipe lot, status, sumber, `Request`, error domain |
| `internal/inventory/service.go` | `Service` (RPC staf), `Receiver`, `Consumer`, `Reader`, port `Repository` |
| `internal/inventory/postgres/repo.go` | `Repository` di atas query sqlc (`db/queries/inventory.sql`) |
| `internal/inventory/connect/handler.go` | Handler Connect |
| `internal/inventory/worker/worker.go` | Job `expire-lots` |

**Dipakai oleh:** `aggregation` (`Reader.Usable` sebagai `aggregation.Stock`, `Consumer.Consume`), `procurement` (`Receiver.ReceiveOrdered`), `cmd/worker` (`Reader.ExpireLots`).

## Verifikasi

- `go test ./internal/inventory/`: ledger = stok (property test), FIFO, opname, cek, kiriman ulang (`TestRetry_*`).
- `make test` (integrasi): `internal/inventory/postgres` (append-only, `TestClaimRequest`), `cmd/api` (`TestInventory_*`, termasuk empat kiriman bersamaan dengan kunci sama → satu lot).

## Celah

- `CheckLot` tidak punya kunci: "Buang" yang terkirim ulang ditolak `lot_empty`, dan "Masih bagus" dua kali tidak mengubah stok.
- Klaim tidak pernah dihapus; jumlahnya sebanding dengan isian staf, jadi kecil.

## Traps

1. **Urutan kunci.** Klaim (`ClaimRequest`, insert `on conflict do nothing`) dijalankan sebelum `LockIngredient`. Kiriman kedua menunggu di primary key sampai transaksi pertama selesai, lalu membaca klaimnya. Kalau urutannya dibalik di satu jalur saja, dua jalur bisa saling menunggu.
2. **Body kiriman, bukan hasil default.** `Receive` menyusun body sebelum `received_at` diisi `now`. Kalau body memakai nilai yang sudah di-default, kiriman ulang beberapa menit kemudian akan dianggap berbeda dan ditolak `idempotency_key_reused`.

## Changelog

<!-- Terbaru di atas. Satu baris per PR: tanggal · PR/commit · apa yang berubah. -->

- 2026-10-28 · #45 · `idempotency_key` di `ReceiveStock` dan `CountStock`, tabel `inventory_requests`; dokumen ini dibuat.
