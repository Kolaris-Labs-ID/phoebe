# iOS foundation: APIs for the feature and service agents

Build and test, verified green (44 tests in 6 suites):

```
cd ios && xcodegen generate
xcodebuild -project Phoebe.xcodeproj -scheme Phoebe \
  -destination 'platform=iOS Simulator,id=AF989DC2-CB86-4161-A4A8-0ABAA181CA85' \
  -derivedDataPath /tmp/phoebe-dd-<you> test
```
The destination is iPhone 16, iOS 18.2. Use your own `-derivedDataPath`. If you run tests at the same time as another agent, pick a different simulator id (`xcrun simctl list devices available`).

`project.yml` picks up every file under `Phoebe/` and `PhoebeTests/`. After you add or delete a file, run `xcodegen generate` again. Swift 6 runs with strict concurrency, so keep all app code `@MainActor` unless it does I/O off the main thread.

## Ownership

The foundation owns these paths. Do not edit them; list any change you need under NEEDS.
- `project.yml`, `Phoebe/App/**`, `Phoebe/DesignSystem/**`, `Phoebe/Data/**`, `Phoebe/Networking/**`
- `Phoebe/Sync/Backoff.swift`, `Phoebe/Sync/Checksum.swift`
- `Phoebe/Resources/{Common.xcstrings,InfoPlist.xcstrings,Assets.xcassets}`

Feature placeholders live in `Phoebe/Features/<Area>/<Area>PlaceholderView.swift`. Delete the placeholder, then define the same type with the same init in your own file:

| Area | Type + init used by RootView or callers |
|---|---|
| Onboarding | `OnboardingView()`. Shown for `.signedOut` and `.onboarding(step)`. Reads `app.session` / `app.signOutReason` |
| Library | `LibraryView()`. Root of the `NavigationStack` |
| Recording | `RecordingView()`. Presented as `fullScreenCover(isPresented: $app.isRecordingPresented)` |
| MeetingDetail | `MeetingDetailView(meetingID: UUID)` |
| Editor | `DocumentEditorView(meetingID: UUID, kind: DocumentKind)` |
| Settings | `SettingsView()` |
| Export | `ExportSheet(meetingID: UUID, kind: DocumentKind)`. Detail and editor present it as a `.sheet` |

## App state (`App/AppModel.swift`)

`@MainActor @Observable final class AppModel` is injected with `.environment(appModel)`. Read it with `@Environment(AppModel.self) private var app`.

- `session: SessionState`, which is one of `.signedOut`, `.onboarding(OnboardingStep)` or `.signedIn(UserDTO)`. `OnboardingStep` = `.welcome / .privacy / .signIn / .language`.
- `user: UserDTO?` and `signOutReason: SignOutReason?` (`.userInitiated / .revoked / .unauthenticated / .accountDeleted`).
- `path: [Route]`. `Route` = `.meeting(UUID)`, `.editor(meetingID:kind:)` or `.settings`. Push with `app.path.append(.meeting(id))` or `app.openMeeting(id)`, which also closes the recording cover.
- `isRecordingPresented` and `presentRecording()`. When the app returns to the foreground with a live mic session, the cover is presented again automatically.
- `pendingRecovery: LocalRecording?`, set at launch from `recorder.recoverOnLaunch()`.
- `serverConfig: ServerConfig?`. It is cached and has `fetchedAt`, and stays `nil` until it is fetched for the first time. `maxAudioDurationMs` falls back to 7,200,000. Call `refreshConfig()` to fetch it again.
- `usage: UsageDTO?` and `usageFetchedAt: Date?`, both cached. `refreshUsage()` fetches `/v1/usage`, calls `uploads.retryWaitingIntents(usage:)` and returns the fresh value, or `nil` when the fetch failed (`usage` keeps the last known value).
- `isOnline`, also available as `connectivity.isOnline` (`Connectivity`, `@Observable`, NWPathMonitor). Going online calls `uploads.resumeAll()` and `sync.pushAll()`.
- `toasts: ToastCenter`. Show a toast with `app.toasts.show(L("..."), actionTitle:, action:)`. The host is attached at the app root and on the recording cover.
- `api: APIClient` (shared), `auth: AuthStore`, `modelContainer` / `modelContext` (per user, at `Application Support/<userID>/Phoebe.store`).
- Services: `recorder`, `uploads`, `sync`, `poller`, `events`. They are the protocols listed below and are valid only while signed in. Before that they are placeholders.
- Onboarding and sign-in:
  - `advanceOnboarding(to:)`
  - `didSignIn(_ AuthResponseDTO, method: .apple|.dev, appleUserID:)`. It stores the token, user id and method in the Keychain, clears the pending display name, opens the store, builds services, then moves to `.onboarding(.language)` on first run, otherwise to `.signedIn`.
  - `completeOnboarding()`. It sets the flag, logs `onboarding_completed` and moves to `.signedIn`.
