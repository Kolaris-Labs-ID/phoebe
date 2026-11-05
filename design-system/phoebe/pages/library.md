# Library

PRD §2 "Library meeting", ORG-01, REC-01 (recovery), IMP-01, BIL-01.

## Purpose
Home screen. List all meetings (newest first), search, filter by folder or tags, start a new recording, import audio, see each meeting's processing status at a glance, recover interrupted recordings.

## Navigation
App root = `NavigationStack` with Library as root. No tab bar (only one top-level content area; Settings is a toolbar destination — avoids a 2-item tab bar). Toolbar: leading `gearshape` (Settings, pushed), trailing `line.3.horizontal.decrease.circle` (filter menu) and `plus` menu (Rekam meeting baru / Impor audio). Bottom: floating record bar via `.safeAreaInset(edge: .bottom)` with one Primary button "Rekam" (`accent` fill, `record.circle` glyph; mic is off here, so no `record` red — Master §1/§7.7). Most frequent action, thumb reach.

## Layout

```
┌──────────────────────────────────┐
│ ⚙                      ⏷   ＋     │
│ Meeting                           │ large title
│ ┌ 🔍 Cari judul, tag, isi ───────┐ │ .searchable (always visible)
│ [Semua] [📁 Klien A] [📁 Internal]│ folder scope chips, horizontal ScrollView (only horizontal scroller)
│ ┌───────────────────────────────┐ │ Banner area (recovery / offline / quota)
│ │ ↺ Rekaman belum selesai…  [Pulihkan]│
│ └───────────────────────────────┘ │
│ Hari ini                          │ section header by day
│ ┌───────────────────────────────┐ │
│ │(◉) Weekly sync produk          │ │ row
│ │    09.30 · 42:10               │ │
│ │    [✓ Notes siap] [📁 Internal] #sprint│
│ ├───────────────────────────────┤ │
│ │(◉) Kick-off klien             │ │
│ │    08.00 · 1:05:22             │ │
│ │    [☁︎↑ Mengunggah 45%]        │ │
│ └───────────────────────────────┘ │
│ Kemarin                           │
│ …                                 │
│ ┌───────────────────────────────┐ │
│ │      ◉ Rekam                  │ │ floating bar (accent fill, record.circle, 56 high)
│ └───────────────────────────────┘ │
└──────────────────────────────────┘
```
(Glyphs in the wireframe stand for SF Symbols listed in Master §6, not emoji.)

Row leading icon from the server `source` (survives reinstall / second device): `waveform` for `recording`, `square.and.arrow.down` for `import`, in a 40pt `surface2` circle.

## Components
`List(.insetGrouped)` with `Section` per day; `MeetingRow` (Master 7.5); `StatusChip`; `Banner`; `ContentUnavailableView`; `.searchable(text:placement: .navigationBarDrawer(displayMode: .always))`; filter `Menu` with folder `Picker` + tag `Picker` (single tag, matches api `tag_id`); `.fileImporter(allowedContentTypes: [.mpeg4Audio, .mp3, .wav])`; folder/tag management sheets.

## Row status (one chip, the most relevant; priority top→bottom)

Derived from local phase + list-item fields (api §4.2: `pipeline_state`, `active_job_types`, `failed_stage`, `transcription_error_category`, `mom_review_status`).

| Condition | Chip (icon, tone) | Copy key |
|---|---|---|
| Recording in progress (this meeting) | `circle.fill`, record | `status.recording` |
| Paused by interruption | `phone.down.circle`, warning | `status.interrupted` |
| Deleted on another device (local phase `orphaned`) | `iphone.slash`, warning | `status.orphaned` |
| No speech (`transcription_error_category = no_speech`) | `waveform.slash`, neutral | `status.noSpeech` |
| Stage failed (`failed_stage` or local upload `failed/rejected`) | `exclamationmark.triangle`, danger | `status.failed.<upload\|transcription\|notes\|mom>` |
| Waiting for storage (merge needs space) | `externaldrive.badge.exclamationmark`, warning | `status.waitingStorage` |
| Waiting for connection (pending upload, offline) | `wifi.slash`, warning | `status.waitingConnection` |
| Plan ended (`402 entitlement_inactive`) | `calendar.badge.exclamationmark`, warning | `status.planExpired` |
| Waiting for quota | `gauge.with.dots.needle.100percent`, warning | `status.waitingQuota` |
| Queued for upload | `icloud.and.arrow.up`, neutral | `status.queued` |
| Uploading | `arrow.up.circle`, info, with % | `status.uploading` |
| Verifying upload | `checkmark.icloud`, info | `status.verifying` |
| Transcribing (`transcribe` in `active_job_types`) | `waveform`, info | `status.transcribing` |
| Generating notes (`generate_notes` active) | `sparkles`, info | `status.generatingNotes` |
| Generating MoM (`generate_mom` active) | `sparkles`, info | `status.generatingMom` |
| MoM reviewed | `checkmark.seal.fill`, success | `status.reviewed` |
| MoM draft | `pencil.circle`, neutral | `status.momDraft` |
| Notes ready | `note.text`, success | `status.notesReady` |
| Transcript ready (notes failed/not yet) | `text.quote`, success | `status.transcriptReady` |

