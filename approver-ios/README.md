# 审批 App(原生 iOS)

这个 App 只做一件网页做不到的事:**下发解锁时必须是真的 Face ID,没有手机密码退路。**

## 为什么需要它

家长端是 PWA,用 passkey 登录。但 iOS 上的 passkey 断言 **Face ID 或手机密码都算通过** ——
WebAuthn 的 `userVerification` 只有 `required / preferred / discouraged`,没有「只要生物识别」
这个选项,回来的 UV 标志也只是一个 bit,不说明用的是哪种。家里孩子知道手机密码,passkey 就
只能当登录凭证。

所以解锁改用第二把钥匙:iPhone Secure Enclave 里的 P-256 密钥,访问控制是
`.biometryCurrentSet`。

| | |
|---|---|
| `.userPresence`(多数示例代码用的) | 生物识别失败**会退回手机密码** —— 等于没解决问题 |
| `.biometryCurrentSet`(这里用的) | **没有密码退路**;而且**往 Face ID 加一张脸,密钥直接失效** |

第二条性质特别值:孩子把自己的脸加进 Face ID,不会获得审批能力,而是**把审批机弄坏** ——
一个响的、可恢复的结果,而不是一个安静的、不可恢复的结果。

## 现在的状态

| 部分 | 状态 |
|---|---|
| 后端(注册、挑战、验签、门禁) | ✅ 写完,18 个测试过,含 `-race` |
| 签名载荷的跨语言契约 | ✅ Swift 产出的黄金值钉在 Go 测试里(空 body / 中文 / 带 id 的路径) |
| `ApproverKey.swift`、`Backend.swift` | ✅ 对 macOS SDK 类型检查通过,零错误零警告 |
| `ApproverApp.swift`(界面) | ⚠️ **一行都没验过** —— `@State` 是 Swift 宏,插件随 Xcode 发布,CLT 里没有 |
| Xcode 工程 | ⬜ 没有,要你在 Xcode 里建(见下) |

工程文件我故意没手写:`project.pbxproj` 是个我无法验证的文件,手写出来可能有微妙的错,
不如你在 Xcode 里点几下,那是对的。

## 一、装 Xcode

```bash
open "macappstores://apps.apple.com/app/xcode/id497799835"
```

装完让它接管命令行:

```bash
sudo xcode-select -s /Applications/Xcode.app/Contents/Developer
```

## 二、建工程

Xcode → File → New → Project → **iOS → App**,然后:

| 设置 | 值 |
|---|---|
| Product Name | `BraviaGuardApprover` |
| Interface | **SwiftUI** |
| Language | **Swift** |
| Storage | None |
| Testing System | None |
| Bundle Identifier | `dev.homelab.braviaguard.approver`(免费账号随便填,但要唯一) |
| Minimum Deployment | **iOS 17.0** 或更高 |

建好之后:

1. 删掉 Xcode 自己生成的 `ContentView.swift` 和 `<名字>App.swift`(本仓库的
   `ApproverApp.swift` 里已经有 `@main`,留着会冲突)。
2. 把 `approver-ios/Sources/` 里的三个 `.swift` 拖进工程,**勾 Copy items if needed 之外
   也可以选 reference** —— 用 reference 的话以后仓库更新了 Xcode 里自动跟着变。
3. Target → Info → 加一条:

   | Key | 值 |
   |---|---|
   | `NSFaceIDUsageDescription` | `用 Face ID 确认是你本人在下发解锁` |

   **这条不加,第一次用 Face ID 会直接崩。**

不需要任何 Capability、不需要 App Group、不需要推送。

## 三、装到自己手机上(免费,7 天)

1. iPhone 用线连上 Mac,手机上「信任这台电脑」。
2. Xcode → Target → Signing & Capabilities:
   - 勾 **Automatically manage signing**
   - Team 选你的 **Personal Team**(用你的 Apple ID 登录 Xcode 就有,不用付 $99)
3. 顶上设备选你的 iPhone,按 ▶︎。
4. 第一次会失败,说开发者不被信任。到 **iPhone → 设置 → 通用 → VPN 与设备管理 →
   开发者 App → 信任**。再按一次 ▶︎。