- `updateUser(_:)` caches the user and upserts `UserProfile`.
- `signOut(reason:callServer:)` calls `POST /v1/auth/logout` when `callServer` is true, clears the Keychain session, drops the store and services, and moves to `.signedOut`.
- A terminal 401 (`unauthenticated`, `session_revoked`, `session_expired`, or any 401 from `POST /v1/auth/refresh`) goes through `APIClient.onUnauthenticated` to `handleUnauthenticated()`, which signs out with `.unauthenticated` and no server call (ignored while a sign-out is running). `401 access_token_expired` never gets here: `APIClient` refreshes and retries once. A refresh that fails on the network keeps the session.
- Launch runs `start()` once: connectivity, the Apple revocation observer and the config fetch. While signed in, `runSignedInLaunchTasks()` runs these in order: recovery, Apple credential state check (Apple sessions only), `uploads.resumeAll`, `sync.fullPull`, `refreshUsage`, `events.flush`.
- `scenePhaseChanged`. On `.active` it calls `poller.setForeground(true)`, `reset()`, `recorder.retryPendingMerges()`, `uploads.resumeAll()` and `events.flush()`. On `.background` it calls `poller.setForeground(false)`.
- In Release, the base URL must be `https` or the app stops at launch. Debug uses `http://localhost:8080`. `APIClient.devLoginEnabled` is true only when the build is DEBUG and `PhoebeDevLogin == "1"`.

## Service seams (`App/Services.swift`)

Every concrete service takes `init(env: ServiceEnvironment)`. `ServiceEnvironment` holds `{ api, context: ModelContext (main), userID, auth, unowned app: AppModel }`. Do all SwiftData writes on `env.context`.

**Wiring:** `App/LiveServices.swift` `Services.live(env)` builds `RecordingEngine`, `UploadManager`, `SyncEngine`, `Poller` and `EventQueue`. Placeholders are used only while signed out.

