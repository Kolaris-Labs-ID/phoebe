# Recording

PRD §2 "Recording", REC-01, REC-02, REC-03, IMP-01, PRV-01, BIL-01. Engine design: `docs/ios.md` §6.

## Purpose
Capture a meeting reliably: title (optional), pre-record notice, permission, timer, live indicator, pause/resume/stop, quick notes, explicit interruption handling. Completed segments are never lost.

## Presentation
`fullScreenCover` from Library (focused task, no competing navigation). `.interactiveDismissDisabled(true)` while recording/paused; Close (`xmark`) only in `ready` state. Screen stays awake is NOT forced (lock screen recording is supported); `isIdleTimerDisabled` stays default.

## Layout

```
pre-record (ready)
┌──────────────────────────────────┐
│ ✕                                │
│ Judul meeting (opsional)          │ label
│ ┌──────────────────────────────┐ │
│ │ Meeting 6 Nov 09.30          │ │ TextField, placeholder = default title
│ └──────────────────────────────┘ │
│ ┌──────────────────────────────┐ │ Info card (surface)
│ │ person.2  Beri tahu peserta  │ │
│ │ lock.shield Audio diproses di cloud · disimpan 30 hari setelah transkrip
│ │ iphone   Panggilan di iPhone ini tidak ikut terekam
│ └──────────────────────────────┘ │
│ Bahasa transkrip   Otomatis ⌄     │ Picker (.menu) → meeting language
│ [quota / plan banner if blocked]  │
│                                   │
│              (◉)                  │ record button 76pt, accent fill, record.circle
│             Rekam                 │
│   footnote: disabled reason       │
└──────────────────────────────────┘

recording / paused
┌──────────────────────────────────┐
│ ● REC  Weekly sync produk         │ status row: StatusChip + title (tap to rename)
│ [Banner: interruption / storage / route / offline]
│                                   │
│            12:04                  │ timer (monospacedDigit, record tone when live, textPrimary when paused)
│     ▁▃▅▂▆▃▁  (level meter)        │ 7 bars, decorative, hidden from VoiceOver
│   Tersimpan di perangkat · 3 segmen│ footnote, textSecondary
│ ┌──────────────────────────────┐ │
│ │ Catatan              Tersimpan ✓│ │ header + save indicator
│ │ 03:12  Budget Q1 disetujui?   │ │ points list, each stamped with active-audio offset
│ │ [ Tambah poin…            ⏎ ] │ │ TextField; Return adds point, keeps focus
│ └──────────────────────────────┘ │
│                                   │
│        (⏸)          (■)           │ main 76pt (Pause/Resume) + Stop 56pt, ≥24pt apart
│        Jeda        Selesai        │
└──────────────────────────────────┘
```
Keyboard: notes editor scrolls content; controls stay pinned in `.safeAreaInset(edge: .bottom)` above the keyboard so Pause/Stop remain reachable.

## Recording states (local state machine)
`idle → ready → (requestingPermission) → starting → recording ⇄ paused | interrupted → stopping → stopped → queued for upload`

