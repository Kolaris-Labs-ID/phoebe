import XCTest

// Drives the main screens against a local API on localhost:8080 and saves screenshots.
// Env (prefix TEST_RUNNER_ when passed through xcodebuild): PHOEBE_SHOTS_DIR, PHOEBE_SHOTS_VARIANT.
final class ScreenshotTests: XCTestCase {
    private var app: XCUIApplication!
    private let env = ProcessInfo.processInfo.environment
    private var variant: String { env["PHOEBE_SHOTS_VARIANT"] ?? "light" }

    override func setUp() {
        continueAfterFailure = true
        app = XCUIApplication()
        app.launchArguments += ["-AppleLanguages", "(id)", "-AppleLocale", "id_ID"]
    }

    func testMainScreens() throws {
        app.launch()
        sleep(2)
        snap("00-onboarding-welcome")
        tap(button: T("Onboarding", "onb.next"))
        snap("01-onboarding-privacy")
        tap(button: T("Onboarding", "onb.privacy.cta"))
        snap("02-onboarding-signin")
        let field = app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        _ = scrollTo(field)
        field.tap()
        field.typeText("shots\n")
        XCTAssertTrue(app.buttons[T("Onboarding", "onb.start")].waitForExistence(timeout: 10))
        snap("03-onboarding-language")
        tap(button: T("Onboarding", "onb.start"))

        let row = app.buttons.containing(NSPredicate(format: "label CONTAINS %@", "Rapat anggaran Q4")).firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15))
        sleep(2)
        snap("04-library")

        row.tap()
        sleep(3)
        snap("05-detail-transcript")
        tapTab(T("MeetingDetail", "detail.tab.notes"))
        snap("06-detail-notes")
        tapTab(T("MeetingDetail", "detail.tab.mom"))
        snap("07-detail-mom")

        let open = app.buttons[T("MeetingDetail", "mom.open")]
        if scrollTo(open) {
            open.tap()
            sleep(3)
            snap("08-editor")
            let export = app.buttons[T("Editor", "editor.export")].firstMatch
            if export.waitForExistence(timeout: 3) {
                export.tap()
                sleep(2)
                snap("09-export")
                app.swipeDown(velocity: .fast)
                sleep(1)
            }
            app.navigationBars.buttons.element(boundBy: 0).tap()
            sleep(1)
        }
        app.navigationBars.buttons.element(boundBy: 0).tap()
        sleep(1)

        tap(button: T("Library", "library.settings"))
        sleep(2)
        snap("10-settings")
        app.navigationBars.buttons.element(boundBy: 0).tap()

        tap(button: T("Library", "library.record"))
        sleep(2)
        app.tap()
        snap("11-recording-ready")
        // Starting capture is not driven here: the simulator's AURemoteIO can abort on an RPC timeout without host mic access.
        let record = app.buttons[T("Common", "recordButton.record")].firstMatch
        if record.waitForExistence(timeout: 3) {
            record.tap()
            sleep(2)
            if app.buttons[T("Recording", "rec.perm.explain.cta")].firstMatch.exists { snap("12-recording-permission") }
        }
    }

    // MARK: Helpers

    private func tap(button label: String, file: StaticString = #filePath, line: UInt = #line) {
        let element = app.buttons[label].firstMatch
        XCTAssertTrue(element.waitForExistence(timeout: 10), "missing button \(label)", file: file, line: line)
        if element.exists { element.tap() }
        sleep(1)
    }

    private func tapTab(_ label: String) {
        let segment = app.buttons[label].firstMatch
        if segment.waitForExistence(timeout: 5) { segment.tap() }
        sleep(2)
    }

    private func scrollTo(_ element: XCUIElement) -> Bool {
        for _ in 0..<6 {
            if element.exists && element.isHittable { return true }
            app.swipeUp()
        }
        return element.exists && element.isHittable
    }

    private func snap(_ name: String) {
        let shot = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = "\(variant)-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = env["PHOEBE_SHOTS_DIR"] else { return }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? shot.pngRepresentation.write(to: URL(filePath: dir).appending(path: "\(variant)-\(name).png"))
        try? app.debugDescription.write(to: URL(filePath: dir).appending(path: "\(variant)-\(name).txt"), atomically: true, encoding: .utf8)
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
