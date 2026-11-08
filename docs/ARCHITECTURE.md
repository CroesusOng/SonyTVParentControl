# 架构与取舍

四个组件,一条数据流:

```
┌─────────────────────┐        bearer token          ┌──────────────────────┐
│  Sony BRAVIA        │  ─── GET  /api/device/sync ──▶│  Raspberry Pi 5      │
│  Android TV / Kotlin│  ─── POST /api/device/usage ─▶│  Go + podman         │
│                     │                               │                      │
│  AccessibilitySvc   │◀── policy / grants ───────────│  state.json          │
│  规则引擎(本地判定) │                               │  usage.json          │
│  TYPE_ACCESSIBILITY │                               │  lastseen.json       │
│  _OVERLAY 拦截层     │                               └────┬──────────┬──────┘
└─────────────────────┘                   passkey session  │          │ bearer + 签名
                                                           │          │
         ▲                                       ┌─────────▼───┐  ┌───▼──────────────┐
         │  离线应急码(TOTP,不走网络)            │ iPhone      │  │ 审批 App         │
         └───────────────────────────────────────│ PWA+Face ID │  │ 原生 iOS         │
                                                 │ 看/改规则/  │  │ Secure Enclave   │
                                                 │ 配对        │  │ 发放时间/批准     │
                                                 └─────────────┘  └──────────────────┘
```

**判定在电视上做,不在 Pi 上做。** Pi 只是规则和授权的存放处。Pi 挂了、网断了、
WiFi 改了密码,电视照样按本地缓存的规则工作。这是整个设计里最重要的一条。

**发放电视时间要的是右下角那个 App,不是 PWA。** iOS 上的 passkey 断言 Face ID 和手机
密码都算通过,所以 PWA 只够拿来登录、看报表和编辑规则;要真正强制「本人」,得用 Secure
Enclave,而那只能是原生 App。两者的分工和理由见第 11 节。

---

## 1. 为什么用 AccessibilityService,而不是 Device Owner

| | AccessibilityService(本项目) | Device Owner |
|---|---|---|
| 安装 | `adb install`,设置里打开开关 | **必须恢复出厂**,跳过账号登录后 `adb shell dpm set-device-owner` |
| 拦截力度 | 盖一层全屏遮罩 + 踢回主界面 | `setPackagesSuspended()` 真正从系统层禁用 |
| 能被绕过吗 | **能** —— 进设置把无障碍关掉 | 基本不能,还能锁住设置项 |
| 代价 | 无 | 清掉电视上现有的全部账号和已装 app |

选前者,因为**恢复出厂这个代价对一台全家在用的电视来说太大了**:妈妈的
Netflix 登录、所有装过的 app、首页布局全部没了,为了一个家长控制 app 不值得。

被绕过这件事的缓解手段,而不是假装它不存在:

- 电视首页会直接显示「规则执行已关闭」
- Pi 那边会看到这台电视**停止签到**(`/api/parent/state` 里的 `online` 变 false),
  家长端顶部会显示 `0/1 台电视在线`
- 无障碍设置页本身不在豁免名单里 —— 但它必须可达,否则家长自己也关不掉这个 app

如果孩子大到会绕,**升级路线是换成 Device Owner**:规则引擎
(`core-rules`)、协议层、后端、家长端一行都不用改,只要把 `EnforcementController`
里 `block()` 的实现从「遮罩 + 踢回主界面」换成 `DevicePolicyManager.setPackagesSuspended()`。
这也是把判定逻辑做成纯 Kotlin 模块的一个附带好处。

**纯 UsageStats 方案**(只统计不拦截)没选,因为用户明确要「时段限制」和「限额」,
那就必须有强制手段。但 `UsageStatsManager` 还是用上了 —— 见第 5 节。

## 2. 拦截层:accessibility overlay,不是 Activity,不是 Compose

