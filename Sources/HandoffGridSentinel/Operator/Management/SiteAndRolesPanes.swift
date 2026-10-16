import SwiftUI
import SentinelCore

// MARK: - Site Information

/// Site Information: name, description and contact over `GET/PUT /api/v2/site`.
struct SiteInfoPane: View {
    let api: SentinelAPI

    @State private var saved = V2.SiteInfo()
    @State private var draft = V2.SiteInfo()
    @State private var loaded = false
    @State private var error: String?
    @State private var status: String?
    @State private var busy = false

    private var dirty: Bool { draft != saved }

    var body: some View {
        PaneFrame(title: "Site Information", subtitle: "Shown in the sidebar header and on reports.", error: error, status: status) {
            Form {
                TextField("Site name", text: $draft.name)
                TextField("Contact", text: $draft.contact, prompt: Text("Name, email or phone"))
                VStack(alignment: .leading, spacing: 4) {
                    Text("Description").font(.caption).foregroundStyle(.secondary)
                    TextEditor(text: $draft.description)
                        .font(.body).frame(minHeight: 90, maxHeight: 140)
                        .overlay(RoundedRectangle(cornerRadius: 5).stroke(Color.secondary.opacity(0.3)))
                }
                HStack {
                    Spacer()
                    Button("Revert") { draft = saved; status = nil }.disabled(dirty == false || busy)
                    Button("Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent).keyboardShortcut(.defaultAction)
                        .disabled(dirty == false || busy || loaded == false)
                }
            }
            .formStyle(.grouped)
            .disabled(loaded == false && error == nil)
        }
        .task { await load() }
    }

    private func load() async {
        do {
            saved = try await api.siteInfo()
            draft = saved
            loaded = true
            error = nil
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func save() async {
        busy = true
        defer { busy = false }
        do {
            saved = try await api.updateSiteInfo(draft)
            draft = saved
            error = nil
            status = "Saved."
        } catch {
            self.error = managementErrorText(error)
        }
    }
}

// MARK: - Roles

/// Roles: list with the built-in lock and user counts, and a permission-matrix
/// editor. Built-ins are read-only (Duplicate makes an editable copy); delete
/// is refused locally and by the server (409) while users hold the role.
struct RolesPane: View {
    let api: SentinelAPI

    @State private var roles: [V2.RoleRecord] = []
    @State private var catalog: [V2.PermissionInfo] = []
    @State private var selection: String?
    @State private var editor = RoleEditorModel(role: nil)
    @State private var creating = false
    @State private var error: String?
    @State private var status: String?
    @State private var busy = false
    @State private var confirmDelete: V2.RoleRecord?

    var body: some View {
        PaneFrame(title: "Roles", subtitle: "Who can do what. Built-in roles are fixed; make a custom role for anything else.",
                  error: error, status: status, trailing: {
            Button { startNew() } label: { Label("New Role", systemImage: "plus") }
        }) {
            HSplitView {
                list.frame(minWidth: 240, idealWidth: 280, maxWidth: 360)
                detail.frame(minWidth: 380)
            }
        }
        .task { await load() }
        .confirmationDialog("Delete \(confirmDelete?.name ?? "role")?", isPresented: Binding(
            get: { confirmDelete != nil }, set: { if $0 == false { confirmDelete = nil } })) {
            Button("Delete Role", role: .destructive) {
                if let role = confirmDelete { Task { await delete(role) } }
            }
        } message: {
            Text("This can't be undone.")
        }
    }

    private var list: some View {
        List(selection: Binding(get: { selection }, set: { select($0) })) {
            ForEach(roles) { role in
                HStack {
                    Image(systemName: role.builtin ? "lock.fill" : "person.badge.key")
                        .foregroundStyle(.secondary).frame(width: 18)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(role.name)
                        if role.description.isEmpty == false {
                            Text(role.description).font(.caption).foregroundStyle(.secondary).lineLimit(1)
                        }
                    }
                    Spacer()
                    Text("\(role.userCount)").font(.caption.monospacedDigit()).foregroundStyle(.secondary)
                        .help("\(role.userCount) user\(role.userCount == 1 ? "" : "s")")
                }
                .tag(role.id)
            }
        }
    }

    @ViewBuilder private var detail: some View {
        if selection == nil && creating == false {
            Text("Select a role, or create one.").foregroundStyle(.secondary).frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            VStack(alignment: .leading, spacing: 10) {
                if let reason = editor.isLocked ? editor.blocker : nil {
                    Label(reason, systemImage: "lock.fill").font(.callout).foregroundStyle(.secondary)
                }
                TextField("Name", text: $editor.name).disabled(editor.isLocked)
                TextField("Description", text: $editor.description).disabled(editor.isLocked)
                matrix
                HStack {
                    if let role = editor.original {
                        Button("Duplicate") { duplicate(role) }
                        Button("Delete…", role: .destructive) { requestDelete(role) }
                            .disabled(busy)
                    }
                    Spacer()
                    if editor.isLocked == false, let blocker = editor.blocker, editor.isDirty {
                        Text(blocker).font(.caption).foregroundStyle(.secondary)
                    }
                    Button("Revert") { reset() }.disabled(editor.isDirty == false || busy)
                    Button(creating ? "Create" : "Save") { Task { await save() } }
                        .buttonStyle(.borderedProminent).disabled(editor.canSave == false || busy)
                }
            }
            .padding(14)
        }
    }

    private var matrix: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                ForEach(RoleEditorModel.groups(catalog), id: \.name) { group in
                    VStack(alignment: .leading, spacing: 4) {
                        HStack {
                            Text(group.name).font(.headline)
                            Spacer()
                            if editor.isLocked == false {
                                Button("Toggle all") { editor.toggleGroup(group.items.map(\.id)) }
                                    .buttonStyle(.link).font(.caption)
                            }
                        }
                        ForEach(group.items) { perm in
                            Toggle(isOn: Binding(get: { editor.has(perm.id) }, set: { editor.set(perm.id, $0) })) {
                                VStack(alignment: .leading, spacing: 0) {
                                    Text(perm.label)
                                    if perm.description.isEmpty == false {
                                        Text(perm.description).font(.caption).foregroundStyle(.secondary)
                                    }
                                }
                            }
                            .toggleStyle(.checkbox).disabled(editor.isLocked)
                        }
                    }
                }
                if catalog.isEmpty { Text("The permission catalog hasn't loaded.").foregroundStyle(.secondary) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.2)))
    }

    // MARK: Actions

    private func load() async {
        do {
            async let r = api.roles()
            async let c = api.permissionCatalog()
            roles = try await r
            catalog = try await c
            error = nil
            if let selection, let role = roles.first(where: { $0.id == selection }) {
                editor = RoleEditorModel(role: role)
            }
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func select(_ id: String?) {
        selection = id
        creating = false
        status = nil
        editor = RoleEditorModel(role: roles.first { $0.id == id })
    }

    private func startNew() {
        selection = nil
        creating = true
        status = nil
        editor = RoleEditorModel(role: nil)
    }

    private func duplicate(_ role: V2.RoleRecord) {
        selection = nil
        creating = true
        var copy = RoleEditorModel(role: nil)
        copy.name = "\(role.name) copy"
        copy.description = role.description
        role.permissions.forEach { copy.set($0, true) }
        editor = copy
    }

    private func reset() {
        editor = RoleEditorModel(role: editor.original)
    }

    private func save() async {
        busy = true
        defer { busy = false }
        do {
            let input = editor.input(catalog: catalog)
            let result: V2.RoleRecord
            if let original = editor.original {
                result = try await api.updateRole(original.id, input)
            } else {
                result = try await api.createRole(input)
            }
            error = nil
            status = "Saved \(result.name)."
            creating = false
            selection = result.id
            await load()
            editor = RoleEditorModel(role: roles.first { $0.id == result.id } ?? result)
        } catch {
            self.error = managementErrorText(error)
        }
    }

    private func requestDelete(_ role: V2.RoleRecord) {
        if let reason = RoleRules.deleteBlocker(role) {
            error = reason
            return
        }
        error = nil
        confirmDelete = role
    }

    private func delete(_ role: V2.RoleRecord) async {
        confirmDelete = nil
        busy = true
        defer { busy = false }
        do {
            try await api.deleteRole(role.id)
            selection = nil
            creating = false
            editor = RoleEditorModel(role: nil)
            status = "Deleted \(role.name)."
            error = nil
            await load()
        } catch {
            // The server's 409 carries the live user count.
            self.error = managementErrorText(error)
        }
    }
}

#Preview("Site Information") { SiteInfoPane(api: ManagementPreview.api()).frame(width: 520, height: 420) }
#Preview("Roles") { RolesPane(api: ManagementPreview.api()).frame(width: 760, height: 520) }
