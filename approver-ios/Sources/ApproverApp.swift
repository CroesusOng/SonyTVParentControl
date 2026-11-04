import SwiftUI

/// Deliberately small. This app does one thing the web app cannot, and every screen it
/// grows is another thing to keep in step with the parent app. Rules, usage charts and
/// history stay in the PWA; what lives here is the handful of actions that must be the
/// parent in person.
@main
struct ApproverApp: App {
    var body: some Scene {
        WindowGroup {
            RootView()
        }
    }
}

struct RootView: View {
    @State private var credentials = Credentials.load()

    var body: some View {
        if let credentials {
            ApproveView(backend: Backend(credentials: credentials)) {
                Credentials.forget()
                ApproverKey.deleteExisting()
                self.credentials = nil
            }
        } else {
            EnrolView { self.credentials = $0 }
        }
    }
}

// MARK: - enrolling

struct EnrolView: View {
    let onEnrolled: (Credentials) -> Void

    @State private var address = ""
    @State private var code = ""
    @State private var name = defaultName
    @State private var busy = false
    @State private var problem: String?

    private static var defaultName: String {
        #if os(iOS)
        UIDevice.current.name
        #else
        Host.current().localizedName ?? "iPhone"
        #endif
    }

    var body: some View {
        Form {
            Section {
                Text("在家长端打开「审批手机 → 注册一台审批手机」,把六位码输到下面。")
                    .font(.footnote).foregroundStyle(.secondary)
            }
            Section("后端地址") {
                TextField("tv.example.com", text: $address)
                    .textContentType(.URL)
                    .autocorrectionDisabled()
                #if os(iOS)
                    .textInputAutocapitalization(.never)
                    .keyboardType(.URL)
                #endif
            }
            Section("注册码") {
                TextField("六位数字", text: $code)
                #if os(iOS)
                    .keyboardType(.numberPad)
                #endif
            }
            Section("这台手机叫什么") {
                TextField("爸爸的 iPhone", text: $name)
            }

            if !ApproverKey.biometricsAvailable {
                // Checked up front: generating the key under .biometryCurrentSet simply
                // fails on a phone with no face enrolled, and the error it fails with is
                // not one anybody should have to read.
                Section {
                    Label("这台手机还没设置 Face ID。先去设置里开好,再回来注册。",
                          systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.orange)
                }
            }
            if let problem {
                Section {
                    Text(problem).foregroundStyle(.red).font(.footnote)
                }
            }

            Button(busy ? "注册中…" : "注册") { enrol() }
                .disabled(busy || code.count != 6 || address.isEmpty
                          || !ApproverKey.biometricsAvailable)
        }
    }

    private func enrol() {
        busy = true
        problem = nil
        Task {
            do {
                onEnrolled(try await Backend.enrol(
                    address: address, code: code, name: name))
            } catch {
                problem = error.localizedDescription
            }
            busy = false
        }
    }
}

// MARK: - approving

struct ApproveView: View {
    let backend: Backend
    let onForget: () -> Void

    @State private var enforcing: Bool?
    @State private var grants = 0
    @State private var busy = false
    @State private var note: String?
    @State private var problem: String?

    var body: some View {
        Form {
            Section("电视现在") {
                switch enforcing {
                case .some(true): Label("正在拦截", systemImage: "lock.fill")
                case .some(false): Label("没有拦截", systemImage: "lock.open")
                case .none: Label("读取中…", systemImage: "ellipsis")
                }
                if grants > 0 {
                    Text("有 \(grants) 个解锁还没到期")
                        .font(.footnote).foregroundStyle(.secondary)
                }
            }

            Section("解锁") {
                // Each of these is one Face ID prompt and one nonce. There is no window
                // in which a second tap goes through unchallenged -- that was the whole
                // problem with the five-minute freshness window on the web side.
                ForEach([15, 30, 60], id: \.self) { minutes in
                    Button("解锁 \(minutes) 分钟") { unlock(minutes) }
                        .disabled(busy)
                }
            }

            if enforcing == true {
                Section {
                    Button("关闭拦截", role: .destructive) { setEnforcement(false) }
                        .disabled(busy)
                }
            } else if enforcing == false {
                Section {
                    Button("重新打开拦截") { setEnforcement(true) }
                        .disabled(busy)
                }
            }

            if let note {
                Section { Text(note).foregroundStyle(.green).font(.footnote) }
            }
            if let problem {
                Section { Text(problem).foregroundStyle(.red).font(.footnote) }
            }

            Section {
                Button("注销这台审批手机", role: .destructive, action: onForget)
                Text("注销只是把这台手机上的密钥和令牌删掉。后端那边还要在家长端「移除」"
                     + "一次,而那一步本身也要一次 Face ID 审批。")
                    .font(.footnote).foregroundStyle(.secondary)
            }
        }
        .task { await refresh() }
        .refreshable { await refresh() }
    }

    private func refresh() async {
        do {
            let state = try await backend.state()
            let policy = state["policy"] as? [String: Any]
            enforcing = policy?["enforcementEnabled"] as? Bool
            grants = (state["grants"] as? [Any])?.count ?? 0
            problem = nil
        } catch {
            problem = error.localizedDescription
        }
    }

    private func unlock(_ minutes: Int) {
        act { try await backend.unlock(minutes: minutes, note: "审批 App") }
    }

    private func setEnforcement(_ enabled: Bool) {
        act { try await backend.setEnforcement(enabled) }
    }

    private func act(_ body: @escaping () async throws -> Void) {
        busy = true
        note = nil
        problem = nil
        Task {
            do {
                try await body()
                note = "done"
                await refresh()
            } catch {
                problem = error.localizedDescription
            }
            busy = false
        }
    }
}
