# Settings

PRD §2 "Settings", BIL-01, PRV-01, ACC-01. StoreKit is out of scope for this build (pilot): subscription section is read-only entitlement.

## Purpose
Quota usage, plan/entitlement status, language preferences, retention info and local-audio preference, privacy summary, sign out, and account deletion.

## Presentation
Pushed from Library toolbar gear. `Form` (swiftui stack rule "Use Form for settings").

## Layout

```
┌──────────────────────────────────┐
│ ‹ Meeting      Pengaturan         │
│ PEMAKAIAN                         │
│ ┌──────────────────────────────┐ │
│ │ gauge  42 dari 120 menit terpakai │ │ headline
│ │ ███████░░░░░░░░  (ProgressView) │ │ determinate, value text adjacent (not bar-only)
│ │ 78 menit tersisa · berakhir 30 Nov │ │ subheadline textSecondary (trial: "Berakhir", else "Direset")
│ │ Ditahan untuk proses: 12 menit │ │ (reserved, if any)
│ └──────────────────────────────┘ │
│ PAKET                             │
│  Paket            Uji coba        │ LabeledContent
│  Berlaku sampai   30 Nov 2026     │
│  ✦ 30 generasi AI per 24 jam   Sisa 4 │ LabeledContent (adaptive; stacks at accessibility sizes)
│  ⌛ Slot berikutnya tersedia 14.30 (dalam 2 jam) │ footnote, only when 0 left
│  Catatan otomatis serta setiap pembuatan… memakai satu. │ footnote
│  footnote: Pembelian paket belum tersedia di versi pilot.
│ BAHASA                            │
│  Bahasa aplikasi          Indonesia ›   (opens iOS Settings)
│  Bahasa transkrip         Otomatis  ⌄   (Picker menu)
│  Bahasa notes & MoM       Indonesia ⌄
│ DATA & PRIVASI                    │
│  Audio di cloud disimpan  30 hari setelah transkrip │ LabeledContent (from server)
│  Notes & MoM disimpan     Sampai dihapus
│  Hapus audio lokal setelah transkrip selesai  [ toggle, default off ] │
│  footnote: Audio tetap di iPhone sampai transkrip selesai.
│  Cara Phoebe memproses audio  ›  │ (privacy explainer, same content as onboarding)
│ AKUN                              │
│  Masuk dengan Apple   Ari              │ display_name, or no value (email is never stored)
│  Keluar                           │ (tertiary, accent)
│                                   │
│  Hapus akun                       │ (destructive red text, own last section, footer below)
│  footer: data dihapus maks 30 hari…│
│ Versi 0.1 (build 12)              │ footnote centered
└──────────────────────────────────┘
```

## States