```swift
@MainActor protocol RecordingEngineAPI: AnyObject {      // Audio agent: RecordingEngine (@Observable)
  var phase: RecordingPhase { get }        // idle ready starting recording paused interrupted stopping stopped; .hasLiveSession, .buttonState
  var meetingID: UUID? { get }; var activeDurationMs: Int { get }; var segmentCount: Int { get }
  var inputLevel: Float { get }            // 0...1, for the level meter at 2 Hz
  var banner: RecordingBanner? { get }     // startFailed writeFailed interrupted(savedMs) interruptionWaiting mediaReset routeChanged(inputName)
                                           // noInput storageLow(freeBytes) storageStopped(savedMs) storagePreflight(requiredBytes) limitNear(limitMinutes,minutesLeft) limitReached(limitMinutes)
  var micPermission: MicPermission { get } // undetermined granted denied
  var canResume: Bool { get }              // interruption ended; resume is still never automatic
  func refreshPermission(); func requestMicPermission() async -> Bool
  func prepare() async                     // idle → ready
  func start(meetingID: UUID) async        // creates LocalRecording; on failure phase stays ready and banner is set
  func pause(); func resume() async
  func stop() async -> RecordingSummary?   // closes the segment, then merge → PendingUpload + PendingIntent(.transcribe) → queued
  func dismissBanner()
  func recoverOnLaunch() async -> LocalRecording?    // never starts the mic
  func recover(_:) async; func discard(_:); func retryPendingMerges() async
}
@MainActor protocol UploadManaging: AnyObject {           // Sync agent: UploadManager
  func resumeAll() async; func kick(meetingID:) async; func retry(meetingID:) async
  func cancel(meetingID:)                  // cancels tasks: delete meeting or orphan
  func progress(meetingID:) -> Double?     // 0...1
  func retryWaitingIntents(usage: UsageDTO) async
}
@MainActor protocol SyncEngineAPI: AnyObject {            // Sync agent: SyncEngine (@Observable)
  var isPulling: Bool { get }; var lastPullFailed: Bool { get }   // library.refresh.error
  func fullPull() async; @discardableResult func pollTick() async -> Bool  // true = something still active
  func refreshMeeting(_:) async; func pushAll() async
  func enqueue(_ m: OutboxMutation)        // createMeeting patchMeeting(id, MeetingPatchRequest) deleteMeeting deleteAudio
                                           // create/rename/deleteFolder, create/rename/deleteTag; does the local effects and pushes
  func saveDraft(meetingID:kind:wireContent: Data, loadedRevision: Int)   // editor debounce, writes PendingDocumentEdit
  func flushEdits(meetingID:kinds:) async -> Bool                          // before generate: true once 2xx
  func resolveDocumentConflict(meetingID:kind:_ ConflictResolution) async // .keepMine / .useLatest
  func resolveMetadataConflict(meetingID:_:) async
  func setReview(meetingID:kind:status:) async throws
  func requestGeneration(meetingID:type: IntentType, body: Data) async throws  // persists a PendingIntent (key generated once)
  func search(_ q: String) async throws -> [MeetingListItemDTO]
  func keepOrphanAsNewMeeting(_:) async -> UUID?; func discardOrphan(_:)
}
@MainActor protocol PollingAPI: AnyObject { func start(_ t: PollTarget); func stop(_:); func reset(); func setForeground(_:) } // .library / .meeting(UUID)
@MainActor protocol EventQueueAPI: AnyObject { func log(_ e: AnalyticsEvent, meetingID: UUID?); func flush() async }
```
For background `URLSession` relaunches, `AppDelegate` stores the system completion handler in `BackgroundSessionEvents.shared`. Upload session id: `com.kolarislabs.phoebe.upload`. UploadManager must call `BackgroundSessionEvents.shared.complete(identifier:)` from `urlSessionDidFinishEvents` on main.

## Data (`Data/`)

`Models.swift` holds the SwiftData models from ios.md §4. Enums are stored as raw `String` and exposed through typed accessors.
- `Meeting`:
  - `sourceRaw` / `source`
  - `localPhaseRaw` / `localPhase: LocalPhase`
  - `language` / `transcriptionLanguage`
  - `pipelineState` / `pipeline`, `activeJobTypes` / `activeJobs`, `momReviewStatus` / `momReview`
  - `searchText`: call `refreshSearchText()` after the title or tags change
  - `sortDate`
- `Folder` / `Tag`: `meetings` inverse.
- `UserProfile`: `transcriptionLanguage`, `deleteLocalAudioAfterTranscript`.
- `TranscriptRevision`: `segments()`.
- `Document`: `apply(DocumentSummaryDTO)`, `reviewStatus`.
- `DocumentRevision`: `init(meetingID:dto:)`, flat `source*` fields, `content: Data?`, `decodedContent()`.
- `ProcessingJob`: `init(dto:)`, `apply`.
- `LocalRecording`: `state`, `segments` cascade, `orderedSegments`.
- `RecordingSegment`: `fileName(index:)` → `seg-0001.m4a`.
- `PendingUpload`: `state: PendingUploadState`.
- `PendingIntent`: `type: IntentType`, unique `idempotencyKey`, exact `body`. `PendingIntent.transcribe(meetingID:language:)` builds the `{"language":…}` body.
- `PendingDocumentEdit`: `hasPendingWork`.
- `PendingMutation`: `kind: MutationKind`, `targetID` (the folder or tag id), `payload` (the exact wire body).
- `PendingEvent`: `dto`.

`ModelContainerFactory` provides `make(userID:)`, `inMemory()` (for tests) and `schema`.

