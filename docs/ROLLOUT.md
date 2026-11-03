# 上线计划

从「代码写完了」到「客厅里真的在跑」。总共大概 1.5 小时动手时间,
建议分 2-3 天做完,中间留观察期。

**标注:** 🧑 你做(需要遥控器 / sudo / DNS / iPhone) · 🤖 我做(给我 ssh 就行)

**现在到哪了:** 阶段 0 ✅ · 阶段 2 ✅ · 阶段 3 ✅ ——
后端在 Pi 上跑着、域名通了、电视端的测试入口也加好了。
**下一步是阶段 1**:把 APK 装到电视上,按那个按钮,看遮罩和遥控器行不行。
这是整个项目的 go / no-go 点,也是唯一还完全没验过的一层。

---

## 先说两件不可逆的事

**1. `BG_ORIGINS` 一旦注册过 passkey 就不能改。**
WebAuthn 把每个凭据绑死在一个 origin 上。换域名、换端口、从 `https://` 改成别的,
已注册的 passkey 全部作废,要删 `state.json` 里的 `parents` 重新注册。
**所以域名必须在第一次注册 passkey 之前定死** —— 这是整个计划的排序依据。

**2. 真·拦截生效那一刻,全家都会受影响。**
这台电视妈妈也在用。在打开任何限制之前先跟家里说一声,并且让妈妈在她手机上
装好家长端、自己试过一次解锁。否则第一次拦截会变成一个你必须立刻到场处理的事故。

另外有一个顺序陷阱:**配对码接口 `POST /api/parent/pairing-code` 要 Face ID**。
也就是说「先用 HTTP 跑通电视端、HTTPS 以后再说」这条路走不通 ——
没 HTTPS 就没 Face ID,没 Face ID 就拿不到配对码。所以域名/隧道排在配对前面。

---

## 阶段 0 — 让电视端能单独验证 ✅ 已完成

**为什么要有这个:** 整个项目风险最高的两处(accessibility overlay 能不能真的盖住
正在播放的视频、能不能抢到 D-pad 焦点)都在拦截层,而它们**不需要后端也能验**。
没有这个按钮,第一次知道答案就得等到阶段 4 —— 后端、域名、配对全做完之后。

落地的东西:

- `EnforcementController.previewBlockOverlay()` 拿假数据直接调 `overlay.show()`。
  **不触发 `GLOBAL_ACTION_HOME`**(否则一按按钮就被踢回主屏,看不到结果),
  **不上报给 Pi**(否则家长端的拦截历史里会多一条假记录)。
  返回 `false` 表示无障碍服务没开 —— 这是「按了没反应」最常见的原因,
  而从这个界面上本来是看不出来的。
- 面板上一个 `if (BuildConfig.DEBUG)` 的「测试拦截遮罩」按钮,
  **配对页和已配对面板上都有**。配对页上的那个才是关键:阶段 1 的时候电视还没配对,
  看到的就是配对页。
- 一个 `previewing` 标志,防止轮询循环三秒后把遮罩自己收掉
  (没有真的拦截 → 下一次判定是 Allow → Allow 会 hide)。真的拦截发生时会接管。

release 版里 R8 会把 `BuildConfig.DEBUG` 折成 false,整个分支都被丢掉。

---

## 阶段 1 — 电视上装包并验证拦截层 🧑 ~20 分钟 ← **下一步**

照 [SIDELOAD.md](SIDELOAD.md) 走。概括:

- [ ] 电视:关于 → 版本号连按 7 次 → 开 **网络调试**
- [ ] `source scripts/env.sh && adb connect <电视IP>:5555`
      —— **电视上会弹授权框,用遥控器勾「一直允许」**(最容易卡在这)
- [ ] `./gradlew assembleDebug && adb install -r app/build/outputs/apk/debug/app-debug.apk`
- [ ] 电视:设置 → 设备偏好设置 → 无障碍 → 电视守护 → 打开
- [ ] 打开 app(还没配对,看到的是配对页),按**调试工具 → 测试拦截遮罩**

**成功标准:**
- 遮罩全屏出现,盖住了下面的内容
- 遥控器上下键能改数字,左右键能换位置
- 按 OK 有反馈(未配对时会显示「这台电视还没配对,无法校验解锁码」—— 这是对的)
- 按返回键遮罩消失

**更狠的一次验证(建议做):** 先打开 YouTube 或 Netflix 放一段视频,
再按 HOME 回到电视守护按那个按钮 —— 真正要确认的是遮罩能盖住**正在播放的视频**,
而不只是盖住一个静止的界面。

按了完全没反应、并且下面出现红字「没反应:先去 设置 → …」,
说明无障碍服务没打开,回上一步。

