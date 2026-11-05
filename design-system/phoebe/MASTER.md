# Phoebe iOS Design System (Master)

> Retrieval: when building a screen, read this file, then `pages/<screen>.md`. Page rules override Master.
> Platform: native iOS 17+, SwiftUI, Swift 6. Languages: Bahasa Indonesia (`id`, default) + English (`en`). Light + dark.

Supersedes the generated landing-page master (Exaggerated Minimalism, Inter, purple/pink, GSAP scroll reveal). That output targeted a marketing site; Phoebe is a native tool used during and after meetings, so it is rebuilt here on iOS idioms.

## 0. Sources

| Decision | Source |
|---|---|
| Style = Minimalism + Flat, functional color | `search.py --domain product "meeting notes transcription productivity"` → *Voice Recorder & Memo*: "Minimalism + AI-Native UI", palette focus "Clean white + recording red + waveform accent"; *Productivity Tool*: "Flat Design + Micro-interactions", "Clear hierarchy + functional colors" |
| Teal accent | `--domain color "productivity calm professional"` → *Productivity Tool* primary `#0D9488`. Darkened to `#0F766E` because `#0D9488` on white = 3.74:1 (fails 4.5:1) |
| Neutral ink/grey base | `--domain color "notes trust minimal blue"` → *Changelog* / *Notes & Writing* neutrals (`#1E293B`, `#64748B`, `#E2E8F0`) |
| Dynamic Type, accessibilityLabel, semantic views, reduced motion | `--stack swiftui "accessibility dynamic type voiceover"`, `"time timing animation"` |
| `List`, `.onDelete`, `NavigationStack` + `navigationDestination`, `@Observable`, `@Bindable`, `.task`, `@MainActor`, `@FocusState`, `Form` for settings | `--stack swiftui` queries: "list search filter", "state observable", "task async loading", "sheet modal navigation", "text field form focus" |
| Loading > 300 ms feedback, error recovery path, confirm destructive, empty-state action, errors announced | `--domain ux` queries: "loading progress retry error", "destructive confirmation delete", "empty state", "offline sync status", validation pass "animation accessibility z-index loading" |
| Safe areas, 44pt, 8pt spacing between touchables, icon button labels | `--domain web "accessibilityLabel touch safe-areas"` |
| Pre-delivery checklist, light/dark rules, icon discipline | `references/pro-rules.md` |
| Tabular figures, truncation, `text-styles-system`, `color-dark-mode` | `references/quick-reference.md` §6 |

Zero-result queries (not fabricated, fell back to built-in rules): `--stack swiftui "recording audio timer"` (0), `"color dark mode"` (0), `"button style"` (0); `--domain ux "editor autosave"` (0), retry `"form draft save"` returned only generic submit-feedback. Autosave behavior below comes from PRD DOC-03 + quick-reference `form-autosave`. `--domain icons` returns web libraries (Phosphor) only; iconography uses SF Symbols per `platform-adaptive` and `system-controls`.

## 1. Principles

1. **Calm, trustworthy.** Neutral surfaces, one teal accent, no gradients, no decorative motion. `record` red means "microphone is live" and appears only from the `recording` state on; idle/ready record buttons use `accent` with the `record.circle` glyph. (`danger` red for failures/destructive actions is a separate token with its own icon + text.)
2. **Status is never color alone.** Every state = SF Symbol + text (+ color). VoiceOver reads the text.
3. **AI drafts, user decides.** Origin (AI vs user) and Draft/Reviewed are always visible as icon + word.
4. **Nothing silently lost.** Every failure says what is still safe ("Rekaman aman di perangkat") and offers one recovery action.
5. **Native first.** System controls (`List`, `Form`, `Picker(.segmented)`, `ShareLink`/`UIActivityViewController`, `.searchable`, `.confirmationDialog`), system font, SF Symbols.

## 2. Color tokens

Defined once in `ios/Phoebe/DesignSystem/Tokens.swift` as dynamic `UIColor { traits in ... }` wrapped in `Color`. No raw hex in views. Ratios are WCAG 2.x, computed with a script (sRGB relative luminance), not estimated.

