# Phoebe iOS Architecture

Scope: P0 only (PRD §1). SwiftUI, iOS 17 minimum, Swift 6 (strict concurrency), SwiftData, XcodeGen. UI/UX: `design-system/phoebe/MASTER.md` + `design-system/phoebe/pages/*.md`.

Wire format (paths, payloads, error codes, content schemas) follows `docs/api.md`; where this file and `docs/api.md` disagree, `docs/api.md` wins. Client invariants here: persisted idempotency keys, `expected_revision` + `mutation_id` on writes (identical body on every retry of a mutation id), processing only after upload verification, no silent overwrite, no local deletion inferred from absence.

## 1. Folder layout

```
ios/
  project.yml
  Phoebe/
    App/
      PhoebeApp.swift            @main; builds ModelContainer, AppModel; .environment(appModel)
      AppDelegate.swift          UIApplicationDelegateAdaptor: background URLSession events
      AppModel.swift             root @Observable: session, connectivity, services
      RootView.swift             onboarding vs library; recovery check on launch
    DesignSystem/
      Tokens.swift               colors (dynamic light/dark), spacing, radii, motion
      Typography.swift           text style tokens
      StatusChip.swift  Banner.swift  Toast.swift  RecordButton.swift
      OriginMarker.swift  SourceReferenceButton.swift  SaveIndicator.swift
    Features/
      Onboarding/  OnboardingView.swift  SignInModel.swift
      Library/     LibraryView.swift  LibraryModel.swift  MeetingRow.swift
                   FolderSheet.swift  TagSheet.swift  ImportModel.swift  ImportConfirmSheet.swift
      Recording/   RecordingView.swift  RecordingModel.swift
      MeetingDetail/ MeetingDetailView.swift  MeetingDetailModel.swift  TranscriptView.swift
                   NotesView.swift  MoMTabView.swift  PlayerModel.swift  MetadataSheet.swift
      Editor/      DocumentEditorView.swift  DocumentEditorModel.swift  BlockView.swift
                   VersionsSheet.swift  ConflictSheet.swift
      Export/      ExportSheet.swift  ExportModel.swift  ActivityView.swift
      Settings/    SettingsView.swift  SettingsModel.swift
    Audio/
      CaptureEngine.swift        AVAudioEngine input tap → per-segment AVAudioFile writer
      RecordingEngine.swift      segment manifest, session + notifications, effects executor
      RecordingStateMachine.swift pure reducer (state, event) -> (state, effects)
      AudioSessionController.swift protocol-backed AVAudioSession wrapper
      SegmentMerger.swift        AVMutableComposition + AVAssetExportSession → tmp + atomic rename
      StorageMonitor.swift       free-space checks
      RecordingRecovery.swift    reconcile recordings on launch
      ImportValidator.swift      type sniff, duration, size
      ImportCopier.swift         streamed copy + incremental SHA-256 + progress
    Data/
      Models.swift               SwiftData @Model types
      DocumentContent.swift      Codable block model (shared with editor + export)
      ModelContainerFactory.swift
    Networking/
      APIClient.swift  Endpoints.swift  DTOs.swift  APIError.swift  Patch.swift
      AuthStore.swift            Keychain: access/refresh token pair + expiries, Apple user id, pending display name
      TokenRefresher.swift       single-flight refresh (§9)
      Connectivity.swift         NWPathMonitor -> @Observable isOnline
    Sync/
      SyncEngine.swift           pull + per-meeting outbox push, conflicts, orphans
      DocumentEditOutbox.swift   in-flight/pending split (§10)
      UploadManager.swift        background URLSession, chunks, resume, verify
      EventQueue.swift           PRD §10 events → POST /v1/events
      Poller.swift  Backoff.swift
      Checksum.swift             streaming SHA-256 (CryptoKit)
    Resources/
      Localizable.xcstrings  InfoPlist.xcstrings  Assets.xcassets
    Phoebe.entitlements
  PhoebeTests/
    (see §13)
```

No third-party dependencies. SF Pro + SF Symbols only.

## 2. XcodeGen `project.yml` (outline)

```yaml
name: Phoebe
options:
  bundleIdPrefix: com.kolarislabs
  deploymentTarget: { iOS: "17.0" }
  developmentLanguage: en
settings:
  base:
    SWIFT_VERSION: "6.0"
    SWIFT_STRICT_CONCURRENCY: complete
    ENABLE_USER_SCRIPT_SANDBOXING: YES
    LOCALIZATION_PREFERS_STRING_CATALOGS: YES
targets:
  Phoebe:
    type: application
    platform: iOS
    sources: [Phoebe]
    entitlements:
      path: Phoebe/Phoebe.entitlements
      properties:
        com.apple.developer.applesignin: [Default]
    info:
      path: Phoebe/Info.plist
      properties:
        CFBundleDisplayName: Phoebe
        CFBundleDevelopmentRegion: en
        CFBundleLocalizations: [en, id]
        NSMicrophoneUsageDescription: "Phoebe uses the microphone only while you record a meeting."
        UIBackgroundModes: [audio]
        UILaunchScreen: {}
        UISupportedInterfaceOrientations: [UIInterfaceOrientationPortrait, UIInterfaceOrientationLandscapeLeft, UIInterfaceOrientationLandscapeRight]
        NSAppTransportSecurity: { NSAllowsLocalNetworking: true }
        PhoebeAPIBaseURL: $(PHOEBE_API_BASE_URL)
        PhoebeDevLogin: $(PHOEBE_DEV_LOGIN)
    settings:
      base:
        PRODUCT_BUNDLE_IDENTIFIER: com.kolarislabs.phoebe
        TARGETED_DEVICE_FAMILY: "1"
      configs:
        Debug:   { PHOEBE_API_BASE_URL: "http://localhost:8080", PHOEBE_DEV_LOGIN: "1" }
        Release: { PHOEBE_API_BASE_URL: "", PHOEBE_DEV_LOGIN: "0" }
  PhoebeTests:
    type: bundle.unit-test
    platform: iOS
    sources: [PhoebeTests]
    dependencies: [{ target: Phoebe }]
schemes:
  Phoebe:
    build: { targets: { Phoebe: all, PhoebeTests: [test] } }
    test:  { targets: [PhoebeTests], gatherCoverageData: true }
```

Notes:
- `NSMicrophoneUsageDescription` is localized in `InfoPlist.xcstrings` (id: "Phoebe memakai mikrofon hanya saat Anda merekam meeting.").
- `UIBackgroundModes: audio` is required for lock-screen/background recording. Background URLSession needs no background mode.
- Release base URL is intentionally empty; `AppModel` fails fast at launch if unset or not `https://` (ATS; backend refuses non-TLS outside dev).
- Bundle id must equal the backend Apple audience (`APPLE_BUNDLE_ID`). Sign in with Apple requires a signing team; the simulator/test path uses dev login (`PhoebeDevLogin == "1"` and DEBUG).
- Signing: target `Phoebe` has `configFiles: Config/Signing.xcconfig` (committed; `DEVELOPMENT_TEAM` empty, `CODE_SIGN_STYLE = Automatic`, `#include? "Local.xcconfig"`). The Team ID goes in the gitignored `ios/Config/Local.xcconfig` (copy `Local.example.xcconfig`). No file → empty team → simulator builds still work.
- iPhone only for MVP (`TARGETED_DEVICE_FAMILY 1`); layouts still honor regular width in landscape.
- Build: `cd ios && xcodegen generate && xcodebuild -scheme Phoebe -destination 'platform=iOS Simulator,name=iPhone 16' test`.