`DocumentContent.swift` mirrors api §3:
- Types:
  - Containers: `ManualNotesContent`, `AINotesContent`, `MoMContent`, `MeetingInfo`
  - Items: `NoteItem`, `NotesSection`, `MoMSection` (its `items` decode by section type), `MoMItem` (`.text/.topic/.action`), `TextItem`, `TopicItem`, `ActionItem`
  - Enums: `SectionType`, `ListStyle`, `Origin`, `SourceRef` (`.transcript(segmentId:startMs:endMs:approximate:)`, `approximate` defaults to `false` and is set by the server; `.userNotes(noteId:)`)
  - `DocumentKind` (`manual_notes/ai_notes/mom`)
- Nullable keys are always encoded as `null`.
- Editing rules:
  - `TextItem.setText`, `TopicItem.setTitle` and `ActionItem.setDescription/setPIC/setDeadline/setDeadlinePhrase` set `edited` on `ai` items. Blank values become `nil`.
  - New items: `.human(...)` / `NoteItem.new(text:atMs:)` get an `h_<uuid>` id.
  - `MoMSection.newCustom(language:)`, `MoMContent.emptyTemplate(language:title:startedAt:timezone:participants:)`.
  - `wireContent()` on every container strips empty items and topics and trims participants.
- `DocumentContentValue` (`.manualNotes/.aiNotes/.mom`) provides `decode(kind:from: Data)`, `wireContent()`, `encoded()` and `wireData()`. Store `wireData()` in `PendingDocumentEdit.content`.

## Networking (`Networking/`)

- `APIClient` (Sendable):
  - `send(Endpoint<R>) async throws -> R`. It always throws `APIError`.
  - `sendDelete(_:) -> R?`. A 404 returns `nil`, which counts as success.
  - `authorizedRequest(_:minValidity:)` (async) returns a `URLRequest` whose bearer stays valid ≥ `minValidity` (default 300 s), refreshing first; for background upload tasks. `makeRequest(_:token:)` builds one with an explicit token.
  - Sessions: `init(baseURL:session:tokens: any TokenStore, now:keepAlive:onUnauthenticated:)` (`keepAlive` begins a background task around each rotation; default `APIClient.backgroundActivity`). `TokenRefresher` (actor) does single-flight `POST /v1/auth/refresh`; `send` refreshes when the access token expires within 60 s and on `401 access_token_expired` (retry once).
  - `handle(_:data:response:)` decodes a background task result into `R` or throws `APIError`.
- `APICoding.encoder()/decoder()`: snake_case conversion, RFC3339 dates with milliseconds, sorted keys. `APICoding.encode(_:)` returns `Data` and `parseDate/formatDate`. Property names end in `Id`, never `ID`. `UUID.lower` gives the lowercase form for paths, headers and bodies.
- `Endpoints.*` covers every api.md route:
  - Config and auth: `health`, `config`, `signInWithApple`, `devLogin`, `logout`, `me`, `updateMe`, `deleteAccount`
  - Meetings: `createMeeting`, `listMeetings(q:folder: FolderQuery(.unfiled/.folder):tagID:limit:cursor:)`, `meeting`, `patchMeeting(id, MeetingPatchRequest)`, `patchMeeting(id, rawBody:)`, `deleteMeeting`
  - Folders and tags: `folders/createFolder/renameFolder/deleteFolder`, `tags/...` (same set)
  - Uploads: `createUpload`, `upload`, `uploadChunk(uploadID, offset:)` (no body, use `uploadTask(fromFile:)`), `completeUpload`
  - Transcription and jobs: `startTranscription(meetingID:idempotencyKey:body:)`, `jobs`, `job`
  - Transcript: `transcript`, `transcriptRevisions`, `transcriptRevision`, `correctTranscript`
  - Documents: `document`, `putDocument(meetingID:kind:expectedRevision:baseRevision:mutationID:content: Data)` (wire JSON spliced in verbatim), `documentRevisions`, `documentRevision`, `restoreDocument`, `setReview`
  - Generation: `generateNotes(meetingID:idempotencyKey:body:)`, `generateMoM(...)`
  - Export, audio, usage, events: `createExport`, `export`, `audioURL`, `usage`, `deleteAudio`, `events`
  - Job endpoints take the persisted `body: Data` so every retry sends identical bytes.
