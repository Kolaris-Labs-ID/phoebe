# Meeting Detail

PRD §2 "Meeting detail", TRN-01, AI-01, DOC-01, §7 state machines, PRV-01 (delete audio), BIL-01.

## Purpose
One meeting: metadata, audio playback, processing status per stage, and the three artifacts in tabs — Transcript, Notes (manual + AI), MoM. Each stage fails independently; the meeting is always openable and everything already available stays usable.

## Layout

```
┌──────────────────────────────────┐
│ ‹ Meeting               ⋯        │ toolbar: back, overflow menu
│ Weekly sync produk               │ title (title2), tap → edit metadata sheet
│ Kam, 6 Nov 2026 · 09.30 WIB · 42:10 │ subheadline textSecondary
│ person.2 Andi, Sari (+ Tambah)    │ participants (manual)
│ [folder Internal] [#sprint] [+tag]│
│ ┌──────────────────────────────┐ │ Pipeline strip (card)
│ │ ✓ Upload  ✓ Transkrip  ◌ Notes │ │ 3 steps: icon + label + state word
│ └──────────────────────────────┘ │
│ [Banner for current stage issue] │
│ ┌──────────────────────────────┐ │ Player (card)
│ │ ↺15  ▶  15↻   12:04 / 42:10   │ │
│ │ ───────●──────────────────── │ │ Slider scrub
│ └──────────────────────────────┘ │
│ [ Transkrip | Catatan | MoM ]    │ segmented, pinned under nav bar on scroll
│ ─────────────────────────────── │
│ tab content (see below)           │
└──────────────────────────────────┘
```
Header + strip + player scroll away; segmented control pins (`.safeAreaInset(edge: .top)` swap when scrolled past). iPad / regular width: content max 680, centered.

### Pipeline strip
Three steps, each `Label` (icon + text) with state word underneath; never color alone.

| Step | Pending | Active | Done | Failed |
|---|---|---|---|---|
| Upload | `icloud.and.arrow.up` "Menunggu" | `arrow.up.circle` "45%" (determinate) | `checkmark.icloud` "Selesai" | `exclamationmark.icloud` "Gagal" |
| Transkrip | `text.quote` "Menunggu" | `waveform` + ProgressView "Memproses" | `checkmark.circle` "Siap" | `exclamationmark.triangle` "Gagal" |
| Notes | `note.text` "Menunggu" | `sparkles` + ProgressView "Memproses" | `checkmark.circle` "Siap" | `exclamationmark.triangle` "Gagal" |

Waiting-for-connection, waiting-for-quota and plan-ended replace "Menunggu" with `wifi.slash` "Menunggu koneksi" / `gauge.with.dots.needle.100percent` "Menunggu kuota" / `calendar.badge.exclamationmark` "Paket berakhir".

### Transcript tab
```
 ┌ Banner (Based on… / corrected) ┐
 12:04  Kita sepakat budget Q1 50 juta.     ← row: timestamp button (accent, monospaced) + body text
 12:15  Siapa yang pegang vendor?
        pencil "Dikoreksi"                 ← correction marker (icon+text) on edited segments
```
`List(.plain)` lazy rows. Tap timestamp → seek + play. Currently playing segment: `accentSoft` background + `speaker.wave.2` icon + VoiceOver value "sedang diputar". Long-press / context menu → "Koreksi teks" (edit sheet) / "Salin". Offline: "Koreksi teks" disabled with footnote `transcript.edit.offline` (corrections are online-only; no outbox). Toolbar on tab: "Lihat versi asli" toggle when corrections exist.

