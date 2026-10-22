# SDLC kue-preorder

Cara kerja dari issue sampai merge, untuk manusia dan agent. Diadaptasi dari SDLC "AI Operator" yang dipakai di kantor, disesuaikan untuk satu orang dengan akun Claude Pro (diputuskan 2026-10-22).

## Peran

| Peran | Siapa | Tugas |
|---|---|---|
| **Lead** | Zefan | Keputusan bisnis, review dan merge PR, review mingguan |
| **Operator** | Zefan | Menjalankan agent, mengklik hasilnya, menulis catatan Operator di PR |
| **Agent** | Claude Code | Merencanakan, menulis kode dan test, memutuskan hal teknis (`[Operator]` di keputusan), membuka PR |

Agent memutuskan detail teknis sendiri dan hanya bertanya untuk keputusan bisnis atau yang mahal. Batasnya ada di `.agents/rules/00-project.md`.

## Alur satu potongan

1. **Mulai sesi:** `make session-start`, lalu agent memilih pekerjaan (`/next`, menyusul di SDLC-3).
2. **Issue:** satu potongan = satu issue, berisi AC yang bisa diuji. Branch `m<N>.<n>-<slug>` (fitur) atau `sdlc-<n>-<slug>` (alat kerja).
3. **Rencana:** ikuti `.agents/workflows/implement-milestone.md`; tunggu persetujuan sebelum kode.
4. **Kerja:** test dulu atau bersamaan untuk modul inti. Dokumen modul (Handoff dan Changelog) dan keputusan baru (ADR) diperbarui di PR yang sama (SDLC-4).
5. **Gerbang lokal:** hook pre-commit dan pre-push berjalan sendiri; `make check` hijau sebelum commit terakhir.
6. **Commit dan PR:** commit hanya setelah Lead setuju. Agent push dan membuka PR (`gh pr create`). PR yang dasarnya belum di-merge ditumpuk di atas branch-nya.
7. **Tutup sesi:** `make session-close ARGS='--done "…" --tests "…" --next "…" --apply'` menulis *Session log* ke PR. Pelajaran: tambah `--lesson "apa · akar masalah · usul"`.
8. **Merge:** hanya Lead, di GitHub. Setelah itu `git pull --ff-only` di `main`.

## Perintah

| Perintah | Isi |
|---|---|
| `make setup` | Menyalakan git hooks, memasang paket client TS |
| `make check` | Gerbang penuh, sama dengan `.github/workflows/ci.yml`: tidy, vet, govulncheck, golangci-lint, codegen (sqlc, buf lint/format/breaking/generate), test `-race` dengan integrasi (butuh Docker), typecheck TS |
| `make test-changed` | Unit test paket yang berubah dibanding `origin/main` (dipakai pre-push) |
| `make eod` | Status proyek tanpa token: PR terbuka, merge hari ini, blocker, lesson, alert Dependabot |
| `make session-start` / `session-close` | Rutinitas sesi, lihat atas |

Jalankan dari akar repo di **Git Bash** (Makefile dan hooks butuh `bash`).

## Gerbang

| Gerbang | Isi | Kapan |
|---|---|---|
| pre-commit | gofmt, golangci-lint pada baris baru, gitleaks | Setiap commit |
| pre-push | `go vet`, test paket yang berubah | Setiap push |
| `make check` | Gerbang penuh | Sebelum commit terakhir sebuah potongan |
| CI GitHub | Isi yang sama | **Belum jalan**: GitHub Actions terkunci billing. Begitu terbuka, jadikan check wajib di `main` (branch protection di repo private butuh GitHub Pro) |

## Belajar dari kesalahan

- Kesalahan yang berulang atau aturan yang kurang → issue berlabel `lesson` (lewat `session-close --lesson`).
- **Review mingguan** oleh Lead: setiap lesson menjadi gerbang (hook, test, lint), template, atau aturan, lalu issue-nya ditutup.

## Yang sengaja tidak ditiru dari kantor

| Di kantor | Alasan tidak dipakai |
|---|---|
| Standar dan ruleset pusat organisasi, rerun saat Lead approve, `loop-alarm` | Untuk organisasi dengan banyak orang dan banyak repo |
| Plugin perusahaan dan fitur Claude Enterprise (kebijakan terpusat, SSO, audit) | Diganti aturan dan skill di repo ini |
| CODEOWNERS | Satu orang |

Batasan akun Pro yang terasa hanya kuota pemakaian. Penawarnya: sesi pendek, Handoff per modul, dan `session-close` setiap selesai.

## Pemasangan

- **SDLC-1** (ini): dokumen ini, `Makefile`, `.githooks/`, `scripts/`.
- **SDLC-2:** template PR dan issue, label (`lesson`, `blocker`, `risk:high`), Dependabot.
- **SDLC-3:** skill `/next` dan hook `SessionStart` Claude Code.
- **SDLC-4:** `docs/features/` (Handoff dan Changelog per modul) dan `docs/decisions/` (ADR untuk keputusan baru).
