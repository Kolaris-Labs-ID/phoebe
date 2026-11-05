# Onboarding & Account

Overrides Master only where stated. PRD §2 "Onboarding dan akun", ACC-01, PRV-01.

## Purpose
Sign the user in (Sign in with Apple), explain microphone use and cloud processing honestly (including that participants must be told), and set language preferences. No mic permission is requested here; it is requested in context on first Record (Recording page).

## Flow
`welcome` → `privacy` → `signIn` → `language` → Library. Shown once per install until `onboarding_completed`; sign-in screen alone is shown again after sign-out / session revoked. Step indicator "1 dari 3" (`multi-step-progress`); Back available on steps 2–3 (`escape-routes`).

## Layout

```
┌──────────────────────────────┐
│                     Lewati ▸ │  (welcome only → jumps to privacy; privacy cannot be skipped)
│                              │
│        [waveform  64pt]      │
│                              │
│   Rekam meeting. Dapatkan    │  title (largeTitle bold, centered)
│   MoM yang bisa diedit.      │
│                              │
│  • mic   Rekam di iPhone     │  3 feature rows: icon + headline + subheadline
│  • quote Transkrip bertimestamp
│  • doc   MoM siap dibagikan  │
│                              │
│ ┌──────────────────────────┐ │
│ │        Lanjut            │ │  Primary, bottom safeAreaInset
│ └──────────────────────────┘ │
│            1 dari 3          │
└──────────────────────────────┘

privacy:
┌──────────────────────────────┐
│ ‹ Kembali                     │
│ [lock.shield]                │
│ Cara Phoebe memproses audio  │ title
│ ┌──────────────────────────┐ │
│ │ mic  Mikrofon            │ │ Card rows (surface): icon, headline, callout
│ │ cloud Diproses di cloud  │ │
│ │ person.2 Beri tahu peserta│ │
│ │ calendar.badge.clock Retensi│
│ │ sparkles AI membuat draft │ │
│ └──────────────────────────┘ │
│ [ Saya mengerti ]            │ Primary
│            2 dari 3          │
└──────────────────────────────┘

signIn:
┌──────────────────────────────┐
│ ‹ Kembali                     │
│        [doc.text 64pt]       │
│   Masuk untuk menyinkronkan  │
│   meeting Anda               │
│   footnote: data disimpan…   │
│ ┌──────────────────────────┐ │
│ │   Sign in with Apple    │ │ SignInWithAppleButton, height 50
│ └──────────────────────────┘ │
│ [Dev login] (DEBUG + env flag only)
│            3 dari 3          │
└──────────────────────────────┘

language (sheet-like page after sign-in):
  Form:
   Section "Bahasa aplikasi": row → opens iOS Settings (per-app language)
   Section "Bahasa transkrip": Picker Otomatis / Indonesia / English
   Section "Bahasa notes & MoM": Picker Indonesia / English (default = app language; saved as user `language`)
  [ Mulai ] Primary
```

## Components
`ContentHeader` (SF Symbol + title + body), `FeatureRow`, `Card` (level 1), Primary button, `SignInWithAppleButton(.signIn)` with `.signInWithAppleButtonStyle(colorScheme == .dark ? .white : .black)`, `Form` + `Picker(.menu)`.

## States

| State | Behavior | Copy key |
|---|---|---|
| Idle | as wireframe | — |
| Signing in | Apple button disabled, `ProgressView` + text under it | `onb.signin.progress` |
| Apple cancelled | Stay on screen, no error shown (user intent) | — |
| Apple failed / server rejected token | Inline danger banner above button, button re-enabled | `onb.signin.error` |
| Offline at sign-in | Warning banner, button disabled | `onb.signin.offline` |
| Session revoked (later launch) | Sign-in screen with info banner; local recordings remain on device and upload after sign-in | `onb.signin.revoked` |
| Dev login | Visible only when built DEBUG and `PHOEBE_DEV_LOGIN=1`; text field "test user ID" (validated on the client as `[a-z0-9._-]{1,64}`, button disabled otherwise; `.textInputAutocapitalization(.never)`) + button | `onb.dev.*` |
| Language save failed | Settings saved locally, synced later; no blocking | — |

## Copy