| Token | Light | Dark | Use |
|---|---|---|---|
| `bg` | `#F5F6F8` | `#0B0D10` | Screen background (grouped) |
| `surface` | `#FFFFFF` | `#16191E` | Rows, cards, sheets |
| `surface2` | `#EEF0F3` | `#1F242B` | Input fill, chip fill neutral, segmented track |
| `textPrimary` | `#111827` | `#F3F4F6` | Body, titles |
| `textSecondary` | `#4B5563` | `#B4BAC4` | Metadata, captions, timestamps |
| `textTertiary` | `#6B7280` | `#8E96A3` | Placeholder, disabled label (still ≥4.5) |
| `separator` | `#D1D5DB` | `#3A414B` | Hairlines (decorative, not sole affordance) |
| `borderStrong` | `#8A929E` | `#6E7682` | Text field outline, focus-independent control edges (≥3:1) |
| `accent` | `#0F766E` | `#2DD4BF` | Primary buttons, links, selection, tint |
| `onAccent` | `#FFFFFF` | `#042F2E` | Text/icon on `accent` fill |
| `accentSoft` | `#E6F4F2` | `#0F2F2C` | Selected row, tag chip fill |
| `record` | `#C4161C` | `#FF6B6B` | Live mic only: main button while recording, REC dot, timer while recording. Never on idle/paused Record buttons |
| `onRecord` | `#FFFFFF` | `#2A0405` | Glyph on record fill |
| `recordSoft` | `#FDECEC` | `#3A1416` | Recording banner fill |
| `warning` | `#92400E` | `#FBBF24` | Interruption, quota low, offline, "Based on earlier version" |
| `warningSoft` | `#FEF3C7` | `#3A2A08` | Warning banner/chip fill |
| `success` | `#166534` | `#4ADE80` | Reviewed, upload verified, ready |
| `successSoft` | `#E8F5EC` | `#0F2E1B` | Success chip fill |
| `danger` | `#B91C1C` | `#F87171` | Failure states, destructive actions |
| `dangerSoft` | `#FDECEC` | `#3A1416` | Failure banner fill |
| `info` | `#1D4ED8` | `#93C5FD` | In-progress processing |
| `infoSoft` | `#E8EFFD` | `#132544` | Processing chip fill |
| `aiOrigin` | `#4338CA` | `#A5B4FC` | AI-origin marker |
| `aiSoft` | `#EEF0FF` | `#1E2147` | AI-origin marker fill |
| `humanOrigin` | `#374151` | `#D1D5DB` | User-origin marker |
| `scrim` | `#000000` @ 45% | `#000000` @ 60% | Modal scrim (pro-rules 40–60%) |

### Verified contrast (text pairs, min 4.5:1)

| Pair | Light | Dark |
|---|---|---|
| textPrimary / bg | 16.41 | 17.68 |
| textPrimary / surface | 17.74 | 16.01 |
| textPrimary / surface2 | 15.54 | 14.18 |
| textSecondary / surface | 7.56 | 9.03 |
| textSecondary / surface2 | 6.62 | 8.00 |
| textTertiary / surface | 4.83 | 5.91 |
| accent / surface | 5.47 | 9.46 |
| accent / bg | 5.06 | 10.45 |
| onAccent / accent | 5.47 | 7.77 |
| accent / accentSoft | 4.84 | 7.71 |
| record / surface | 6.04 | 6.35 |
| onRecord / record | 6.04 | 6.77 |
| record / recordSoft | 5.29 | 5.86 |
| warning / surface | 7.09 | 10.55 |
| warning / warningSoft | 6.37 | 8.31 |
| success / surface | 7.13 | 10.11 |
| success / successSoft | 6.35 | 8.45 |
| danger / surface | 6.47 | 6.37 |
| danger / dangerSoft | 5.66 | 5.88 |
| info / surface | 6.70 | 9.77 |
| info / infoSoft | 5.81 | 8.46 |
| aiOrigin / surface | 7.90 | 8.84 |
| aiOrigin / aiSoft | 6.98 | 7.72 |
| humanOrigin / surface | 10.31 | 11.96 |

