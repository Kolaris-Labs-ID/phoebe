import SwiftUI
import UIKit

struct ToastMessage: Identifiable {
    let id = UUID()
    let text: LocalizedStringResource
    let actionTitle: LocalizedStringResource?
    let action: (() -> Void)?
}

@MainActor @Observable
final class ToastCenter {
    static let infoDuration: Duration = .seconds(4)
    static let actionDuration: Duration = .seconds(8)

    private(set) var current: ToastMessage?
    private var dismissTask: Task<Void, Never>?

    func show(_ text: LocalizedStringResource, actionTitle: LocalizedStringResource? = nil, action: (() -> Void)? = nil) {
        let message = ToastMessage(text: text, actionTitle: actionTitle, action: action)
        current = message
        AccessibilityNotification.Announcement(String(localized: text)).post()
        dismissTask?.cancel()
        guard !Self.assistiveTechRunning else { return }
        let duration = action == nil ? Self.infoDuration : Self.actionDuration
        dismissTask = Task { [weak self] in
            try? await Task.sleep(for: duration)
            guard !Task.isCancelled else { return }
            self?.dismiss(message.id)
        }
    }

    func dismiss(_ id: UUID? = nil) {
        guard id == nil || current?.id == id else { return }
        current = nil
        dismissTask?.cancel()
    }

    static var assistiveTechRunning: Bool {
        UIAccessibility.isVoiceOverRunning || UIAccessibility.isSwitchControlRunning
    }
}

struct ToastView: View {
    let message: ToastMessage
    let onDismiss: () -> Void
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        HStack(spacing: Spacing.md) {
            Text(message.text)
                .font(Typography.callout)
                .foregroundStyle(foreground)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let title = message.actionTitle, let action = message.action {
                Button {
                    action()
                    onDismiss()
                } label: {
                    Text(title)
                        .font(Typography.callout.weight(.semibold))
                        .foregroundStyle(foreground)
                        .underline()
                        .minTouchTarget()
                }
                .buttonStyle(.plain)
            }
            if ToastCenter.assistiveTechRunning {
                Button(action: onDismiss) {
                    Image(systemName: "xmark")
                        .foregroundStyle(foreground)
                        .minTouchTarget()
                }
                .buttonStyle(.plain)
                .accessibilityLabel(Text(L("common.close")))
            }
        }
        .padding(.horizontal, Spacing.lg)
        .padding(.vertical, Spacing.xs)
        .frame(minHeight: Spacing.minTouchTarget + Spacing.sm)
        .background(background, in: RoundedRectangle(cornerRadius: Radius.md, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityActions {
            if let title = message.actionTitle, let action = message.action {
                Button {
                    action()
                    onDismiss()
                } label: { Text(title) }
            }
        }
    }

    private var background: Color { scheme == .dark ? Palette.surface2 : Palette.textPrimary }
    private var foreground: Color { scheme == .dark ? Palette.textPrimary : Palette.surface }
}

struct ToastHostModifier: ViewModifier {
    let center: ToastCenter
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    func body(content: Content) -> some View {
        content.overlay(alignment: .bottom) {
            ZStack {
                if let message = center.current {
                    ToastView(message: message) { center.dismiss(message.id) }
                        .padding(.horizontal, Spacing.lg)
                        .padding(.bottom, Spacing.xxxl + Spacing.xl)
                        .transition(Motion.transition(reduceMotion: reduceMotion))
                        .id(message.id)
                }
            }
            .animation(Motion.standard(reduceMotion: reduceMotion), value: center.current?.id)
            .zIndex(ZLayer.toast)
        }
    }
}

extension View {
    func toastHost(_ center: ToastCenter) -> some View { modifier(ToastHostModifier(center: center)) }
}
