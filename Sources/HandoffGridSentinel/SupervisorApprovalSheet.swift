import SwiftUI
import SentinelCore

/// A pending request for supervisor sign-off. `onApproved` runs only after the
/// PIN checks out and the approval has been written to the audit log.
struct ApprovalRequest: Identifiable {
    let id = UUID()
    let action: SensitiveAction
    /// What exactly is being approved, e.g. "HG-20261007-00003 · Dock camera".
    let detail: String
    let onApproved: (_ approver: String, _ reason: String) -> Void
}

struct SupervisorApprovalSheet: View {
    @EnvironmentObject private var operatorSessionStore: OperatorSessionStore
    @EnvironmentObject private var userDirectoryStore: UserDirectoryStore
    @Environment(\.dismiss) private var dismiss

    let request: ApprovalRequest
    @State private var approverID: UUID?
    @State private var pin = ""
    @State private var reason = ""
    @State private var errorMessage: String?
    @FocusState private var pinFocused: Bool

    private var approvers: [UserAccount] { operatorSessionStore.approvers(in: userDirectoryStore.users) }
    private var selfConfirm: Bool { operatorSessionStore.canSelfConfirmWithoutPin }
    private var lockedUntil: Date? {
        operatorSessionStore.approvalLockedUntil.flatMap { $0 > Date() ? $0 : nil }
    }

    private var canSubmit: Bool {
        guard lockedUntil == nil else { return false }
        if request.action.requiresReason, reason.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return false }
        return selfConfirm || (approverID != nil && pin.isEmpty == false)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 10) {
                Image(systemName: "person.badge.shield.checkmark.fill")
                    .font(.title2)
                    .foregroundStyle(SentinelTheme.accent)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Supervisor approval required")
                        .font(.title3.weight(.semibold))
                    Text(request.action.rawValue)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                }
            }

            if request.detail.isEmpty == false {
                Text(request.detail)
                    .font(.callout)
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(SentinelTheme.panelRaised, in: RoundedRectangle(cornerRadius: 8))
            }

            if selfConfirm {
                Label("You're signed in as \(operatorSessionStore.roleLabel) with no PIN set, so you can confirm this yourself. Set a PIN in Users & Roles to require one.",
                      systemImage: "info.circle")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if approvers.isEmpty {
                Label("No supervisor or admin has a PIN set, so nobody can approve this. Set a PIN in Users & Roles.",
                      systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(SentinelTheme.amber)
            } else {
                Picker("Approver", selection: $approverID) {
                    ForEach(approvers) { user in
                        Text("\(user.name) · \(user.role)").tag(Optional(user.id))
                    }
                }
                SecureField("Approver's PIN", text: $pin)
                    .textFieldStyle(.roundedBorder)
                    .focused($pinFocused)
                    .onSubmit(submit)
            }

            VStack(alignment: .leading, spacing: 4) {
                TextField(request.action.requiresReason ? "Reason (required)" : "Reason (optional)", text: $reason, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(2...4)
                Text("Recorded in the audit log with your name and the approver's.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }

            if let lockedUntil {
                Text("Locked until \(RecordingFormatters.timeFormatter.string(from: lockedUntil)) after repeated wrong PINs.")
                    .font(.caption)
                    .foregroundStyle(.red)
            } else if let errorMessage {
                Text(errorMessage)
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("Approve", action: submit)
                    .buttonStyle(.borderedProminent)
                    .keyboardShortcut(.defaultAction)
                    .disabled(canSubmit == false)
            }
        }
        .padding(20)
        .frame(width: 440)
        .onAppear {
            // Prefer the signed-in supervisor so a supervisor acting for themselves
            // only types their PIN.
            let current = operatorSessionStore.currentOperator?.id
            approverID = approvers.first { $0.id == current }?.id ?? approvers.first?.id
            pinFocused = true
        }
    }

    private func submit() {
        guard canSubmit else { return }
        let approver = approvers.first { $0.id == approverID }
        let result = operatorSessionStore.authorize(
            request.action,
            approver: selfConfirm ? nil : approver,
            pin: pin,
            reason: reason,
            detail: request.detail
        )
        switch result {
        case .approved(let name):
            let trimmedReason = reason.trimmingCharacters(in: .whitespacesAndNewlines)
            dismiss()
            request.onApproved(name, trimmedReason)
        case .denied(let message):
            errorMessage = message
            pin = ""
        }
    }
}

extension View {
    /// Presents the supervisor-approval sheet whenever `request` is set.
    func approvalSheet(_ request: Binding<ApprovalRequest?>) -> some View {
        sheet(item: request) { SupervisorApprovalSheet(request: $0) }
    }
}
