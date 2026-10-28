# recipe

## Handoff

- **Posisi:** engine selesai dan stabil (M1.1, M1.2, M1.6); dipakai `catalog` (simpan + fit) dan `aggregation` (`Quantity`). Perbaikan terakhir: pembulatan `pcs` di jumlah besar (#31).
- **Berikutnya:** tidak ada pekerjaan terjadwal. Modul ini disentuh lagi hanya kalau ada model baru atau bug.
- **Jebakan:** pembulatan ke atas memakai `floatNoise`, bukan `slack` (Traps 1). `Resolve(0) == 0` walau affine punya `a` (Traps 2). Fit wajib lolos `Validate` (Traps 3).
- **Menunggu keputusan:** tidak ada.

## Tujuan

Mengubah jumlah unit **komponen** (adonan, isian, topping) menjadi jumlah satu bahan, dengan rumus non-linear (adonan bersama lebih hemat daripada adonan terpisah). Desain lengkap: §10 `docs/architecture.md`; posisinya di resep dua tingkat: §9.3.

Engine murni: tanpa DB, tanpa HTTP, tidak mengimpor paket `internal/` lain. Penjaganya depguard `recipe-is-pure` di `.golangci.yml` (hanya stdlib, `expr-lang/expr`, dan `pgregory.net/rapid` untuk test) dan `internal/archtest`. Stdlib tetap mencakup `net/http` dan `database/sql`, jadi "tanpa DB, tanpa HTTP" dijaga lewat review.

## Data

Engine tidak menyimpan apa pun. Pemiliknya `catalog`, tabel `component_ingredients` (§9.3, migrasi `00003_catalog.sql`):

| Kolom | Hubungan dengan engine |
|---|---|
| `model_type` | `recipe.ModelType`: `affine`, `power`, `piecewise`, `formula` |
| `params` | JSON yang dibaca `recipe.Build`, ditulis dalam bentuk kanonik oleh `recipe.Params` |
| `measured_points` | titik ukur mentah (`[]recipe.Point`), maks. `recipe.MaxPoints` = 100 |
| `waste_factor` | argumen `Quantity`, harus `>= 1` |

Aturan pembulatan per bahan berasal dari satuan dasarnya: `catalog.BaseUnit.Rounding()` (`pcs` → `RoundUp`, `g`/`ml` → `RoundNearest`).

## Alur

1. **Simpan resep** (`catalog.RecipeLineInput.resolve`): dari `params` → `Build` → `Validate`; atau dari titik ukur → `FitAffine`/`FitPower`/`FitPiecewise`. Hasilnya disimpan lewat `Params`. Baris yang gagal tidak pernah tersimpan (§9.3, "Aturan menyimpan resep").
2. **Pratinjau fit** (`catalog.Service.FitPreview`): `FitAll` mengembalikan ketiga model plus `FitReport` (R², selisih per titik) untuk grafik CMS, tanpa menyimpan.
3. **Hitung batch** (`aggregation`, §11): `catalog.Reader` merakit baris menjadi `recipe.Model` dengan `Build`; baris yang gagal dirakit menjadi `catalog.BrokenRecipeError` (`ErrBrokenRecipe`, §9.3). `aggregation.Totals` (`compute.go`) memanggil `Quantity` dengan **total unit komponen per batch** dan menjumlahkan hasil `int64` lintas komponen menjadi `Need` per bahan. Error dari `Quantity`, dan `BrokenRecipeError` yang dibungkus di `engine.go`, menjadi `RecipeError`: batch ditandai error, tidak ditebak. Pembulatan ke `pack_size` terjadi belakangan, di `aggregation.Shop` (`packsFor`) setelah stok dikurangi.

## Akses

Engine tidak punya endpoint. Endpoint yang memakainya ada di `kuepreorder.catalog.v1.CatalogAdminService` (`SetRecipeLine`, `RemoveRecipeLine`, `ListRecipeLines`, `FitPreview`), untuk peran `owner` dan `kitchen` (§8).

## Peta kode

| File | Isi |
|---|---|
| `internal/recipe/recipe.go` | `Model`, `ModelType`, error bersama, `evaluate` (kontrak bersama: `u` valid, `Resolve(0) == 0`, hasil finite dan `>= 0`) |
| `internal/recipe/models.go` | `AffineModel`, `PowerModel`, `PiecewiseModel`, `Point`, `MaxPoints` |
| `internal/recipe/formula.go` | `FormulaModel`: sandbox `expr`, fungsi yang diizinkan, batas panjang dan node AST |
| `internal/recipe/build.go` | `Build`: `model_type` + `params` → `Model`, decode ketat |
| `internal/recipe/params.go` | `Params`: `Model` → `params` kanonik (kebalikan `Build`) |
| `internal/recipe/validate.go` | `Validate` (u = 0,5..100, langkah 0,5), `slack` |
| `internal/recipe/quantity.go` | `Quantity`, `Rounding`, `floatNoise`, batas 2^53 |
| `internal/recipe/fit.go` | `FitAffine`, `FitPower`, `FitPiecewise`, `FitAll`, `FitReport`, `ErrFit` |
| `internal/recipe/testdata/rapid/` | kasus gagal yang ditemukan `rapid`, diputar ulang setiap test |

**Dipakai oleh:**
- `catalog`:
  - `recipes.go` (simpan baris resep): `Build`, `Validate`, `Params`, `FitAffine`/`FitPower`/`FitPiecewise`, `MaxPoints`, `ErrDecreasing`, `ErrFit`, `ErrInvalidResult`.
  - `service_recipes.go` (pratinjau fit): `FitAll`, `FitResult`.
  - `reader.go` (model untuk agregasi): `Build`, `Model`, `Rounding`.
  - `domain.go`: `Rounding`, `RoundNearest`, `RoundUp`.
  - `connect/convert.go`, `postgres/recipes.go` (konversi proto dan DB): `ModelType` dan konstantanya, `Point`, `FitResult`, `Params`.
- `aggregation`: `Quantity` (`compute.go`).

## Verifikasi

```bash
go test -race ./internal/recipe/...
go test -run '^$' -fuzz FuzzFormula -fuzztime 60s ./internal/recipe/   # sandbox rumus, saat formula.go berubah
```

- Property test (`property_test.go`, `fit_property_test.go`, `pgregory.net/rapid`): semua invariant §10 (`Resolve(0) == 0`, finite `>= 0`, tidak turun, power `b < 1` sublinear), `Quantity` dalam satu unit dari nilai pasti dan tidak turun, fit mengembalikan model yang valid, `Params` ↔ `Build` bolak-balik.
- Contoh dari dokumen sebagai table test (`models_test.go`, `fit_test.go`, `quantity_test.go`), termasuk input yang ditemukan `rapid` di #31.
- Batas modul: `internal/archtest`.

## Celah

- `Validate` hanya mencicipi setiap 0,5 unit. Rumus yang turun lalu naik lagi di antara dua sampel bisa lolos; saat berjalan hasilnya tetap finite dan `>= 0` (§10). Dibiarkan karena rumus seperti itu hampir pasti salah ketik, dan grafik di CMS memperlihatkannya.
- `Validate` tidak memeriksa di atas u = 100. `piecewise` meneruskan kemiringan terakhir dan `affine`/`power` monoton dari bentuknya, jadi yang berisiko hanya `formula`.
- Rumus hanya mengenal `u`. Bahan yang bergantung pada dua komponen sekaligus belum bisa dinyatakan; belum ada kebutuhan.

## Traps

1. **Pembulatan ke atas dan sisa float.** `RoundUp` mengurangi `floatNoise(x)` (1e-12 relatif) sebelum `Ceil`, supaya 3,0000000000000004 butir tetap 3. Dulu memakai `slack` (1e-9 relatif) milik `Validate`; di sekitar 6,5e8 unit itu sudah 0,65 unit, sehingga jumlah bisa dibulatkan *ke bawah*. Ditemukan `TestProperty_QuantityRoundsWithinOneUnit`, diperbaiki di #31. Jangan menyatukan kedua toleransi.
2. **`Resolve(0) == 0` dijaga di `evaluate`, bukan per model.** Affine `a + b*u` dengan `a` > 0 tetap 0 di u = 0. Model baru jangan menangani u = 0 sendiri; fungsi model hanya melihat u > 0.
3. **Fit tidak boleh menghasilkan model yang ditolak `Validate`.** Lonjakan 500× antara 12,5 dan 13 unit memberi power `b` ≈ 158 yang meluap sebelum u = 100; sekarang menjadi `ErrFit`. Setiap fit baru harus diuji dengan `TestProperty_FitsOfRisingDataAreValidModels`.
4. **`params` dibaca ketat.** Field tak dikenal atau data sisa menghasilkan `ErrInvalidParams`. Menambah field ke model berarti mengubah `Build` dan `Params` bersama, plus test bolak-balik.

## Changelog

- 2026-10-26 · #38 · Dokumen modul ini dibuat (SDLC-4).
- 2026-10-22 · #31 · `RoundUp` memakai `floatNoise`, tidak lagi kehilangan satu unit di jumlah besar.
- 2026-09-25 · M1.6 (`82b6a8d`) · Batas `MaxPoints` = 100 titik untuk fit dan `piecewise`.
- 2026-09-25 · M1.2 (`d86436b`) · Fit model dari titik ukur (`FitAffine`, `FitPower`, `FitPiecewise`, `FitAll`, `Params`).
- 2026-09-25 · M1.1 (`1fe9df5`) · Engine murni: empat model, `Build`, `Validate`, `Quantity`, property dan fuzz test.