- `DTOs.swift`:
  - Response objects use a `DTO` suffix: `UserDTO`, `FolderDTO`/`TagDTO`, `MeetingDTO` (`documents[kind]`, `jobs[type]`), `MeetingListItemDTO`, `AudioAssetDTO`, `UploadSessionDTO`, `UploadResultDTO`, `JobDTO`, `TranscriptRevisionDTO`, `TranscriptSegment`, `DocumentSummaryDTO`, `DocumentRevisionDTO` (`content: DocumentContentValue?`), `ExportResponse`, `UsageDTO`, `ServerConfig`, plus the envelopes, `Page<T>` and `ItemsResponse<T>`.
  - Enums: `MeetingSource`, `PipelineState`, `JobType`, `JobStatus`, `ReviewStatus`, `RevisionOrigin`, `UploadStatus`, `ExportFormat`, `EntitlementStatus`, `TranscriptionLanguage` (auto/id/en), `OutputLanguage`, `AnalyticsEvent`.
  - Request bodies encode nullable fields as explicit `null`: `CreateMeetingRequest`, `CreateUploadRequest` + `TranscriptionStart`, `TranscriptionRequest`, `GenerateNotesRequest`, `GenerateMoMRequest`, `TranscriptCorrectionRequest`, `RestoreRequest`, `ExportRequest`, `EventDTO`, `AppleAuthRequest`, `UpdateMeRequest`.
- `Patch.swift`:
  - `Patch<T>` has three cases: `.unchanged` (key omitted), `.set(T)` and `.clear` (`null`).
  - `MeetingPatchRequest` has `expectedMetadataRevision`, `title`, `startedAt`, `timezone`, `language`, `participants`, `folderId` and `tagIds`, plus `hasMetadataChanges` and `isEmpty`.
- `APIError`:
  - One case per api §1.2 code, with typed details such as `.revisionConflict(currentRevisionNumber:currentRevisionId:)`, `.uploadOffsetMismatch(receivedBytes:)`, `.quotaExceeded(...)`, `.aiGenerationLimit(nextSlotAt:)`, `.validationFailed([FieldError])` and `.invalidContent(path:reason:)`.
  - Local cases: `.offline`, `.transport`, `.timeout`, `.decoding`, plus `.server(status:code:)` for unknown codes.
  - `isAmbiguous` is true for transport, timeout and 5xx; retry the identical request. `code` is the stable string to localize the UI by. Never show the server `message`.
- `AuthStore` (Keychain, `AfterFirstUnlockThisDeviceOnly`):
  - Read-only properties: `tokens: TokenPairDTO?` (access + refresh token and expiries, one Keychain item), `appleUserID`, `pendingDisplayName`, `signInMethod`, `userID`. Conforms to `TokenStore` (`tokens`, `saveTokens(_:)`).
  - `value(for:)` / `set(_:for:)` read and write any key.
  - `saveSession(tokens:userID:method:)` and `clearSession()`. `clearSession()` keeps `pendingDisplayName`.

## Sync helpers

- `Backoff.poll`: 2 s × 1.5, capped at 30 s, no jitter.
- `Backoff.upload`: 2, 4, 8, 16, 30, then 60 s, with ±20% jitter around the base.
- `Backoff` methods: `nextDelay()`, `nextDelay(using:)` (seedable), `baseDelay(forAttempt:)`, `reset()`, `attempt`.
- `Checksum`: `sha256Hex(fileURL:chunkSize:)` (streamed, 1 MiB), `Checksum.Streaming` (`update`, `finalizeHex`, `bytes`), `sha256Hex(Data|String)` (Sign in with Apple nonce).

## Design system (`DesignSystem/`)

