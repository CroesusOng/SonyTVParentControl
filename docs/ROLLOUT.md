# 上线计划

从「代码写完了」到「客厅里真的在跑」。总共大概 1.5 小时动手时间,
建议分 2-3 天做完,中间留观察期。

**标注:** 🧑 你做(需要遥控器 / sudo / DNS / iPhone) · 🤖 我做(给我 ssh 就行)

**现在到哪了:** 阶段 0 ✅ · 阶段 1 ✅ · 阶段 2 ✅ · 阶段 3 ✅ · 阶段 4 ✅ ——
后端在 Pi 上跑着、域名通了、家长 passkey 已注册、电视已配对,
拦截层在真机上验过:遮罩盖得住 YouTube,遥控器被它吃住。
**整条链路已经打通,总开关现在是关着的(「规则已暂停」),它只在收集数据。**
**下一步是阶段 5**:观察几天,演练一次离线应急码,然后一条一条加规则。

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

## 阶段 1 — 电视上装包并验证拦截层 ✅ 已完成

**结论:go。** 三种失效模式一个都没出现。

验证机型:Sony BRAVIA X85J 系列,**Android 12 / SDK 31**,debug 包 `…braviaguard.debug`
(debug 构建带 `.debug` 后缀,`adb shell pm list packages` 里要按这个名字找)。

实际走下来的步骤:

- [x] 电视:设置 → 系统 → 关于 → **版本号连按 7 次** → 开发者选项 → **USB 调试**
      —— Android TV 开了 USB 调试之后 5555 就自动监听了,不用再单独找「网络调试」
- [x] `adb connect <电视IP>:5555` → 第一次回的是 **`unauthorized`**,这是正常的:
      **电视上会弹授权框,遥控器勾「一律允许」再确定**,之后状态才变 `device`
- [x] `adb install -r app/build/outputs/apk/debug/app-debug.apk` → `Success`
- [x] 电视:无障碍 → 最底下「服务」组 → **电视守护 · 规则执行** → 启用
      → 系统弹「可以收集您输入的所有文字信息」确认框 → 确定
- [x] 打开 app(未配对,看到的是配对页),按**调试工具 → 测试拦截遮罩**

**三种失效模式的实测结果:**

| 失效模式 | 结果 |
|---|---|
| 遮罩根本不出现 | 没发生 —— 全屏出现,app 名 / 额度 / 「明天零点之后可以继续」都对 |
| 出现了但被视频盖住 | 没发生 —— 遮罩亮着时从 adb 拉起 YouTube,遮罩仍在最上层 |
| 出现了但遥控器穿透 | 没发生 —— 上下键改数字、左右键换格都生效,底下 YouTube 一个按键都没收到 |

顺带确认的几件事:

- 无障碍一开,面板上那块红色「规则执行已关闭」自动消失
- avahi 广播可用,电视自己发现了 `BraviaGuard on rpi01 — http://<PiIP>:8443`
- 返回键能收掉遮罩,收掉之后轮询循环**没有**把它弹回来 —— `previewing` 标志按预期清掉了

**关于那个「完全控制」确认框:** Android 对所有无障碍服务都弹同一段模板文案,
不是在描述本应用。本应用在 `accessibility_service_config.xml` 里只订阅了
`typeWindowStateChanged|typeWindowsChanged`(前台应用变化),
**没有**订阅 `typeViewTextChanged`,也没有声明 `canRequestFilterKeyEvents`
或 `canPerformGestures` —— 系统只投递已订阅的事件,所以文字输入拿不到。

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

## 阶段 4 — 注册 passkey,配对电视 ✅ 已完成(妈妈那台手机还没做)

- [x] iPhone Safari 打开 `https://tv.你的域名` —— **不带端口**,见阶段 2 第 1 条
- [x] 填名字 + SETUP CODE → **Face ID 注册**
      —— setup code **只对当前这次进程有效**,重启会换一个,
      要重新拿:`podman logs braviaguard | grep "SETUP CODE"`
- [x] **分享 → 添加到主屏幕**(这步别跳,否则每次都要在 Safari 里翻)
- [ ] **妈妈的手机同样做一遍** —— 同一个网址 →「这台手机还没注册过」→
      先用你的 Face ID 授权,再给她注册一个 passkey
