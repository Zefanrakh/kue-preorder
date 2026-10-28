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

1. **Mulai sesi:** `/zefan-sdlc:start` (memanggil `make session-start`), lalu agent memilih pekerjaan dengan `/zefan-sdlc:next`. Perintah plugin lain: `build-issue <N>`, `review-pr <N>`, `close` (lihat "Sesi Claude Code dan kerja jarak jauh").
2. **Issue:** satu potongan = satu issue, berisi AC yang bisa diuji. Branch `m<N>.<n>-<slug>` (fitur) atau `sdlc-<n>-<slug>` (alat kerja).
3. **Rencana:** ikuti `.agents/workflows/implement-milestone.md`. Agent menyampaikan rencana singkat lalu langsung mengerjakannya; berhenti dan bertanya hanya untuk keputusan bisnis, yang memakan biaya, atau yang sulit dibalik.
4. **Kerja:** sebelum menyentuh modul, baca Handoff-nya di `docs/features/<modul>.md`; belum ada, buat dari `docs/features/_TEMPLATE.md`. Test dulu atau bersamaan untuk modul inti. Handoff dan Changelog modul diperbarui di PR yang sama. Keputusan arsitektur baru ditulis sebagai ADR di `docs/decisions/`; keputusan lama tetap di §27 `docs/architecture.md`.
5. **Gerbang lokal:** hook pre-commit dan pre-push berjalan sendiri; `make check` hijau sebelum commit terakhir.
6. **Commit dan PR:** agent commit, push, dan membuka PR (`gh pr create`) tanpa menunggu persetujuan; Lead menilai di PR. PR yang dasarnya belum di-merge ditumpuk di atas branch-nya. Sebelum push ke branch yang sudah punya PR, cek `gh pr view`: kalau PR-nya sudah di-merge, commit baru masuk branch dan PR baru dari `main`, karena push ke branch itu tidak akan sampai ke `main` (#34).
7. **Review:** PR `risk:high` (uang, stok, status order, auth, migrasi): setelah PR dibuka, agent langsung menjalankan review-pr di konteks bersih: subagent yang membaca `skills/review-pr/SKILL.md` plugin `zefan-sdlc` dari disk (skill plugin tidak bisa dipanggil agent sendiri), bukan konteks yang menulis PR, ditambah `/code-review <N>` kalau ada kode, lalu menulis hasilnya sebagai komentar di PR sebelum Lead merge. PR `risk:low`: pesan penutup agent menyebut bahwa review-pr dilewati beserta perintahnya.
8. **Tutup sesi:** `make session-close ARGS='--done "…" --tests "…" --next "…" --apply'` menulis *Session log* ke PR. Pelajaran: tambah `--lesson "apa · akar masalah · usul"`. `session-close` memperingatkan kalau PR branch ini sudah di-merge tetapi ada commit yang belum ada di `main`, dan kalau PR-nya bertumpuk (base bukan `main`) beserta PR di bawahnya.
9. **Merge:** hanya Lead, di GitHub. Setelah itu `git pull --ff-only` di `main`. PR bertumpuk di-merge satu per satu dari bawah, dan PR berikutnya hanya setelah GitHub menampilkan base `main` untuknya. GitHub memindahkan base saat branch dasarnya terhapus setelah merge; kalau belum pindah, jalankan `gh pr edit <N> --base main`. Merge sebelum base pindah memasukkan PR ke branch dasarnya, bukan ke `main` (#34).

## Perintah

| Perintah | Isi |
|---|---|
| `make setup` | Menyalakan git hooks, memasang paket client TS |
| `make check` | Gerbang penuh, sama dengan `.github/workflows/ci.yml`: versi alat lokal (`make versions`), tidy, vet, govulncheck, golangci-lint, codegen (sqlc, buf lint/format/breaking/generate), actionlint, test `-race` dengan integrasi (butuh Docker), `npm audit signatures`, typecheck TS |
| `make versions` | Gagal kalau golangci-lint, sqlc atau buf lokal berbeda dari versi yang di-pin `ci.yml`. govulncheck dan actionlint jalan lewat `go run` dengan versi yang dibaca dari `ci.yml` |
| `make test-changed` | Unit test paket yang berubah dibanding `origin/main` (dipakai pre-push) |
| `make eod` | Status proyek tanpa token: PR terbuka, merge hari ini, blocker, lesson, alert Dependabot |
| `make session-start` / `session-close` | Rutinitas sesi, lihat atas |

Jalankan dari akar repo di **Git Bash** (Makefile dan hooks butuh `bash`).

Yang masih berbeda dari CI: CI berjalan di Linux dengan Node 22 (lokal memakai Node yang terpasang, tidak dicek), dan `buf breaking` di CI membandingkan dengan base PR, bukan `origin/main`. Langkah CI lainnya dijalankan `make check` juga.

## Gerbang

| Gerbang | Isi | Kapan |
|---|---|---|
| pre-commit | gofmt, golangci-lint pada baris baru, gitleaks | Setiap commit |
| pre-push | `go vet`, test paket yang berubah | Setiap push |
| `make check` | Gerbang penuh | Sebelum commit terakhir sebuah potongan |
| CI GitHub | Isi yang sama | **Belum jalan**: GitHub Actions terkunci billing. Begitu terbuka, jadikan check wajib di `main` (branch protection di repo private butuh GitHub Pro) |

## Sesi Claude Code dan kerja jarak jauh

Perintah SDLC tersedia sebagai plugin pribadi `zefan-sdlc` (repo private `Zefanrakh/zefan-plugins`, terpasang dengan scope user): `/zefan-sdlc:start`, `next`, `build-issue <N>`, `review-pr <N>`, `close`.

**Sekali per laptop** (harus di depan layar atau lewat Chrome Remote Desktop, karena ada dialog yang perlu diklik):

1. CLI native ada di `%USERPROFILE%\.local\bin\claude.exe`. Tambahkan folder itu ke PATH user, lalu tutup dan buka lagi terminal **dan IDE** (terminal di dalam IDE mewarisi PATH lama): `claude --version`. Jangan memakai binary di folder ekstensi editor, karena path-nya berubah setiap kali ekstensi diperbarui.
2. Remote control untuk semua sesi: `/config` → **Enable Remote Control for all sessions** = `true` (tersimpan sebagai `"remoteControlAtStartup": true` di `%USERPROFILE%\.claude\settings.json`). Hanya bisa di scope user; settings repo tidak boleh menyalakannya. Sejak itu **setiap sesi baru** (terminal, panel IDE, Claude Desktop) otomatis muncul di sidebar claude.ai/code dengan ikon laptop.
3. Untuk setiap repo, jalankan `claude` sekali di akar repo dan jawab **Yes** pada dialog *trust*. Tanpa itu, sesi remote untuk repo tersebut ditolak ("Workspace not trusted").
4. Pasang plugin: `/plugin install zefan-sdlc --marketplace Zefanrakh/zefan-plugins`, pilih scope **user**.

**Sesi induk per repo.** Setiap repo punya satu sesi yang dibiarkan hidup dan dipakai terus, dari laptop maupun claude.ai/code. Langkahnya sama untuk semua repo:

1. Buka **terminal terpisah** (Git Bash atau Windows Terminal, bukan terminal di dalam IDE) di akar repo.
2. `claude --continue` untuk melanjutkan percakapan terakhir repo itu, atau `claude` untuk percakapan baru. Beri nama sekali dengan `/rename <repo>` supaya mudah dikenali di sidebar.
3. `/zefan-sdlc:start`. Di antara dua potongan kerja: `/zefan-sdlc:close`, lalu `/clear` (konteks bersih, sesi dan koneksinya tetap), lalu `/zefan-sdlc:start` lagi.
4. Biarkan terminal itu terbuka. Sesi lain (IDE, Claude Desktop) boleh dibuka dan ditutup kapan saja tanpa memengaruhinya.

| Sesi berjalan di | Saat IDE ditutup lalu dibuka lagi |
|---|---|
| Terminal terpisah (sesi induk) | Tidak terpengaruh |
| Panel Claude Code di IDE | Biasanya dipulihkan dan tersambung lagi; plugin yang baru dipasang ikut termuat |
| Terminal di dalam IDE | Mati. Lanjutkan di terminal terpisah dengan `claude --continue` (atau `claude --resume` untuk memilih percakapan) |

Tombol **New** di claude.ai/code membuat sesi *cloud* (repo di-clone dari GitHub, bukan laptop ini). Plugin user, Docker, dan alat lokal tidak ada di sana. Untuk bekerja di laptop dari jauh, pakai sesi induk di sidebar.

**Kalau sedang jauh dari laptop:**

- Dari claude.ai/code tidak bisa menjawab dialog Windows atau dialog *trust*. Agent bertanya dulu sebelum menjalankan instalasi, perintah admin, atau apa pun yang memunculkan dialog (UAC, Docker pertama kali, firewall). Hal yang perlu diklik dikerjakan lewat **Chrome Remote Desktop**.
- Sesi induk mati (laptop restart, terminal tertutup, sidebar menunjukkan *Remote Control disconnected*): lewat Chrome Remote Desktop, buka terminal terpisah di akar repo dan jalankan `claude --continue`.
- Laptop jangan tidur saat tercolok listrik (Settings → System → Power).
- Plugin hanya termuat di sesi yang dimulai setelah plugin dipasang atau diperbarui, dan `/reload-plugins` diblokir lewat koneksi remote. Di sesi lama, minta dengan kalimat biasa ("jalankan zefan-sdlc start"): agent membaca `SKILL.md`-nya dari disk dan mengikutinya. Setelah CLI diperbarui ("Update installed · Restart to update"), mulai ulang sesi induk dengan `/exit` lalu `claude --continue`.

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
- ~~**SDLC-3:** skill `/next` dan hook `SessionStart` Claude Code.~~ Digantikan plugin pribadi `zefan-sdlc` (2026-10-23), yang dipakai bersama satb-lab.
- **SDLC-4:** `docs/features/` (Handoff dan Changelog per modul, contoh lengkap: `recipe.md`) dan `docs/decisions/` (ADR untuk keputusan baru). Modul lain mendapat dokumennya saat pertama kali disentuh.
