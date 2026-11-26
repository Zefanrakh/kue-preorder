# catalog

## Handoff

- **Posisi:** skema, service, dan API admin + storefront selesai (M1.3–M1.7); `Reader` dipakai `aggregation`, `orders`, `inventory`, `procurement`. `FitPreview` mengembalikan titik kurva untuk grafik fit (M5.2, #51).
- **Berikutnya:** celah API 5 (foto produk dan bukti bayar, `docs/screens.md`), belum ada issue. Layar CMS C2–C4 (M5, menunggu desain disetujui).
- **Jebakan:** `version` resep hanya naik pada perubahan nyata (Traps 1). Varian nonaktif tetap dibaca agregasi (Traps 2). `FitPreview` tidak gagal hanya karena tidak ada model yang cocok (Traps 3).
- **Menunggu keputusan:** tidak ada.

## Tujuan

Mengelola apa yang dijual toko dan bahan pembuatnya: produk → varian → komponen → bahan, beserta supplier dan kemasan. Resep dua tingkat: varian ke komponen linear (`units_per_item`), komponen ke bahan lewat model non-linear dari engine `recipe`. Desain: §6 dan §9.3 `docs/architecture.md`; aturan model: §10.

## Data

Tabel dan aturan skema di §9.3 (migrasi `00003_catalog.sql`). Yang perlu diingat saat mengubah kode:

- Unik per tenant (bukan global), dan foreign key komposit `(tenant_id, …_id)` sehingga resep tidak bisa menyeberang tenant.
- `component_ingredients.params` selalu bentuk kanonik dari `recipe.Params`; `measured_points` maksimal `recipe.MaxPoints`.
- Perubahan harga varian dicatat di `audit_log` (`catalog.variant.price_changed`, alasan wajib, §22).
- Setiap perubahan resep menulis `catalog.recipe_changed` ke `outbox` di transaksi yang sama (§9.3, "Aturan menyimpan resep").

## Alur

1. **CMS** → `connect.Handler` (terjemahan proto saja) → `catalog.Service` (otorisasi, normalisasi, validasi dengan pesan berbahasa Indonesia) → `catalog.Repository` (`postgres.Repository`, sqlc).
2. **Simpan baris resep** (`SetRecipeLine`): `RecipeLineInput.resolve` memakai `recipe.Build` + `recipe.Validate`, atau fit dari titik ukur. Yang gagal tidak tersimpan; pesannya dari `ExplainRecipeError`.
3. **Pratinjau fit** (`FitPreview`): `recipe.FitAll` untuk ketiga model, tanpa menyimpan. Setiap hasil membawa parameter, R², selisih per titik, dan kurva (`recipe.Curve`) untuk digambar CMS tanpa menghitung rumus di browser.
4. **Storefront** (`catalog.Storefront`): produk aktif dan variannya, dibaca dengan satu `join`.
5. **Modul lain** membaca lewat `catalog.Reader`, tidak lewat paket `postgres`.

## Akses

- `kuepreorder.catalog.v1.CatalogAdminService` (staf, §8): owner mengelola produk, varian, harga, supplier, dan kemasan; owner dan kitchen mengelola komponen, bahan, dan resep (termasuk `FitPreview`); semua staf boleh membaca.
- `kuepreorder.catalog.v1.StorefrontService` (publik): `ListShopProducts`, `GetShopProduct`, tanpa login dan boleh di-cache CDN 1 menit (`cacheable` di `cmd/api/main.go`).

## Peta kode

| File | Isi |
|---|---|
| `internal/catalog/domain.go` | tipe katalog dan `…Input` beserta validasinya, `BaseUnit.Rounding`, `LeftoverPolicy`, `PriceChange` |
| `internal/catalog/service.go` | `Service`, `authorize` (tabel peran §8), CRUD produk sampai kemasan, `ChangeVariantPrice` |
| `internal/catalog/recipes.go` | `VariantComponent`, `RecipeLine`, `RecipeLineInput.resolve`, `ExplainRecipeError` |
| `internal/catalog/service_recipes.go` | komposisi varian, baris resep, `FitPreview` |
| `internal/catalog/reader.go` | `Reader` untuk modul lain: `ComponentUses`, `RecipeModels`, `DefaultPacks`, `Labels`, `Variants`, `IngredientCosts`; `BrokenRecipeError` |
| `internal/catalog/storefront.go` | `Storefront`: `Products`, `Product` |
| `internal/catalog/repo.go` | interface `Repository` |
| `internal/catalog/postgres/` | `Repository` (sqlc), outbox dan audit di transaksi yang sama |
| `internal/catalog/connect/` | `Handler`, `StorefrontHandler`, `convert.go` (proto ↔ domain), `errors.go` (`params` → `params_json` di `FieldErrors`) |

**Dipakai oleh:**
- `aggregation`: `Reader` (`ComponentUses`, `RecipeModels`, `DefaultPacks`, `Labels`), `BrokenRecipeError`, `BaseUnit`.
- `orders` (checkout, DP): `Reader.Variants`, `Reader.IngredientCosts` (§14).
- `inventory`: `Reader.Labels`, `Ingredient`, `LeftoverPolicy`, `BaseUnit`.
- `procurement`: `Reader.Labels`, `DefaultPack`, `Supplier`.

## Verifikasi

```bash
go test -race -tags=integration ./internal/catalog/...   # butuh Docker
```

- `domain_test.go`: validasi input dan pesan per field.
- `service_integration_test.go`: tabel peran §8, data tenant lain tidak terlihat, harga yang diaudit.
- `recipes_integration_test.go`: versi resep hanya naik pada perubahan nyata (dan event `outbox`-nya), resep tidak menyeberang tenant, `FitPreview`, `Reader` (termasuk `BrokenRecipeError` dan `IngredientCosts`).
- `postgres/catalog_integration_test.go`: constraint skema dan query baca agregasi.
- `connect/handler_integration_test.go`: API ujung ke ujung, `FieldErrors`, kurva `FitPreview`; `storefront_integration_test.go`: hanya yang aktif tampil.
- Kurva dan fit sendiri diuji di `internal/recipe` (`curve_test.go`, `fit_property_test.go`).

## Celah

- `ListRecipeLines` tidak membawa kurva untuk baris yang sudah tersimpan; belum ada layar yang memintanya. Kalau dibutuhkan, panggil `recipe.Curve` dengan model hasil `Build` di sana juga.
- Foto produk: `image_path` ada, tetapi bucket Storage dan aturan upload belum ada (celah API 5).

## Traps

1. **`version` resep hanya naik pada perubahan nyata.** Perbandingannya memakai `jsonb` dan `numeric` di SQL, jadi `100` vs `100.0` atau urutan kunci yang berbeda bukan perubahan, dan tidak menulis event `outbox`. Membandingkan teks JSON di Go akan menaikkan versi dan memicu recompute batch tanpa alasan.
2. **Varian nonaktif tetap ikut query agregasi** (`ComponentUses`, `RecipeModels`). Order yang masuk sebelum varian dimatikan tetap harus diproduksi; jangan menambah filter `is_active` di sana. Storefront yang menyaring.
3. **`FitPreview` tidak gagal hanya karena tidak ada model yang cocok.** Alasan gagal ada per model (`FitResult.error`, berbahasa Indonesia); error RPC hanya untuk otorisasi. Kurva kosong pada model yang gagal.

## Changelog

- 2026-11-26 · #51 · `FitResult.curve`: titik kurva setiap model yang cocok, dihitung `recipe.Curve` (celah API 7). Dokumen modul ini dibuat.
- 2026-09-29 · #23 · `Reader.Labels` untuk daftar belanja (M3.1).
- 2026-09-26 · #15 · `Reader.IngredientCosts` untuk penawaran order dan DP (M2.3).
- 2026-09-25 · #12 · Storefront API publik (M1.7).
- 2026-09-25 · #11 · Connect API admin (M1.6).
- 2026-09-25 · #10 · Service resep, event outbox, `Reader` (`ComponentUses`, `RecipeModels`, `DefaultPacks`, `Variants`) (M1.5).
- 2026-09-25 · #9 · Service katalog dengan cek peran dan harga yang diaudit (M1.4).
- 2026-09-25 · #8 · Skema katalog dan query baca agregasi (M1.3).