- [x] 家长端 →「电视」→「+ 添加一台电视」→ 拿到 6 位配对码
- [x] 电视上打开 app:地址**自动出现在列表里**(avahi 生效),选中,输配对码

**实际结果:** 电视面板从配对页变成已配对面板,顶部显示「○ 规则已暂停」,
当天使用时长开始累计。服务端 `devices` 里多了一条记录(型号和 Android 版本是
电视自己报上来的),`pairingCodes` 回到 0 —— 码被正常消费掉了。

**配对之后才出现的东西:** `escapeSecretBase32` 这时才生成。
在此之前家长端按「离线应急码」拿到的是 404 `no_secret_yet`(「先配一台电视」),
**这是对的,不是故障** —— 应急码是拿设备密钥派生的,没有设备就没有码。

---

## 阶段 5 — 渐进放开,别一次全开 🧑 2-3 天 + 每步验证

这是最容易被跳过、但最该做的一段。

**第 1-3 天:只看不管。** 保持总开关**关着**,让它纯收集数据。
- [ ] 三天后对一下:家长端显示的时长和你实际观察的差得多吗?
- [ ] 各个 app 的名字显示对不对(白名单要靠这个选)

**然后:演练一次断电。** ✅ 已做过一次,**通过**。

- [x] 手机上「用 Face ID 显示应急码」,确认能出码
- [x] 把后端停掉:`systemctl --user stop braviaguard`
- [x] 手机上再出一次码(走**离线路径**,用 localStorage 里缓存的密钥)
- [x] 电视上用遥控器输进去 —— **解锁成功**
- [x] 恢复后端,电视 30 秒内自己连回来

**顺序有个坑,排在最前面说:** 离线那条路读的是 `localStorage` 里的密钥,
而**只有在线成功取过一次才会写进去**:

```js
try {
  const fetched = await apiWrite('GET', '/api/parent/escape-secret');
  localStorage.setItem(SECRET_KEY, secret);   // ← 唯一的播种点
} catch {
  if (!secret) { toast('还没缓存过应急密钥,需要先联网打开一次'); return; }
}
```

配对之前按这个按钮拿到的是 404,走 `catch`、`secret` 是空 —— **什么都没缓存下来**。
所以「停后端之前先在线出一次码」不是走过场,不做的话演练只会测出这条 toast。
**新注册一台家长手机,也要记得让它在线出一次码**,否则那台手机的离线路径是空的。

**这次实测到的数据:**

| | |
|---|---|
| 停机到兑码 | 39 秒 |
| 校验方式 | 电视本地 `OfflineCodeGate`,**全程不碰网络** |
| 落地凭证 | `{"source":"OFFLINE_CODE","note":"offline escape code"}` |
| 授权时长 | 30 分钟,`bypassList/bypassCurfew/bypassBudget` 全开 |
| 时钟偏差 | 电视比 Mac 慢 2 秒(容差 ±60 秒,余量很大) |
| 恢复后重连 | 后端起来后 30 秒内 |

电视的数字框除了方向键也认数字键(`KEYCODE_0..9`),带数字小键盘的遥控器可以直接打。

## 解锁延迟:从 26 秒到 123 毫秒 ✅ 已上线并实测

**原来慢在哪**(都是 Pi 日志里量出来的,不是估的):

| | |
|---|---|
| 电视轮询间隔 | 30.0 秒(连续 13 次,误差 ±1 秒) |
| 实测:家长下发 → 电视收到 | **26 秒**(`POST /api/parent/grants` 01:42:55 → 电视 01:43:21) |
| 最差 | ~33 秒 |

最差那 33 秒是因为 `loop()` 里的 `delay()` **叫不醒**:挡屏弹出来的时候循环可能刚睡下,
它要睡满 30 秒才会发现「现在该 3 秒一档了」。所以真正的修法是两件事,不是一件:

1. tick 循环的睡眠改成可打断(`withTimeoutOrNull { kick.receive() }`)
2. 另开一个协程,把一个普通的 GET 挂在后端上(`/api/device/sync?wait=20`)

**修好之后,同一条链路实测**(`podman logs --timestamps`,微秒精度):

| 时刻 | 服务端时钟 | 相对 |
|---|---|---|
| 授权签发(handler 进到 `Store.Write`) | 14:28:36.138 | — |
| 挂起的请求被叫醒(`sync: pushed to a held request after 15.398s`) | 14:28:36.1496 | **+11.6 ms** |
| 电视被叫醒的 tick 循环打回后端 | 14:28:36.2614 | **+123 ms** |