Non-text: `borderStrong` / surface = 3.14 (light), 3.84 (dark) → meets 3:1 for control boundaries. `separator` is 1.47 / 1.71 and is decorative only; never the only cue that something is tappable.

Rejected from generated master: pink CTA `#EC4899` + white = 3.53 (fails); purple/pink "AI hype" tone conflicts with calm/trust brief and pink competes with recording red.

`PhoebeTests/ContrastTests.swift` asserts every row above resolves ≥4.5 in both trait collections, so token edits cannot regress.

## 3. Typography

**Font: SF Pro (system), not Inter.** Reasons: (1) Dynamic Type scaling, optical sizes (Text/Display) and `.leading` adjustments come free with text styles; a custom font needs `UIFontMetrics` plumbing on every style; (2) full Latin Extended coverage for Indonesian and English loanwords with no bundle weight; (3) matches system controls (`List`, `Form`, nav bars) so screens don't look half-custom (`platform-adaptive`, `system-controls`); (4) SF has tabular figures via `.monospacedDigit()` for timers and timestamps (`number-tabular`). Inter was chosen by the generator for a web landing page.

Always use text styles; never `.system(size:)` (swiftui stack rule "Support Dynamic Type", severity High).

| Token | SwiftUI | Default size/weight (Large) | Use |
|---|---|---|---|
| `display` | `.largeTitle.weight(.bold)` | 34 bold | Library nav title (system large title) |
| `timer` | `.system(.largeTitle, design: .rounded).weight(.semibold).monospacedDigit()` scaled via `@ScaledMetric` base 56 | 56 semibold | Recording timer |
| `title` | `.title2.weight(.semibold)` | 22 semibold | Meeting title in detail header, MoM section heading |
| `headline` | `.headline` | 17 semibold | Row title, card title, button label |
| `body` | `.body` | 17 regular | Transcript, notes, MoM body |
| `callout` | `.callout` | 16 regular | Banner body |
| `subheadline` | `.subheadline` | 15 regular | Row metadata |
| `footnote` | `.footnote` | 13 regular | Helper text, legal copy |
| `caption` | `.caption.weight(.medium)` | 12 medium | Chips, timestamps (`.monospacedDigit()`) |

Rules: body line spacing default (SF already ~1.3 at 17pt; add `.lineSpacing(4)` for transcript/MoM reading views to approach 1.5); wrap, don't truncate, except row titles (2 lines max, full title in detail) (`truncation-strategy`); reading views constrain to `maxWidth: 680` on iPad (`readable text measure`); no letter-spacing changes.

Accessibility sizes (AX1–AX5): horizontal stacks of metadata switch to vertical via `ViewThatFits` or `dynamicTypeSize.isAccessibilitySize`; record controls stay 1 row but labels move below icons.

## 4. Spacing, radius, elevation, layout

Spacing (4/8 rhythm): `xxs 2`, `xs 4`, `sm 8`, `md 12`, `lg 16`, `xl 24`, `xxl 32`, `xxxl 48`.
- Screen horizontal inset: 16 (compact), 20 (regular width / landscape), reading views centered max 680.
- Row vertical padding 12; between sections 24; between touch targets ≥8.

Radii: `sm 6` (chips, small buttons), `md 10` (text fields, banners), `lg 14` (cards, sheets content), `full` (record button, pills). Use `.continuous` corner style.

Elevation (flat style, hierarchy by surface color not shadow):
| Level | Light | Dark | Use |
|---|---|---|---|
| 0 | `bg` | `bg` | Screen |
| 1 | `surface`, no shadow | `surface` | Rows, cards |
| 2 | `surface` + shadow `black 8%, y 2, blur 8` | `surface2`, no shadow (shadows invisible on dark) | Floating record bar, mini player |
| 3 | system sheet | system sheet | `.sheet`, `.confirmationDialog` (system scrim) |

