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
| 待审批队列(入队、白名单、哈希绑定、摘要) | ✅ 写完,api 包 58 个测试过,含 `-race` |
| 三个 `.swift` 文件 | ✅ 对 iOS SDK **编译并链接通过**,零错误零警告 —— 详见下表 |
| Xcode 工程 | ✅ 在仓库里,`xcodebuild` 验过能 build,模拟器里起得来 |

Swift 那三个文件验到什么程度(Xcode 27.2,部署目标 iOS 17.0):

| 验法 | 结果 |
|---|---|
| 真机 `arm64-apple-ios17.0`,编译 + 链接 | ✅ 退出 0,零诊断,`_main` 在符号表里 |
| 模拟器 `arm64-apple-ios17.0-simulator`,编译 + 链接 | ✅ 退出 0,零诊断 |
| **Xcode 27 新建工程的那一套编译设置** | ✅ 退出 0,零诊断 —— 见下 |
| Swift 5 语言模式 + `-warnings-as-errors` | ✅ 退出 0,**一条警告都没有** |
| Swift 6 语言模式(严格并发) | ✅ 退出 0 |

链接成功比类型检查说明的多两件事:**框架没漏**(自动链接拉进了 CryptoKit、
LocalAuthentication、Security、SwiftUI、UIKit,正好是该有的那几个),以及 **`@main` 真的展开了**
—— `_main` 出现在符号表里,说明 SwiftUI 的宏插件跑过了。那个插件缺失正是这个文件之前一行都
验不了的原因。

第三条不是我猜的默认值,是从 Xcode 自己的工程模板里抄出来的
(`Base_ProjectSettings.xctemplate/TemplateInfo.plist`):`SWIFT_VERSION = 5.0`、
`SWIFT_APPROACHABLE_CONCURRENCY = YES`、`SWIFT_UPCOMING_FEATURE_MEMBER_IMPORT_VISIBILITY = YES`,
一共展开成七个 `-enable-upcoming-feature`。里面最容易咬人的是 `MemberImportVisibility` ——
界面文件只 `import SwiftUI`,却用了 UIKit 的 `UIDevice`。拿那套设置编了一遍,不咬。

Swift 6 那条是余量:新工程默认仍是 Swift 5 模式,但两种模式都干净,所以以后在 Build Settings
里把语言模式切到 6 也不会炸 —— 界面里到处是 `Task { ... }` 里改 `@State`,要是 SwiftUI 的
`View` 不是 `@MainActor` 隔离的,那些在严格并发下全是数据竞争**错误**而不是警告。它是隔离的。

工程文件本来是我拒绝手写的 —— `project.pbxproj` 手写出来可能有微妙的错,而我当时验不了它。
装上 Xcode 之后那个理由就没了:现在它能 `xcodebuild` 到 `** BUILD SUCCEEDED **`,还能装进
模拟器跑起来。验得了的东西才值得替你写。

## 一、装 Xcode

```bash
open "macappstores://apps.apple.com/app/xcode/id497799835"
```

装完让它接管命令行:

```bash
sudo xcode-select -s /Applications/Xcode.app/Contents/Developer
```

## 二、打开工程

工程已经在仓库里了:

```bash
open approver-ios/BraviaGuardApprover.xcodeproj
```

**不用新建、不用拖文件、不用手加 Info.plist 的键。** 这些都在工程里设好了:

| 设置 | 值 |
|---|---|
| Bundle Identifier | `dev.homelab.braviaguard.approver` |
| Minimum Deployment | iOS 17.0 |
| `INFOPLIST_KEY_NSFaceIDUsageDescription` | 用 Face ID 确认是你本人在下发解锁 |
| Targeted Device Family | 只有 iPhone |
| 源码 | `Sources/` 整个目录同步进来(Xcode 16 的 synchronized group,以后往里加文件不用动工程) |

那条 Face ID 的用途说明是建在**编译设置**里的,不是靠你记得去 Info 标签页加一行 ——
漏了它,第一次调用 Face ID 会直接崩,而且崩在运行时,编译一切正常。

不需要任何 Capability、不需要 App Group、不需要推送。

工程是验过的:

```bash
xcodebuild -project approver-ios/BraviaGuardApprover.xcodeproj \
  -scheme BraviaGuardApprover -destination 'generic/platform=iOS Simulator' build
```

`** BUILD SUCCEEDED **`,零错误零警告。装进 iPhone 17 模拟器也起得来,注册页渲染正常、
手机名自动填好、没设 Face ID 时注册按钮正确地灰着。

> 模拟器只能验到这里。**模拟器没有 Secure Enclave**,密钥生成必然失败,所以注册流程本身
> 只能在真机上验 —— 这正是第五节那几条测试要你亲自跑的原因。

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