那 11.6 ms 是 handler 自己的活(改状态 + 落盘);广播本身 —— 授权那行日志到 `sync: pushed`
那行 —— 是 **161 微秒**。123 ms 是一个完整往返**加**电视应用状态的时间,所以单程到电视小于
123 ms。原来是 26 秒。

`after 15.398s` 是这里最关键的一个数:那条请求当时已经挂了 15.4 秒,不是碰巧轮到它轮询。
**按原来 30 秒的节奏,这一次点击要等到 14:28:48.4** —— 实际 14:28:36.26 就到了,早 12.1 秒;
而且之后的 usage 节奏从被叫醒那一刻重新起算(下一次 14:29:06.35,相差 30.085 秒),
说明 `wake()` 确实把 `delay()` 切断了,没有白等。

这次下发的是授权,**policy version 没变**(还是 13)。电视是靠 `revision` 才发现自己落后的 ——
这正是要加那个计数器的原因,授权没有自己的版本号。

**两个坑,都是写的时候踩出来的,现在有测试盯着:**

- **Go 的读/写 deadline 从读到请求头时就开始算**,不是从 handler 返回时算。原来
  `WriteTimeout` 是 30 秒,挂 20 秒就只剩 10 秒写响应。现在两个 timeout 都从
  `api.MaxSyncWait` 推出来,改一个另一个跟着动。
- **超时那条路径本来返回挂起之前构造的响应**,于是 `serverTimeMillis` 比真实时间慢 20 秒 ——
  而那个字段的用途正是让电视判断自己的时钟。现在超时也重建响应。测试 `TestLongPollTimesOutQuietly`
  就是抓到这个的。

**还有一个不是 bug 但该改的:** 最初是 `Store.Write` 每次都广播。但走 `Write` 的大多是会话、
WebAuthn 挑战码、拦截历史,电视一个都不读 —— 而 `/api/parent/login/begin` 是**公开接口**,
等于任何能打开登录页的人都能让电视一直重新同步。现在改成 `deviceFacingLocked()` 对「电视会
收到的那部分状态」做哈希、写入前后比一次,只有真变了才广播。

这个当场验到了:同一次点击里 `POST /api/parent/login/begin`(14:28:32.593)和
`POST /api/parent/login/finish`(14:28:36.079)都**没有**产生 `sync: pushed`,只有
`POST /api/parent/grants` 产生了。公开接口叫不醒电视。

**现场验证到的:**

| | |
|---|---|
| 电视端配的地址 | `https://tv.你的域名` —— **走 Cloudflare 隧道,不是局域网** |
| 隧道往返 | 254 ms(局域网直连同一个后端是 8.5 ms,差 30 倍) |
| 20 秒挂起能穿过隧道 | 能。接上之后持续零次 `watch disconnected` |
| 挂起超时不写闪存 | 验到了。电视 DataStore 的写入间隔是 30.105 / 30.080 秒 —— tick 循环的节奏,20 秒那一档一次都没写 |
| 后端重启时的行为 | 挂起的请求收到 Cloudflare 的 502,退避 1 秒、2 秒后自己接回来 |
| 重装 APK | 无障碍服务**保住了**,不用重新授权(同一个组件名、同一个签名) |

重装 APK 之后我误判过一次:YouTube 没被拦,我以为是无障碍服务没重新绑定。其实 `dumpsys
accessibility` 的 `Bound services` 里它在,真正的原因是手机上有两个 2 小时的全绕过授权还没到期。
**查这类问题先读电视自己的状态,别从症状倒推** ——
`run-as dev.homelab.braviaguard.debug toybox base64 .../guard.preferences_pb` 能直接把
policy、grants、`unreachable_since_millis` 全拿出来。

## 部署的坑:`podman-compose up -d --build` 不换容器

podman 4.3.1 + podman-compose 0.0.1 上,这条命令会**重建镜像但保留旧容器**,而且整体退出码
还是 **0**。日志里只有一行不起眼的提示:

```
podman run --name=braviaguard ...        exit code: 125   ← 名字已占用,创建失败
podman start braviaguard                 exit code: 0     ← 退回去启动了旧容器
braviaguard   Up 11 hours ago                             ← 从来没重启
```