Failure chip never hides the meeting; row remains openable.

## Screen states

| State | UI |
|---|---|
| First load (no cache) | 6 skeleton rows `.redacted(.placeholder)` |
| Loaded | List |
| Empty (no meetings) | `ContentUnavailableView`: `waveform.badge.plus`, `library.empty.title`, `library.empty.body`, buttons Rekam (Primary) + Impor audio (Tertiary) |
| Empty search/filter | `ContentUnavailableView.search(text:)` style + `library.search.empty` + "Hapus filter" |
| Offline | Warning banner `library.offline` (dismissible, returns on next offline transition); list works from cache; search shows `library.search.offlineNote` footer |
| Search online | Metadata results instantly (local), then `GET /v1/meetings?q=` (debounced 300 ms) text matches merged under section "Ditemukan di isi" |
| Refreshing | `.refreshable` pull-to-refresh, system spinner |
| Sync error | Inline footnote at top "Gagal memperbarui. Tarik untuk coba lagi." (not blocking) |
| Recovery available (on launch, segments from unclean stop) | Warning banner `library.recovery.*` with Pulihkan (Primary small) and Hapus (Tertiary, confirm). Mic is NOT started. |
| Quota exhausted | Warning banner `library.quota.exhausted` with "Lihat pemakaian" → Settings. Record stays enabled. |
| Plan ended | Warning banner `library.plan.expired` with "Lihat pemakaian". Record stays enabled. |
| Deleted on another device | Warning banner `library.orphan.*` per orphaned meeting: "Simpan sebagai meeting baru" (Secondary) / "Hapus" (Tertiary, confirm `library.orphan.discard.confirm`) |
| Merge blocked by storage | Warning banner `library.storage.merge`; retries automatically when space frees |
| Import validating | Toast-like inline row at top "Memeriksa file…" with ProgressView |
| Import failed | Danger banner with specific reason (see copy), action "Pilih file lain" |
| Import confirm | Sheet after validation passes (below); nothing is copied or uploaded before "Unggah" |
| Import copying (large) | Row appears with chip `status.importing` + determinate progress (streamed copy reports bytes; ios.md §7) |
| App returns to foreground while recording | Recording full-screen cover is re-presented; Library is never shown with a live mic and no visible controls |