| Key | Indonesian (id) | English (en) |
|---|---|---|
| onb.skip | Lewati | Skip |
| onb.step | %1$lld dari %2$lld | %1$lld of %2$lld |
| onb.next | Lanjut | Continue |
| onb.back | Kembali | Back |
| onb.welcome.title | Rekam meeting. Dapatkan MoM yang bisa diedit. | Record meetings. Get minutes you can edit. |
| onb.welcome.f1.title | Rekam di iPhone | Record on iPhone |
| onb.welcome.f1.body | Untuk meeting tatap muka atau suara dari perangkat lain. Panggilan di iPhone ini tidak ikut terekam. | For in-person meetings or audio from another device. Calls on this iPhone aren't captured. |
| onb.welcome.f2.title | Transkrip bertimestamp | Timestamped transcript |
| onb.welcome.f2.body | Ketuk kalimat untuk memutar audionya. | Tap a line to play that moment. |
| onb.welcome.f3.title | MoM siap dibagikan | Minutes ready to share |
| onb.welcome.f3.body | Edit, tandai sudah ditinjau, lalu ekspor ke PDF, Word, Markdown, atau WhatsApp. | Edit, mark as reviewed, then export to PDF, Word, Markdown, or WhatsApp. |
| onb.privacy.title | Cara Phoebe memproses audio | How Phoebe processes audio |
| onb.privacy.mic.title | Mikrofon | Microphone |
| onb.privacy.mic.body | Phoebe hanya merekam setelah Anda menekan Rekam. Mikrofon tidak pernah menyala otomatis. | Phoebe records only after you tap Record. The mic never turns on by itself. |
| onb.privacy.cloud.title | Diproses di cloud | Processed in the cloud |
| onb.privacy.cloud.body | Audio dikirim lewat koneksi terenkripsi (TLS) dan disimpan terenkripsi untuk ditranskripsi dan diringkas oleh layanan AI. | Audio is sent over an encrypted connection (TLS) and stored encrypted so AI services can transcribe and summarize it. |
| onb.privacy.consent.title | Beri tahu peserta | Tell participants |
| onb.privacy.consent.body | Izin mikrofon iPhone bukan persetujuan peserta. Beri tahu semua orang sebelum merekam. | iPhone mic permission isn't participant consent. Tell everyone before you record. |
| onb.privacy.retention.title | Retensi | Retention |
| onb.privacy.retention.body | Audio di cloud disimpan %lld hari setelah transkrip selesai. Notes dan MoM disimpan sampai Anda menghapusnya. | Cloud audio is kept for %lld days after the transcript is ready. Notes and minutes are kept until you delete them. |
| onb.privacy.retention.unknown | Masa simpan audio di cloud ditampilkan sebelum upload. | Cloud audio retention is shown before upload. |
| onb.privacy.ai.title | AI membuat draft | AI writes the draft |
| onb.privacy.ai.body | Anda yang menentukan versi final. Hal yang tidak disebutkan ditandai "Belum ditentukan". | You decide the final version. Anything not mentioned is marked "Not determined". |
| onb.privacy.cta | Saya mengerti | I understand |
| onb.signin.title | Masuk untuk menyinkronkan meeting Anda | Sign in to sync your meetings |
| onb.signin.body | Akun Anda hanya dapat melihat meeting milik Anda. | Your account can only see your own meetings. |
| onb.signin.progress | Sedang masuk… | Signing in… |
| onb.signin.error | Gagal masuk. Periksa koneksi lalu coba lagi. | Couldn't sign in. Check your connection and try again. |
| onb.signin.offline | Tidak ada koneksi. Masuk memerlukan internet. | No connection. Signing in needs the internet. |
| onb.signin.revoked | Sesi Anda berakhir. Masuk lagi untuk melanjutkan. Rekaman di perangkat tetap aman. | Your session ended. Sign in again to continue. Recordings on this device are safe. |
| onb.dev.title | Login pengembang | Developer login |
| onb.dev.field | ID pengguna uji (huruf kecil, mis. alice) | Test user ID (lowercase, e.g. alice) |
| onb.dev.cta | Masuk (dev) | Sign in (dev) |
| onb.dev.invalid | Hanya huruf kecil, angka, titik, garis bawah, atau tanda hubung (maks. 64). | Lowercase letters, digits, dot, underscore, or hyphen only (max 64). |
| onb.lang.title | Bahasa | Language |
| onb.lang.app | Bahasa aplikasi | App language |
| onb.lang.app.footer | Diubah di Pengaturan iOS. | Changed in iOS Settings. |
| onb.lang.transcript | Bahasa transkrip | Transcript language |
| onb.lang.output | Bahasa notes & MoM | Notes & minutes language |
| lang.auto | Otomatis | Automatic |
| lang.id | Indonesia | Indonesian |
| lang.en | English | English |
| onb.start | Mulai | Get started |

## Accessibility
- Each step root gets `.accessibilityAddTraits(.isHeader)` on the title; focus moves to title on step change (`focus-on-route-change`).
- Feature/card rows: `.accessibilityElement(children: .combine)`.
- Step indicator read as "Langkah 2 dari 3 / Step 2 of 3" (`onb.step.a11y`: "Langkah %1$lld dari %2$lld" / "Step %1$lld of %2$lld").
- Apple button uses system label. Progress state announces `onb.signin.progress`.
- AX sizes: illustration hidden (`dynamicTypeSize >= .accessibility1`) to keep CTA on screen; content in `ScrollView`.

## Interactions
- Continue/Back: push transition (system). Swipe-back enabled on steps 2–3.
- Successful sign-in → language step → `onboarding_completed` event → Library (replace stack root, no back to onboarding).
- Retention days come from `GET /v1/config` (no auth, fetched at launch, cached). If it can't be fetched yet, the retention row reads `onb.privacy.retention.unknown` instead of a guessed number.
- "Bahasa transkrip" is a local default sent as each new meeting's `language` (`auto|id|en`); "Bahasa notes & MoM" is `PATCH /v1/me {language}`.