### Notes tab
Two cards, stacked:
1. **Catatan Anda** (manual notes) — `person.fill`; a list of short points (api §3.1 items; points typed during recording show their `[mm:ss]` offset as a tappable ref). Each point is an inline `TextField(axis: .vertical)`; a typed Return (`"\n"` detected in `onChange`) splits the point and focuses the new one; delete via swipe / context menu Hapus or the keyboard-toolbar `trash` button ("Hapus poin"); "Tambah poin" row at the end. Empty points stay in the local draft and are not synced. Autosave ≤2 s, save indicator. Editable offline, even if AI fails or quota is exhausted. Point ids stay stable across edits. A `409` (another device, or a lost-response replay) opens the same ConflictSheet as the MoM editor (`mom-editor.md`), driven by `PendingDocumentEdit.conflictCurrentRevision`.
2. **Notes AI** — `sparkles` "AI"; structured read view of AI notes (sections + bullets with source refs). Editable via "Edit" → same item editor as MoM (`mom-editor.md`), creates a revision. Version menu (`clock.arrow.circlepath`) lists AI notes versions; "Buat ulang" (regenerate) creates a new version.

### MoM tab
- No MoM yet: `ContentUnavailableView` `doc.text`, title `mom.none.title`, body, Primary "Buat MoM" (enabled when an AI notes revision exists — api `ai_notes_not_ready` otherwise), plus source picker "Dari notes versi: v2 (terbaru)" (Menu, sends `ai_notes_revision_id`). Secondary "Tulis MoM manual" creates the empty MoM template (`PUT …/documents/mom` with `expected_revision: 0`) — always available, so a MoM can be written when AI fails or quota is exhausted (PRD DOC-03 acceptance).
- Generating: card with ProgressView + `mom.generating` + "Anda bisa meninggalkan layar ini."
- Ready: card with latest MoM title, review chip (Draft/Reviewed), updated time, "Buka editor" Primary, "Ekspor" Secondary. Below: "Versi lain" list (v1 generated, v2 edited…) each opens editor read-only with Restore.
- Failed: danger banner + retry; previous MoM versions (if any) remain listed.

## States (complete)