> 免费账号的描述文件 **7 天过期**,到期 App 打不开,重新按一次 ▶︎ 就行。
> 要管一年的话才需要那 $99。先用免费的把下面的测试跑完,确认值得,再决定。

## 四、注册

1. 手机上家长端 → **审批手机 → + 注册一台审批手机** → 过一次 Face ID → 拿到六位码
   (10 分钟有效,只能用一次)。
2. 审批 App 里填后端地址和这个码 → 注册。
   - 注册时会要一次 Face ID:App 要用新建的密钥给这个码签名,证明私钥真在自己手上。

**注册成功的那一刻,家长端的解锁按钮就会开始报
「这一步要在审批 App 上用 Face ID 确认」。这是对的,不是坏了。**

## 五、你要跑的测试

前四条是功能,**后三条才是重点** —— 它们验的是这整件事的前提。

### 功能

- [ ] 审批 App 里按「解锁 30 分钟」→ 过 Face ID → 电视应该**一秒内**解锁
      (长轮询已经上线,实测 123 ms)
- [ ] Pi 上应该同时出现两行:
      ```
      approved by <审批机id>: POST /api/parent/grants
      sync: pushed to a held request after <N>s
      ```
- [ ] 连按两次解锁 → **应该弹两次 Face ID**。网页端那个「一次验证管 5 分钟」的窗口
      在这里不存在,一次人脸只换一次审批
- [ ] 家长端点解锁 → 应该被拒,提示去审批 App

### 重点:没有密码退路

- [ ] 按「解锁 30 分钟」,然后**用手遮住摄像头**(或者让别人的脸对着)
- [ ] **Face ID 失败时,屏幕上绝对不能出现「输入密码」/「Enter Passcode」**
- [ ] 连失败 3 次 → 会进 Face ID 锁定(`biometryLockout`),App 里**依然没有**密码入口

> 这一条是整个方案成不成立的唯一判据。如果这里出现了密码入口,说明访问控制标志写错了,
> 立刻告诉我。

### 重点:加一张脸会毁掉密钥

- [ ] 设置 → Face ID 与密码 → **设置备用外貌**,随便加一个
- [ ] 回到审批 App 按解锁 → **应该报错说这台手机没注册**(密钥已被系统销毁)
- [ ] 重新走一遍第四节注册流程就能恢复

> 这条验的是 `.biometryCurrentSet`。孩子想「把自己的脸加进去」这条路因此是死路。

### 重点:没有回头路

- [ ] 家长端 → 审批手机 → 对那台手机按「移除」→ **应该被拒**
      (移除审批机本身也要审批,否则删光审批机就是绕过整个机制的办法)
- [ ] 家长端 → 「+ 注册一台审批手机」→ 也应该要审批 App 确认一次

## 还没关的两个洞

说清楚,别以为装完就密不透风了。

**一、`PUT /api/parent/policy` 没加门禁。** 它是整份覆盖策略(`st.Policy = incoming`),
`enforcementEnabled` 也在里面 —— 也就是说知道手机密码的人打开家长端改规则,照样能把拦截
关掉。要堵这个得让家长端能把一次改动**提交给审批 App 确认**,那是个 UX 设计决定,不是
一行代码。

**二、离线应急码还是手机密码能拿到的。** `showEscapeCode` 把 TOTP 密钥缓存在
`localStorage`,离线时只靠一次 `navigator.credentials.get` 把门 —— 同一个会退回密码的调用。
孩子进去一次就能离线、永久地生成电视解锁码。

这两个一起考虑的话,比较完整的下一步是**一个「待审批改动」队列**:家长端提交任何受控写入,
审批 App 列出来、显示改动内容、Face ID 批准。一个机制同时覆盖策略、应急码和以后新加的东西,
而且天然留下审计记录。

## 这个方案不能证明什么

服务端**看不出**密钥到底是不是用那个访问控制建的 —— 它只能验签名,而免费描述文件拿不到
App Attest。防的是「孩子拿起家长手机、输一个他已经知道的密码」,不是「孩子重新编译这个 App
来谎报自己的 keychain 标志」。对家里这个场景,这个边界是对的。