**为什么是 overlay 而不是启动一个 Activity:** Android 10 开始有
[background activity launch 限制](https://developer.android.com/guide/components/activities/background-starts),
后台启 Activity 会被静默丢掉。`TYPE_ACCESSIBILITY_OVERLAY` 是无障碍服务可以无条件添加的
窗口类型,不需要 `SYSTEM_ALERT_WINDOW`,而且一定画在最上层。

**为什么 overlay 里用原生 View 而不是 Compose:** 在 `WindowManager` 窗口里跑 Compose
需要手工提供 `ViewTreeLifecycleOwner` / `SavedStateRegistryOwner`,配错了的表现是**一片
空白** —— 恰好在该显示拦截提示的时候。这个失败模式的代价太高,所以拦截层用
`LinearLayout` + `TextView` 写死。Compose for TV 用在家长面板那个 Activity 上,
那里有正常的 Activity 宿主,风险为零。

**解锁码为什么是上下键选数字:** 无障碍 overlay 窗口里**唤不出输入法**。所以
`DigitPickerView` 自己处理按键:上下改数字、左右换位、OK 确认,不依赖系统任何东西。

**遮罩是提示,不是护盾。** 真正起作用的是每次被拦的 app 到前台就
`GLOBAL_ACTION_HOME` 踢回主界面;遮罩 90 秒后自动消失,否则没人看的电视会一直黑屏。

## 3. 没有前台服务

`AccessibilityService` 本身就是系统绑定的长生命周期服务,**不受后台执行限制**。
所以同步循环就跑在它的生命周期里,于是完全绕开了:

- Android 14 的 `foregroundServiceType` 声明要求
- Android 15 对 `dataSync` 类型每天 6 小时的上限
- 电视上根本看不到的通知栏图标

无障碍服务没开的时候,`WorkManager` 的 15 分钟周期任务兜底(这是 WorkManager 的最小周期)。

## 4. 长轮询,不是 SSE / WebSocket

电视是插电的,省电这个理由不成立。真正的理由是重连:长连接要维护待机/唤醒的重连状态机,
一台电视每天要过好几次这个循环,而重连逻辑是最容易写错的地方。

所以推送是靠**把一个普通的 GET 挂住**:`/api/device/sync?wait=20`。电视已经是最新的时候
后端不回,一直挂到有家长改动、挂满 20 秒、或者连接断掉为止。这样既有推送的延迟,又保住了
「每个响应都只是个响应」—— 挂着的请求断了就是一个失败的请求,重试即可,没有状态机。

怎么判断「已经是最新」:`store.State.Revision`,电视把上次看到的值带回来。光比 policy
版本号不够,因为授权(grant)没有版本号。注意是比**不等**而不是比大小,所以 state.json 被
重置、revision 归零时每台电视照样会立刻收到新状态,而不是卡死。

哪些写入该广播,是**算出来的**而不是列出来的:`Store.deviceFacingLocked()` 对「电视会收到的
那部分状态」(policy + grants + escape secret)做哈希,写入前后比一次,不一样才自增 revision
并广播。这样以后新加的 handler 只要改了规则就自动会叫醒电视,不用记得去加一行;而只动会话、
挑战码、拦截历史的写入永远不会 —— `/api/parent/login/begin` 是公开接口,如果每次 `Write`
都广播,任何能打开登录页的人都能让电视一直重新同步。这个哈希要覆盖 `api.syncResponse` 读的
字段,不多不少 —— 那边加字段,这边也要加。

授权**到期**故意不推。到期的时候后端什么都没写,没有 revision 可以自增;而电视一开始就知道
每个授权的到期时刻,靠 `RuleEngine.nextBoundary()` 自己锁回去。

长轮询之下仍然垫着原来的自适应轮询,间隔由状态决定:

| 状态 | 间隔 |
|---|---|
| 长轮询连接正常 | **后端一写就到**(一个局域网往返) |
| 正显示拦截遮罩 | 3 秒(维持遮罩上的倒计时,也是长连接断掉时的兜底) |
| 规则生效中、有 app 在前台 | 30 秒 |
| 规则暂停 | 120 秒 |
| 无障碍服务关闭 | 15 分钟(WorkManager) |

两个循环分开跑(`EnforcementController.loop` 和 `watchLoop`),理由有两条:tick 循环还要
驱动遮罩倒计时和记用量,不能被挂起 20 秒;分开也意味着待机时挂着的连接断掉,只是退回原来的
轮询行为,而不是停止拦截。tick 循环的睡眠是**可打断**的 —— 长轮询拿到消息、或者有 app 被拦,
都会立刻把它叫起来,不用等睡满。

`RuleEngine.nextBoundary()` 会算出「下一个可能改变判定结果的时刻」—— 宵禁开始、配额耗尽、
授权到期、本地零点 —— 哪个近就睡到哪个。所以配额是**用完的那一秒**就拦,而不是等到下次轮询。

一个坑:Go 的读/写 deadline 是从读到请求头时开始算的,不是从 handler 返回时算。所以
`http.Server` 的 `ReadTimeout` / `WriteTimeout` 都是从 `api.MaxSyncWait` 推出来的,不能
各改一边 —— 否则长轮询会在正常工作的时候死于 i/o timeout。`ReadHeaderTimeout` 保持短,
那条才是用来挡慢速发头的客户端的。

## 5. 两个用量来源,一套账

- **主来源:** AccessibilityService 自己记前台会话。不需要额外权限,enforcement
  靠它,所以权限没给也能拦。
- **补来源:** `UsageStatsManager`(需要 `PACKAGE_USAGE_STATS`)。补无障碍服务关闭期间的
  空档,以及让报表能看到开启执行之前的历史。

关键是 `UsageStatsReader` **重建的是 session 而不是读 UsageStats 的总时长**,
然后和实时记录走同一个 `UsageLedger`。两个来源对「一天从哪里开始」的理解必须一致,
否则比一个不完美的来源更糟。

跨零点的会话会被 `UsageLedger.attribute()` 切开 —— 不切的话 23:40 开始看到 00:20
会全记到前一天,等于给了一个躲开每日限额的办法。

## 6. DataStore,不是 Room

整个工作集是:一份规则文档、几条生效中的授权、一个 `日期 → 包名 → 秒数` 的 map
(保留 90 天)。几 KB,而且**每次都是整体读写**。Room 要加 KSP 处理器、schema 迁移和
一层 DAO,换不回任何东西。

秘密单独放:`SecretStore` 用 AndroidKeyStore 里的 AES-256-GCM 密钥加密
SharedPreferences 里的两个值(device token 和应急码密钥)。没用
`androidx.security:security-crypto`,因为它的 stable 版停在 1.0.0、之后全是 alpha;
这 60 行是 Keystore 的标准用法,没有会腐烂的依赖。

## 7. `core-rules` 是纯 JVM 模块

整条判定路径 —— 名单、宵禁、三种配额、授权、离线行为 —— 都在一个**不碰任何 Android
API** 的模块里。好处很直接:

```bash
./gradlew :core-rules:test     # 87 个测试,几百毫秒,不需要模拟器
```

`RuleEngine.evaluate()` 是纯函数:时间、策略、用量、授权全靠参数传入。所以
「周五 21:30 到周六 06:30 的宵禁,在周六 03:00 该不该拦」这种问题是一行断言,
而不是一次手动测试。

判定优先级(有测试锁住):**豁免 → 总开关 → 离线宽限 → 授权 → 名单 → 宵禁 → 配额**。

## 8. 离线应急码

用户选的离线策略是「继续按缓存规则执行」,那就必须有一条不走网络的解锁路径,
否则 Pi 一挂,妈妈就被锁在外面了。

机制是一个共享的 TOTP 密钥(HMAC-SHA1 / 6 位 / 30 秒):

- Pi 生成,配对时下发给电视,家长端 Face ID 后缓存到 `localStorage`
- 手机离线算码,电视离线验码,**两边都不需要网络**
- 容忍 ±2 个步长(±60 秒),因为刚断电的 BRAVIA 在 NTP 同步前时钟是错的,
  而「手机上显示的码电视不认」是这里最糟的失败模式
- 用过的 counter 会记下来,同一个码不能用第二次

**为什么是 SHA-1 而不是 SHA-256:** 这样密钥可以直接导入任何一个标准
authenticator app —— 家长手机丢了的备用方案。

这套东西有**三份实现**:`core-rules/Totp.kt`、`server/internal/totp`、`web/app.js`。
三份都测 RFC 6238 的官方向量 —— 那些数字就是它们之间的契约。

## 9. 后端:Go + JSON 文件

一家人的数据量用不到数据库。文件的好处是 Pi 上不用跑也不用备份一个数据库,
而且**机器的主人能用 `jq` 直接看和改自己的数据**:

```bash
jq '.policy.curfews' /var/lib/braviaguard/state.json
```

写入是原子的(临时文件 + `rename`),断电只会留下上一个完好的文件。
写入分两档:家长改的东西立刻落盘;电视每几秒一次的签到和用量上报攒到 60 秒
flush 一次 —— 否则 SD 卡要被白写坏。

成品是 `FROM scratch` 的单文件镜像,8.7 MB,没有 shell、没有包管理器、没有 libc。

## 10. 家长端:PWA + passkey(外加一个很小的原生 App)

用户全家是 iPhone,要 Face ID。两条路:

| | PWA + WebAuthn(本项目) | 原生 iOS app |
|---|---|---|
| Face ID | WebAuthn platform authenticator,UV required | `LocalAuthentication` |
| 装到手机上 | Safari「添加到主屏幕」 | Xcode 15 GB + 免费证书 7 天过期 |
| 妈妈的手机 | 打开网址,注册一个 passkey | 要接电脑重新推包 |
| 代价 | **必须 HTTPS**(见 [TLS.md](TLS.md)) | 无,局域网 HTTP 就行 |

选 PWA,因为「妈妈也要能随时打开」这个需求下,**每 7 天给两台手机重新签包**是个
长期负担,而配一次证书是一次性的。

**这个结论后来只对了一半。** 家长端还是 PWA,但另外有一个**很小的原生 App**,只做一件
WebAuthn 做不到的事:真正强制 Face ID。见第 11 节。两者互补,不是替代 —— 规则编辑、
用量、历史、配对全在 PWA,原生 App 里只有那几个「必须是本人」的动作。那个 7 天重签的
代价因此只落在**一台**手机上,而不是每台。

**会话策略** —— 这是把「不要密码」做成能用的东西的关键:

- **读**(看状态、用量、报表):会话 cookie,7 天
- **写**(配对电视、看应急密钥、删授权、驳回待审批):要求 **5 分钟内有过一次 assertion**
- **发放电视时间**(解锁、总开关)和**改规则**:注册了审批手机之后,这两类不再由
  assertion 决定 —— 见第 11 节

服务端在 assertion 过期时返回 `reauth_required` 而不是报错,前端自动弹 Face ID 并
重放请求。结果就是:看一眼不用验,点「解锁 30 分钟」要过一次 Face ID,
连点几下不会反复弹。

那个 **5 分钟窗口**值得单独说一句,因为它是第 11 节存在的一半理由:过一次验证之后,
窗口内**所有**写入都免提示。对「配对一台电视」这种操作这是合适的权衡;对「发放电视
时间」不是。所以第 11 节那条路上没有窗口,一次人脸只换一次审批。

## 11. 批准:Secure Enclave 签名,不是 passkey

第 10 节那套会话策略有个洞,而且是 WebAuthn 自己的洞:**iOS 上的 passkey 断言,Face ID
和手机密码都算通过。** `userVerification` 只有 `required / preferred / discouraged`,
没有「只要生物识别」这个选项;回来的 UV 标志是**一个 bit**,不说明用了哪种。规范里确实
有个 `uvm` 扩展能报告模态,但 Safari 不实现,而且它只是**报告**,不是**约束**。

家里孩子知道手机密码,passkey 就只能当登录凭证。

所以「发放电视时间」改用第二把钥匙:iPhone Secure Enclave 里的 P-256 密钥,访问控制是
`.biometryCurrentSet`。

| | `.userPresence`(多数示例代码用的) | `.biometryCurrentSet`(本项目) |
|---|---|---|
| 生物识别失败 | **退回手机密码** | **没有退路** |
| 往 Face ID 加一张脸 | 密钥照用 | **密钥立刻失效** |

第二行那个性质是白捡的:孩子把自己的脸加进 Face ID,不会获得审批能力,而是**把审批机
弄坏** —— 一个响的、可恢复的结果,而不是一个安静的、不可恢复的结果。

**签名覆盖整个请求**,不只是随机数:

```
braviaguard/approval/v1 \n 随机数 \n 方法 \n 路径 \n sha256(body)
```

所以为「30 分钟」签的名搬不到「1440 分钟」上,为解锁签的名也递不到总开关那边。Swift
和 Go 两侧的载荷构造由 `TestApprovalPayloadMatchesTheiOSApp` 用黄金值钉住 —— 差一个
换行,现场症状只有「签名验不过」,没有任何线索指向是哪一边错了。

随机数服务端签发、一次性,而且**验失败也烧掉**:一次人脸只换一次审批,不给重试空间。

### 为什么改规则要走队列

规则编辑器在家长端,也该在家长端 —— 审批 App 没理由长出一个。但 `handlePutPolicy` 是
**整份覆盖**策略(`st.Policy = incoming`),`enforcementEnabled` 也在里面。把它留在
`parentWrite` 上,那只守住解锁按钮就是摆设:知道手机密码的人打开规则编辑器就能关掉拦截。

所以改规则**入队**:家长端提交,拿到 202 和一个 id,什么都没发生;审批 App 列出等待的
改动、逐条显示改了什么,Face ID 批准之后才真的执行。

三件事让它不只是「绕了一圈」:

- **`approvableAction` 是代码里的白名单。** 队列存的是 method + path + body —— 那正好
  是「任意请求」的形状,白名单就是让它不是任意请求的那个东西。手工往 `state.json` 塞
  一条指向 `/api/parent/enforcement` 的待审批项,批准时会被拒,有测试。
- **摘要在入队时算,拿当时的状态做 diff。** 每一处**放松**规则的改动都必须出现在摘要里
  (关总开关、名单模式改 OFF、白名单加应用、限额调高、宵禁减少)。少写一条,就等于拿
  家长的批准去盖一个他没看见的改动。
- **签名覆盖内容哈希。** 批准 A 不可能执行 B。「塞一个无害的加一个恶意的,指望第二个
  搭车」也有测试。

驳回**不需要** Face ID,两个 App 里都是:它是不改变任何东西的方向,而一个难清空的队列
会变成大家不看就批的队列。

### 两个刻意选的性质

**注册第一台审批机之前,所有门禁穿透回退到第 10 节那套。** 部署这套东西本身什么都不改,
不可能把家里锁在外面。这一半同样有测试 —— 最该怕的失败是「给一个没要求过它的家庭换了
行为」。

**注册之后没有回头路。** 发注册码和移除审批机自己也在门禁里,否则绕过办法就是
「把审批机全删了」。

### 这套东西证明不了什么

服务端**看不出**那把密钥到底是不是用那个访问控制建的:它只能验签名,而免费描述文件拿不
到 App Attest。防的是「孩子拿起家长的手机、输一个他已经知道的密码」,不是「孩子重新编译
这个 App 来谎报自己的 keychain 标志」。对家里这个场景,这个边界是对的。

## 12. 一些小决定

- **mDNS 用 Pi 上现成的 avahi**,不在 Go 里引 mDNS 库。电视用 `NsdManager` 发现,
  省掉用遥控器拼 URL 这件苦事。
- **device token 只存 SHA-256**。`state.json` 被看到也拿不到能用的设备身份。
- **policy version 由服务端分配**。客户端要是能自己填版本号,就能让所有电视
  永远停在一个旧规则上。
- **`validatePolicy` 在服务端严格校验**。电视是**信任**这份文档的,一个 `"25:00"`
  会让 Kotlin 侧解析宵禁时抛异常,而解析不了规则的电视就是不再执行规则的电视。
- **只用 `androidx.tv:tv-material`,不用 `tv-foundation`**。后者在 1.0.0-alpha12
  已废弃,lazy list 并回了 `androidx.compose.foundation`。
- **minSdk 26**,因为规则引擎用 `java.time`,API 26 起原生可用,不需要
  core library desugaring。覆盖 2017 年以后的 BRAVIA。
- **电视端界面是只读的**,所以**不需要 PIN**:那里没有任何能放松限制的操作,
  密码也就没有保护对象。唯一的例外是「解除配对」,它要求输一个应急码。

---

## 这套东西防得住什么,防不住什么

**防得住:**
- 按时段、按总时长、按单个 app 时长、按名单限制使用
- 孩子自己卸载这个 app(需要设置里的权限,不过见下)
- Pi 挂掉或者断网(继续按缓存规则执行)
- 改电视时钟往前拨(配额是累计用量,不是看当前时间;宵禁会受影响)
- **拿到家长解锁着的手机 —— 前提是注册了审批手机。** 发放电视时间和改规则都要一个
  Secure Enclave 签名,而手机密码到不了 Secure Enclave(第 11 节)

**防不住,也没打算防:**
- **进设置关掉无障碍。** 这是选这条路线的已知代价,缓解靠「电视上显示状态 +
  家长端显示离线」。真要堵就上 Device Owner(第 1 节)。
- **`adb uninstall`。** 所以 [SIDELOAD.md](SIDELOAD.md) 最后让你把网络调试关掉。
- **恢复出厂设置。**
- **没注册审批手机的话,拿到解锁着的手机就什么都能做。** 门禁在没有审批机时是穿透的
  (第 11 节),这是刻意的 —— 但也就是说,这一条防不防得住**取决于你有没有注册**。
- **拿到解锁着的手机 + 离线应急码。** 应急码的密钥缓存在 `localStorage`,离线出码只靠
  一次会退回手机密码的断言。这是**看过之后决定保留的**,不是漏的:它是整套机制唯一的
  退路(审批手机丢了、Face ID 锁定、或者加了张脸把密钥毁了,都只能靠它),而它需要生效
  的场景恰好是队列用不了的场景 —— 队列要 Pi,应急码正是为「Pi 没了」准备的。
  详见 [../approver-ios/README.md](../approver-ios/README.md)。
- **在 Pi 上有 shell 的人。** `state.json` 是明文的,看得到 TOTP 密钥。