| Area | State | UI | Action |
|---|---|---|---|
| Usage | loading | redacted placeholder card | — |
| Usage | loaded | as wireframe | — |
| Usage | low (≤ 10% left) | warning `exclamationmark.triangle` + `settings.usage.low` under bar | — |
| Usage | exhausted (status active) | warning icon `gauge.with.dots.needle.100percent` + `settings.usage.exhausted` (trial: `settings.usage.exhausted.trial`) | — |
| Usage | entitlement `expired`/`inactive` | warning `calendar.badge.exclamationmark` + `settings.plan.expired`; remaining minutes not shown as available | — |
| Usage | offline | last known values + footnote `settings.usage.stale` ("Terakhir diperbarui %@") | — |
| Usage | error | inline `settings.usage.error` | Coba lagi |
| Plan | entitlement `status` `active` / `expired` / `inactive` | LabeledContent: plan name (`trial` → `plan.trial`; unknown plan ids shown as returned) + status word (`plan.status.*`) | — |
| AI generations (C2) | from `/v1/usage.ai_generations` `{limit, window_hours, used, reserved, remaining, next_slot_at}` | `sparkles` label `ai.allowance.label` ("30 generasi AI per 24 jam", numbers from the server) with value `ai.allowance.left` ("Sisa n"); footnote `ai.allowance.footnote` | — |
| AI generations used up (`remaining` = 0) | same row, value "Sisa 0" | adds `hourglass` footnote `ai.allowance.next` with `next_slot_at` as time (date too when not today) plus relative time, e.g. "Slot berikutnya tersedia 14.30 (dalam 2 jam)"; the row is one VoiceOver element | — |
| Language | output language → `PATCH /v1/me {language}`; transcript language → local default for new meetings | saved locally immediately, synced | — |
| Language | sync failed | footnote `settings.syncPending` | auto retry |
| Local audio toggle | default off. On → local audio of meetings with a transcript is deleted (never before the transcript exists: until then cloud audio may be the only other copy); existing eligible copies deleted after confirm | confirm dialog `settings.localAudio.confirm` | — |
| Sign out | pending uploads exist | confirm dialog warns `settings.signout.pending` (recordings stay on device and upload after next sign-in to the same account) | Keluar / Batal |
| Sign out | no pending | confirm simple `settings.signout.confirm` | — |
| Sign out | in progress | button loading | — |
| Delete account | confirm step 1 | `.confirmationDialog` explaining consequences | Lanjut |
| Delete account | confirm step 2 | alert requiring typing `HAPUS` / `DELETE` (irreversible, `confirmation-dialogs`) | Hapus akun |
| Delete account | in progress | full-screen ProgressView `settings.delete.progress` | — |
| Delete account | done | local data wiped (SwiftData store, recordings, Keychain) → onboarding welcome with banner `settings.delete.done` | — |
| Delete account | failed / offline | danger alert `settings.delete.failed`; nothing deleted locally | Coba lagi |

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| settings.title | Pengaturan | Settings |
| settings.usage | Pemakaian | Usage |
| settings.usage.used | %1$lld dari %2$lld menit terpakai | %1$lld of %2$lld minutes used |
| settings.usage.left | %lld menit tersisa | %lld minutes left |
| settings.usage.reset | Direset %@ | Resets %@ |
| settings.usage.ends | Berakhir %@ | Ends %@ |
| settings.usage.reserved | Ditahan untuk proses berjalan: %lld menit | Held for processing in progress: %lld minutes |
| settings.usage.low | Kuota hampir habis. | Quota is almost used up. |
| settings.usage.exhausted | Kuota habis. Rekaman tetap bisa disimpan; transkripsi menunggu periode berikutnya. | Quota used up. You can still save recordings; transcription waits for the next period. |
| settings.usage.exhausted.trial | Kuota uji coba habis. Rekaman tetap bisa disimpan; transkripsi menunggu tambahan kuota. | Trial quota used up. You can still save recordings; transcription waits for more quota. |
| settings.plan.expired | Paket berakhir %@. Rekaman tetap bisa disimpan; transkripsi menunggu paket aktif. | Plan ended %@. You can still save recordings; transcription waits for an active plan. |
| settings.usage.stale | Terakhir diperbarui %@ | Last updated %@ |
| settings.usage.error | Pemakaian tidak dapat dimuat. | Couldn't load usage. |
| settings.usage.a11y | Pemakaian kuota: %1$lld dari %2$lld menit, %3$lld menit tersisa | Quota usage: %1$lld of %2$lld minutes, %3$lld minutes left |
| settings.plan | Paket | Plan |
| settings.plan.name | Paket | Plan |
| settings.plan.until | Berlaku sampai | Valid until |
| plan.trial | Uji coba | Trial |
| plan.status.active | Aktif | Active |
| plan.status.expired | Berakhir | Expired |
| plan.status.inactive | Tidak aktif | Inactive |
| ai.allowance.label | %1$lld generasi AI per %2$lld jam | %1$lld AI generations per %2$lld hours |
| ai.allowance.left | Sisa %lld | %lld left |
| ai.allowance.next | Slot berikutnya tersedia %@ | Next slot available %@ |
| ai.allowance.footnote | Catatan otomatis serta setiap pembuatan atau pembuatan ulang catatan dan MoM memakai satu. | Automatic notes and each notes or minutes generation or regeneration use one. |
| settings.plan.footnote | Pembelian paket belum tersedia di versi pilot. | Plan purchases aren't available in the pilot version. |
| settings.language | Bahasa | Language |
| settings.language.app | Bahasa aplikasi | App language |
| settings.language.transcript | Bahasa transkrip | Transcript language |
| settings.language.output | Bahasa notes & MoM | Notes & minutes language |
| settings.syncPending | Akan disinkronkan saat online. | Will sync when online. |
| settings.privacy | Data & privasi | Data & privacy |
| settings.retention.audio | Audio di cloud disimpan | Cloud audio kept for |
| settings.retention.audio.value | %lld hari setelah transkrip | %lld days after transcript |
| settings.retention.docs | Notes & MoM disimpan | Notes & minutes kept |
| settings.retention.docs.value | Sampai dihapus | Until deleted |
| settings.localAudio | Hapus audio lokal setelah transkrip selesai | Delete local audio after the transcript is ready |
| settings.localAudio.footer | Audio selalu tetap di iPhone sampai upload terverifikasi dan transkrip selesai. | Audio always stays on this iPhone until the upload is verified and the transcript is ready. |
| settings.localAudio.confirm | Hapus juga audio lokal meeting yang sudah punya transkrip (%@)? | Also delete local audio of meetings that already have a transcript (%@)? |
| settings.privacy.explainer | Cara Phoebe memproses audio | How Phoebe processes audio |
| settings.privacy.noE2E | Audio dan dokumen dienkripsi saat dikirim dan saat disimpan, lalu diproses oleh layanan AI di server. Ini bukan enkripsi end-to-end. | Audio and documents are encrypted in transit and at rest, then processed by AI services on our servers. This isn't end-to-end encryption. |
| settings.account | Akun | Account |
| settings.account.apple | Masuk dengan Apple | Signed in with Apple |
| settings.account.dev | Login pengembang | Developer login |
| settings.signout | Keluar | Sign out |
| settings.signout.confirm | Keluar dari Phoebe? | Sign out of Phoebe? |
| settings.signout.pending | %lld rekaman belum terunggah. Rekaman tetap di iPhone dan diunggah setelah Anda masuk lagi dengan akun yang sama. | %lld recordings haven't uploaded. They stay on this iPhone and upload after you sign in again with the same account. |
| settings.delete | Hapus akun | Delete account |
| settings.delete.footer | Akses dicabut segera. Data aktif dihapus dalam maksimal 30 hari; cadangan kedaluwarsa dalam maksimal 90 hari. | Access is revoked immediately. Active data is deleted within 30 days; backups expire within 90 days. |
| settings.delete.step1.title | Hapus akun dan semua data? | Delete your account and all data? |
| settings.delete.step1.body | Semua meeting, audio, transkrip, notes, dan MoM akan dihapus, termasuk yang ada di iPhone ini. Tindakan ini tidak dapat dibatalkan. | All meetings, audio, transcripts, notes, and minutes will be deleted, including those on this iPhone. This can't be undone. |
| settings.delete.step1.cta | Lanjut | Continue |
| settings.delete.step2.title | Ketik HAPUS untuk konfirmasi | Type DELETE to confirm |
| settings.delete.step2.word | HAPUS | DELETE |
| settings.delete.step2.cta | Hapus akun | Delete account |
| settings.delete.progress | Menghapus akun… | Deleting account… |
| settings.delete.done | Akun Anda telah dihapus. | Your account has been deleted. |
| settings.delete.failed | Akun belum dihapus. Periksa koneksi lalu coba lagi. | Your account wasn't deleted. Check your connection and try again. |
| settings.version | Versi %1$@ (build %2$@) | Version %1$@ (build %2$@) |

