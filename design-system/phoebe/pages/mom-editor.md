# MoM Editor

PRD §2 "Editor MoM", DOC-01, DOC-02, DOC-03, AI-01, AI-02. The same item editor is reused for AI notes (sections of text items only).

## Purpose
Review and correct the AI draft: sections, participants, decisions with source references, action items (task, PIC, deadline, reference), open issues, follow-up. Versions, Draft/Reviewed status, export. Edits never lost: autosave locally ≤2 s, sync with revision checks, explicit conflict handling.

## Presentation
Pushed from Meeting detail (MoM tab "Buka editor"). Toolbar: back; principal = save indicator (Master 7.12); trailing = Undo (`arrow.uturn.backward`), Redo (`arrow.uturn.forward`), overflow `⋯` (Versi, Tambah bagian, Ekspor, Buat ulang). Bottom bar (`.safeAreaInset(edge: .bottom)`): review control + Ekspor.

## Layout

```
┌──────────────────────────────────┐
│ ‹  Tersimpan ✓        ↶ ↷  ⋯     │
│ [pencil.circle Draft]  v3 · diedit 10.42 │ review chip + version line
│ [Banner: Berdasarkan versi lama / conflict / offline]
│                                   │
│ Informasi meeting                 │ section heading (title2), header trait
│  Judul      Weekly sync produk    │ form-like rows (label + value), editable
│  Tanggal    6 Nov 2026 09.30 WIB  │
│  Peserta    Andi, Sari  [+]       │
│  Agenda     • Budget Q1           │
│                                   │
│ Ringkasan                  ⋯      │ section menu: rename, style, move up/down, delete
│  [sparkles AI] Tim membahas …     │ paragraph-style section, origin marker leading
│                                   │
│ Pembahasan                        │
│  Vendor                [AI]       │ topic item: title + points
│   • [AI, diedit] Vendor A lebih murah 10%  clock 12:15
│   • [Ditambahkan pengguna] Opsi B ditunda
│                                   │
│ Keputusan                         │
│ ┌──────────────────────────────┐ │ decision card
│ │ [AI] Budget Q1 Rp50 juta      │ │
│ │ clock 12:04  clock 18:30      │ │ source refs (buttons)
│ └──────────────────────────────┘ │
│                                   │
│ Action items                      │
│ ┌──────────────────────────────┐ │ action item card
│ │ [AI] Kirim RFQ ke vendor      │ │ task (headline, editable)
│ │ PIC       Sari                │ │
│ │ Deadline  Belum ditentukan    │ │ unspecified value: textSecondary text, no color-only cue
│ │ 13 Nov 2026 (Jumat depan)     │ │ date + original phrase; phrase only if not normalized
│ │ clock 21:02                   │ │
│ └──────────────────────────────┘ │
│  + Tambah action item             │
│ Hal terbuka …                     │
│ Tindak lanjut …                   │
│  + Tambah bagian                  │
│                                   │
│ ┌─────────────────┬────────────┐ │ bottom bar
│ │ ✓ Tandai sudah ditinjau │ Ekspor │
│ └─────────────────┴────────────┘ │
└──────────────────────────────────┘
```
Reading width max 680. Long documents: `ScrollView` + `LazyVStack` of items (variable-height editable fields; reorder via menu, not drag — avoids drag/scroll gesture conflict).

## Content model (UI) — mirrors `docs/api.md` §3.3
- **Meeting info**: title, date/time + time zone, participants (manual list), agenda (text items). Snapshot inside the MoM; editing it doesn't change meeting metadata.
- **Sections**: `summary`, `discussion`, `decisions`, `action_items`, `open_issues`, `follow_ups` (each at most once, deletable) + unlimited `custom`. Each has a heading (editable) and a **section style** (Poin / Bernomor / Paragraf) — style is per section, not per item. Headings are section headings only.
- **Items**: text item (summary, decisions, open issues, follow-ups, custom, agenda), topic item (discussion: title + points), action item (description, PIC, deadline, deadline phrase). Each carries `origin` + `edited` and `sources`.
- Origin marker per item (Master 7.8). Editing an AI item sets `edited` → "AI, diedit". New items are "Ditambahkan pengguna" and need no source.
- AI decisions/action items always show ≥1 source ref; user-added ones show none.

