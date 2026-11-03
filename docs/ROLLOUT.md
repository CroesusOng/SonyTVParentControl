# 上线计划

从「代码写完了」到「客厅里真的在跑」。总共大概 1.5 小时动手时间,
建议分 2-3 天做完,中间留观察期。

**标注:** 🧑 你做(需要遥控器 / sudo / DNS / iPhone) · 🤖 我做(给我 ssh 就行)

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
也就是说「先用 HTTP 跑通电视端、证书以后再配」这条路走不通 ——
没 HTTPS 就没 Face ID,没 Face ID 就拿不到配对码。所以证书排在配对前面。

---

## 阶段 0 — 让电视端能单独验证 🤖 ~20 分钟

**为什么先做这个:** 整个项目风险最高的两处(accessibility 事件的准确度、
overlay 能不能抢到 D-pad 焦点)都在拦截层,而它们**不需要后端也能验**。
先加一个 debug 版才有的「测试拦截遮罩」按钮,你就能在不碰 DNS、证书、Pi 的前提下
确认这条路走不走得通。

- `EnforcementController` 加一个 `previewBlockOverlay()`,拿假数据直接调 `overlay.show()`,
  不触发 `GLOBAL_ACTION_HOME`(否则测试时会被踢出 app)
- 面板上加一个 `if (BuildConfig.DEBUG)` 的按钮

约 20 行。**要不要做由你定** —— 不做也能往下走,只是风险会一直背到阶段 4。

---

## 阶段 1 — 电视上装包并验证拦截层 🧑 ~20 分钟

照 [SIDELOAD.md](SIDELOAD.md) 走。概括:

- [ ] 电视:关于 → 版本号连按 7 次 → 开 **网络调试**
- [ ] `source scripts/env.sh && adb connect <电视IP>:5555`
      —— **电视上会弹授权框,用遥控器勾「一直允许」**(最容易卡在这)
- [ ] `./gradlew assembleDebug && adb install -r app/build/outputs/apk/debug/app-debug.apk`
- [ ] 电视:设置 → 设备偏好设置 → 无障碍 → 电视守护 → 打开
- [ ] 打开 app,按「测试拦截遮罩」(如果做了阶段 0)

**成功标准:**
- 遮罩全屏出现,盖住了下面的内容
- 遥控器上下键能改数字,左右键能换位置
- 按 OK 有反馈(未配对时会显示「还没配对,无法校验解锁码」—— 这是对的)
- 按返回键遮罩消失

**这是整个项目的 go / no-go 点。** 这步过了,后面都是配置工作。
过不了的话,`app/src/main/kotlin/dev/homelab/braviaguard/enforce/BlockOverlay.kt`
的窗口参数需要按真机表现调,告诉我现象我来改。

**要重来:** `adb shell pm clear dev.homelab.braviaguard.debug` 把 app 数据清空。

---

## 阶段 2 — 定域名,配证书 🧑(我能写脚本,token 和 sudo 得你来) ~20-40 分钟

照 [TLS.md](TLS.md),推荐**自有域名 + Let's Encrypt DNS-01**:
不用端口转发、不用公网能访问到 Pi、iPhone 上不用装任何证书。

- [ ] 决定域名,比如 `tv.yourdomain.com`。**这个值之后不能改**
- [ ] DNS 加 A 记录指向 **Pi 的内网 IP**(公网解析出私有地址,外面的人拿不到任何东西)
- [ ] Pi 上 certbot DNS-01 签证书,放到 `/etc/braviaguard/certs/`
- [ ] 装续期 hook(TLS.md 里有现成的)

没有自己的域名的话,DuckDNS 免费域名走同一套 DNS-01 也行,证书同样公信。

**成功标准:** Mac 上 `curl -v https://tv.yourdomain.com:8443/healthz` 在证书校验上不报错
(这时还连不上是正常的,后端还没起)。

---

## 阶段 3 — 后端上 Pi 🤖 ~10 分钟

给我 ssh 地址(比如 `pi@pi5.local`)和你定的域名,我来做:

- [ ] 交叉编译 linux/arm64(`scripts/build-server.sh`)
- [ ] 按你的习惯选 **podman quadlet** 或**裸 systemd**(两份 unit 都写好了)
- [ ] 填 `BG_ORIGINS` / `BG_TZ` / 证书路径
- [ ] 装 avahi service,这样电视能自动发现,不用遥控器拼 URL
- [ ] 从日志里读出 **SETUP CODE** 给你

**成功标准:** `curl https://tv.yourdomain.com:8443/healthz` 返回 `ok`,
iPhone Safari 打开能看到登录页且**地址栏没有证书警告**。

---

## 阶段 4 — 注册 passkey,配对电视 🧑 ~10 分钟

- [ ] iPhone Safari 打开 `https://tv.yourdomain.com:8443`
- [ ] 填名字 + SETUP CODE → **Face ID 注册**
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

| | 用在哪 |
|---|---|
| 你定的域名 | 阶段 2、3 —— 定了就不能改 |
| Pi 的 ssh 地址和用户 | 阶段 3 |
| podman quadlet 还是裸 systemd | 阶段 3(两份 unit 都现成) |
| 时区确认(默认写的 `Asia/Shanghai`) | 日/周边界按它算 |
| 阶段 1 的结果 | 遮罩和 D-pad 到底行不行 |

## 还没解决的事

- **电视的 accessibility 可以被关掉。** 已知代价。孩子大到会绕的时候升级 Device Owner ——
  只改 `EnforcementController.block()` 一个方法,但**需要恢复出厂**,到时候再单独规划。
- **`adb uninstall` 能卸掉这个 app。** 所以阶段 1 做完记得把电视的网络调试关掉。
- **内容分级过滤没做**,得靠各 app 自己的儿童档案(Netflix / YouTube Kids)。