也就是说:**你以为部署了,其实跑的还是旧二进制。** 验证方法是比镜像 ID,不是看退出码:

```bash
podman images --format '{{.ID}} {{.Repository}}:{{.Tag}}' | grep braviaguard
podman inspect braviaguard --format '{{.Image}}'
```

两个对不上就是没换。正确的部署顺序:

```bash
cd ~/braviaguard/server && git pull --ff-only
podman-compose up -d --build        # 只为了出镜像
podman stop braviaguard && podman rm braviaguard
podman-compose up -d                # 这次名字空着,才真的用新镜像创建
```

删容器是安全的:数据是 **bind mount**(`./data -> /var/lib/braviaguard`),在宿主机上,
不随容器走。确认一下再删:

```bash
podman inspect braviaguard --format '{{range .Mounts}}{{.Type}} {{.Source}}{{"\n"}}{{end}}'
```

换容器那几秒,电视挂起的请求会收到 502,然后退避 1 秒、2 秒自己接回来 —— 实测 16 秒内恢复。

## 待办:把电视重新指到局域网 ⬜ 查清了,结论是别急着做

### 先说值不值得

提这件事的原始理由是量到隧道往返 254 ms、局域网 8.5 ms,差 30 倍。**这个理由现在不成立了** ——
长轮询上线后端到端是 123 ms,改局域网最多再省 100 ms,而这是一天按几次的动作,省下来没人感觉
得到。30 倍是个好看的比值,不是一个有意义的绝对值。

**剩下一个理由,但它是真的:规则执行现在挂在互联网上。** 链路是
电视 → ISP → Cloudflare 边缘 → cloudflared(Pi 上另一个 Docker daemon)→ 后端,
而电视和 Pi 隔三米。任何一环断掉,电视进 `KEEP_ENFORCING`、按最后一份策略无限期执行 ——
而 `dailyMinutes` 全是 0,`RuleEngine.budgetVerdict` 里的 `used >= limit * 60L` 在 limit=0 时
**恒为真**,所以**断网 = 电视全锁**。家长这时也打不开家长端(同一条隧道),只能用离线应急码,
30 秒换一个,一次一次输。

**但这个风险有两个比重新配对更划算的解法:**

1. ~~把 0 分钟预算换成真实数字~~ —— **这条作废了,因为那些 0 是有意的**(见下面的清单)。
   断网时 `KEEP_ENFORCING` 等于全锁,这个后果跟着一起被接受了:默认关着,要看电视就由家长
   临时解锁,Pi 连不上的时候那条路是离线应急码。所以它不再是一个可以拿来降低风险的改动。
2. **Pi 上放真证书 + 内网 DNS 把 `tv.你的域名` 解到 Pi 的局域网地址**(`docs/TLS.md` 方案二
   或方案三)。这样**完全不用重新配对**:电视存的 URL 不变,手机在家时也走短路径,release 包
   同样能用。

重新指到局域网只修了「电视 → Pi」这一条腿,手机那条还是走隧道;还额外欠一个 DHCP 保留,而且
只在 debug 包成立(见下)。所以下面的流程是查代码+实测核实过的、备用,但不是下一步该做的事。

### 如果还是要做:先办这个,不然会把电视锁死

**Pi 的 eth0 现在是 DHCP,不是静态**(`inet <PiIP>/24 scope global dynamic`)。而
`BackendDiscovery.onServiceResolved` 存的是 mDNS **解析出来的 IP**(`info.host.hostAddress`),
不是主机名 —— 电视会把 `http://<PiIP>:8443` 这个字面地址写进 DataStore。Pi 换了租约,电视就
指向一个死地址。

接下来这条链比看上去严重:`offlineBehavior` 是 `KEEP_ENFORCING`,而 `offlineGraceMinutes: 60`
在这个模式下是**死配置** —— `RuleEngine` 只在 `OPEN_AFTER_GRACE` 时才读它。所以后端不可达时
电视按最后一份策略**无限期**执行;当前策略是「全 0 分钟预算的白名单」,等于全锁。

**所以:先在路由器上给 Pi 做 DHCP 保留(或改静态),再谈重新配对。顺序不能反。**

### 流程