## Accessibility
- Usage card is one element using `settings.usage.a11y`; ProgressView has the same value (bar isn't the only cue — numbers are written).
- Destructive section separated and last (`destructive-nav-separation`); "Hapus akun" button has `role: .destructive` (VoiceOver announces).
- Typed confirmation field: visible label, `.autocorrectionDisabled()`, `.textInputAutocapitalization(.characters)`; Delete button disabled until match, with hint stating why.
- Pickers use system `Picker` (accessible by default).

## Interactions
- App language row: `UIApplication.openSettingsURLString` (per-app language in iOS Settings). No in-app override.
- Usage: `GET /v1/usage` on appear + pull-to-refresh (also refetched app-wide on launch/foreground while any meeting waits for quota or plan, ios.md §8); minutes shown are `floor(seconds/60)` of `consumed_seconds`, `reserved_seconds`, `remaining_seconds`, `entitlement.quota_seconds`; `entitlement.period_end` shown with `settings.usage.ends` for `plan == trial` (a trial has no next period) and `settings.usage.reset` otherwise (server is source of truth; no client rounding per segment).
- Account row: value = `User.display_name` (empty → no value); the backend stores no email.
- Sign out: `POST /v1/auth/logout`, delete Keychain token, keep local recordings and pending uploads keyed by user ID (not uploaded under another account).
- Delete account: `DELETE /v1/me`; on 2xx wipe all local data and Keychain.