**这是整个项目的 go / no-go 点。** 这步过了,后面都是配置工作。
过不了的话,`app/src/main/kotlin/dev/homelab/braviaguard/enforce/BlockOverlay.kt`
的窗口参数需要按真机表现调,告诉我现象我来改。

**要重来:** `adb shell pm clear dev.homelab.braviaguard.debug` 把 app 数据清空。

---

## 阶段 2 — 把域名接到 Cloudflare Tunnel ✅ 已完成

用隧道的话这一步比签证书简单得多:**不用证书、不用续期、不用端口转发、不用 A 记录**。
细节和坑在 [TLS.md](TLS.md) 方案一。

- [ ] Pi 上跑 `cloudflared`
- [ ] Zero Trust → Networks → Tunnels → 加一条 Public Hostname:

  | 字段 | 值 |
  |---|---|
  | Subdomain | `tv` |
  | Domain | 你的域名 |
  | Service Type | `HTTP` |
  | URL | `localhost:8443` |

- [ ] 确认这个 hostname 上**没有 Cloudflare Access 策略**(或者设成 bypass)
- [ ] 记下 **Pi 的内网 IP** —— 电视要用,它不走隧道

**三个必须记住的点:**

1. **`BG_ORIGINS` 不带端口** —— Cloudflare 在 443 上服务,浏览器发的 Origin 就是
   `https://tv.你的域名`。带了 `:8443` 所有 passkey 都认证失败。
2. **电视直连内网 IP,不走隧道** —— 否则宽带一断,明明在同一个交换机上的电视就连不上 Pi。
3. **家长端现在暴露在公网了** —— 挡在前面的只有 passkey。好处是出门也能解锁。

**成功标准:** 后端起来之后(阶段 3),手机上 `https://tv.你的域名` 能打开且没有证书警告。

**实测通过的项(阶段 3 之后复验的):** HTTPS 200 / HTTP2、PWA 全部资源、
manifest 的 Content-Type 是 `application/manifest+json`(装得成 app 而不是书签)、
Service Worker 注册成功且整个 shell 已缓存、WebAuthn 的 `rp.id` 等于域名、
`isSecureContext` 为 true、`isUserVerifyingPlatformAuthenticatorAvailable()` 为 true
(Face ID 的前提条件)、hostname 上没有 Access 拦截。

**两个只有上了 CDN 才会出现、已经修掉的坑:**

1. **明文 `http://` 也能打开整个 app。** WebAuthn 要 secure context,
   所以注册按钮会**一声不响地什么都不做**;而 session cookie 带 `Secure`,
   就算登进去了下一个请求也会被浏览器丢掉。Safari 只要有一次落在 http:// 上拿到 200
   就可能停在那。现在 `api.RequireHTTPS` 会把 `X-Forwarded-Proto: http` 308 跳到
   规范 origin —— **只认这一个精确条件**,因为电视是直连内网没有这个头,
   把电视弹到公网 https 上去会在宽带断掉时恰好废掉拦截。
2. **Cloudflare 把 `app.js` / `styles.css` 缓存了 4 小时。** 源站原来只给 HTML 设了
   `no-store`,剩下的 CDN 按自己默认填,结果是新的 `index.html` 配旧的 `app.js` ——
   半新半旧的版本在手机上跑,从手机上根本没法 debug。现在整个 shell 都 `no-store`。

顺手记一条:**隧道那个 `cloudflared` 可能不是 systemd 服务。** 如果
`systemctl is-active cloudflared` 说 inactive、`~/.cloudflared` 是空的,
不等于隧道没配 —— 先 `pgrep -af cloudflared` 看看,它可能是个 token 模式的容器。

---

## 阶段 3 — 后端上 Pi ✅ 已完成

实际落地的方式(和原计划有出入的地方都在这):

- [x] **在 Pi 上直接构建**,没有交叉编译 —— 仓库 clone 到 `~/braviaguard`,
      以后 `git pull` 就能更新
- [x] **podman-compose,不是 Quadlet** —— 这台机器的 podman 是 4.3.1,
      Quadlet 要 4.4+,所以 `deploy/braviaguard.container` 在这里根本用不了。
      改成了这台机器其它 stack 在用的 `compose.yaml` + 用户 systemd unit 的模式
- [x] `~/braviaguard/server/.env`(从 `braviaguard.env.example` 复制)——
      **不在 git 里**,域名和时区在这
- [x] avahi service,电视能自动发现。**`scheme` 必须是 `http`** ——
      隧道方案下 Pi 本地跑的是明文 HTTP,写 `https` 电视会连不上,
      而且失败得像「Pi 挂了」
- [x] `systemctl --user enable` + `Linger=yes`,重启自动起来

**成功标准(都过了):**
- Pi 上 `curl http://localhost:8443/healthz` 返回 `ok`
- 启动日志里有 `serving plain HTTP behind a TLS terminator`(说明它认出了隧道)
- iPhone Safari 打开 `https://tv.你的域名` 看到登录页,**地址栏没有证书警告**