1. **电视上解除配对** —— 打开「电视守护」→ 底部「解除配对」→ 要 6 位**应急码**,不是配对码
   (手机上「用 Face ID 显示应急码」,30 秒换一个)。这个门槛是故意的:否则绕过整个 app 最
   简单的办法就是打开它按解除配对,见 `EnforcementController.unpair` 的注释。
2. **手机生成配对码** —— 家长端 → 配对电视 → Face ID → 6 位码,10 分钟有效,单次使用。
3. **电视上配对** —— 解除后自动回到配对页,mDNS 列表会自己列出
   `BraviaGuard on <主机名> — http://<PiIP>:8443`。**直接按那一项。**
   不要照手机的提示填:手机上写的是「地址填 tv.你的域名」(`app.js` 用的 `location.host`),
   那是隧道地址,照着填等于什么都没改。
4. **手机上移除旧设备** —— 设备列表里旧的那条按「移除」。不删也能跑,只是挂一条僵尸记录。

万一第 3 步 mDNS 没出来(组播被拦),退路是手动输 `http://<PiIP>:8443`,或者填回隧道地址。
这期间电视是未配对状态,也就是不执行任何规则。

### 实测过的两件事

**局域网明文直连不会被跳转。** 这点值得单独验,因为 `main.go` 在隧道模式下把 `RequireHTTPS`
包在了**整个** handler 外面。但它只在 `X-Forwarded-Proto` 字面等于 `http` 时才发 308,直连没
这个头,所以放行:

| 请求 | 结果 |
|---|---|
| `http://<PiIP>:8443/healthz` | **200**,10 ms |
| `http://<PiIP>:8443/api/device/sync` | **401 missing_token** —— 可达,没被跳转 |
| 同上,带 `X-Forwarded-Proto: http` | **308** —— 跳转范围正好 |

**切换不会丢东西。** `clearPairing()` 只删 `backend_base_url` 和 `device_id` 两个键。
`usage_json`、`policy_json`、`grants_json`、`offline_counter` 全留着,所以**每日限额的计数不会
被重置**。应急密钥是全家一个(`handleEnroll` 只在 `EscapeSecret == ""` 时才生成),所以重新
配对后手机上的应急码**完全一样**,不用重新记。

### 两个代价

**服务端今天的时长会分成两段。** `Usage` 是 `deviceID -> 日期 -> 包名 -> 秒`,新设备是新 ID;
`handleDeleteDevice` 只删设备记录、不删 usage,旧的那段会留在 `state.json` 里。只影响家长端看
到的数字,不影响执行(电视本地计数见上)。

**这条路只在 debug 版成立。** `app/src/main/res/xml/network_security_config.xml` 是
`cleartextTrafficPermitted="false"`,只有 `app/src/debug/` 那一份放开了明文。电视现在跑的是
debug 包,所以能用 —— 但出 release 版之后局域网直连会**直接连不上**,得在 Pi 上放证书,或者给
私网段加一条 `domain-config` 例外。avahi 那个文件的注释里只说了「明文 + bearer token 是设计
如此」,没提 release 版会拒绝,这里补上。


**再然后:一条一条加规则,每加一条观察一天。**
- [x] ~~先把 0 分钟预算换成真实数字~~ —— **决定保持全 0,2026-11-06。** 这不是漏配,是
      选定的形状:**默认一分钟都不开,要看就由家长临时解锁。** 每日限额里填数字等于「这天
      可以自己看这么久」,而这里要的是每一次都经过家长。
      代价想清楚了:断网时 `KEEP_ENFORCING` 等于全锁,只有离线应急码能开(那条路本身也是
      家长自己管的,见 `approver-ios/README.md`)。**所以不要再把这些 0 当成待办「顺手修掉」。**
      家长端那几个输入框现在会写明「填 0 = 这天完全不开,留空 = 不限」,免得下次又被读成漏配。
      电视拦截画面也跟着改了:0 限额不再说「明天 00:00 之后可以继续」(那是句假话,明天也是 0),
      改成「今天没有可以自己看的时间」+「让家长在手机上解锁」—— **要装新 APK 才生效**
- [ ] 先加一条**没人会碰到的**宵禁(比如 02:00-05:00),打开总开关,确认什么都没坏
- [ ] 换成真的宵禁(作业时间 / 就寝后)
- [ ] ~~加每日限额~~ —— 同上,按决定不加;要放行走「临时解锁」
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