| Area | State | UI | Action |
|---|---|---|---|
| Screen | loading (no cache) | header skeleton + tab skeleton | — |
| Screen | offline | warning banner `detail.offline`; cached content shown; AI actions disabled with reason | — |
| Upload | queued / waiting connection | strip "Menunggu koneksi"; banner `detail.upload.waiting` | — (auto resumes) |
| Upload | uploading | strip % | — |
| Upload | verifying | strip `checkmark.icloud` "Memverifikasi" | — |
| Upload | failed (retryable: network/checksum mismatch after auto retries) | danger banner `detail.upload.failed` | Coba lagi |
| Upload | rejected (invalid/too long/too big/corrupt per server validation) | danger banner with specific reason (`import.error.*` copy) | Hapus meeting (confirm) |
| Quota | exhausted before transcription | warning banner `detail.quota.waiting` | Lihat pemakaian; auto-continues when quota available, or "Coba lagi" after upgrade/period reset |
| Plan | entitlement expired/inactive (`402 entitlement_inactive`) | warning banner `detail.entitlement.expired` | Lihat pemakaian; auto-continues when the plan is active and quota suffices |
| Transcription | transcribing | Transcript tab: skeleton lines + `detail.transcribing` ("biasanya beberapa menit") | — |
| Transcription | failed | Transcript tab banner per `error_category` (table below) — always says audio is safe and quota was not charged | per table |
| Transcription | no speech / silent audio | neutral `ContentUnavailableView` `waveform.slash` `detail.noSpeech`; Notes and MoM generation disabled with that reason (no fictional MoM) | — |
| Transcript | ready | rows | tap to seek |
| Transcript | corrected | "Dikoreksi" markers; toggle original | Lihat versi asli |
| Notes AI | generating | card ProgressView `detail.notes.generating`; transcript fully readable | — |
| Notes AI | failed | card danger banner `detail.notes.failed` + per-category line (table below); manual notes still editable | per table |
| Notes AI | based on earlier version (transcript corrected after generation) | warning chip + banner `doc.outdated.*` on the AI notes card; content still shown; not auto-replaced | Buat ulang (new version) / Tutup |
| Notes AI | regenerate rate limited | Buat ulang disabled + footnote `doc.regen.limited` with `/v1/usage.ai_generations.next_available_at` (known before tapping) or `retry_after_seconds` from a 429 | — |
| Notes AI / MoM | unsynced manual-notes (or AI-notes) edits, offline or in conflict | Buat ulang / Buat MoM / Coba lagi disabled + footnote `doc.regen.syncFirst` | resolve sync |
| MoM | none / generating / ready / failed | see MoM tab | — |
| MoM | based on earlier version (notes or transcript changed) | warning chip on MoM card `doc.outdated.*` | Buat ulang / Buka tetap |
| MoM | quota exhausted | generation still allowed (MoM doesn't consume audio quota) | — |
| Playback | local file available (before/after upload) | player enabled (local URL) | — |
| Playback | cloud only | play → `GET …/audio/url`, download whole file to cache with determinate progress `detail.player.downloading`, then play locally (api §4.9) | — |
| Playback | download failed / URL expired before start | inline `detail.player.error` + Coba lagi (fetches a fresh URL) | Coba lagi |
| Playback | audio deleted or retention expired | player disabled: `speaker.slash` + `detail.player.expired`; transcript timestamps become plain text (not buttons) | — |
| Playback | offline & no local audio | player disabled `detail.player.offline` | — |
| Sync | pending local edits | save indicator "Tersimpan di perangkat, sinkron saat online" | — |
| Delete | delete in progress | full-screen `ProgressView` overlay "Menghapus…", then pop to Library + toast | — |

### Failure by `error_category` (transcription, notes, MoM)

Retry creates a new intent with a new `Idempotency-Key`. A disallowed retry would fail again and cost provider calls.

| `error_category` | Copy key (line under the stage banner) | Retry |
|---|---|---|
| `no_speech` | `detail.noSpeech` (ContentUnavailableView instead of banner) | no |
| `audio_unreadable` | `fail.audioUnreadable` | no — offer Hapus meeting |
| `audio_unavailable` | `detail.player.expired` | no |
| `provider_unavailable`, `provider_timeout`, `provider_rate_limited`, `timeout` | `fail.provider` | yes |
| `provider_rejected` | `fail.rejected` | no (support `request_id` shown) |
| `invalid_output` | `fail.invalidOutput` | yes |
| `internal` | `fail.internal` + `request_id` (selectable) | yes |

Polling (when any job `queued|running` or upload active): `GET /v1/meetings/{id}` (includes jobs, documents, audio) with backoff 2 s ×1.5, cap 30 s, foreground only; reset on foreground and after user actions. Stops when no active jobs. Transcript/doc revisions are fetched only when their `current_revision_number` changes.

## Overflow menu (`⋯`)
Edit info (title, date, time zone, participants) · Pindahkan ke folder · Atur tag · Ekspor (opens Export sheet) · Hapus audio (destructive) · Hapus meeting (destructive). Destructive items in a separate `Section` of the menu, `role: .destructive`, confirmation dialogs.

## Edit metadata sheet
`Form`: Judul (TextField), Tanggal & waktu (`DatePicker`), Zona waktu (Picker, default device), Peserta (list of TextFields + "Tambah peserta"; empty entries dropped on save), Bahasa transkrip (Picker Otomatis / Indonesia / English → meeting `language`; enabled only while no upload is in progress and transcription has not started or has failed — otherwise disabled with footnote `meta.language.locked`). Save / Cancel; unsaved-changes confirm on swipe dismiss (`sheet-dismiss-confirm`). Validation: title ≤ 200 chars.

Metadata conflict (`409 metadata_conflict`, metadata changed on another device): sheet `arrow.triangle.branch` `meta.conflict.title` / `meta.conflict.body`, showing server values; Primary `meta.conflict.keepMine` (re-PATCH my changed fields on top of the latest), Destructive `meta.conflict.useLatest` (confirm). Never applied silently.

## Transcript correction sheet
TextEditor prefilled with segment text; footnote `transcript.edit.footnote` (original kept, notes/MoM will be marked as based on earlier version). Save (online only) → `POST …/transcript/revisions {expected_revision, mutation_id, edits: [{segment_id, text}]}`; a timeout is retried with the same `mutation_id` and edits; 409 → refetch and reapply the edit to the latest text after the user confirms.

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| detail.tab.transcript | Transkrip | Transcript |
| detail.tab.notes | Catatan | Notes |
| detail.tab.mom | MoM | Minutes |
| detail.participants | Peserta | Participants |
| detail.participants.add | Tambah peserta | Add participant |
| detail.participants.none | Belum ada peserta | No participants yet |
| detail.step.upload | Upload | Upload |
| detail.step.transcript | Transkrip | Transcript |
| detail.step.notes | Notes | Notes |
| step.pending | Menunggu | Pending |
| step.processing | Memproses | Processing |
| step.verifying | Memverifikasi | Verifying |
| step.done | Selesai | Done |
| step.ready | Siap | Ready |
| step.failed | Gagal | Failed |
| step.waitingConnection | Menunggu koneksi | Waiting for connection |
| step.waitingQuota | Menunggu kuota | Waiting for quota |
| detail.offline | Offline. Menampilkan data tersimpan. Pembuatan notes dan MoM memerlukan koneksi. | Offline. Showing saved data. Generating notes and minutes needs a connection. |
| detail.upload.waiting | Rekaman tersimpan di perangkat. Upload dimulai saat terhubung. | Recording is saved on this device. Upload starts when you're connected. |
| detail.upload.failed | Upload gagal. Rekaman tetap aman di perangkat. | Upload failed. The recording is still safe on this device. |
| detail.quota.waiting | Kuota transkripsi habis. Audio aman; transkripsi dimulai saat kuota tersedia. | Transcription quota used up. Audio is safe; transcription starts when quota is available. |
| detail.quota.cta | Lihat pemakaian | View usage |
| detail.entitlement.expired | Masa uji coba atau paket berakhir. Rekaman aman; transkripsi menunggu paket aktif. | Your trial or plan has ended. The recording is safe; transcription waits for an active plan. |
| step.planExpired | Paket berakhir | Plan ended |
| fail.audioUnreadable | Audio tidak bisa dibaca layanan transkripsi. Mencoba lagi tidak akan membantu. | The transcription service couldn't read this audio. Trying again won't help. |
| fail.provider | Layanan AI sedang bermasalah. Coba lagi sebentar lagi. | The AI service had a problem. Try again in a moment. |
| fail.rejected | Layanan AI menolak permintaan ini. Hubungi dukungan dengan kode %@. | The AI service rejected this request. Contact support with code %@. |
| fail.invalidOutput | Hasil AI tidak valid dan tidak disimpan. Coba lagi. | The AI result was invalid and wasn't saved. Try again. |
| fail.internal | Terjadi kesalahan di server. Coba lagi. Kode: %@ | Something went wrong on the server. Try again. Code: %@ |
| doc.regen.syncFirst | Catatan Anda belum tersinkron. Sinkronkan dulu agar AI memakai versi terbaru. | Your notes haven't synced yet. Sync first so the AI uses the latest version. |
| transcript.edit.offline | Koreksi transkrip memerlukan koneksi. | Correcting the transcript needs a connection. |
| meta.language | Bahasa transkrip | Transcript language |
| meta.language.locked | Bahasa tidak bisa diubah setelah upload atau transkripsi dimulai. | Language can't be changed once upload or transcription has started. |
| meta.conflict.title | Info meeting diubah di perangkat lain | Meeting info changed on another device |
| meta.conflict.body | Perubahan Anda belum disimpan. Pilih versi yang dipakai. | Your changes weren't saved. Choose which version to keep. |
| meta.conflict.keepMine | Pakai perubahan saya | Keep my changes |
| meta.conflict.useLatest | Pakai versi terbaru, buang perubahan saya | Use latest, discard my changes |
| notes.point.delete | Hapus poin | Delete point |
| detail.transcribing | Mentranskripsi audio… biasanya selesai dalam beberapa menit. Anda bisa menutup aplikasi. | Transcribing audio… usually done in a few minutes. You can close the app. |
| detail.transcription.failed | Transkripsi gagal. Rekaman Anda aman dan kuota tidak terpakai. | Transcription failed. Your recording is safe and no quota was used. |
| detail.noSpeech | Tidak ada ucapan yang terdeteksi dalam audio ini, jadi notes dan MoM tidak dibuat. | No speech was detected in this audio, so notes and minutes weren't created. |
| detail.notes.manual | Catatan Anda | Your notes |
| detail.notes.manual.empty | Belum ada catatan. Ketuk untuk menulis. | No notes yet. Tap to write. |
| detail.notes.ai | Notes AI | AI notes |
| detail.notes.generating | Membuat notes dari transkrip… | Creating notes from the transcript… |
| detail.notes.failed | Notes gagal dibuat. Transkrip tetap bisa dibaca dan catatan Anda tetap bisa diedit. | Notes couldn't be created. You can still read the transcript and edit your notes. |
| detail.notes.edit | Edit | Edit |
| doc.regen | Buat ulang | Regenerate |
| doc.regen.footnote | Membuat versi baru. Versi sebelumnya dan hasil edit Anda tetap tersimpan. | Creates a new version. Earlier versions and your edits are kept. |
| doc.regen.limited | Batas pembuatan ulang tercapai. Coba lagi %@. | Regenerate limit reached. Try again %@. |
| doc.outdated.chip | Berdasarkan versi lama | Based on earlier version |
| doc.outdated.body | Sumbernya sudah berubah sejak versi ini dibuat. Isinya tidak diganti otomatis. | Its source changed after this version was created. It isn't replaced automatically. |
| doc.outdated.reason.transcript | Transkrip sudah dikoreksi. | The transcript was corrected. |
| doc.outdated.reason.manualNotes | Catatan Anda sudah berubah. | Your notes changed. |
| doc.outdated.reason.aiNotes | Notes AI sudah berubah. | AI notes changed. |
| doc.versions | Versi | Versions |
| doc.version.row | v%1$lld · %2$@ · %3$@ | v%1$lld · %2$@ · %3$@ |
| doc.version.generated | Dibuat AI | AI generated |
| doc.version.edited | Diedit | Edited |
| doc.version.restored | Dipulihkan dari v%lld | Restored from v%lld |
| mom.none.title | Belum ada MoM | No minutes yet |
| mom.none.body | Buat MoM terstruktur dari notes dan transkrip. Anda bisa mengedit semuanya. | Create structured minutes from notes and the transcript. You can edit everything. |
| mom.create | Buat MoM | Create minutes |
| mom.createManual | Tulis MoM manual | Write minutes manually |
| mom.needsNotes | Buat MoM memerlukan notes AI. Anda tetap bisa menulis MoM manual. | Creating minutes needs AI notes. You can still write minutes manually. |
| detail.notes.manual.add | Tambah poin | Add point |
| detail.player.downloading | Mengunduh audio… %lld%% | Downloading audio… %lld%% |
| mom.source | Dari notes versi: %@ | From notes version: %@ |
| mom.source.latest | v%lld (terbaru) | v%lld (latest) |
| mom.generating | Membuat MoM… Anda bisa meninggalkan layar ini. | Creating minutes… You can leave this screen. |
| mom.failed | MoM gagal dibuat. Notes dan transkrip tetap tersedia. | Minutes couldn't be created. Notes and the transcript are still available. |
| mom.open | Buka editor | Open editor |
| mom.export | Ekspor | Export |
| mom.otherVersions | Versi lain | Other versions |
| review.draft | Draft | Draft |
| review.reviewed | Sudah ditinjau | Reviewed |
| detail.player.play | Putar | Play |
| detail.player.pause | Jeda | Pause |
| detail.player.back15 | Mundur 15 detik | Back 15 seconds |
| detail.player.fwd15 | Maju 15 detik | Forward 15 seconds |
| detail.player.position | Posisi | Position |
| detail.player.error | Audio tidak dapat diputar. | Audio couldn't be played. |
| detail.player.expired | Audio sudah dihapus atau melewati masa simpan. Transkrip, notes, dan MoM tetap tersedia. | Audio was deleted or passed its retention period. Transcript, notes, and minutes are still available. |
| detail.player.offline | Audio belum tersedia di perangkat ini. Hubungkan internet untuk memutar. | Audio isn't on this device. Connect to the internet to play. |
| transcript.corrected | Dikoreksi | Corrected |
| transcript.showOriginal | Lihat versi asli | Show original |
| transcript.showCurrent | Lihat versi terbaru | Show latest |
| transcript.edit | Koreksi teks | Correct text |
| transcript.edit.footnote | Teks asli tetap disimpan. Notes dan MoM yang sudah ada akan ditandai "Berdasarkan versi lama". | The original text is kept. Existing notes and minutes will be marked "Based on earlier version". |
| transcript.copy | Salin | Copy |
| transcript.playing | Sedang diputar | Now playing |
| ref.unverified | Rujukan tidak terverifikasi | Unverified reference |
| ref.fromNotes | Dari catatan pengguna | From user notes |
| ref.playAt | Putar dari %@ | Play from %@ |
| detail.menu.edit | Edit info | Edit info |
| detail.menu.export | Ekspor | Export |
| audio.delete | Hapus audio | Delete audio |
| audio.delete.title | Hapus audio meeting ini? | Delete this meeting's audio? |
| audio.delete.body | Audio di perangkat dan cloud dihapus. Transkrip, notes, dan MoM tetap ada, tetapi audio tidak bisa diputar lagi. | Audio is deleted from this device and the cloud. Transcript, notes, and minutes stay, but audio can't be played again. |
| audio.delete.busy | Audio tidak bisa dihapus saat transkripsi berjalan. Coba lagi setelah selesai. | Audio can't be deleted while transcription is running. Try again when it's done. |
| audio.delete.blocked | Audio belum terunggah. Menghapusnya sekarang berarti meeting ini tidak bisa ditranskripsi. | Audio hasn't been uploaded. Deleting it now means this meeting can't be transcribed. |
| detail.deleting | Menghapus… | Deleting… |
| detail.deleted | Meeting dihapus | Meeting deleted |
| meta.title | Judul | Title |
| meta.date | Tanggal & waktu | Date & time |
| meta.timezone | Zona waktu | Time zone |
| meta.discard.title | Buang perubahan? | Discard changes? |
| meta.discard.confirm | Buang | Discard |
| meta.title.error.long | Judul maksimal 200 karakter. | Title can be up to 200 characters. |

## Accessibility
- Title is header trait; focus lands on it after push.
- Pipeline strip: one element per step, label "Upload: selesai", "Transkrip: memproses", "Notes: gagal" (step + state word).
- Player: play button label toggles Putar/Jeda; slider `accessibilityValue` "12 menit 4 detik dari 42 menit 10 detik"; adjustable action steps 15 s.
- Transcript row: label "<spoken timestamp>, <text>", action "Putar dari 12:04" as default activation; custom actions Koreksi, Salin. Disabled playback → row is static text only.
- Origin and outdated markers are text, read in-line ("AI", "Berdasarkan versi lama").
- Segmented control: system accessibility; tab content change moves VoiceOver focus to first element of the tab.
- Banners announced on appear; failures use `.accessibilityAddTraits(.isStaticText)` with action button separate.

## Interactions
- Retry buttons (only where the failure table allows) create a new user intent with a new persisted `Idempotency-Key`: upload → resume/new session (api §4.4); transcription → `POST …/transcription` (allowed from `transcription_failed`); notes → `POST …/documents/ai_notes/generate`; MoM → `POST …/documents/mom/generate`. Button shows loading, then strip goes Active. `409 job_in_progress` is treated as success (job already running).
- Before any notes/MoM generate (Buat MoM, Buat ulang, Coba lagi): push the pending `manual_notes` edit (and the `ai_notes` edit for MoM) and wait for its 2xx, because the server pins the current server revision. Offline or conflicted → disabled with `doc.regen.syncFirst`.
- Editing manual notes: autosave locally 1 s after typing pause; sync via `PUT …/documents/manual_notes` with `expected_revision` + `mutation_id`; leaving with pending sync → no dialog, save indicator already says "Tersimpan di perangkat" (PRD DOC-03); a toast `editor.leave.saved` confirms once.
- Swipe back always allowed; scroll position and selected tab restored (`state-preservation`).
- When transcript becomes ready while viewing, a polite announcement "Transkrip siap" and the tab content crossfades in (no jump if user is on another tab).