Z-order (validation pass "Z-Index Management"): content 0 → sticky banners 10 → floating bottom bar 20 → toast 30 → sheet (system).

Safe areas: bottom bars use `.safeAreaInset(edge: .bottom)` so lists get correct content inset (`fixed-element-offset`, `Scroll and fixed element coexistence`).

## 5. Motion

| Token | Value | Use |
|---|---|---|
| `fast` | 0.15 s `.easeOut` | Press feedback, chip state change |
| `standard` | 0.25 s `.snappy` (spring) | Banner in/out, segment switch, row insert |
| `exit` | 0.17 s `.easeIn` | Banner/toast dismissal (≈70% of enter) |
| `recordPulse` | 1.2 s ease-in-out repeat, opacity 1→0.35 | REC dot only while recording |

Rules: animate opacity/offset/scale only; never block input; max 1–2 animated elements per view; no list stagger. Press = system button styles (opacity/highlight within 100 ms), custom buttons scale 0.97.
**Reduced motion** (`@Environment(\.accessibilityReduceMotion)`): pulse replaced by static dot + "REC" text (already present); spring transitions become 0.15 s crossfade; no level-meter animation (meter shows static peak bars updated at 2 Hz). Haptics stay (they are not motion): `.sensoryFeedback(.start)` on record start, `.stop` on stop, `.warning` on interruption, `.success` on Mark Reviewed.

## 6. Iconography (SF Symbols)

One family, rendering `.hierarchical` for status, `.monochrome` for toolbar. Outline symbols for navigation/toolbar, `.fill` variants only for active/selected state (filled-vs-outline discipline). Sizes via `.imageScale` relative to text style: `sm` = `.caption` chips, `md` = `.body` rows/toolbars, `lg` = 28pt `@ScaledMetric` record controls.

| Concept | SF Symbol |
|---|---|
| Record (start, mic off) | `record.circle` on `accent` fill |
| Recording live dot | `circle.fill` (tinted `record`) |
| Pause | `pause.fill` |
| Resume | `play.fill` |
| Stop | `stop.fill` |
| Microphone permission | `mic` / denied `mic.slash` |
| Import audio | `square.and.arrow.down` |
| New meeting | `plus` (toolbar), `waveform.badge.plus` (empty state) |
| Folder | `folder` / selected `folder.fill`; move to folder `folder.badge.plus` |
| Tag | `tag` / selected `tag.fill` |
| Filter | `line.3.horizontal.decrease.circle` / active `.fill` |
| Search | system `.searchable` |
| Transcript | `text.quote` |
| Notes (manual + AI) | `note.text` |
| MoM | `doc.text` |
| Export | `arrow.up.doc` |
| Share | `square.and.arrow.up` |
| Copy text | `doc.on.doc` |
| Reviewed | `checkmark.seal.fill` |
| Draft | `pencil.circle` |
| AI-origin | `sparkles` |
| Human-origin | `person.fill` |
| From user notes (source label) | `note.text.badge.plus` |
| Source reference (timestamp) | `clock` |
| Interruption | `phone.down.circle` |
| Route change (headphones unplugged) | `headphones` |
| Storage low | `externaldrive.badge.exclamationmark` |
| Offline / Waiting for connection | `wifi.slash` |
| Upload queued | `icloud.and.arrow.up` |
| Uploading | `arrow.up.circle` + determinate progress ring |
| Upload verified | `checkmark.icloud` |
| Upload failed | `exclamationmark.icloud` |
| Processing (transcribing / generating) | `waveform` (transcribing), `sparkles` (generating) + `ProgressView()` |
| Stage failed | `exclamationmark.triangle` |
| Retry | `arrow.clockwise` |
| Based on earlier version | `clock.arrow.circlepath` |
| Version history | `clock.arrow.circlepath` (toolbar) |
| Restore | `arrow.uturn.backward` |
| Quota | `gauge.with.dots.needle.33percent`; exhausted `gauge.with.dots.needle.100percent` |
| Audio expired / playback off | `speaker.slash` |
| Play / pause playback | `play.fill` / `pause.fill`; skip `gobackward.15` / `goforward.15` |
| Recovery of segments | `arrow.counterclockwise.circle` |
| Language | `globe` |
| Retention | `calendar.badge.clock` |
| Delete | `trash` |
| Sign out | `rectangle.portrait.and.arrow.right` |
| Privacy / cloud processing | `lock.shield` |
| Conflict | `arrow.triangle.branch` |
| Settings | `gearshape` |