| State | Timer | Main button | Stop | Banner / chip | Copy keys |
|---|---|---|---|---|---|
| ready | hidden | Rekam (`accent` fill, `record.circle`; mic off → no red) | hidden | quota/plan/offline banners if applicable | `rec.ready.*` |
| permission not determined | hidden | Rekam enabled; tap → explanation sheet → system prompt | — | — | `rec.perm.explain.*` |
| permission denied | hidden | disabled, `mic.slash` | — | footnote + "Buka Pengaturan" button | `rec.perm.denied.*` |
| starting | hidden | ProgressView in button, disabled | — | — | `rec.starting` |
| session failed to start | hidden | Rekam re-enabled | — | danger banner with Coba lagi | `rec.error.start` |
| recording | live, record tone, pulse dot | Jeda (`record` fill, `pause.fill`) | Selesai | chip `● REC Merekam` | `rec.state.recording` |
| paused (user) | frozen, textPrimary | Lanjutkan (`accent` fill, `play.fill`) | Selesai | chip `pause.fill` Dijeda (neutral) | `rec.state.paused` |
| interrupted (call, Siri, other app took audio) | frozen | Lanjutkan (enabled only when session can reactivate; disabled + reason while interruption active) | Selesai | warning banner `phone.down.circle` | `rec.interrupt.*` |
| media services reset | frozen | Lanjutkan | Selesai | warning banner | `rec.reset.*` |
| route changed (input device lost) | continues on new input (new segment) | — | — | info banner `headphones`, auto-dismiss after 5 s | `rec.route.*` |
| route change, no input available | frozen → interrupted | Lanjutkan disabled | Selesai | warning banner | `rec.route.noInput` |
| storage low (< 300 MB free) | live | — | — | warning banner, persistent | `rec.storage.low` |
| storage critical (< 100 MB) | stopped safely | — | — | danger banner → transitions to stopped summary | `rec.storage.stopped` |
| segment file write failed | frozen → interrupted | Lanjutkan | Selesai | warning banner | `rec.error.write` |
| max duration near (limit − 10 min) | live | — | — | warning banner | `rec.limit.near` |
| max duration reached (limit − 5 s, so the server's strict limit is never exceeded) | stopped automatically | — | — | info → stopped summary | `rec.limit.reached` |
| offline | live | — | — | neutral banner `wifi.slash`, dismissible (recording unaffected) | `rec.offline` |
| stopping | frozen | disabled ProgressView | disabled | — | `rec.stopping` |
| stopped | — | → summary sheet then dismiss to Meeting detail | — | — | `rec.stopped.*` |

Active duration excludes paused/interrupted time. Limits (`max_audio_duration_ms` from `GET /v1/config`, 5 s stop margin, storage thresholds) come from config, not literals. Retention days come from the cached `ServerConfig`; if it was never fetched, the info card shows `rec.ready.cloud.unknown` and the upload waits until config is fetched (ios.md §8).

### Stop confirmation
`confirmationDialog`: title `rec.stop.title`, primary `rec.stop.confirm` "Selesai & simpan", cancel "Lanjut merekam". Stop is reversible until confirmed; nothing is discarded on Cancel.

### Stopped summary (inline, then auto-navigate)
Shows duration, "Tersimpan di perangkat" + next step: online → "Mengunggah…", offline → "Menunggu koneksi", quota exhausted → "Transkripsi menunggu kuota". Button "Buka meeting" (Primary). Event `recording_saved`.

### Pre-record permission explanation sheet
Shown once before the system prompt: `mic` icon, title, body, Lanjut → system prompt. Denied → page state above.

### Notes during recording
Short points (api §3.1 manual-notes items): a `TextField` "Tambah poin…"; Return appends a point with a stable id and `at_ms` = current active duration (shown as `mm:ss`, monospaced), and keeps focus for the next one. Tap a point to edit it inline. Autosave to SwiftData 1 s after the last keystroke (≤2 s budget) and on every state change. Works offline. Indicator Master 7.12 (local states only). Manual notes are a separate artifact from AI notes.

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| rec.title.label | Judul meeting (opsional) | Meeting title (optional) |
| rec.ready.consent | Beri tahu peserta bahwa meeting ini direkam. | Let participants know this meeting is being recorded. |
| rec.ready.cloud | Audio diproses di cloud dan disimpan %lld hari setelah transkrip selesai. | Audio is processed in the cloud and kept for %lld days after the transcript is ready. |
| rec.ready.cloud.unknown | Audio diproses di cloud. Masa simpan ditampilkan sebelum upload. | Audio is processed in the cloud. The retention period is shown before upload. |
| rec.ready.language | Bahasa transkrip | Transcript language |
| rec.ready.plan | Paket berakhir. Anda tetap bisa merekam; transkripsi menunggu paket aktif. | Your plan has ended. You can still record; transcription waits for an active plan. |
| rec.ready.calls | Panggilan telepon, WhatsApp, Zoom, atau FaceTime di iPhone ini tidak ikut terekam. | Phone, WhatsApp, Zoom, or FaceTime calls on this iPhone aren't recorded. |
| rec.ready.quota | Kuota transkripsi habis. Anda tetap bisa merekam; transkripsi menunggu kuota tersedia. | Transcription quota is used up. You can still record; transcription waits until quota is available. |
| rec.record | Rekam | Record |
| rec.pause | Jeda | Pause |
| rec.resume | Lanjutkan | Resume |
| rec.stop | Selesai | Stop |
| rec.close | Tutup | Close |
| rec.starting | Menyiapkan mikrofon… | Preparing microphone… |
| rec.stopping | Menyimpan… | Saving… |
| rec.state.recording | Merekam | Recording |
| rec.state.paused | Dijeda | Paused |
| rec.state.rec | REC | REC |
| rec.saved.segments | Tersimpan di perangkat · %lld segmen | Saved on device · %lld segments |
| rec.perm.explain.title | Izinkan mikrofon | Allow microphone |
| rec.perm.explain.body | Phoebe memakai mikrofon hanya saat Anda merekam. Anda dapat menjeda atau berhenti kapan saja. | Phoebe uses the microphone only while you record. You can pause or stop anytime. |
| rec.perm.explain.cta | Lanjut | Continue |
| rec.perm.denied.body | Akses mikrofon dimatikan. Izinkan di Pengaturan untuk merekam. Impor audio tetap bisa digunakan. | Microphone access is off. Allow it in Settings to record. You can still import audio. |
| rec.perm.denied.cta | Buka Pengaturan | Open Settings |
| rec.error.start | Mikrofon tidak dapat dimulai. Tutup aplikasi lain yang memakai audio, lalu coba lagi. | Couldn't start the microphone. Close other apps using audio, then try again. |
| rec.error.write | Rekaman dijeda karena file tidak bisa ditulis. Audio sebelumnya sudah tersimpan. Ketuk Lanjutkan. | Recording paused because the file couldn't be written. Earlier audio is saved. Tap Resume. |
| rec.interrupt.title | Dijeda karena interupsi | Paused by interruption |
| rec.interrupt.body | Audio hingga %@ sudah tersimpan. Ketuk Lanjutkan setelah panggilan selesai. | Audio up to %@ is saved. Tap Resume when the call ends. |
| rec.interrupt.waiting | Menunggu audio tersedia… | Waiting for audio to be available… |
| rec.reset.title | Audio sistem dimulai ulang | System audio restarted |
| rec.reset.body | Rekaman dijeda dan bagian sebelumnya sudah tersimpan. Ketuk Lanjutkan. | Recording paused and earlier audio is saved. Tap Resume. |
| rec.route.title | Input audio berubah | Audio input changed |
| rec.route.body | Sekarang merekam dari %@. | Now recording from %@. |
| rec.route.noInput | Tidak ada mikrofon yang tersedia. Rekaman dijeda; audio sebelumnya sudah tersimpan. | No microphone available. Recording paused; earlier audio is saved. |
| rec.storage.low | Penyimpanan iPhone hampir penuh (%@ tersisa). Rekaman akan berhenti otomatis jika penuh. | iPhone storage is almost full (%@ left). Recording stops automatically if it fills up. |
| rec.storage.stopped | Penyimpanan penuh. Rekaman dihentikan dan %@ audio sudah tersimpan. | Storage is full. Recording stopped and %@ of audio is saved. |
| rec.storage.preflight | Penyimpanan tidak cukup untuk mulai merekam. Kosongkan minimal %@. | Not enough storage to start recording. Free up at least %@. |
| rec.limit.near | Batas %1$lld menit tercapai dalam %2$lld menit. | %1$lld-minute limit in %2$lld minutes. |
| rec.limit.reached | Batas %lld menit tercapai. Rekaman dihentikan dan disimpan. | %lld-minute limit reached. Recording stopped and saved. |
| rec.offline | Offline. Rekaman tetap tersimpan di perangkat. | Offline. Recording is still saved on this device. |
| rec.stop.title | Selesaikan rekaman? | Finish recording? |
| rec.stop.confirm | Selesai & simpan | Finish & save |
| rec.stop.cancel | Lanjut merekam | Keep recording |
| rec.stopped.title | Rekaman tersimpan | Recording saved |
| rec.stopped.duration | Durasi %@ | Duration %@ |
| rec.stopped.next.upload | Mengunggah untuk transkripsi… | Uploading for transcription… |
| rec.stopped.next.offline | Menunggu koneksi untuk mengunggah. | Waiting for connection to upload. |
| rec.stopped.next.quota | Transkripsi menunggu kuota tersedia. | Transcription waits until quota is available. |
| rec.stopped.open | Buka meeting | Open meeting |
| rec.notes.label | Catatan | Notes |
| rec.notes.placeholder | Tambah poin… | Add a point… |
| rec.notes.point.a11y | Poin pada %1$@: %2$@ | Point at %1$@: %2$@ |
| rec.notes.hint | Catatan Anda membantu AI memprioritaskan, tapi bukan bukti kesepakatan. | Your notes help the AI prioritize, but aren't proof of agreement. |
| save.local | Tersimpan di perangkat | Saved on device |
| save.saving | Menyimpan… | Saving… |
| save.syncing | Menyinkronkan… | Syncing… |
| save.synced | Tersinkron | Synced |
| save.offline | Tersimpan di perangkat, sinkron saat online | Saved on device, will sync when online |

## Accessibility
- Timer: `accessibilityLabel` "Durasi rekaman" / "Recording duration"; `accessibilityValue` spoken duration; `.accessibilityAddTraits(.updatesFrequently)`.
- Main button label = action (`rec.record` / `rec.pause` / `rec.resume`), value = state ("Merekam" / "Dijeda"), hint on first state "Mulai merekam meeting" / "Starts recording the meeting".
- Stop: label `rec.stop`, hint "Menyelesaikan dan menyimpan rekaman" / "Finishes and saves the recording".
- State changes (recording → interrupted, storage stopped, limit reached) post `AccessibilityNotification.Announcement` with the banner title (high priority for interruption/stop, polite for route/offline).
- Live state is conveyed by chip text "REC/Merekam", timer color AND pulse — never color alone; with Reduce Motion the dot is static.
- Level meter `accessibilityHidden(true)`.
- Magic Tap (`.accessibilityAction(.magicTap)`) toggles Pause/Resume.
- AX sizes: info card in ScrollView; timer uses `@ScaledMetric` capped; control labels below buttons; buttons never shrink below 44pt.

## Interactions
- Record tap: storage preflight (need ≥ 300 MB) → permission → `AVAudioSession.setActive(true)` → engine started, first segment opened, first buffer written → timer starts → haptic `.start` → event `recording_started`. Only then the UI enters `recording` (Start enabled only after session success — REC-01).
- Transcript language picker (pre-record only): Otomatis / Indonesia / English, default = Settings "Bahasa transkrip"; saved as the meeting's `language`.
- Pause: close current segment (so it's durable), haptic light.
- Resume: reactivate session; on failure stay paused with `rec.error.start` banner.
- Interruption began: segment closed, state `interrupted`, haptic `.warning`, event `recording_interrupted`. Interruption ended: never auto-resume; enable Lanjutkan (even if `.shouldResume`).
- Title tap during recording → inline rename sheet; saved locally.
- App backgrounded/locked: recording continues (UIBackgroundModes audio). Returning shows this cover.
- Force quit: no promise; next launch Library shows recovery banner.
