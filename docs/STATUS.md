# Phoebe MVP status

Verified 2026-11-06 on macOS with Go 1.27, Xcode 26.5, iPhone 16 simulator (iOS 18.2), Postgres 16 (docker compose, host port 55432), fake STT/LLM providers, dev login.

## Test suites

| Suite | Command | Result |
|---|---|---|
| Backend vet + unit/integration | `cd backend && go vet ./... && go test ./... -count=1` | vet clean; 325 tests pass, 0 fail, 0 skip (26 packages) |
| Backend smoke (real api + worker binaries, curl) | `backend/scripts/smoke.sh` | `SMOKE PASS` |
| iOS unit | scheme `Phoebe` (`ios/README.md`) | 216 tests in 28 suites pass |
| iOS E2E UI against the real backend | scheme `PhoebeScreenshots`, `-only-testing:PhoebeUITests/E2EFlowTests` | 3 pass, 1 skipped (live recording, see below) |

## Works end to end (simulator app + real api + worker)

Evidence: `ios/screenshots/e2e/*.png` (with `.txt` accessibility dumps), the api request log (`http.request` lines with route and status), the worker job log, and Postgres rows checked after the run.

| Flow | Evidence |
|---|---|
| Dev login, onboarding (welcome, privacy, sign-in, language) | `POST /v1/auth/dev` 200, then `GET /v1/config`, `/v1/folders`, `/v1/tags`, `/v1/meetings`, `/v1/usage` all 200 |
| Import an m4a from Files, confirm sheet, local copy | `e2e-02-picker`, `e2e-03-import-confirm` |
| Create meeting + upload + auto-started transcription | `POST /v1/meetings` 201, `POST /v1/meetings/{id}/uploads` 201, `PUT /v1/uploads/{id}` 200 (single chunk verifies and starts the job); worker `transcribe` then `generate_notes` succeeded |
| Library row status refresh: Notes siap → MoM draft → Sudah ditinjau | `e2e-05-library-ready`, `e2e-16-library-reviewed` |
| Detail: pipeline strip, timestamped transcript, AI notes | `e2e-06-detail-transcript`, `e2e-07-detail-notes`; `GET .../transcript`, `GET .../documents/{kind}` 200 |
| Buat MoM | `POST .../documents/mom/generate` 202; worker `generate_mom` succeeded; `e2e-09-mom-ready` |
| Edit MoM (title) | `PUT .../documents/mom` 201; editor shows `v2 · Diedit`; DB `current_revision_number = 2` |
| Tandai sudah ditinjau | `PUT .../documents/mom/review` 200; `e2e-12-editor-reviewed` |
| Export PDF + preview + iOS share sheet | `POST /v1/exports` 201, signed `GET /v1/files/{kind}/{id}` 200; preview shows the edited title; share sheet shows `rapatujie2e-diedit-undated-mom.pdf`, 18 KB; DB export row `is_draft = false` |
| Personal notes (Catatan Anda) | `PUT .../documents/manual_notes` 201; AI notes then show "Berdasarkan versi lama" |
| Rename via Edit info | `PATCH /v1/meetings/{id}` 200; DB `title = "RapatUjiE2E Final"`, `metadata_revision = 2` |
| Delete meeting | `DELETE /v1/meetings/{id}` 202; row leaves the Library; worker `purge_meeting` succeeded |
| Settings usage, plan and retention | `e2e-20-settings` (usage, trial plan, AI regeneration counter, retention rows) |
| Fresh install on an existing account | Library restored from the server with the MoM draft chip (`e2e-restore-01-library`) |
| Library search (filter + match) | `e2e-restore-02-search-empty`, search for "server" opens the meeting |
| Playback of server audio (no local file) | `GET .../audio/url` 200 then `GET /v1/files/...` 200; player advances and highlights the active segment (`e2e-restore-04-playing`) |
| Tags | `POST /v1/tags` 201 + `PATCH /v1/meetings/{id}` 200; `#klien` chip shown (`e2e-restore-05b-tagged`) |
| Delete account | `DELETE /v1/me` 202; app returns to the welcome step; per-user store directory removed; `purge_account` job queued (runs after its delay) |
| Sign out | returns to sign-in, app keeps running |

The backend smoke additionally covers, through the real binaries: 2-chunk upload, `/complete` and explicit transcription replays returning the same job, stale MoM edit → 409 `revision_conflict`, md/docx/pdf/whatsapp downloads with file signatures, audio Range 206, tampered signed URL 403, usage 60 s consumed, post-delete 404s and meeting-id reuse 409.

## Bugs found and fixed in this pass

| Bug | Fix |
|---|---|
| Sign out and delete account crashed the app (SIGILL in SwiftData `@Query` update). `AppModel.signOut` dropped the last reference to the `ModelContainer` while the signed-in views were still running their final `@Query` update. | `ios/Phoebe/App/AppModel.swift`: the retired container is kept alive for 1 s after sign-out. |
| Delete account then removed the store files while the views could still read them (`vnode unlinked while in use`). | `ios/Phoebe/Features/Settings/SettingsModel.swift`: waits up to 3 s for the container to be released before deleting `Application Support/<userID>/`. |
| Tag picker: a newly created tag appeared twice in its `ForEach`, giving a SwiftUI duplicate-ID fault on save. | `ios/Phoebe/Features/MeetingDetail/MetadataSheet.swift`: `TagPickerSheet.all` skips created tags that are already in the store. |