Editing mechanics (plain SwiftUI, iOS 17-safe): each item is a `TextField(axis: .vertical)`. A vertical-axis field inserts `"\n"` on Return instead of firing `onSubmit`, so `onChange(of: text)` detects a typed newline: text before it stays, text after it becomes a new item below in the same section, focus moves there (`@FocusState`). Backspace on an empty item cannot be detected with the software keyboard, so deletion is via the keyboard toolbar `trash` button ("Hapus item", deletes the focused item and focuses the previous), swipe, or the item context menu. Keyboard toolbar: section style menu, Hapus item, Done. Empty items stay in the local draft only and are stripped before sync (api §3). No nesting beyond topic → points.

Action item fields: Tugas (text), PIC (TextField; empty = `nil` → shows `mom.unspecified`), Deadline (optional `DatePicker` + "Hapus tanggal"); `deadline_phrase` shown read-only under it when present. No assignment to other accounts.

AI notes reuse this editor with sections of text items only (no meeting info, no action items).

## States

| State | UI | Action |
|---|---|---|
| Loading | skeleton blocks | — |
| Draft | chip `pencil.circle` "Draft"; bottom primary "Tandai sudah ditinjau" | tap → Reviewed |
| Reviewed | chip `checkmark.seal.fill` "Sudah ditinjau"; bottom shows "Sudah ditinjau ✓" + "Kembalikan ke Draft" (Tertiary) | — |
| Edit after Reviewed | First content change flips to Draft; toast `review.reverted` ("Diubah — status kembali ke Draft"); haptic none | — |
| Mark Reviewed | footnote under button once: `review.footnote` (not a signature, nothing sent) ; haptic success; event `mom_reviewed` | — |
| Saved locally | `save.local` | — |
| Syncing | `save.syncing` | — |
| Synced | `save.synced` | — |
| Offline | `save.offline`; warning banner `editor.offline` once; editing fully available | — |
| Sync failed (non-conflict, e.g. 5xx) | indicator `exclamationmark.icloud` "Belum tersinkron" + auto-retry backoff; tap → retry now | Coba lagi |
| Conflict (`409 revision_conflict`: another device, or a regenerate made a newer revision current) | blocking banner `conflict.*` + sheet (below). Local edits kept in SwiftData until resolved | choose |
| Based on earlier version | warning banner `doc.outdated.*` with "Buat ulang" (creates new MoM version; this one stays) | Buat ulang |
| Viewing old version (read-only) | banner `version.viewing` "Anda melihat v2 (hanya baca)"; fields disabled (read-only styling distinct from disabled: normal text, no field chrome) | Pulihkan versi ini / Kembali ke terbaru |
| Regenerating | inline ProgressView banner `mom.generating`; current version stays editable; when ready, banner "Versi baru v4 siap" + "Buka" (doesn't replace current view) | Buka |
| Regenerate failed | danger banner `mom.failed` | Coba lagi |
| Regenerate rate-limited | menu item disabled + `doc.regen.limited` (time from `/v1/usage.ai_generations.next_slot_at`, so it is shown before tapping) | — |
| Regenerate with unsynced source edits | menu item disabled + `doc.regen.syncFirst` while this document's or the manual notes' edit is pending/offline/in conflict | — |
| AI failed / quota exhausted | editor fully works; regenerate unavailable with reason | — |
| Empty section | placeholder row "Belum ada isi" + "Tambah" | — |
| Delete section | confirm `section.delete.confirm`; undoable in session | Urungkan |
| Leave with pending sync | no blocking dialog; toast `editor.leave.saved` | — |

### Conflict sheet
```
arrow.triangle.branch  Ada versi yang lebih baru
Versi di server (v5, 10.40) berbeda dengan versi yang Anda edit (dasar v4).
[ Lihat versi server ]           (opens read-only preview)
[ Simpan versi saya sebagai revisi baru ]   Primary — PUT with expected_revision = 5, base_revision = 4; v5 stays in history; result keeps v4's sources (stale label if they're older)
[ Pakai versi server, buang perubahan saya ] Destructive — confirm; local draft removed
```
No silent overwrite in either path; option 1 keeps the other device's revision in history.

### Versions sheet
List newest first: "v4 · Dibuat AI · 6 Nov 10.05", "v3 · Diedit · 10.42 · Sudah ditinjau", "v2 · Dipulihkan dari v1"; each row shows source ("Dari notes v2, transkrip r1") and outdated chip if applicable. Tap → read-only view. "Pulihkan" → creates a new revision (toast `version.restored`).

## Undo
Session-scoped `UndoManager` per editor screen: text edits (coalesced per item per pause), item add/delete/move, section add/delete, field changes, review status. Cleared on leaving the screen (PRD: "undo saat sesi aktif"). Shake-to-undo enabled (system).

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| mom.section.info | Informasi meeting | Meeting information |
| mom.section.summary | Ringkasan | Summary |
| mom.section.discussion | Pembahasan | Discussion |
| mom.section.decisions | Keputusan | Decisions |
| mom.section.actions | Action items | Action items |
| mom.section.open | Hal terbuka | Open issues |
| mom.section.followup | Tindak lanjut | Follow-up |
| mom.field.title | Judul | Title |
| mom.field.date | Tanggal | Date |
| mom.field.timezone | Zona waktu | Time zone |
| mom.field.participants | Peserta | Participants |
| mom.field.agenda | Agenda | Agenda |
| mom.action.task | Tugas | Task |
| mom.action.pic | PIC | Owner |
| mom.action.deadline | Deadline | Due date |
| mom.action.deadline.clear | Hapus tanggal | Clear date |
| mom.action.original | Disebut: "%@" | Mentioned as: "%@" |
| mom.action.add | Tambah action item | Add action item |
| mom.decision.add | Tambah keputusan | Add decision |
| mom.unspecified | Belum ditentukan | Not determined |
| mom.notMentioned | Tidak disebutkan | Not mentioned |
| mom.section.empty | Belum ada isi | Nothing here yet |
| mom.section.add | Tambah bagian | Add section |
| mom.section.new | Bagian baru | New section |
| mom.section.rename | Ganti nama bagian | Rename section |
| mom.section.moveUp | Pindah ke atas | Move up |
| mom.section.moveDown | Pindah ke bawah | Move down |
| mom.section.delete | Hapus bagian | Delete section |
| section.delete.confirm | Hapus bagian "%@" beserta isinya? | Delete section "%@" and its content? |
| section.style | Gaya | Style |
| section.style.paragraph | Paragraf | Paragraph |
| section.style.bullet | Poin | Bulleted |
| section.style.numbered | Bernomor | Numbered |
| mom.topic.title | Topik | Topic |
| mom.topic.addPoint | Tambah poin | Add point |
| mom.topic.add | Tambah topik | Add topic |
| item.add | Tambah | Add |
| item.delete | Hapus | Delete |
| item.deleteFocused | Hapus item | Delete item |
| origin.ai | AI | AI |
| origin.aiEdited | AI, diedit | AI, edited |
| origin.human | Ditambahkan pengguna | Added by user |
| origin.ai.a11y | Dibuat AI | Written by AI |
| origin.aiEdited.a11y | Dibuat AI, diedit oleh Anda | Written by AI, edited by you |
| origin.human.a11y | Ditambahkan oleh Anda | Added by you |
| review.markReviewed | Tandai sudah ditinjau | Mark as reviewed |
| review.revertDraft | Kembalikan ke Draft | Return to draft |
| review.footnote | Ini hanya penanda untuk Anda. Bukan tanda tangan atau persetujuan peserta, dan tidak mengirim dokumen ke siapa pun. | This is only a marker for you. It isn't a signature or participant approval, and it doesn't send the document to anyone. |
| review.reverted | Diubah — status kembali ke Draft | Edited — status is back to Draft |
| review.offline | Menandai sudah ditinjau memerlukan koneksi. | Marking as reviewed needs a connection. |
| editor.undo | Urungkan | Undo |
| editor.redo | Ulangi | Redo |
| editor.offline | Offline. Perubahan tersimpan di perangkat dan disinkronkan saat online. | Offline. Changes are saved on this device and sync when you're online. |
| editor.syncFailed | Belum tersinkron | Not synced yet |
| editor.leave.saved | Draft tersimpan di perangkat. Sinkronisasi dilanjutkan otomatis. | Draft saved on this device. Syncing continues automatically. |
| conflict.title | Ada versi yang lebih baru | A newer version exists |
| conflict.body | Versi terbaru (v%1$lld, %2$@) dibuat di perangkat lain atau oleh Buat ulang, setelah versi yang Anda edit (v%3$lld). | The latest version (v%1$lld, %2$@) was made on another device or by Regenerate, after the version you edited (v%3$lld). |
| conflict.view | Lihat versi server | View server version |
| conflict.keepMine | Simpan versi saya sebagai revisi baru | Save my version as a new revision |
| conflict.useServer | Pakai versi server, buang perubahan saya | Use server version, discard my changes |
| conflict.useServer.confirm | Perubahan Anda yang belum tersinkron akan dibuang. | Your unsynced changes will be discarded. |
| version.viewing | Anda melihat v%lld (hanya baca). | You're viewing v%lld (read-only). |
| version.restore | Pulihkan versi ini | Restore this version |
| version.backToLatest | Kembali ke terbaru | Back to latest |
| version.restored | Dipulihkan sebagai v%lld | Restored as v%lld |
| version.source | Dari notes v%1$lld, transkrip r%2$lld | From notes v%1$lld, transcript r%2$lld |
| version.newReady | Versi baru v%lld siap | New version v%lld is ready |
| version.open | Buka | Open |
| mom.regen.confirm.title | Buat ulang MoM? | Regenerate minutes? |
| mom.regen.confirm.body | AI membuat versi baru. Versi ini dan semua hasil edit Anda tetap tersimpan. | The AI creates a new version. This version and all your edits are kept. |

## Accessibility
- Section headings: header trait; rotor "Headings" navigates sections.
- Each item: label = "<origin a11y>, <text>" (section heading read before the first item); refs exposed as custom actions "Putar dari 12:04"; editing via standard text field.
- Action item card: grouped container; each field labeled ("PIC, belum ditentukan").
- Review chip: label "Status: Draft" / "Status: Sudah ditinjau"; toggling announces new status.
- Save indicator: `accessibilityLabel` = text; changes announced politely only for failures and conflicts (not every save).
- Origin readable without color: glyph + "AI"/"Anda" text.
- AX sizes: label/value rows stack vertically; bottom bar buttons stack vertically.

## Interactions
- Autosave: debounce 1 s after last edit (budget ≤2 s), write the draft's wire form into `PendingDocumentEdit.content` (ios.md §4/§10; an in-flight request keeps its own frozen snapshot); sync engine sends `PUT /v1/meetings/{id}/documents/{kind}` with `expected_revision`, `base_revision`, `mutation_id`. Edits typed while a save is in flight are sent next, never dropped. 409 → conflict sheet. Event `document_saved` on 2xx.
- Review: `PUT /v1/meetings/{id}/documents/{kind}/review {status, expected_revision}` after pending edits are pushed (no new revision; nothing is sent to anyone). Offline: button disabled with footnote `review.offline`.
- Regenerate: first push pending `manual_notes` (and for MoM `ai_notes`) edits; then `POST …/documents/mom/generate` (or `ai_notes/generate`) with a persisted `Idempotency-Key`; `429 ai_generation_limit` → refresh `/v1/usage`, `doc.regen.limited` ("Batas generasi AI tercapai. Slot berikutnya tersedia <time>." / "AI generation limit reached. Next slot available <time>.") with `next_slot_at`.
- Ekspor → Export & share sheet for this document; if unsynced edits exist, export waits for sync (see export page).
- Source ref tap → plays audio in a compact inline player sheet (`.presentationDetents([.height(160)])`) with the segment text, so the user doesn't lose their editing place. Its range shows `~mm:ss–~mm:ss` for approximate (chunk-timed) refs, VoiceOver "sekitar …" / "about …".