### 改规则要走队列

- [ ] 家长端改一条规则 → 保存 → 应该提示**「已提交 — 去审批 App 上确认」**,规则**还没生效**
- [ ] 家长端顶部出现「等待审批」卡片,列着这次改了什么,旁边有「撤回」
- [ ] 审批 App 打开 → 最上面就是「等你批准」,逐条列出改动内容
- [ ] 按「批准」→ 过 Face ID → 规则这时才生效
- [ ] 再改一条 → 在审批 App 上按「驳回」→ **不需要 Face ID**,改动消失,规则没变
- [ ] **删掉一条宵禁**(同时随便加一条别的)→ 摘要里必须**同时**出现「宵禁删除:…」
      和「宵禁新增:…」

> 摘要里必须能看见每一处**放松**规则的改动:关总开关、名单模式改 OFF、白名单加应用、
> 限额调高,以及宵禁被删掉、时间缩短、或者不再管某个应用。少写一条,就等于拿你的批准
> 去盖一个你没看见的改动。如果你发现改了什么但摘要里没列出来,立刻告诉我。
>
> 上面那条「删掉一条宵禁」值得单独跑一次:在 2026-11-06 之前,删宵禁的同时加一条别的,
> 摘要**只会提那条新增的** —— 条数没变,删掉的那条什么都不印。已经修了,这条是用来
> 确认修对了。

### 重点:没有回头路

- [ ] 家长端 → 审批手机 → 对那台手机按「移除」→ **应该被拒**
      (移除审批机本身也要审批,否则删光审批机就是绕过整个机制的办法)
- [ ] 家长端 → 「+ 注册一台审批手机」→ 也应该要审批 App 确认一次

### 重点:加一张脸会毁掉密钥 —— 放到最后跑

- [ ] 设置 → Face ID 与密码 → **设置备用外貌**,随便加一个
- [ ] 回到审批 App 按解锁 → **应该报错「这台手机还没注册成审批手机」**(密钥已被系统销毁)

> 这条验的是 `.biometryCurrentSet`。孩子想「把自己的脸加进去」这条路因此是死路。

**但它同时把你自己也锁在外面,所以放到最后跑。** 只有一台审批手机的时候这是个死锁:
密钥一毁,这台手机就再也签不出签名,而「发一个新的注册码」
(`POST /api/parent/approver-code`)**本身也在门禁后面** —— 它也要一个签名。
删那台手机(`DELETE /api/parent/approvers/{id}`)同样要。于是家长端既发不出新码、
也删不掉死掉的审批机,电视就只剩离线应急码能解锁。

恢复要上 Pi,把那条审批机记录清掉,让门禁重新变回穿透:

```bash
cd ~/braviaguard/server && podman stop braviaguard && jq '.approvers = []' data/state.json > data/state.json.new && mv data/state.json.new data/state.json && podman start braviaguard
```

**必须先停容器。** 进程把状态拿在内存里,运行中改 `state.json`,下一次写入就把你的改动盖掉了。

清完之后照第四节重新注册。要避开这个死锁的办法是**注册两台审批手机**:毁掉一把钥匙,
另一把还签得动,新码就还发得出来。

## 一个有意接受的风险

**离线应急码是手机密码能拿到的。** `showEscapeCode` 把 TOTP 密钥缓存在 `localStorage`,
离线时只靠一次 `navigator.credentials.get` 把门 —— 同一个会退回手机密码的调用。

**这是看过之后决定保留的,不是漏掉的。** 理由:

- 应急码的用途就是「Pi 连不上的时候解锁电视」,而 Pi 连不上的时候队列也用不了。
  两者需要的恰好相反,所以队列堵不住它,也不该堵。
- 它是整套机制**唯一的退路**:审批手机丢了、Face ID 锁定了、或者 Face ID 加了张脸把密钥
  毁了,都只能靠它。
- 这条路径由家长自己管理。

**所以不要「顺手修掉」它。** 真要收紧的话,唯一说得通的形状是:配对时显示一次、之后永不
再显示,家长抄在纸上或存进自己的密码管理器(那有它自己的主密码),同时删掉 `localStorage`
那份缓存。代价是纸丢了退路就没了,只能 ssh 上 Pi 读 `state.json`。那是个要重新决定的取舍,
不是一个待办事项。

## 这个方案不能证明什么

服务端**看不出**密钥到底是不是用那个访问控制建的 —— 它只能验签名,而免费描述文件拿不到
App Attest。防的是「孩子拿起家长手机、输一个他已经知道的密码」,不是「孩子重新编译这个 App
来谎报自己的 keychain 标志」。对家里这个场景,这个边界是对的。