No raw hex values in views.
- **Colors:** `Palette.<token>` (SwiftUI `Color`, dynamic light/dark). `ColorToken.<token>.uiColor` gives the `UIColor`. Tokens are the ones in MASTER §2.
- **Tone:** `.neutral/.info/.success/.warning/.danger/.record/.ai`, each with `.foreground` and `.fill`.
- **Spacing:** `Spacing.xxs…xxxl` (2…48), `screenInsetCompact` 16, `minTouchTarget` 44, `readingMaxWidth` 680.
- **Radius and layers:** `Radius.sm/md/lg` (6/10/14). `ZLayer.banner/bottomBar/toast`.
- **Motion:** `Motion.fast/standard/exit/recordPulse`, `Motion.standard(reduceMotion:)`, `Motion.transition(reduceMotion:)`, `pulseMinOpacity`.
- **Modifiers:** `.elevation(0|1|2)`, `.readingWidth()`, `.minTouchTarget()`, `PressScaleButtonStyle`.
- **Typography:** `Typography.display/title/headline/body/callout/subheadline/footnote/caption`, `.timerTextStyle()` (@ScaledMetric 56, capped at 88, monospaced digits), `.readingTextStyle()` (body + lineSpacing 4).
- **Formatting:**
  - `timer(ms:)` returns `mm:ss`, or `h:mm:ss` from one hour.
  - `timestamp(ms:approximate:)` returns the same text, `~mm:ss` when approximate; `reference(ms:approximate:)` returns `[mm:ss]` / `[~mm:ss]`; `spokenTimestamp(ms:approximate:)` prefixes "sekitar" / "about" (Common `time.approximate.a11y`).
  - `spokenDuration(ms:locale:includeSeconds:)` returns, for example, "12 menit, 4 detik".
  - `deadline(date:phrase:locale:)` returns "13 Nov 2026 (minggu depan)", the phrase alone, or "Belum ditentukan".
  - `notDetermined(_:)`, `parseDay`, `dayString`, `quotaMinutes(seconds:)`.

Components. Each takes `LocalizedStringResource` and is labeled for VoiceOver.
- `StatusChip(_ status: ChipStatus)` or `StatusChip(text, systemImage:, tone:)`. `ChipStatus` cases:
  - Local and upload: `localOnly`, `waitingConnection`, `queued`, `uploading(percent:)`, `verifying`, `waitingQuota`, `planExpired`, `waitingStorage`, `orphaned`, `importing`
  - Processing: `transcribing`, `transcriptReady`, `generatingNotes`, `notesReady`, `generatingMom`
  - Review and failure: `momDraft`, `reviewed`, `failed(FailedStage: .upload/.transcription/.notes/.mom)`, `noSpeech`
  - Live and stale: `recording`, `interrupted`, `outdated`
- `Banner(tone:, systemImage:, title:, message:, actions: [BannerAction(title, isLoading:) { }], onClose:)`. Announced on appear; never auto-dismissed.
- `ToastCenter.show(text, actionTitle:, action:)` and `.toastHost(center)`. A toast stays 4 s, or 8 s with an action, and never auto-dismisses while VoiceOver or Switch Control is running.
- `RecordButton(state: RecordButtonState(.ready/.starting/.recording/.paused/.stopping/.disabled), durationMs:, disabledReason:, action:)`, `StopRecordButton(isEnabled:, action:)`, `RecordControls(state:, durationMs:, disabledReason:, showsStop:, onMain:, onStop:)`. `RecordingPhase.buttonState` maps a phase to a button state. Haptics are the caller's job.
- `OriginMarker(origin:, edited:)`.
- `SourceReferenceButton(ref:, isVerified:, isPlayable:, onSeek: (Int) -> Void)`.
- `SaveIndicator(state: SaveState(.saving/.local/.syncing/.synced/.offline/.notSynced))`.
- `PrimaryButton(title, systemImage:, isLoading:, action:)`, `SecondaryButton(...)`.

## Localization (`App/Localization.swift`)

- `L("key", table: .library)` returns a `LocalizedStringResource`; use it as `Text(L(...))`.
- `L("key", table: .library, args...)` formats eagerly, for example `L("status.uploading", Int64(45))`. Pass `Int64` for `%lld` and `String` for `%@`.
- `LS("key", table:, localization: "id"|"en"|nil, args...)` returns a `String`.
- Tables (`StringTable`): `.common` (Common.xcstrings, owned by the foundation), `.onboarding`, `.library`, `.settings`, `.recording`, `.meetingDetail`, `.editor`, `.export`, `.audio`, `.sync`. Each feature creates `Resources/<Table>.xcstrings` (sourceLanguage `en`, with both `en` and `id` per key).
- Common keys (already present):
  - `common.cancel/done/retry/save/close/undo/notDetermined/storeError.*`
  - `status.*` (all of library.md, plus `status.localOnly` and `status.outdated`)
  - `origin.*` (+ `.a11y`)
  - `ref.unverified/fromNotes/playAt`
  - `save.local/saving/syncing/synced/offline/notSynced`
  - `recordButton.*`
