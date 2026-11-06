import XCTest

// Full flow against the real backend on localhost:8080 (fake providers, dev login).
// Needs an m4a named PHOEBE_E2E_FILE (default "RapatUjiE2E") in the simulator's Files "On My iPhone".
// Env (prefix TEST_RUNNER_ when passed through xcodebuild): PHOEBE_SHOTS_DIR, PHOEBE_E2E_SUBJECT, PHOEBE_E2E_FILE.
@MainActor
final class E2EFlowTests: XCTestCase {
    private var app: XCUIApplication!
    private let env = ProcessInfo.processInfo.environment
    private var fileName: String { env["PHOEBE_E2E_FILE"] ?? "RapatUjiE2E" }

    override func setUp() {
        continueAfterFailure = false
        app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(id)", "-AppleLocale", "id_ID"]
    }

    func testImportToExport() throws {
        signIn(env["PHOEBE_E2E_SUBJECT"] ?? "e2e-\(Int(Date().timeIntervalSince1970))")
        snap("e2e-01-library-empty")

        tap(T("Library", "library.add"))
        tap(T("Library", "library.import"))
        sleep(2)
        snap("e2e-02-picker")
        pickFile()

        let upload = app.buttons[T("Library", "import.confirm.upload")].firstMatch
        XCTAssertTrue(upload.waitForExistence(timeout: 15), "import confirm sheet")
        snap("e2e-03-import-confirm")
        upload.tap()

        let row = app.buttons.containing(NSPredicate(format: "label CONTAINS %@", fileName)).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "library row")
        sleep(1)
        snap("e2e-04-library-uploading")
        let ready = app.buttons.containing(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", fileName, T("Common", "status.notesReady"))).firstMatch
        let reachedReady = ready.waitForExistence(timeout: 120)
        snap("e2e-05-library-ready")
        XCTAssertTrue(reachedReady, "row never showed notes ready")
        row.tap()
        sleep(2)
        snap("e2e-06-detail-transcript")

        tapTab(T("MeetingDetail", "detail.tab.notes"))
        snap("e2e-07-detail-notes")
        tapTab(T("MeetingDetail", "detail.tab.mom"))
        snap("e2e-08-mom-empty")

        let create = app.buttons[T("MeetingDetail", "mom.create")].firstMatch
        XCTAssertTrue(scrollTo(create), "create MoM button")
        create.tap()
        let open = app.buttons[T("MeetingDetail", "mom.open")].firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 60), "MoM never became ready")
        sleep(1)
        snap("e2e-09-mom-ready")
        XCTAssertTrue(scrollTo(open))
        open.tap()
        sleep(3)
        snap("e2e-10-editor")

        let title = app.textFields.firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 10), "editor title field")
        title.tap()
        sleep(1)
        title.typeText(" diedit")
        app.buttons[T("Common", "common.done")].firstMatch.tapIfExists()
        sleep(4)
        snap("e2e-11-editor-edited")

        let review = app.buttons[T("Editor", "review.markReviewed")].firstMatch
        XCTAssertTrue(scrollTo(review), "mark reviewed button")
        review.tap()
        let reviewed = app.staticTexts[T("Editor", "review.reviewed")].firstMatch
        XCTAssertTrue(reviewed.waitForExistence(timeout: 20), "never reviewed")
        sleep(1)
        snap("e2e-12-editor-reviewed")

        let export = app.buttons[T("Editor", "editor.export")].firstMatch
        XCTAssertTrue(scrollTo(export), "export button")
        export.tap()
        sleep(1)
        snap("e2e-13-export-sheet")
        let share = app.buttons[T("Export", "export.share")].firstMatch
        XCTAssertTrue(share.waitForExistence(timeout: 30), "share button")
        let enabled = NSPredicate(format: "isEnabled == true")
        expectation(for: enabled, evaluatedWith: share)
        waitForExpectations(timeout: 30)
        sleep(3)
        snap("e2e-14-export-preview")
        share.tap()
        sleep(3)
        snap("e2e-15-share-sheet")
        // The activity sheet lives outside the app's hierarchy; a tap on the dimmed area above it dismisses it.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
        sleep(2)
        let exportBar = app.navigationBars[T("Export", "export.title")]
        let close = app.buttons.matching(identifier: "xmark").firstMatch
        if close.waitForExistence(timeout: 3) { close.tap() }
        sleep(2)
        XCTAssertFalse(exportBar.exists, "export sheet still open")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        sleep(2)
        app.navigationBars.buttons.element(boundBy: 0).tap()

        let reviewedRow = app.buttons.containing(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", fileName, T("Common", "status.reviewed"))).firstMatch
        let rowReviewed = reviewedRow.waitForExistence(timeout: 20)
        snap("e2e-16-library-reviewed")
        XCTAssertTrue(rowReviewed, "library row never showed reviewed")

        reviewedRow.tap()
        sleep(2)
        tapTab(T("MeetingDetail", "detail.tab.notes"))
        let addPoint = app.buttons[T("MeetingDetail", "detail.notes.manual.add")].firstMatch
        XCTAssertTrue(scrollTo(addPoint), "add personal note")
        addPoint.tap()
        sleep(1)
        app.typeText("Poin pribadi e2e")
        app.buttons[T("Common", "common.done")].firstMatch.tapIfExists()
        sleep(4)
        snap("e2e-17-personal-note")

        tap(T("MeetingDetail", "detail.menu.a11y"))
        tap(T("MeetingDetail", "detail.menu.edit"))
        let titleField = app.textFields.matching(NSPredicate(format: "value == %@", fileName)).firstMatch
        let sheetOpen = titleField.waitForExistence(timeout: 5)
        snap("e2e-18a-metadata-sheet")
        XCTAssertTrue(sheetOpen, "metadata title field")
        titleField.tap()
        titleField.typeText(" Final")
        tap(T("Common", "common.save"))
        sleep(3)
        let renamed = app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "\(fileName) Final")).firstMatch
        let renameShown = renamed.waitForExistence(timeout: 10)
        snap("e2e-18-renamed")
        XCTAssertTrue(renameShown, "rename not shown")

        tap(T("MeetingDetail", "detail.menu.a11y"))
        let delete = app.buttons[T("MeetingDetail", "meeting.delete")].firstMatch
        XCTAssertTrue(scrollTo(delete), "delete menu item")
        delete.tap()
        tap(T("MeetingDetail", "meeting.delete.confirm"))
        sleep(3)
        snap("e2e-19-library-after-delete")
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons.containing(NSPredicate(format: "label CONTAINS %@", fileName)).firstMatch)
        waitForExpectations(timeout: 20)

        tap(T("Library", "library.settings"))
        sleep(3)
        snap("e2e-20-settings")
    }

    // Fresh install, existing account: needs PHOEBE_E2E_RESTORE_SUBJECT seeded with "Rapat dari server" (MoM drafted).
    func testRestoreFromServer() throws {
        guard let subject = env["PHOEBE_E2E_RESTORE_SUBJECT"] else { throw XCTSkip("PHOEBE_E2E_RESTORE_SUBJECT not set") }
        let title = "Rapat dari server"
        signIn(subject)

        let row = app.buttons.containing(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", title, T("Common", "status.momDraft"))).firstMatch
        let restored = row.waitForExistence(timeout: 20)
        snap("e2e-restore-01-library")
        XCTAssertTrue(restored, "server meeting not restored with MoM draft status")

        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 5))
        search.tap()
        search.typeText("anggaran-tidak-ada")
        sleep(2)
        XCTAssertFalse(row.exists, "search did not filter")
        snap("e2e-restore-02-search-empty")
        search.buttons.firstMatch.tapIfExists()
        search.typeText("server")
        XCTAssertTrue(app.buttons.containing(NSPredicate(format: "label CONTAINS %@", title)).firstMatch.waitForExistence(timeout: 5), "search miss")
        app.buttons.containing(NSPredicate(format: "label CONTAINS %@", title)).firstMatch.tap()
        sleep(3)
        snap("e2e-restore-03-detail")
        XCTAssertTrue(app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "Selamat pagi")).firstMatch.waitForExistence(timeout: 10), "transcript not loaded")

        let play = app.buttons.matching(identifier: "play.fill").firstMatch
        XCTAssertTrue(play.waitForExistence(timeout: 10), "play button")
        play.tap()
        let pause = app.buttons.matching(identifier: "pause.fill").firstMatch
        let playing = pause.waitForExistence(timeout: 15)
        sleep(3)
        snap("e2e-restore-04-playing")
        XCTAssertTrue(playing, "server audio never played")
        pause.tapIfExists()

        tap(T("MeetingDetail", "tag.new"))
        let tagField = app.searchFields.firstMatch
        let tagSheet = tagField.waitForExistence(timeout: 5)
        snap("e2e-restore-05a-tag-sheet")
        XCTAssertTrue(tagSheet, "tag search field")
        tagField.tap()
        tagField.typeText("klien")
        let create = app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "klien")).firstMatch
        XCTAssertTrue(create.waitForExistence(timeout: 5), "create tag row")
        create.tap()
        sleep(1)
        // An active search hides the sheet's toolbar until the system search cancel button ends it.
        for label in ["Batalkan", "Cancel"] where app.buttons[label].exists {
            app.buttons[label].tap()
            sleep(1)
        }
        tap(T("Common", "common.done"))
        sleep(3)
        snap("e2e-restore-05b-tagged")
        XCTAssertTrue(app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "klien")).firstMatch.waitForExistence(timeout: 5), "tag chip missing")

        app.navigationBars.buttons.element(boundBy: 0).tap()
        sleep(1)
        for label in ["Batalkan", "Cancel"] where app.buttons[label].exists {
            app.buttons[label].tap()
            sleep(1)
        }
        tap(T("Library", "library.settings"))
        let deleteAccount = app.buttons[T("Settings", "settings.delete")].firstMatch
        XCTAssertTrue(scrollTo(deleteAccount), "delete account button")
        deleteAccount.tap()
        tap(T("Settings", "settings.delete.step1.cta"))
        let word = app.alerts.textFields.firstMatch
        XCTAssertTrue(word.waitForExistence(timeout: 5), "confirm word field")
        word.typeText(T("Settings", "settings.delete.step2.word"))
        app.alerts.buttons[T("Settings", "settings.delete.step2.cta")].tap()
        let welcome = app.buttons[T("Onboarding", "onb.next")].firstMatch
        let signedOut = welcome.waitForExistence(timeout: 30)
        snap("e2e-restore-06-account-deleted")
        XCTAssertTrue(signedOut, "account deletion did not return to onboarding")
    }

    func testSignOut() throws {
        signIn("e2e-out-\(Int(Date().timeIntervalSince1970))")
        tap(T("Library", "library.settings"))
        let signOut = app.buttons[T("Settings", "settings.signout")].firstMatch
        XCTAssertTrue(scrollTo(signOut), "sign out button")
        signOut.tap()
        sleep(2)
        let confirm = app.sheets.buttons[T("Settings", "settings.signout")].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "sign out confirm")
        confirm.tap()
        let loginField = app.textFields.firstMatch
        let signedOut = loginField.waitForExistence(timeout: 15) || app.buttons[T("Onboarding", "onb.next")].firstMatch.exists
        snap("e2e-signout-01")
        XCTAssertTrue(signedOut, "sign out did not return to onboarding")
        XCTAssertEqual(app.state, .runningForeground, "app terminated after sign out")
    }

    func testRecordShortMeeting() throws {
        guard env["PHOEBE_E2E_RECORD"] == "1" else { throw XCTSkip("needs host microphone access for Simulator; set PHOEBE_E2E_RECORD=1") }
        signIn("e2e-rec-\(Int(Date().timeIntervalSince1970))")

        tap(T("Library", "library.record"))
        sleep(2)
        tap(T("Common", "recordButton.record"))
        let explain = app.buttons[T("Recording", "rec.perm.explain.cta")].firstMatch
        if explain.waitForExistence(timeout: 3) { explain.tap() }
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["Allow", "Izinkan", "OK"] where springboard.buttons[label].waitForExistence(timeout: 2) {
            springboard.buttons[label].tap()
            break
        }
        let stop = app.buttons[T("Common", "recordButton.stop")].firstMatch
        if !stop.waitForExistence(timeout: 5) { app.buttons[T("Common", "recordButton.record")].firstMatch.tapIfExists() }
        XCTAssertTrue(stop.waitForExistence(timeout: 10), "recording never started")
        sleep(12)
        snap("e2e-rec-01-recording")
        stop.tap()
        tap(T("Recording", "rec.stop.confirm"))
        let open = app.buttons[T("Recording", "rec.stopped.open")].firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 30), "stopped summary")
        snap("e2e-rec-02-stopped")
        open.tap()
        let ready = app.staticTexts[T("MeetingDetail", "detail.tab.transcript")].firstMatch
        _ = ready.waitForExistence(timeout: 5)
        sleep(20)
        snap("e2e-rec-03-detail")
    }

    // Tests share one install, so a session left by an earlier test is signed out first.
    private func signIn(_ subject: String) {
        app.launch()
        let settings = app.buttons[T("Library", "library.settings")].firstMatch
        if settings.waitForExistence(timeout: 5) {
            settings.tap()
            let signOut = app.buttons[T("Settings", "settings.signout")].firstMatch
            XCTAssertTrue(scrollTo(signOut), "sign out button")
            signOut.tap()
            let confirm = app.sheets.buttons[T("Settings", "settings.signout")].firstMatch
            XCTAssertTrue(confirm.waitForExistence(timeout: 5), "sign out confirm")
            confirm.tap()
        }
        for key in ["onb.next", "onb.privacy.cta"] {
            let button = app.buttons[T("Onboarding", key)].firstMatch
            if button.waitForExistence(timeout: 5) { button.tap(); sleep(1) }
        }
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10), "dev login field")
        _ = scrollTo(field)
        field.tap()
        field.typeText(subject + "\n")
        let start = app.buttons[T("Onboarding", "onb.start")].firstMatch
        if start.waitForExistence(timeout: 15) { start.tap(); sleep(1) }
        XCTAssertTrue(settings.waitForExistence(timeout: 15), "library not shown after sign in")
    }

    // The simulator's document picker service sometimes crashes mid-navigation; reopen it and retry.
    private func pickFile() {
        let predicate = NSPredicate(format: "label CONTAINS %@", fileName)
        for _ in 0..<6 {
            let cell = app.descendants(matching: .any).matching(predicate).firstMatch
            if cell.waitForExistence(timeout: 3) { cell.tap(); return }
            let pickerOpen = ["Browse", "Telusuri", "Recents", "Terbaru"].contains { app.buttons[$0].firstMatch.exists }
            if !pickerOpen {
                sleep(2)
                tap(T("Library", "library.add"))
                tap(T("Library", "library.import"))
                sleep(2)
                continue
            }
            let browse = app.buttons["Browse"].firstMatch.exists ? app.buttons["Browse"].firstMatch : app.buttons["Telusuri"].firstMatch
            if browse.exists { browse.tap(); sleep(1) }
            for label in ["On My iPhone", "Di iPhone Saya"] {
                let local = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", label)).firstMatch
                if local.exists { local.tap(); sleep(2); break }
            }
        }
        snap("e2e-02b-picker-miss")
        XCTFail("file \(fileName) not found in picker")
    }

    private func tap(_ label: String, timeout: TimeInterval = 10, file: StaticString = #filePath, line: UInt = #line) {
        let element = app.buttons[label].firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: timeout), "missing button \(label)", file: file, line: line)
        element.tap()
        sleep(1)
    }

    private func tapTab(_ label: String) {
        let segment = app.buttons[label].firstMatch
        if segment.waitForExistence(timeout: 5) { segment.tap() }
        sleep(2)
    }

    private func scrollTo(_ element: XCUIElement) -> Bool {
        for _ in 0..<8 {
            if element.exists && element.isHittable { return true }
            app.swipeUp()
        }
        return element.exists && element.isHittable
    }

    private func snap(_ name: String) {
        let shot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = env["PHOEBE_SHOTS_DIR"] else { return }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? shot.pngRepresentation.write(to: URL(filePath: dir).appending(path: "\(name).png"))
        try? app.debugDescription.write(to: URL(filePath: dir).appending(path: "\(name).txt"), atomically: true, encoding: .utf8)
    }

    nonisolated(unsafe) private static var catalogs: [String: [String: Any]] = [:]

    private func T(_ table: String, _ key: String) -> String {
        if Self.catalogs[table] == nil {
            let url = URL(filePath: #filePath).deletingLastPathComponent().appending(path: "../Phoebe/Resources/\(table).xcstrings").standardized
            let json = (try? JSONSerialization.jsonObject(with: Data(contentsOf: url))) as? [String: Any]
            Self.catalogs[table] = json?["strings"] as? [String: Any] ?? [:]
        }
        let entry = Self.catalogs[table]?[key] as? [String: Any]
        let id = (entry?["localizations"] as? [String: Any])?["id"] as? [String: Any]
        return (id?["stringUnit"] as? [String: Any])?["value"] as? String ?? key
    }
}

private extension XCUIElement {
    func tapIfExists() {
        if waitForExistence(timeout: 2) { tap() }
    }
}