## 3. App state (Observation)

- `AppModel` (`@MainActor @Observable`): `session: Session` (`.signedOut`, `.onboarding(step)`, `.signedIn(User)`), `isOnline`, `pendingRecovery: LocalRecording?`, `serverConfig: ServerConfig?`, and service references (`api`, `sync`, `uploads`, `recorder`, `poller`, `events`). Injected with `.environment(appModel)`; views read via `@Environment(AppModel.self)`. Not `ObservableObject`/`@EnvironmentObject`.
- Feature models (`LibraryModel`, `RecordingModel`, `MeetingDetailModel`, `DocumentEditorModel`, `ExportModel`, `SettingsModel`): `@MainActor @Observable final class`, owned by their view via `@State`, bindings via `@Bindable`.
- Persistent data: SwiftData `ModelContainer` per signed-in user (store at `Application Support/<userID>/Phoebe.store`), attached with `.modelContainer`. Lists use `@Query`; local search uses one `#Predicate` on `Meeting.searchText` (`localizedStandardContains`), no to-many predicate.
- All SwiftData writes happen on the main `ModelContext`. Network I/O runs off-main (`nonisolated async`), results are applied on `@MainActor`. Rationale: iOS 17 `@Query` does not reliably refresh from saves in a separate `@ModelActor` context; data volumes (≤1,000 meetings metadata, one transcript at a time) are small enough for main-context writes.
- Navigation: `NavigationStack(path:)` with `navigationDestination(for: Route.self)`; `Route` = `.meeting(UUID)`, `.editor(meetingID: UUID, kind: DocumentKind)`, `.settings`. Recording is a `fullScreenCover`, export a `sheet`. Selected detail tab in `@SceneStorage`.

## 4. SwiftData models

iOS 17 SwiftData rules: no enums with associated values in `@Model` (store raw `String` + typed computed accessors), no Codable arrays of structs as attributes for data that changes (use a child `@Model`), no predicates across to-many relationships.

Mirrors of server entities (server is source of truth; shapes per `docs/api.md` §2–§3):