**做这一步时发现并修掉的一个会把你锁死的 bug:**
`BeginRegistration` 必须在 Face ID 弹窗**之前**建好 parent 记录
(WebAuthn 要 user handle 才能生成 challenge),而 `HasParents()` 原来只数记录条数。
于是「点注册 → 把 Face ID 弹窗划掉」就会让 `needsSetup` 变成 false,
setup code 从此不被接受,而又没有任何凭据能登录 —— 只能手改 `state.json`。
划掉弹窗是个再正常不过的操作。现在「没有凭据的 parent 不算数」,重试时还会把残留清掉。

---

## 阶段 4 — 注册 passkey,配对电视 🧑 ~10 分钟(阶段 1 过了就能做)

- [ ] iPhone Safari 打开 `https://tv.你的域名` —— **不带端口**,见阶段 2 第 1 条
- [ ] 填名字 + SETUP CODE → **Face ID 注册**
      —— setup code **只对当前这次进程有效**,重启会换一个,
      要重新拿:`podman logs braviaguard | grep "SETUP CODE"`
- [ ] **分享 → 添加到主屏幕**(这步别跳,否则每次都要在 Safari 里翻)
- [ ] **妈妈的手机同样做一遍** —— 同一个网址 →「这台手机还没注册过」→
      先用你的 Face ID 授权,再给她注册一个 passkey
- [ ] 家长端 →「电视」→「+ 添加一台电视」→ 拿到 6 位配对码
- [ ] 电视上打开 app:地址应该**自动出现在列表里**(avahi 生效的话),选中,输配对码

**成功标准:** 家长端顶部显示「1/1 台电视在线」,电视面板显示已配对。

---

## 阶段 5 — 渐进放开,别一次全开 🧑 2-3 天 + 每步验证

这是最容易被跳过、但最该做的一段。

**第 1-3 天:只看不管。** 保持总开关**关着**,让它纯收集数据。
- [ ] 三天后对一下:家长端显示的时长和你实际观察的差得多吗?
- [ ] 各个 app 的名字显示对不对(白名单要靠这个选)

**然后:演练一次断电。** 在真正依赖应急码之前先确认它能用。
- [ ] 手机上「用 Face ID 显示应急码」,确认能出码
- [ ] 把后端停掉:`systemctl --user stop braviaguard`
- [ ] 等 1-2 分钟,电视面板应该显示「连不上家长端」
- [ ] 手机上再出一次码(这时走的是**离线路径**,用 localStorage 里缓存的密钥)
- [ ] 电视上输进去 —— **能解锁才算过**
- [ ] 恢复后端

> 没演练过的应急方案等于没有。这一步花 5 分钟,省的是某个周日晚上 Pi 挂了、
> 妈妈想看电视而你不在家的麻烦。

**再然后:一条一条加规则,每加一条观察一天。**
- [ ] 先加一条**没人会碰到的**宵禁(比如 02:00-05:00),打开总开关,确认什么都没坏
- [ ] 换成真的宵禁(作业时间 / 就寝后)
- [ ] 加每日限额
- [ ] 最后才考虑白名单/黑名单(影响最大,最容易误伤)

每加一条之后检查一次 `journalctl` 和电视面板,确认没有误拦。

---

## 我需要你给我的东西

| | 用在哪 | 状态 |
|---|---|---|
| 域名 | 阶段 2、3 —— 定了就不能改 | ✅ 已定并实测 |
| Pi 的 ssh 地址和用户 | 阶段 3 | ✅ |
| Pi 的内网 IP | 电视直连用 | ✅ 已记,avahi 也会广播 |
| podman quadlet 还是裸 systemd | 阶段 3 | ✅ podman-compose(4.3.1 没有 Quadlet) |
| 时区 | 日/周边界按它算 | ✅ 跟随 Pi 的系统时区 |
| **阶段 1 的结果** | **遮罩和 D-pad 到底行不行** | ⬜ **还差这一个** |

> 文档里一律用 `tv.example.com` 占位。真实域名不进 git —— 这个仓库在 GitHub 上,
> 而家长端现在是公网可达的,没必要让「这个域名跑的是什么、有哪些接口」变得可搜索。
> 真实值只存在 Pi 上那个 `braviaguard.env` 里。

## 还没解决的事

- **电视的 accessibility 可以被关掉。** 已知代价。孩子大到会绕的时候升级 Device Owner ——
  只改 `EnforcementController.block()` 一个方法,但**需要恢复出厂**,到时候再单独规划。
- **`adb uninstall` 能卸掉这个 app。** 所以阶段 1 做完记得把电视的网络调试关掉。
- **内容分级过滤没做**,得靠各 app 自己的儿童档案(Netflix / YouTube Kids)。