## Interactions
- Tap row → push Meeting detail.
- Swipe leading: Pindahkan (folder sheet). Swipe trailing: Hapus (confirmationDialog `meeting.delete.*`). Context menu (long press): Ganti judul, Pindahkan ke folder, Atur tag, Hapus. Same via VoiceOver custom actions.
- Filter menu: folder single-select (Semua / folders / Tanpa folder), tag single-select (Semua tag / tags), "Kelola folder", "Kelola tag". Active filter: filled icon + chip row under search showing active filters with `xmark` remove (filter is visible, not color-only).
- Sort: newest first by meeting date (only sort in MVP).
- `plus` menu → Rekam meeting baru (opens Recording pre-record), Impor audio (file importer; no mic permission requested).
- Folder chips: horizontal scroll of folders, each ≥44pt high; "Semua" selected by default; selected chip uses `.fill` icon + accentSoft + bold text.
- Polling: while any row has non-empty `active_job_types` or a local upload phase (`queued/uploading/verifying`), poll the first page of `GET /v1/meetings` with backoff 2 s ×1.5, cap 30 s, foreground only; reset on foreground / pull-to-refresh (api §4.5). Pull-to-refresh does a full pull and, if any meeting waits for quota/plan, refetches `GET /v1/usage`.
- Import flow: file importer → validation (type, duration, size, storage) → **Import confirm sheet** (`.presentationDetents([.medium])`): file name + duration, `lock.shield` `import.confirm.cloud` (retention days from `ServerConfig`; if config was never fetched, `onb.privacy.retention.unknown` and "Unggah" stays disabled with footnote `import.confirm.waitConfig` until it is fetched), "Bahasa transkrip" `Picker` (Otomatis / Indonesia / English, default = Settings default; sets meeting `language`), Primary "Unggah" (`import.confirm.upload`), Cancel. Then streamed copy → upload.
- Folder management sheet: list of folders, add (TextField, max 60 chars), rename, delete (meetings move to "Tanpa folder", confirm). One level only.
- Tag management: same pattern; tag assign sheet on a meeting has search + create.

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| library.title | Meeting | Meetings |
| library.search.prompt | Cari judul, tag, atau isi | Search titles, tags, or content |
| library.search.empty | Tidak ada meeting yang cocok dengan "%@". | No meetings match "%@". |
| library.search.contentSection | Ditemukan di isi | Found in content |
| library.search.offlineNote | Offline: hanya mencari judul dan tag. Pencarian isi transkrip, notes, dan MoM memerlukan koneksi. | Offline: searching titles and tags only. Searching transcript, notes, and minutes needs a connection. |
| library.filter | Filter | Filter |
| library.filter.all | Semua | All |
| library.filter.noFolder | Tanpa folder | No folder |
| library.filter.tags | Tag | Tags |
| library.filter.clear | Hapus filter | Clear filters |
| library.filter.manageFolders | Kelola folder | Manage folders |
| library.filter.manageTags | Kelola tag | Manage tags |
| library.new | Rekam meeting baru | Record new meeting |
| library.import | Impor audio | Import audio |
| library.record | Rekam | Record |
| library.settings | Pengaturan | Settings |
| library.section.today | Hari ini | Today |
| library.section.yesterday | Kemarin | Yesterday |
| library.empty.title | Belum ada meeting | No meetings yet |
| library.empty.body | Rekam meeting atau impor file audio untuk membuat transkrip dan MoM. | Record a meeting or import an audio file to get a transcript and minutes. |
| library.offline | Tidak ada koneksi. Anda tetap bisa merekam; upload dilanjutkan saat online. | No connection. You can still record; uploads resume when you're online. |
| library.refresh.error | Gagal memperbarui. Tarik untuk coba lagi. | Couldn't refresh. Pull to try again. |
| library.recovery.title | Rekaman belum selesai | Unfinished recording |
| library.recovery.body | "%1$@" berhenti tiba-tiba. %2$@ audio tersimpan dapat dipulihkan. | "%1$@" stopped unexpectedly. %2$@ of saved audio can be recovered. |
| library.recovery.restore | Pulihkan | Recover |
| library.recovery.discard | Hapus | Discard |
| library.recovery.discard.confirm | Hapus audio yang tersimpan? Tindakan ini tidak dapat dibatalkan. | Delete the saved audio? This can't be undone. |
| library.recovery.done | Rekaman dipulihkan (%@). | Recording recovered (%@). |
| library.quota.exhausted | Kuota transkripsi habis. Rekaman tetap disimpan; transkripsi menunggu kuota tersedia. | Transcription quota used up. Recordings are still saved; transcription waits until quota is available. |
| library.quota.cta | Lihat pemakaian | View usage |
| library.plan.expired | Paket berakhir. Rekaman tetap disimpan; transkripsi menunggu paket aktif. | Your plan has ended. Recordings are still saved; transcription waits for an active plan. |
| library.orphan.title | Dihapus di perangkat lain | Deleted on another device |
| library.orphan.body | "%@" dihapus di perangkat lain. Audio dan catatan masih ada di iPhone ini. | "%@" was deleted on another device. Its audio and notes are still on this iPhone. |
| library.orphan.keep | Simpan sebagai meeting baru | Save as new meeting |
| library.orphan.discard | Hapus | Delete |
| library.orphan.discard.confirm | Hapus audio dan catatan di iPhone ini? Tindakan ini tidak dapat dibatalkan. | Delete the audio and notes on this iPhone? This can't be undone. |
| library.storage.merge | Penyimpanan iPhone penuh. Bagian rekaman aman; rekaman digabung dan diunggah setelah ada ruang. | iPhone storage is full. Recorded parts are safe; the recording is combined and uploaded once there's space. |
| import.confirm.title | Unggah audio ini? | Upload this audio? |
| import.confirm.cloud | Audio diproses di cloud dan disimpan %lld hari setelah transkrip selesai. | Audio is processed in the cloud and kept for %lld days after the transcript is ready. |
| import.confirm.language | Bahasa transkrip | Transcript language |
| import.confirm.upload | Unggah | Upload |
| import.confirm.waitConfig | Menunggu info masa simpan dari server sebelum mengunggah. | Waiting for the retention policy from the server before uploading. |
| import.validating | Memeriksa file… | Checking file… |
| import.copying | Menyalin file… %lld%% | Copying file… %lld%% |
| import.error.type | File ini bukan audio M4A, MP3, atau WAV yang valid. | This file isn't a valid M4A, MP3, or WAV audio file. |
| import.error.corrupt | File audio rusak atau tidak dapat dibaca. | The audio file is damaged or can't be read. |
| import.error.empty | File audio kosong. | The audio file is empty. |
| import.error.tooLong | Durasi audio %1$@ melebihi batas %2$lld menit. | Audio length %1$@ exceeds the %2$lld-minute limit. |
| import.error.tooBig | Ukuran file %1$@ melebihi batas %2$lld MB. | File size %1$@ exceeds the %2$lld MB limit. |
| import.error.storage | Penyimpanan iPhone tidak cukup untuk menyalin file. | Not enough iPhone storage to copy the file. |
| import.error.pickAnother | Pilih file lain | Choose another file |
| status.recording | Merekam | Recording |
| status.interrupted | Dijeda karena interupsi | Paused by interruption |
| status.waitingConnection | Menunggu koneksi | Waiting for connection |
| status.waitingQuota | Menunggu kuota | Waiting for quota |
| status.planExpired | Paket berakhir | Plan ended |
| status.waitingStorage | Menunggu ruang penyimpanan | Waiting for storage |
| status.orphaned | Dihapus di perangkat lain | Deleted on another device |
| status.queued | Antre upload | Queued for upload |
| status.uploading | Mengunggah %lld%% | Uploading %lld%% |
| status.verifying | Memverifikasi upload | Verifying upload |
| status.importing | Mengimpor | Importing |
| status.transcribing | Mentranskripsi | Transcribing |
| status.transcriptReady | Transkrip siap | Transcript ready |
| status.generatingNotes | Membuat notes | Generating notes |
| status.notesReady | Notes siap | Notes ready |
| status.generatingMom | Membuat MoM | Generating minutes |
| status.momDraft | MoM draft | Minutes draft |
| status.reviewed | Sudah ditinjau | Reviewed |
| status.noSpeech | Tidak ada ucapan | No speech detected |
| status.failed.upload | Upload gagal | Upload failed |
| status.failed.transcription | Transkripsi gagal | Transcription failed |
| status.failed.notes | Notes gagal | Notes failed |
| status.failed.mom | MoM gagal | Minutes failed |
| folder.title | Folder | Folders |
| folder.new | Folder baru | New folder |
| folder.name | Nama folder | Folder name |
| folder.rename | Ganti nama | Rename |
| folder.delete | Hapus folder | Delete folder |
| folder.delete.confirm | Hapus folder "%@"? Meeting di dalamnya pindah ke Tanpa folder. | Delete folder "%@"? Its meetings move to No folder. |
| folder.moved | Dipindahkan ke %@ | Moved to %@ |
| folder.name.error.empty | Nama folder wajib diisi. | Folder name is required. |
| folder.name.error.duplicate | Folder dengan nama ini sudah ada. | A folder with this name already exists. |
| tag.title | Tag | Tags |
| tag.new | Tag baru | New tag |
| tag.search | Cari atau buat tag | Search or create tag |
| tag.create | Buat tag "%@" | Create tag "%@" |
| tag.delete.confirm | Hapus tag "%@" dari semua meeting? | Remove tag "%@" from all meetings? |
| meeting.rename | Ganti judul | Rename |
| meeting.move | Pindahkan ke folder | Move to folder |
| meeting.tags | Atur tag | Edit tags |
| meeting.delete | Hapus meeting | Delete meeting |
| meeting.delete.title | Hapus meeting ini? | Delete this meeting? |
| meeting.delete.body | Audio, transkrip, notes, dan MoM akan dihapus permanen. Proses yang sedang berjalan dibatalkan. | Audio, transcript, notes, and minutes will be permanently deleted. Any running processing is cancelled. |
| meeting.delete.confirm | Hapus | Delete |
| meeting.untitled | Meeting %@ | Meeting %@ |
| common.cancel | Batal | Cancel |
| common.done | Selesai | Done |
| common.retry | Coba lagi | Try again |
| common.save | Simpan | Save |
| common.close | Tutup | Close |

`meeting.untitled` takes a localized date-time ("Meeting 6 Nov 09.30").

## Accessibility
- Row: one element, label `"<title>, <date>, durasi <spoken duration>, <status>"`, then folder/tags; hint `library.row.hint` ("Buka meeting" / "Opens meeting"). Custom actions: Pindahkan, Atur tag, Hapus.
- Floating record button: label `library.record`, hint "Mulai meeting baru" / "Starts a new meeting".
- Toolbar: gear → `library.settings`; filter → `library.filter` with value "aktif: Klien A, #sprint" / "active: …" when filtered; plus → "Tambah" / "Add".
- Banners: announced once on appear; actions are buttons in the element.
- Status changes in visible rows (e.g., Transcribing → Notes ready) post a polite announcement only for the meeting opened most recently, to avoid chatter.
- Duration spoken via `Duration.formatted(.units(allowed: [.hours, .minutes], width: .wide))`.
- AX sizes: row metadata stacks vertically; folder chip scroller becomes a single "Folder: Semua" menu button.