| Model | Key fields |
|---|---|
| `UserProfile` | `id`, `displayName`, `language` (`id/en`, output language for notes/MoM), `timezone`; local prefs: `defaultTranscriptionLanguage` (`auto/id/en`, default for new meetings' `language`), `deleteLocalAudioAfterTranscript` (default `false`) |
| `Folder` / `Tag` | `id` (client UUID on create), `name`, `meetingCount`, `updatedAt`, `isServerCreated` |
| `Meeting` | `id` (client UUID = server id), `source` (`recording/import`, sent on create, from server after), `title`, `startedAt?`, `timezone`, `language` (transcription `auto/id/en`), `participants: [String]`, `metadataRevision`, `folder: Folder?`, `tags: [Tag]`, `searchText` (lowercased title + tag names, rewritten on every change of either), `pipelineState` (raw server value), `activeJobTypes: [String]`, `failedStage: String?`, `transcriptionErrorCategory: String?`, `localPhaseRaw: String` (`none/recording/merging/waitingStorage/waitingConnection/queued/uploading/verifying/waitingQuota/waitingEntitlement/rejected/orphaned`) + `rejectCode: String?`, `metadataConflict: Bool`, `durationMs?`, `audioAvailable`, `audioDeletedReason?` (`user/expired`), `hasTranscript`, `momReviewStatus?`, `isServerCreated`, `updatedAt` |
| `TranscriptRevision` | `id`, `meetingID`, `revisionNumber`, `kind` (`original/correction`), `language`, `segmentsJSON: Data` (immutable per revision; decoded to `[TranscriptSegment]`; each stored segment carries `uncertain`, `uncertain_reason` and the revision's `timing`, so no model change was needed; a cached segment without `timing` reads as `chunk`, i.e. approximate) |
| `Document` | `meetingID`, `kind` (`manual_notes/ai_notes/mom`), `id`, `currentRevisionNumber`, `currentRevisionID`, `reviewStatus`, `isStale`, `staleReasons: [String]` |
| `DocumentRevision` | `id`, `meetingID`, `kind`, `revisionNumber`, `origin` (`ai_generated/user_edit/restore`), `restoredFromRevisionNumber?`, `reviewedAt?`, `source` (transcript/manual notes/AI notes revision ids+numbers), `content: Data?` (JSON; nil for listing-only rows), `createdAt` |
| `ProcessingJob` | `id`, `meetingID`, `type` (`transcribe/generate_notes/generate_mom`), `trigger`, `status` (`queued/running/succeeded/failed/canceled`), `attempts`, `maxAttempts`, `errorCategory?` |

`ServerConfig` (`GET /v1/config`: max duration, max upload bytes, chunk max, content types, retention days, plus `fetchedAt`) and the last `GET /v1/usage` response are small Codable values cached in `UserDefaults`, not SwiftData.

Local-only:

| Model | Key fields |
|---|---|
| `LocalRecording` | `meetingID`, `ownerUserID`, `stateRaw` (`recording/paused/interrupted/stopped/merged/queued`), `startedAt`, `activeDurationMs`, `segments: [RecordingSegment]` (relationship, cascade), `mergedFileName?` (set only after the atomic rename) |
| `RecordingSegment` (`@Model`) | `index`, `fileName` (`seg-0001.m4a`), `durationMs`, `bytes`, `closedAt` |
| `PendingUpload` | `meetingID`, `ownerUserID`, `filePath`, `sizeBytes`, `sha256`, `contentType`, `durationMs`, `uploadID?`, `receivedBytes`, `stateRaw` (`waiting/creating/uploading/completing/verified/failed/rejected`), `rejectCode?`, `attempts`, `nextAttemptAt` |
| `PendingIntent` | job request awaiting a 2xx: `meetingID`, `type` (`transcribe/generate_notes/generate_mom`), `idempotencyKey: UUID` (generated once per user intent, reused on every retry — api §1.4), `body: Data` (exact bytes sent on every retry), `keySent: Bool`, `lastErrorCode?` (`quota_exceeded` → waitingQuota, `entitlement_inactive` → waitingEntitlement, `ai_generation_limit` → transcribe intent held for the AI slot choice), `quotaBlockedLogged: Bool`. Transcribe bodies are `{"language", "notes": "auto"|"skip"}` (bodies persisted without `notes` decode as `auto`) |
| `PendingDocumentEdit` | one row per (meeting, kind): `content: Data` (latest local draft, **wire form**: already stripped of empty items, §4 rules), `baseRevision` (revision the draft was derived from; 0 = new document), `expectedRevision` (current revision the next PUT replaces; = `baseRevision` except after "Keep mine"), `inFlightMutationID: UUID?`, `inFlightContent: Data?`, `inFlightExpected: Int?`, `inFlightBase: Int?` (frozen snapshot of the request in flight), `conflictCurrentRevision: Int?`, `failedCode: String?`, `updatedAt` |
| `PendingMutation` | outbox, `meetingID?` (nil = global: folder/tag create/rename/delete), `kind` (meeting create, meeting PATCH, meeting delete, audio delete, folder/tag ops), `payload: Data` (meeting PATCH: changed fields + `expected_metadata_revision` for metadata fields), `createdAt`, `attempts`, `failedCode?` |
| `PendingEvent` | `eventID: UUID`, `name`, `occurredAt`, `meetingID?` |

`DocumentContent.swift` mirrors api §3 exactly (`schema_version` 1). Property names use the `Id` suffix (`segmentId`, `noteId`) so `.convertFromSnakeCase` / `.convertToSnakeCase` round-trip symmetrically; `ID`-suffixed names would decode as `segmentId` and fail.
```swift
struct ManualNotesContent: Codable, Equatable { var schemaVersion = 1; var items: [NoteItem] }
struct NoteItem: Codable, Equatable, Identifiable { var id: String; var text: String; var atMs: Int? }

struct AINotesContent: Codable, Equatable { var schemaVersion = 1; var language: String; var sections: [NotesSection] }
struct NotesSection: Codable, Equatable, Identifiable { var id: String; var heading: String; var style: ListStyle; var items: [TextItem] }

struct MoMContent: Codable, Equatable { var schemaVersion = 1; var language: String; var meetingInfo: MeetingInfo; var sections: [MoMSection] }
struct MeetingInfo: Codable, Equatable { var title: String; var startedAt: Date?; var timezone: String; var participants: [String]; var agenda: [TextItem] }
struct MoMSection: Codable, Equatable, Identifiable { var id: String; var type: SectionType; var heading: String; var style: ListStyle?; var items: [MoMItem] }
enum SectionType: String, Codable { case summary, discussion, decisions, actionItems = "action_items", openIssues = "open_issues", followUps = "follow_ups", custom }
enum ListStyle: String, Codable { case bullet, numbered, paragraph }
enum MoMItem: Equatable { case text(TextItem), topic(TopicItem), action(ActionItem) }   // custom Codable in MoMSection, chosen by section type
struct TextItem: Codable, Equatable { var id: String; var text: String; var origin: Origin; var edited: Bool; var sources: [SourceRef] }
struct TopicItem: Codable, Equatable { var id: String; var title: String; var origin: Origin; var edited: Bool; var sources: [SourceRef]; var points: [TextItem] }
struct ActionItem: Codable, Equatable { var id: String; var description: String; var pic: String?; var deadline: String?; var deadlinePhrase: String?; var origin: Origin; var edited: Bool; var sources: [SourceRef] }
enum Origin: String, Codable { case ai, human }
enum SourceRef: Equatable { case transcript(segmentId: String, startMs: Int, endMs: Int), userNotes(noteId: String) }  // custom Codable on "type"
```
- Every type with optionals implements `encode(to:)` with `encodeNil` for `nil` (synthesized `Encodable` uses `encodeIfPresent` and would omit `pic`, `deadline`, `deadline_phrase`, `at_ms`, `meeting_info.started_at`, `style`; api §1/§3 require them present).
- Serialization for `PUT` (`wireContent()`, pure, unit-tested) strips items whose `text`/`description` is empty after trim, topics with empty `title` and no points, and empty manual-notes items; the editor keeps them in its in-memory draft only. Participants are trimmed and empty ones dropped.
- Editing rules (pure functions, unit-tested): modifying an `ai` item sets `edited = true` (origin stays `ai`; label "AI, diedit"); new items are `human` with id `h_<uuid>`; nullable fields use `nil`, never `""`; manual-note ids stay stable across edits. Any content save returns the document to Draft (server clears `reviewed_at`); the UI flips to Draft immediately.

## 5. Files on disk

- Recordings: `Application Support/recordings/<meetingID>/seg-XXXX.m4a`, merged `audio.m4a` (written as `audio.m4a.tmp`, then `FileHandle.synchronize()` and atomic `replaceItemAt`).
- Imports: `Application Support/recordings/<meetingID>/import.<ext>` via `ImportCopier` (§7).
- File protection `.completeUntilFirstUserAuthentication` on the recordings directory (writing must work while locked; `.complete` would break lock-screen recording). `isExcludedFromBackup = true`.
- Upload chunk staging: `Caches/uploads/<uploadID>/chunk` (written via `FileHandle` ranges, replaced per chunk); skipped when the whole file is one chunk (upload task reads the audio file directly).
- Export cache: `Caches/exports/<document_revision_id>-<draft|reviewed>.<format>` (+ server `filename` in a sidecar). Key suffix comes from the response's `is_draft`. Used only offline, and only when the suffix matches the document's current `review_status`; online the app always calls `POST /v1/exports` (cheap, deterministic) so title or review changes are never served stale.
- Keychain (`kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly`, needed for background upload relaunch while locked): access/refresh token pair and their expiries (one item, §9); Apple user identifier (`ASAuthorizationAppleIDCredential.user`) for the credential-state check; pending `display_name` until `POST /v1/auth/apple` returns 2xx (Apple sends the name only once).
- **Delete meeting** (local effects, after the user confirms; server call via outbox, skipped for a never-created meeting): remove `recordings/<id>/`, `Caches/uploads/<uploadID>/`, `Caches/exports/*` for that meeting's revisions, cancel its background upload tasks, delete `LocalRecording`, all `Pending*` rows and mirrors for the meeting.
- **Delete audio**: remove the local merged/imported file and segments, then queue `DELETE /v1/meetings/{id}/audio` (outbox). Blocked with `audio.delete.blocked` while the upload is not verified.

## 6. Recording engine

### Session
- Category `.record`, mode `.default`, no options. `setPrefersNoInterruptionsFromSystemAlerts(true)`. Activate with `setActive(true)`; Start is enabled in UI only after activation + engine start + first buffer written succeed (REC-01).
- Why `.record` not `.playAndRecord`: capture is the iPhone mic in a room; `.record` guarantees nothing the app plays is mixed into the capture, and playback is disabled during recording anyway. Bluetooth HFP input (8–16 kHz) is worse than the built-in mic for room meetings, so it isn't requested.
- Playback screens switch to `.playback` only when no recording is active.

### Capture (AVAudioEngine, one continuous tap)
- `CaptureEngine` installs one tap on `inputNode` (hardware format) for the whole session and never restarts capture to rotate files. The tap block (audio thread) converts buffers with an `AVAudioConverter` to the file's processing format and writes them to the current segment's `AVAudioFile` on a private serial queue (`@unchecked Sendable` writer box; no actor hop on the audio thread).
- Rotation = on the writer queue, release the current `AVAudioFile` (finalizes `moov`; `close()` on iOS 18+, deinit on 17) and open the next one; the next buffer goes to the new file. No `record()` call from the background, so locked-screen rotation cannot hit `cannotStartRecording`.
- Format (`RecordingConfig`): AAC-LC (`kAudioFormatMPEG4AAC`), 1 channel, 16 kHz, 24 kbps (`AVEncoderBitRateKey`) → ≈10.8 MB/h, ≈21.6 MB for 120 min, under the backend `STT_MAX_FILE_BYTES` (24 MB) so recordings need no server chunking; imports may (backend chunks with ffmpeg).
- Active duration = frames written / 16 000. Pauses and interruptions are not counted because nothing is written. UI timer ticks at 2 Hz from this value.

### Segments
- Segment length N = **2 minutes** (`RecordingConfig.segmentSeconds`). An unfinalized M4A (no `moov`) is unreadable, so N bounds worst-case loss on crash/force quit to the in-progress segment. ≤60 segments for 120 min.
- After each close (rotation, pause, interruption, stop): read actual duration (`AVURLAsset.load(.duration)`), insert `RecordingSegment`, `modelContext.save()`. This is the segment manifest; it is persisted before anything else happens.
- A segment file that fails to open or write → event `segmentWriteFailed`: close what is valid, state `interrupted`, banner `rec.error.write`.

### State machine
`RecordingStateMachine` is a pure reducer: `idle → ready → starting → recording ⇄ paused | interrupted → stopping → stopped`. Events: `start`, `startSucceeded`, `startFailed`, `pause`, `resume`, `stop`, `segmentTimerFired`, `segmentWriteFailed`, `interruptionBegan`, `interruptionEnded(canResume)`, `routeLostInput`, `routeChanged(newInput)`, `engineConfigurationChanged`, `mediaServicesReset`, `storageLow`, `storageCritical`, `durationLimitNear`, `durationLimitReached`. Effects: `openSegment`, `closeSegment`, `startEngine`, `stopEngine`, `activateSession`, `showBanner(kind)`, `haptic(kind)`, `logEvent(name)`. `RecordingEngine` (`@MainActor`) executes effects through protocols (`AudioCapturing`, `AudioSessioning`, `StorageProviding`) so the reducer and engine are testable without a microphone.

### Notifications
Observed with `NotificationCenter.default.addObserver(forName:object:queue: .main)`; the closure extracts Sendable values (interruption type/options, route-change reason as raw `UInt`) from `userInfo` and then calls `MainActor.assumeIsolated { engine.handle(event) }`. (`notifications(named:)` async sequences yield non-Sendable `Notification` and fail Swift 6 checking.)
- `AVAudioSession.interruptionNotification` `.began` → `closeSegment`, `stopEngine`, state `interrupted`, banner "Paused by interruption", event `recording_interrupted`. `.ended` → enable Resume; **never auto-resume**, even with `.shouldResume`.
- `routeChangeNotification` `.oldDeviceUnavailable` and `AVAudioEngineConfigurationChange` → close segment; reinstall the tap and restart the engine on the new input if one is available (info banner); if restart fails or no input → `interrupted`.
- `mediaServicesWereResetNotification` → finalize the current file, rebuild engine and session objects, state `interrupted` with "System audio restarted".
- Mic permission revoked while running (app is terminated by iOS) → handled by recovery on next launch.

### Storage
`StorageMonitor` reads `volumeAvailableCapacityForImportantUsageKey`. Preflight: require ≥ 300 MB to start. During recording: check every 10 s; < 300 MB → warning banner; < 100 MB → `closeSegment` + stop safely (state `stopped`, banner states what's saved). The 100 MB floor covers the merge (≤ ~22 MB output). Thresholds in `RecordingConfig`.

### Duration limit
`limit = ServerConfig.max_audio_duration_ms` (default 7,200,000 before the first fetch). Warn at `limit − 10 min`; auto-stop at `limit − 5 s` (`RecordingConfig.limitMarginMs = 5000`) so AAC frame/priming rounding in the merged file can never exceed the server's strict limit. Meeting saved, not rejected.

### Relaunch & recovery
`RecordingRecovery` runs on launch for every `LocalRecording` not `queued`:
- `recording/paused/interrupted` (unclean end): scan `recordings/<meetingID>/`, validate each `seg-*.m4a` with `AVURLAsset` (playable, duration > 0), include valid files missing from the manifest, drop the unreadable in-progress file. Result → `AppModel.pendingRecovery` → Library banner "Recover". The mic is never started. Recover → `stopped` → merge pipeline. Discard (confirmed) → delete directory and rows.
- `stopped` (user already pressed Stop; killed during merge): delete any `audio.m4a.tmp`, rerun `SegmentMerger` automatically (no prompt).
- `merged` without `PendingUpload`: create `PendingUpload` + `PendingIntent(.transcribe)` and set `queued` (automatic).
- No promise of continuing after force quit (PRD REC-01).

### Merge vs segment upload — decision: **merge on device, upload one file**
After stop (or recovery), inside `UIApplication.beginBackgroundTask` (so locking/backgrounding right after Stop does not cut the export): `AVMutableComposition` inserts each valid segment's audio track sequentially; `AVAssetExportSession` with `AVAssetExportPresetPassthrough` → `audio.m4a.tmp` (no re-encode; seconds for 2 h); fallback `AVAssetExportPresetAppleM4A` if passthrough lacks `.m4a`. Then fsync + atomic rename to `audio.m4a`, compute streaming SHA-256, and in **one** `modelContext.save()`: `mergedFileName`, `PendingUpload`, `PendingIntent(.transcribe)`, state `queued`.
Merge fails for lack of space → keep segments, `localPhase = waitingStorage`, banner `library.storage.merge`; retried on foreground and when free space ≥ segments size + 50 MB.
Why one file:
1. One upload session, one SHA-256, one server-side verification, one idempotent "start transcription" — matches PRD §7 upload contract directly.
2. Server-computed duration and quota reservation operate on a single validated asset; no partial-meeting states.
3. Backend needs no audio concatenation; transcript timestamps map 1:1 to the merged file's timeline = active time. (Backend ffmpeg is used only to split large files for STT.)
4. Imports already arrive as one file, so recording and import share one pipeline (PRD §2 alur impor).
Costs accepted: upload starts after Stop (no progressive upload; not required by PRD) and temporary 2× storage (≤ ~22 MB for 120 min).

## 7. Import

`.fileImporter([.mpeg4Audio, .mp3, .wav])` → security-scoped access → `ImportValidator`:
1. Size from `fileSizeKey` ≤ `ServerConfig.max_upload_bytes` (250 MB default) else `import.error.tooBig`.
2. Magic bytes (first 12 bytes via `FileHandle`): `ftyp` at offset 4 (M4A), `ID3` or MPEG frame sync `0xFFE` (MP3), `RIFF....WAVE` (WAV); mismatch → `import.error.type` (content, not extension — IMP-01).
3. `AVURLAsset.load(.tracks, .duration)`: no audio track / load error → `corrupt`; duration < 1 s → `empty`; > limit → `tooLong`.
4. Free space ≥ file size + 50 MB else `storage`.
Then the confirmation sheet (library.md "Import confirm": cloud processing, retention days, transcription language, "Unggah"). On confirm, `ImportCopier` streams 1 MB `FileHandle` reads into `recordings/<id>/import.<ext>`, updating CryptoKit `SHA256` incrementally and publishing determinate progress (`import.copying`); the digest goes straight into `PendingUpload.sha256` (no second pass). Then create `Meeting(source: .import, language: picked)` + `PendingUpload` + `PendingIntent(.transcribe)` (key generated now) in one save. No mic permission requested. Server re-validates (silence/no speech is detected server-side → `no_speech`).

## 8. Upload (background URLSession, resumable, checksummed)

Protocol per api §4.4 (offset-based, tus-like).
- Session: `URLSessionConfiguration.background(withIdentifier: "com.kolarislabs.phoebe.upload")`, `sessionSendsLaunchEvents = true`, `isDiscretionary = false`. `AppDelegate.application(_:handleEventsForBackgroundURLSession:completionHandler:)` stores the handler; `UploadManager` creates the next chunk task inside `urlSession(_:task:didCompleteWithError:)` and calls the stored handler only in `urlSessionDidFinishEvents`.
- Gate: no `PendingUpload` starts until `ServerConfig` has been fetched at least once (retention policy shown before upload, PRV-01).
- Flow per `PendingUpload`:
  1. Meeting exists on server: `POST /v1/meetings` with client `id` + `source` (`200` if it already exists → no duplicate meeting). `409 meeting_id_conflict` → orphan (§10), stop.
  2. `sha256` already computed (merge/import). Client-measured `duration_ms` from `AVURLAsset`.
  3. `POST /v1/meetings/{id}/uploads {size_bytes, sha256, content_type, duration_ms, transcription: {idempotency_key: PendingIntent(.transcribe).idempotencyKey, language}}`, with `language` taken from the intent's stored `body` (so the server auto-start and any explicit call are byte-identical requests); set `keySent = true`. → `UploadSession` (a live session with same sha256+size is returned for resume). `409 audio_exists` → `GET /v1/meetings/{id}`: `audio != null` → mark `verified` (sha mismatch only logged: the server already holds verified audio for this meeting) and go to 7. `409 upload_busy` → backoff.
  4. Chunks: `chunk = min(upload_chunk_max_bytes, size − received_bytes)` (32 MiB → most recordings are one task), sequential. For each: stage `[received_bytes, +chunk)` (or use the file itself when it is the whole file), `uploadTask(with: PUT /v1/uploads/{upload_id}` + `Upload-Offset`, `fromFile:)`. `taskDescription = upload_id`; the response's `received_bytes` drives the next chunk.
  5. Resume (launch, foreground, connectivity regained, task error): `GET /v1/uploads/{upload_id}` → `open`: continue from `received_bytes`; `verifying`: poll GET with backoff; `completed`: step 6; `failed`: `checksum_mismatch` → recompute checksum and restart from step 3 once, then `failed`; other reasons → `rejected(code)`; `expired|canceled`: step 3. Error mapping: `409 upload_offset_mismatch` → `details.received_bytes`; `409 upload_busy` → retry after 1–5 s; `410 upload_expired` → step 3; `409 upload_not_open` with `details.status == completed` → step 6, otherwise GET and branch as above.
  6. Verified when the last chunk's response (or `POST /v1/uploads/{upload_id}/complete`, idempotent, called whenever that response was not seen) has `audio != null` → `PendingUpload = verified`, segments deleted. `job != null` → the transcribe intent is satisfied → delete `PendingIntent`. `422 checksum_mismatch|audio_*` → as in step 5.
  7. If the transcribe `PendingIntent` remains: `POST /v1/meetings/{id}/transcription` with its key and exact `body`. `200/202`, `409 already_transcribed`, `409 job_in_progress` → delete intent. `409 audio_not_ready` → not yet (retry next cycle). `402 quota_exceeded` → `localPhase = waitingQuota`; `402 entitlement_inactive` → `waitingEntitlement` (log `quota_blocked` once per intent). `410 audio_unavailable` → `rejected(audio_unavailable)`. `429 ai_generation_limit` (automatic notes, no AI slot; nothing reserved) → keep the intent with `lastErrorCode = ai_generation_limit` and stop sending it; Meeting Detail shows the AI slot banner and choice sheet (meeting-detail.md). Retrying with notes (same body) rotates the key; "Transkrip saja" replaces the intent with a `notes: "skip"` body and a new key. An upload completion with `job: null` + `transcription_blocked` parks the intent the same way (`lastErrorCode = ai_generation_limit`) without sending this step. The upload create's `transcription` block carries the intent's `notes`; a language change rewrites the body keeping `notes`.
- Waiting intents are retried when `GET /v1/usage` (fetched on launch, foreground, Library pull-to-refresh and Settings appear whenever any meeting is `waitingQuota|waitingEntitlement`) shows `entitlement.status == active && remaining_seconds ≥ ceil(server audio.duration_ms / 1000)`, or when the user taps Retry.
- Transport retries: `Backoff` 2, 4, 8, 16, 30, 60 s cap, ±20% jitter, unlimited while the file exists.
- Local audio: segments deleted at `verified`; merged/imported file deleted only when `meeting.hasTranscript` and `deleteLocalAudioAfterTranscript` (default off). Never right after complete (cloud audio of an untranscribed meeting is the only other copy).
- Auth: each chunk task is created with `APIClient.authorizedRequest`, which refreshes first unless the access token stays valid ≥ 5 min (a background task can wait in the system queue). A chunk that still completes with `401 access_token_expired` (also after a background relaunch) → `UploadPlanner` → `GET /v1/uploads/{id}` (which refreshes and retries once, §9) → the chunk is re-sent from the server's `received_bytes`: same meeting, same session, no data loss. Terminal 401 (`unauthenticated`, `session_revoked`, `session_expired`) → stop queue, `AppModel` → sign-in (recordings stay local, keyed by `ownerUserID`; never uploaded under a different account). A refresh that fails on the network → normal transport backoff, never a sign-out.
- `Connectivity` (`NWPathMonitor`) offline → "Waiting for connection"; no new tasks until online (in-flight background tasks are left to the system). Expected iOS deferral of background relaunches is noted in the device test plan (§13).

## 9. API client

- `APIClient` (`Sendable` struct): base URL from Info.plist `PhoebeAPIBaseURL`, `URLSession.shared` for JSON, async/await. `JSONDecoder.keyDecodingStrategy = .convertFromSnakeCase`, `JSONEncoder.keyEncodingStrategy = .convertToSnakeCase`; DTO and content properties end in `Id`, never `ID` (symmetry, §4). Dates: `.custom` strategies using `Date.ISO8601FormatStyle(includingFractionalSeconds: true)` (a Sendable value; a shared `ISO8601DateFormatter` is not Sendable). Deadlines are `YYYY-MM-DD` strings. Headers: `Authorization: Bearer`, `X-Request-ID` (UUID), `Idempotency-Key` on job endpoints.
- Explicit nulls: request DTOs with nullable fields implement `encode(to:)` with `encodeNil`. Meeting PATCH uses `enum Patch<T: Encodable & Sendable> { case unchanged, set(T), clear }` per field (`unchanged` → key omitted, `clear` → `null`), so "Tanpa folder" sends `"folder_id": null`.
- Sessions (api §4.1, contract C3): Keychain item `tokens` (`AfterFirstUnlockThisDeviceOnly`, one JSON value so the pair and expiries are written together) holds `access_token`, `access_token_expires_at`, `refresh_token`, `refresh_token_expires_at`, `session_expires_at`. `TokenRefresher` (actor) is single-flight: concurrent callers share one `POST /v1/auth/refresh {refresh_token}` (refresh tokens are single-use; a second exchange with the rotated token would revoke the family). `send` refreshes first when the access token expires within 60 s, and on `401 access_token_expired` refreshes and retries the request once (rejected again → `.unauthenticated` for the caller, no sign-out). Refresh `401` (any code) → `onUnauthenticated` → `AppModel.handleUnauthenticated` → sign out to onboarding (`SignOutReason.unauthenticated`; message `session.ended` in `Common.xcstrings`: id "Sesi berakhir. Silakan masuk lagi.", en "Your session ended. Please sign in again."). Refresh transport error / timeout / offline / 5xx → the call throws that error and the session and tokens stay. Lost-reply tolerance: the server rotates before it replies, and re-accepts the old refresh token only within `REFRESH_REUSE_GRACE` (30 s) while the successor is unused, so the refresh request times out after 8 s and an ambiguous failure (transport, timeout, 5xx) is retried at once with the same refresh token, up to 3 attempts within 20 s; the exchange and Keychain save run inside a `UIApplication` background task so suspension cannot strand a committed rotation. A refresh result (or refresh `401`) is applied only while the Keychain still holds the refresh token it exchanged: after a sign-out or a new sign-in it is discarded and never signs the new session out, and a new session's refresh never joins an exchange started for the old one. Clock skew: the access expiry is re-anchored to the device clock on receipt (`receivedAt + (access_token_expires_at − response Date header)`), so a wrong device clock neither refreshes before every request nor sends tokens the server considers expired; a sign-in pair is stored as received, so a skewed device refreshes once after sign-in. `session_revoked` and `session_expired` decode to `.unauthenticated` (every caller only needs "stop, signed out"). The session's 180-day end is enforced by the server (`session_expired`), never extended by the client. Sign out calls `POST /v1/auth/logout` (refreshing first if needed) before clearing the Keychain.
- `APIError` decodes the envelope `{error: {code, message, details, request_id}}` and switches on `code`: `unauthenticated` (also `session_revoked`, `session_expired`), `access_token_expired` (handled inside `APIClient`), `quota_exceeded`, `entitlement_inactive`, `revision_conflict(current_revision_number)`, `metadata_conflict(current_metadata_revision)`, `job_in_progress(job_id)`, `already_transcribed`, `audio_exists`, `audio_not_ready`, `meeting_id_conflict`, `id_conflict`, `name_taken`, `idempotency_key_reused`, `ai_generation_limit(next_slot_at)`, `upload_*`, `audio_*`, `checksum_mismatch`, `validation_failed(fields)`, `invalid_content(path, reason)`, `not_found`, `transcript_not_ready`, `ai_notes_not_ready`, `internal`, `unavailable`, plus local `.offline`/`.transport`/`.timeout`/`.decoding`. `.transport`, `.timeout` and 5xx are *ambiguous* (the server may have committed). UI copy maps from `code`; server `message` is never shown. `404` on delete counts as success.
- No meeting content in logs: `os.Logger` with `privacy: .private` for identifiers; never log bodies.

Endpoints used (api §4):

| Purpose | Method & path |
|---|---|
| Limits, retention (no auth) | `GET /v1/config` |
| Sign in with Apple | `POST /v1/auth/apple {identity_token, nonce, display_name, authorization_code}` → `{user, access_token, access_token_expires_at, refresh_token, refresh_token_expires_at, session_expires_at}` |
| Dev login (DEBUG + flag) | `POST /v1/auth/dev {subject}` (same response) |
| Refresh (no bearer) | `POST /v1/auth/refresh {refresh_token}` → new pair, same `session_expires_at` |
| Sign out | `POST /v1/auth/logout` (revokes the session family) |
| Profile | `GET /v1/me`, `PATCH /v1/me {language, timezone, display_name}` |
| Delete account | `DELETE /v1/me` → `202` |
| Usage & entitlement | `GET /v1/usage` |
| Folders / tags | `GET /v1/folders`, `POST /v1/folders {id, name}`, `PATCH/DELETE /v1/folders/{id}`; same for `/v1/tags` |
| Meetings | `GET /v1/meetings?q=&folder_id=&tag_id=&limit=&cursor=`, `POST /v1/meetings`, `GET/PATCH/DELETE /v1/meetings/{id}` |
| Upload | `POST /v1/meetings/{id}/uploads`, `GET /v1/uploads/{id}`, `PUT /v1/uploads/{id}` (`Upload-Offset`), `POST /v1/uploads/{id}/complete` |
| Start transcription | `POST /v1/meetings/{id}/transcription` (`Idempotency-Key`) |
| Jobs | embedded in `GET /v1/meetings/{id}`; `GET /v1/jobs/{id}` |
| Transcript | `GET /v1/meetings/{id}/transcript`, `GET …/transcript/revisions[/{n}]`, `POST …/transcript/revisions {expected_revision, mutation_id, edits}` (online only) |
| Documents | `GET/PUT /v1/meetings/{id}/documents/{kind}` (`expected_revision`, `base_revision`), `GET …/documents/{kind}/revisions[/{n}]`, `POST …/documents/{kind}/restore`, `PUT …/documents/{kind}/review {status, expected_revision}` |
| Generate | `POST …/documents/ai_notes/generate {language}`, `POST …/documents/mom/generate {ai_notes_revision_id, language}` (`Idempotency-Key`) |
| Export | `POST /v1/exports {document_revision_id, format}` (sync, returns `download_url` + `text` for md/whatsapp), `GET /v1/exports/{id}` (fresh URL) |
| Playback | `GET /v1/meetings/{id}/audio/url` → signed URL; download whole file to cache, play locally |
| Delete audio | `DELETE /v1/meetings/{id}/audio` |
| Events | `POST /v1/events {events}` |

Sign in with Apple: random raw nonce; `request.nonce = sha256hex(raw)`; send raw nonce + identity token + `authorizationCode` (UTF-8) + name. Store `credential.user` in Keychain; keep the name as pending `display_name` in Keychain until a 2xx (retried on the next sign-in attempt if the first fails). On launch, only for Apple sessions (skip dev login): `ASAuthorizationAppleIDProvider().getCredentialState(forUserID:)`; `.revoked/.notFound` → sign out; `.authorized`/`.transferred` or any error (e.g. offline) → no action. Observe `credentialRevokedNotification`.

## 10. Sync engine

`SyncEngine` (`@MainActor` coordinator; network calls `nonisolated`), following api §6.

### Pull
- **Full pull** (launch, pull-to-refresh): page `GET /v1/meetings?limit=200` to the end → upsert list fields (`source`, `active_job_types`, `failed_stage`, `transcription_error_category`, …). Folders and tags: full `GET` each.
- **Poll tick**: first page only (library.md polling).
- For meetings whose `updated_at` changed and that are open or have active jobs: `GET /v1/meetings/{id}` → documents, jobs, audio; fetch transcript/doc revisions only when `current_revision_number` changed.
- **Deletion is never inferred from absence** (keyset order uses `coalesce(started_at, created_at)`, which another device can change mid-pagination; a page error is not an end). A server-created meeting missing from a completed full pull is confirmed with `GET /v1/meetings/{id}`: `200` → keep (it moved); `404` → if it has no `Pending*` rows, no `LocalRecording`, and no local audio file → delete locally (incl. §5 file cleanup); otherwise → `orphaned`. Any `404` on a meeting-scoped write is confirmed the same way.
- **Orphaned** (`404` confirmed, or `409 meeting_id_conflict` on create): stop all outbox items and uploads for it (cancel tasks), keep local audio and manual notes, show Library banner `library.orphan.*`: "Simpan sebagai meeting baru" → new UUID, move `recordings/<old>/` → `recordings/<new>/`, re-key `LocalRecording`/`PendingUpload`/`PendingDocumentEdit`(manual notes only, `baseRevision = expectedRevision = 0`, in-flight cleared)/`PendingIntent` (new keys), `isServerCreated = false`, then it syncs as a new meeting; "Hapus" (confirm) → local delete.

### Push (outbox)
Each cycle: global queue first (folder/tag creates/renames/deletes, FIFO), then one independent queue per meeting, so a failing item never blocks other meetings. Within a meeting: meeting create → `PendingMutation`s FIFO → `PendingDocumentEdit`s → `PendingIntent`s → upload (UploadManager waits for `isServerCreated`). Gates: a transcribe intent waits for `PendingUpload.state == verified` (or server `audio != null` when this device did not upload); a generate intent waits until the `manual_notes` edit (and, for MoM, the `ai_notes` edit) has no pending or in-flight content.

Terminal handling (per item; ambiguous errors — transport, timeout, 5xx — always retry with backoff, unchanged):
- `404` → confirm (Pull); orphan or local delete; drop the item.
- `422 validation_failed` / `invalid_content` → drop the item, keep local data, mark "Not synced" (`failedCode`, field). If the field is `folder_id`/`tag_ids` with reason `not_found`: refresh folders/tags, retry once without those fields.
- `409 name_taken` (folder/tag) → refetch; if a server folder/tag with that name exists, remap local references to its id and drop the create.
- `409 id_conflict` → new local id, retry once.
- `409 metadata_conflict` → `meeting.metadataConflict = true`, fetch the meeting; detail shows the metadata conflict sheet (keep mine → re-PATCH with `expected_metadata_revision = current`; use latest → drop the PATCH).
- `401` → stop everything, sign-in.

### Document edits (`DocumentEditOutbox`, never loses or misattributes an edit)
- Editor debounce 1 s → write `content = wireContent(draft)` on the (meeting, kind) row; a new row gets `baseRevision = expectedRevision` = the current server revision the editor loaded (0 if none). The in-flight snapshot is never mutated.
- Push: if `inFlightMutationID == nil` and `content` exists → freeze `inFlight = (new UUID, content, expectedRevision, baseRevision)`. Send `PUT {expected_revision: inFlightExpected, base_revision: inFlightBase, mutation_id: inFlightMutationID, content: inFlightContent}`.
- `201/200` (returned `revision_number R`) → store the revision, update `Document`, emit `document_saved`, clear in-flight. If `content == inFlightContent` → delete the row; else (user kept typing) `baseRevision = expectedRevision = R` and keep the row → next push sends the newer content with a fresh mutation id.
- Ambiguous failure → keep in-flight; retry the **same** mutation id with the **same** snapshot until a definitive answer. A lost response therefore replays to `200` (never a false `409` against the user's own write, never success for content the server did not store).
- `409 revision_conflict` → clear in-flight, set `conflictCurrentRevision`; editor shows the conflict sheet. "Keep mine" → `expectedRevision = conflictCurrentRevision` (keep `baseRevision`), new push. "Use latest" (confirmed) → delete the row, load current. Never auto-merge. Applies to all three kinds (manual notes included).
- `422 idempotency_key_reused` (client bug: snapshot drift) → clear in-flight and retry once with a new mutation id; `422 invalid_content` → clear in-flight, `failedCode`, "Not synced".
- **Review**: `PUT …/documents/{kind}/review {status, expected_revision}` only when the row for that document has no content and no in-flight request (review what you saw). 409 → refetch, show conflict sheet.

### Other
- **Metadata**: `PATCH /v1/meetings/{id}` with changed fields only (`Patch<T>`), plus `expected_metadata_revision` = the `metadataRevision` the edit was based on when any metadata field changed. Folder/tag changes are sent without it (LWW).
- **AI generation slots** (C2): generate notes / MoM `429 ai_generation_limit` is a definitive rejection: the intent is deleted, usage is refreshed, and the UI shows `next_slot_at` (Meeting Detail footnotes). The app never counts slots itself; it reads `/v1/usage.ai_generations`.
- **Transcription language** (`meeting.language`) is editable while no transcribe job is active or succeeded and no upload is in progress; changing it rewrites the transcribe `PendingIntent.body` and, if `keySent`, rotates its key.
- **Events**: `EventQueue` inserts `PendingEvent` rows; flushed with the outbox in batches ≤ 100 via `POST /v1/events`; rows deleted on `202`; `422` drops the batch's invalid names (bug path).
- **Search**: offline → local `#Predicate` on `searchText`; online → additionally `GET /v1/meetings?q=` (transcript/notes/MoM text) and merge results.
- **Stale** ("Based on earlier version") comes from `is_stale`/`stale_reasons`; the client never auto-regenerates.

## 11. Polling with backoff

Per api §4.5: while a visible meeting has a job `queued|running` or an upload in progress, poll `GET /v1/meetings/{id}` (Library: first page of `GET /v1/meetings` while any row has non-empty `active_job_types` or a local upload phase) with `Backoff(initial: 2 s, factor: 1.5, cap: 30 s)`, foreground only. Reset to 2 s on scene `active`, after a user-initiated retry/generate, and when a state changes. Stops when nothing is active or the app backgrounds; the server keeps processing (auto-start after the last chunk) and the next foreground fetches the latest (PRD §7). Push is P1.

## 12. Localization

- `Resources/Localizable.xcstrings` with languages `en` (development) and `id`. Keys are stable dotted ids from the page specs (`library.empty.title`), each with both translations; plurals/format args via catalog variations (`%lld`, `%@`).
- `InfoPlist.xcstrings` for `NSMicrophoneUsageDescription`.
- Views use `Text("key")` / `String(localized: "key")`; models expose `LocalizedStringResource`. App language is chosen via iOS per-app language (Settings link); transcript/output language are server preferences, independent of UI language.
- Dates/durations: `Date.FormatStyle` and `Duration.formatted` with current locale (Indonesian uses `09.30` time separator automatically).

## 13. PhoebeTests (Swift Testing)

| Suite | Covers |
|---|---|
| `RecordingStateMachineTests` | every transition in §6; interruption never auto-resumes; storage critical stops; limit reached at `max − 5 s` stops; warn at `max − 10 min`; pause excluded from active duration; `segmentWriteFailed` → interrupted |
| `RecordingEngineTests` | fake `AudioCapturing`/`AudioSessioning`: rotation at N without engine restart, manifest saved after each close, start failure keeps Start enabled, route change reinstalls tap on new input |
| `RecordingRecoveryTests` | fixtures written with `AVAudioFile` (AAC) + one truncated file: valid segments recovered, unreadable dropped, unlisted valid file included; `stopped` with partial `audio.m4a.tmp` → tmp deleted, merge rerun; `merged` without `PendingUpload` → upload + intent created, state `queued` |
| `SegmentMergerTests` | 3 segments → merged duration ≈ sum (±50 ms), playable; 60 × 2-min segments → merged duration ≤ sum + 1 s; output appears only after rename |
| `ImportValidatorTests` | valid m4a/wav; wrong magic bytes named `.m4a`; zero-length; too large; too long (limits from a `ServerConfig` fixture) |
| `ImportCopierTests` | streamed copy byte-equal; incremental digest equals one-shot digest; progress monotonic to 1.0 |
| `ChecksumTests` | streaming SHA-256 equals one-shot CryptoKit digest on a 20 MB file |
| `AuthRefreshTests`, `AppSessionTests` | `URLProtocol` auth server: token expiring within 60 s → refresh before the request; `401 access_token_expired` → refresh → retry once succeeds; 8 concurrent requests (expired or expiring) → exactly one refresh; refresh `401 session_revoked`/`session_expired`/`unauthenticated` → signed out (AppModel: `.signedOut`, reason `.unauthenticated`, Keychain cleared); refresh transport error/offline/timeout/503 → not signed out, old refresh token kept and reused (ambiguous ones retried at once, 3 attempts); lost refresh reply → retried with the same token; rotation wrapped in a background task; refresh landing after sign-out or a new sign-in → discarded, no sign-out, new session refreshes on its own; device clock 20 min fast → one refresh for three requests, expiry anchored via the Date header; rejected again after refresh → no second refresh, no sign-out; upload requests get a token valid ≥ 5 min; refreshed pair persisted in the Keychain |
| `UploadPlannerTests` | chunk = min(max, remaining); chunk `401 access_token_expired` → refresh → chunk re-sent with the new bearer (one meeting, one session); chunk task created with a token valid ≥ 5 min; single-chunk file uploads from source; resume after `upload_offset_mismatch` uses `details.received_bytes`; `checksum_mismatch` restarts once then fails; `audio_*` → rejected; `upload_not_open` completed → complete; `audio_exists` with server audio → verified, continue to intent; `meeting_id_conflict` → orphaned, no loop; last-chunk response with `job` deletes the transcribe intent; transcribe intent not sent before verified; `already_transcribed` → success; upload create carries the intent's `notes`; completion with `transcription_blocked` parks the intent, no `POST …/transcription` |
| `BackoffTests` / `PollerTests` | 2 s ×1.5 cap 30 s; upload backoff cap 60 s with jitter bounds; reset; stops with no active jobs |
| `APIClientTests` | `URLProtocol` stub: snake_case round-trip of `segmentId`/`noteId`, fractional-second dates, envelope → `APIError` for 401/402/409/410/422/429, `Idempotency-Key` persisted and identical across retries, `"pic":null` and `"folder_id":null` on the wire, `Patch.unchanged` omits the key |
| `SyncEngineTests` | edit 201 → revision stored & row removed; typing during in-flight PUT → newer content survives and is sent with base = returned revision; response lost then more edits then replay → same mutation id, `200`, no 409, no loss; 409 → conflict kept; keep-mine sends expected = current, base = original base, new mutation id; review waits for pending edits; meeting missing from listing but `GET` 200 → kept; `GET` 404 with pending edit → orphaned, not deleted; `started_at` changed mid-pagination → nothing deleted; failing item in meeting A does not block meeting B; 422 `folder_id not_found` → retried once without folder; usage refresh unblocks waitingQuota intents; transcribe `429 ai_generation_limit` → intent kept blocked and not resent; retry rotates the key; transcribe-only replaces it with `notes: skip` and a new key; notes `429 ai_generation_limit` → rejected, not queued; a language change keeps `notes: skip` |
| `DocumentContentTests` | decode/encode the api §3 example JSON byte-stable (transcript refs with `approximate`; absent → `false`); chunk `timing` copied onto segments and kept in local storage; `uncertain` decode; transcription bodies carry `notes`; correction sends `resolved_uncertain_segment_ids`; upload result `transcription_blocked`; editing an `ai` item sets `edited`; new items `human` with `h_` id; `nil` encoded as `null` for pic/deadline/at_ms/started_at; `wireContent` strips an empty new item, empty topic, empty note |
| `ExportCacheTests` | key `<rev>-draft.pdf` vs `<rev>-reviewed.pdf`; offline hit only when suffix matches current review status |
| `ContrastTests` | every token pair in MASTER §2 ≥ 4.5:1 in light and dark trait collections |
| `LocalizationTests` | every key in `en` exists in `id` and vice versa; no empty values |
| `FormattingTests` | timer `mm:ss`/`h:mm:ss`, ref `[mm:ss]`/`[h:mm:ss]`, approximate `~mm:ss` / `[~mm:ss]` with spoken "sekitar …" / "about …", spoken duration id/en, deadline rendering rules (api §3.3) |
| `AllowanceTests` | `/v1/usage.ai_generations` decode; "30 generasi AI per 24 jam" / "30 AI generations per 24 hours", "Sisa n" / "n left", next slot text id/en; choice needed only for auto notes with 0 left; notes/MoM gate uses `next_slot_at` only when exhausted; `transcript_ready` → notes step "Belum dibuat"; blocked intent → transcript step "Menunggu slot AI"; uncertain-segment navigation wraps; unchanged uncertain text can be confirmed |

Runtimes: simulator tests run on the installed iOS 18.2/26 runtimes; iOS 17.0 behavior (SwiftData, AVAudioFile finalize by deinit) needs an iOS 17 simulator runtime or device (not installed; see open decisions).

Not unit-testable (manual on device, PRD §9 / stage A): 60-min lock-screen recording with ≥ 30 segment rotations while locked; incoming call interruption; force quit during recording and during merge; background upload across lock, suspend, force quit and network switch (iOS may defer background relaunches; record observed delays); WhatsApp share.

## 14. Analytics events

PRD §10 events with a UUID `event_id` and no meeting content, through `EventQueue` → `POST /v1/events` (api §4.12). Client emits: `onboarding_completed`, `recording_started`, `recording_interrupted`, `recording_saved`, `document_saved`, `mom_reviewed`, `export_completed`, `share_sheet_opened`, `quota_blocked`. Server emits `upload_verified`, `transcription_completed`, `notes_ready`, `mom_generated` (the client never sends these).