Icon-only buttons always carry `.accessibilityLabel` (localized) and are framed ≥44×44.

## 7. Components

All in `ios/Phoebe/DesignSystem/`. Each takes localized `LocalizedStringResource`, never raw strings.

### 7.1 Buttons
| Variant | Spec |
|---|---|
| `Primary` | `.borderedProminent`, tint `accent`, label `headline`, min height 50, full width in sheets/bottom bars, radius `md`. One per screen (`primary-action`). |
| `Secondary` | `.bordered`, tint `accent`, min height 44. |
| `Tertiary` | `.borderless` text button, `accent`, min hit 44. |
| `Destructive` | `role: .destructive`; in dialogs only via `.confirmationDialog`; in Settings as separated section at bottom (`destructive-nav-separation`). |
| `Loading` | Label replaced by `ProgressView()` + same label text kept for VoiceOver; `.disabled(true)` during request (`loading-buttons`). |
| Disabled | system disabled (opacity ~0.4) + reason shown as footnote below when not obvious (e.g., "Izinkan mikrofon untuk mulai merekam"). |

### 7.2 StatusChip (icon + text, never color alone)
`HStack(spacing: 4) { Image(systemName:) ; Text }`, `caption` font, padding 4×8, radius `sm`, fill `<tone>Soft`, foreground `<tone>`. `accessibilityElement(children: .combine)`; label = text. Tones: `neutral` (surface2/textSecondary), `info`, `success`, `warning`, `danger`, `record`, `ai`.

Canonical chips (copy in `pages/library.md`):
| State | Icon | Tone |
|---|---|---|
| Local only (not uploaded) | `iphone` | neutral |
| Waiting for connection | `wifi.slash` | warning |
| Queued for upload | `icloud.and.arrow.up` | neutral |
| Uploading n% | `arrow.up.circle` | info |
| Uploaded / verifying | `checkmark.icloud` | info |
| Waiting for quota | `gauge.with.dots.needle.100percent` | warning |
| Plan ended (entitlement expired/inactive) | `calendar.badge.exclamationmark` | warning |
| Waiting for storage (merge) | `externaldrive.badge.exclamationmark` | warning |
| Deleted on another device (local audio kept) | `iphone.slash` | warning |
| Transcribing | `waveform` | info |
| Transcript ready | `text.quote` | success |
| Generating notes | `sparkles` | info |
| Notes ready | `note.text` | success |
| Generating MoM | `sparkles` | info |
| MoM draft | `pencil.circle` | neutral |
| Reviewed | `checkmark.seal.fill` | success |
| Stage failed | `exclamationmark.triangle` | danger |
| Recording | `circle.fill` | record |
| Paused by interruption | `phone.down.circle` | warning |
| Based on earlier version | `clock.arrow.circlepath` | warning |