No HTTP contract mismatches or decoding errors were seen: every request in the E2E runs returned 2xx. App logs show no `com.kolarislabs.phoebe` errors.

## Verified only by unit or integration tests

- **Recording engine** (`RecordingEngineTests`, `RecordingStateMachineTests`, `SegmentWriterTests`, `SegmentMergerTests`, `RecordingRecoveryTests`):
  - segment rotation without restarting the engine; pause excludes paused time;
  - interruption, route loss and media-services reset, never auto-resuming;
  - storage preflight and low-storage stop;
  - crash recovery keeps valid segments; merge sums within 1 s over 62 minutes.
- **Background upload planning and resume**, checksum restart, upload error mapping (`UploadPlannerTests`, `UploadManagerFlowTests`, `ChecksumTests`).
- **Offline outbox and sync**:
  - queued creates, patches and edits;
  - metadata, document and notes conflicts (`SyncEngineTests`, `EditorDraftTests`, `APIClientTests`).
  - The E2E run never went offline.
- **Import validation**: wrong type, corrupt file, too long, too big (`ImportValidatorTests`, `ImportCopierTests`).
- **Backend acceptance scenarios**, all in `go test`:
  - quota and entitlement: `TestStartQuota`, `TestEntitlementInactiveOrExpired`, `TestReserveConsumeRelease`;
  - duplicate and retried jobs: `TestStartIdempotency`, `TestEnqueueDuplicateAndLookups`, `TestFinalizeOnceUnderLeaseRerun`;
  - provider timeout and invalid output: `TestProviderTimeout`, `TestTranscribeTimeout`, `TestInvalidOutputRetried`;
  - revision conflict: `TestRevisionConflictAndMutationReplay`;
  - regenerate after edit: `TestRestoreAfterRegenerate`, `TestKeepMineProvenanceAndStaleness`;
  - delete while a job is active: `TestDeleteDuringActiveJob`, `TestDeleteDuringGenerate`;
  - cross-account access: `TestCrossAccountAllRoutes` and the per-domain `*CrossAccount` tests;
  - prompt injection: `TestPromptInjectionContainment`;
  - audio deletion and retention: `TestDeleteAudioKeepsNotes` and the sweep tests.
- **Exports** in DOCX, MD and WhatsApp text: through smoke and `internal/export` tests. The UI exported PDF only.
- **Localization**: all keys exist in both `en` and `id` (`LocalizationTests`). The E2E ran in Indonesian.

## Needs a physical device or real accounts

- **Live recording.** On this simulator, starting capture aborts inside `AURemoteIO::Initialize` (an RPC timeout from `AVAudioEngine.inputNode`, `CaptureEngine.swift:94`) because Simulator has no host microphone permission. `testRecordShortMeeting` is ready: run it with `PHOEBE_E2E_RECORD=1` on a Mac where Simulator has microphone access, or check recording on a device.
- **60-minute lock-screen recording** with at least 30 segment rotations while locked.
- **Incoming call interruption**, then resume.
- **Force quit during recording and during merge**, then recovery.
- **Background upload** across lock, suspend, force quit and a network switch. Record any delay in iOS background relaunches.
- **WhatsApp share** of the text export and the files. The simulator share sheet shows only Copy, Markup, Print and Save to Files.
- **Sign in with Apple**: needs a signing team, the capability and a real Apple ID. Server-side Apple revocation is not configured locally (`startup.apple_revocation_not_configured`).
- **Real providers** (OpenAI STT, DeepSeek or Gemini LLM) and the 30-meeting AI evaluation gate (PRD §9). Every run so far used the fake providers.
- **Performance targets** (60-minute audio to notes in 15 minutes; a 1,000-meeting library searched in 2 s), and testing on an iOS 17 device (no iOS 17 runtime is installed).

## Observations (not changed)

- **Usage minutes don't add up.** Settings shows `0 dari 120 menit terpakai · 119 menit tersisa` after 20 s of audio. This follows the spec (`floor(seconds/60)` for each figure, `design-system/phoebe/pages/settings.md`), but used + left ≠ quota.
- **Tag search hides the toolbar.** In the tag picker, an active search hides the sheet toolbar until the system "Batalkan" is tapped, so "Selesai" isn't reachable while typing. This is standard `.searchable` behaviour.
- **Stray SwiftUI fault.** One `onChange(of: String) action tried to update multiple times per frame` fault was logged when opening a meeting detail. No visible effect.
- **System picker flakiness.** The simulator's document picker service (`com.apple.DocumentManager.Service`) sometimes crashes while navigating to "On My iPhone". The E2E test reopens the picker and retries.

## Open decisions

1. **Usage rounding:** keep floor-per-figure, or round used minutes up so used + left = quota?
2. **Container release:** replace the 1 s container retention on sign-out with an explicit "views released" signal if SwiftUI ever makes this deterministic. The current approach is a timing margin, not a guarantee.
3. **Real stack:** STT and LLM providers, models, region and retention must be chosen before the pilot (PRD §10). The smoke and E2E runs need repeating against them.
4. **Purchases:** plans, pricing and trial length are still open. The UI says "Pembelian paket belum tersedia di versi pilot".
5. **Device test plan:** the minimum devices and iOS versions for stage A.
6. **Port conflicts:** compose binds the API to host port 8080, which is taken on some machines. Decide whether to make it configurable like `PHOEBE_PG_PORT`. The iOS Debug build expects `localhost:8080`.