### 7.3 Banner
Inline, top of content, full width minus insets. `HStack(alignment: .top)`: icon (`body`), VStack(title `headline`, message `callout`), optional action button (`Secondary`, small) and optional close (`xmark`, 44pt hit). Fill `<tone>Soft`, text `textPrimary` for message, icon + title in `<tone>`. Radius `md`, padding 12. Announced with `AccessibilityNotification.Announcement` on appear (polite; doesn't steal focus — `toast-accessibility`). Never auto-dismiss failure banners; info banners may dismiss after action.

### 7.4 Toast
Bottom, above safe area and bottom bar, `surface2` (dark) / `textPrimary` bg + `surface` text (light inverse), `callout`. Informational toasts auto-dismiss after 4 s (`toast-dismiss` 3–5 s). Toasts with an action ("Urungkan / Undo") stay 8 s; when `UIAccessibility.isVoiceOverRunning` or `isSwitchControlRunning`, they never auto-dismiss and show a close button (`xmark`, 44pt), and the toast is reachable as an element (posted with `AccessibilityNotification.Announcement`, action exposed as an accessibility action). The toast is never the only route: every undoable action is also undoable from the editor toolbar Undo (or the same menu that performed it). Used for: "Disalin / Copied", "Dipindahkan ke <folder>", "Dipulihkan sebagai versi baru", section delete undo.

### 7.5 List row (meeting)
`List` `.insetGrouped`. Row min height 64:
```
[icon 28 in 40 circle surface2]  Title (headline, 2 lines)
                                  Date · Duration (subheadline, textSecondary, monospacedDigit)
                                  [StatusChip] [folder chip] [#tag …]   (wraps)
```
Swipe actions (with visible alternatives in context menu and detail toolbar — `gesture-alternative`): trailing `Hapus` (destructive, confirm), leading `Pindahkan`. Combined accessibility label: "<title>, <date>, <duration>, <status>, folder <x>, tag <y>". Custom actions `.accessibilityActions` for Move, Tag, Delete.

### 7.6 Segmented tabs
`Picker(selection:) { … }.pickerStyle(.segmented)` under the detail header: Transkrip / Catatan / MoM (Transcript / Notes / MoM). Each segment text, not icon-only. Each segment may show a status via the content area, not inside the segment (segments can't hold chips readably). Selection persisted per meeting in `@SceneStorage`.

### 7.7 Record button
Circle 76pt (`@ScaledMetric` capped 96). Idle/ready (mic off): fill `accent`, glyph `record.circle` 28pt `onAccent`, label "Rekam". Recording (mic live): fill `record`, glyph `pause.fill` `onRecord`, label "Jeda". Paused/interrupted (mic off): fill `accent`, glyph `play.fill` `onAccent`, label "Lanjutkan". Text label always below the circle. Stop is a separate 56pt `surface2` circle with `stop.fill` in `textPrimary`, label "Selesai / Stop", placed right of the main button; ≥24pt gap to prevent mis-tap. Disabled (no permission / session failed): fill `surface2`, glyph `mic.slash` `textTertiary`, explanation text below. Accessibility: label = action ("Mulai merekam"), value = state ("Sedang merekam, 12 menit 4 detik"), `.accessibilityAddTraits(.startsMediaSession)` on start.

### 7.8 Origin marker
Leading inline badge on AI notes / MoM items, three states (api §3 `origin` + `edited`), each glyph + text so it reads without color:
| Data | Badge | Copy (id / en) |
|---|---|---|
| `ai`, `edited=false` | `sparkles` aiOrigin on aiSoft | "AI" / "AI" |
| `ai`, `edited=true` | `sparkles` aiOrigin on aiSoft | "AI, diedit" / "AI, edited" |
| `human` | `person.fill` humanOrigin on surface2 | "Ditambahkan pengguna" / "Added by user" |
Editing an AI item sets `edited`; it never silently becomes `human`.

### 7.9 Source reference
`Button { seek } label: { Label("12:04", systemImage: "clock") }` caption, monospacedDigit, `accent`. Invalid/unverified timestamp → plain text "Rujukan tidak terverifikasi / Unverified reference", no button. From manual notes → `note.text.badge.plus` "Dari catatan pengguna / From user notes" (api SourceRef wording). Exports render refs as `[mm:ss]` server-side.

### 7.10 Text fields / editor
`TextField`/`TextEditor` on `surface2`, 1pt `borderStrong` outline, radius `md`, min height 44, visible label above (`subheadline`, `textSecondary`), helper/error below (`footnote`; error in `danger` with `exclamationmark.circle`, posted as announcement). `@FocusState` to move focus.

### 7.11 Empty / loading / error blocks
`ContentUnavailableView` (iOS 17) with SF Symbol, title, description, one action. Loading > 300 ms: skeleton rows via `.redacted(reason: .placeholder)`; inline `ProgressView` for stage processing.

### 7.12 Save indicator (editors)
Toolbar caption: `checkmark` "Tersimpan di perangkat / Saved on device" → `arrow.triangle.2.circlepath` "Menyinkronkan… / Syncing…" → `checkmark.icloud` "Tersinkron / Synced"; offline: `wifi.slash` "Tersimpan di perangkat, sinkron saat online / Saved on device, will sync when online".

## 8. Copy rules

- Indonesian default for `id` locale; English for everything else. Both in `Localizable.xcstrings`; keys are stable dotted ids (`library.empty.title`).
- Sentence case. Verbs on buttons ("Mulai merekam", not "OK").
- Errors = cause + what's safe + fix ("Transkripsi gagal. Rekaman Anda aman. Coba lagi.").
- Never claim delivery: after the share sheet closes the app shows nothing about sending (PRD SHR-01, §6 acceptance).
- Never say "end-to-end encrypted". Say "Diproses di cloud secara aman (TLS, penyimpanan terenkripsi)".
- Missing values in MoM: "Belum ditentukan" / "Not determined" (api §3.3). `deadline_phrase` only → show the phrase; date + phrase → "13 Nov 2026 (minggu depan)".
- Durations: minutes for quota ("42 menit tersisa"), `mm:ss` / `h:mm:ss` for audio, using `Duration.formatted(.time(pattern:))`.

## 9. Anti-patterns (do not)

- Emoji as icons; mixed icon families.
- Color-only status (red dot without "REC", green without "Reviewed").
- Red for anything other than live recording or failure/destructive.
- Fixed font sizes; truncated body text; icon-only nav.
- Auto-starting the mic; spinner with no text for long stages; failure that hides content ("meeting unavailable").
- Silent overwrite on conflict; auto-replacing outdated notes/MoM.
- Toasts for errors that need action (use banners).
- Custom controls where a system one exists.

## 10. Pre-delivery checklist (adapted from `references/pro-rules.md`)

### Process
- [ ] Ran `search.py --domain ux "animation accessibility z-index loading"` validation pass (done for this spec; rerun when adding screens)
- [ ] Reviewed quick-reference §1–§3
- [ ] Tested iPhone SE (375pt) and iPhone Pro Max, portrait + landscape; iPad if enabled
- [ ] Tested Reduce Motion on, and Dynamic Type at XS, Large, AX3, AX5
- [ ] Dark mode checked independently (Xcode previews in both + device)
- [ ] All touch targets ≥44pt; nothing under Dynamic Island/home indicator

### Visual
- [ ] No emoji icons; SF Symbols only, outline vs fill used per §6
- [ ] Only `Tokens` colors referenced (grep for `Color(red:`/`#` hex in Views = 0)
- [ ] Press states don't change layout bounds
- [ ] Sign in with Apple uses `SignInWithAppleButton` (official asset), style follows color scheme

### Interaction
- [ ] Press feedback ≤100 ms; motion 150–300 ms
- [ ] Disabled controls explain why when not obvious
- [ ] VoiceOver order = visual order; every icon button labeled; recording state readable as value
- [ ] Swipe actions have visible alternatives
- [ ] Haptics on record start/stop/interruption/reviewed only

### Light/Dark
- [ ] Text pairs ≥4.5 (ContrastTests green); `borderStrong` ≥3
- [ ] Separators visible in both modes
- [ ] Sheet scrim system default (≥40%)

### Layout
- [ ] `.safeAreaInset` for bottom bars; list content not hidden
- [ ] Reading views max width 680
- [ ] 4/8 spacing only

### Accessibility
- [ ] Status chips combine icon+text into one element
- [ ] Banners announced; errors announced
- [ ] Timer uses `.monospacedDigit()` and `accessibilityValue` spoken as "12 menit 4 detik", updated ≤ every 1 s without flooding (use `.updatesFrequently` trait)
- [ ] Every user-facing string exists in `id` and `en` in `Localizable.xcstrings` (no "stale"/"new" states left)
