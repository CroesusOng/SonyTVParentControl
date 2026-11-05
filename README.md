# BraviaGuard · 电视守护

给索尼 BRAVIA(Android TV / Google TV)做的家长控制。电视上装一个 app 执行规则,
树莓派上跑一个后端存规则,手机上用 **Face ID** 随时解锁 —— 没有密码。

```
Sony BRAVIA (Kotlin, Compose for TV)  ←→  Raspberry Pi 5 (Go, podman)  ←→  iPhone (PWA + 原生审批 App)
```

**现状:在真机上跑着** —— 一台 BRAVIA 和一台树莓派 5,2026 年 11 月起。
`./gradlew assembleDebug` 和 `go test ./... -race` 都通过;核心判定逻辑 88 个单元测试,
后端 78 个。解锁延迟实测 **123 毫秒**(过程见 [docs/ROLLOUT.md](docs/ROLLOUT.md))。

**唯一还没上真机的是审批 App**(`approver-ios/`):加密核心做过类型检查、和后端的签名
载荷有测试互相钉住,但界面一行都没验过 —— 那需要 Xcode。见
[approver-ios/README.md](approver-ios/README.md)。

---

## 能管什么

- **每日/每周屏幕时间限额** —— 按星期分别设,可以只限工作日
- **单个 app 限额** —— 「Netflix 每天 45 分钟」
- **禁用时段** —— 作业时间、就寝后。支持跨零点
- **App 白名单 / 黑名单**
- **使用统计** —— 今天/本周总时长、每个 app 多久、14 天趋势
- **临时解锁** —— 手机上 Face ID 一下,放行 15/30/60/120 分钟,**123 毫秒**到电视
- **离线应急码** —— Pi 挂了也能用遥控器输码解锁

## 架构选择的理由

完整版在 **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)**。几条最关键的:

**判定在电视上做,不在 Pi 上做。** Pi 只存规则。断网、Pi 挂掉、WiFi 改密码,
电视照样按本地缓存的规则工作。

**用 AccessibilityService,不用 Device Owner。** Device Owner 防绕过能力强得多,
但**必须恢复出厂设置**才能激活 —— 妈妈的 Netflix 登录、所有已装 app、首页布局全没。
为了一个家长控制 app 不值得。代价是孩子进设置能把无障碍关掉;缓解手段是电视首页
直接显示「规则执行已关闭」,同时 Pi 会看到这台电视停止签到。孩子大到会绕的时候,
**升级到 Device Owner 只需要改 `EnforcementController.block()` 一个方法** ——
规则引擎、协议、后端、家长端一行都不用动。

**日常家长端是 PWA + passkey。** 全家 iPhone,要 Face ID。原生 app 用免费开发者账号
签的包 **7 天就过期**,每台手机每周都要接电脑重推 —— 这是个长期负担。PWA 用 WebAuthn
platform authenticator 调 Face ID,Safari「添加到主屏幕」就完事,妈妈的手机自己注册一个
passkey 就行。代价是**家长端必须 HTTPS**(见 [docs/TLS.md](docs/TLS.md))。电视端不受
影响,debug 版本直接走 HTTP 就能跑。

**但发放电视时间要一个很小的原生 App。** 因为 PWA 有个堵不住的洞,而且是 WebAuthn 自己
的洞:**iOS 上的 passkey 断言,Face ID 和手机密码都算通过** —— `userVerification` 没有
「只要生物识别」这个选项,回来的 UV 标志也不说明用了哪种。家里孩子知道手机密码,passkey
就只能当登录凭证。所以「解锁」和「改规则」改由一个原生 App 批准:它用 Secure Enclave 里
`.biometryCurrentSet` 的密钥签名 —— **没有密码退路**,而且**往 Face ID 加一张脸密钥就
失效**。7 天重签的代价因此只落在**一台**手机上,而不是每台。
详见 [ARCHITECTURE.md 第 11 节](docs/ARCHITECTURE.md)。

**判定逻辑在一个纯 JVM 模块里。** `core-rules` 不碰任何 Android API,所以
「周五 21:30 到周六 06:30 的宵禁在周六 03:00 该不该拦」是一行断言,不是一次手动测试。

**用 DataStore,不用 Room。** 工作集是一份规则文档加一个 90 天的用量 map,几 KB,
每次整体读写。Room 的 KSP 处理器 + schema 迁移 + DAO 换不回任何东西。

**后端用 Go + JSON 文件,不用数据库。** Pi 上不用跑也不用备份数据库,
而且你能 `jq` 直接看自己的数据。成品是 `FROM scratch` 的单文件镜像,8.7 MB。

## 需要的权限

两个都是 **special access** 权限,装包时弹不出来,只能手动进电视设置开。

| 权限 | 必须? | 在哪开 | 干什么 |
|---|---|---|---|
| `BIND_ACCESSIBILITY_SERVICE` | **是** | 设置 → 设备偏好设置 → 无障碍 | 识别前台是哪个 app。不开则完全不生效 |
| `PACKAGE_USAGE_STATS` | 否 | 设置 → 设备偏好设置 → 安全和限制 → 具有使用权限的应用 | 补报表历史。不开也能拦截 |

另外还有 `INTERNET`、`ACCESS_NETWORK_STATE`、`RECEIVE_BOOT_COMPLETED` —— 都是普通权限。
**没有** 用 `QUERY_ALL_PACKAGES`:manifest 里用 `<queries>` 声明了可启动的 intent,
拿到的正好是「孩子能打开的 app」。

app 首页会直接显示这两个开没开,旁边有按钮能跳过去。

---

## 1. 装工具链

> 这台机器上我已经装过了。换机器的话:

```bash
brew install openjdk@17 gradle
brew install --cask android-commandlinetools android-studio
sdkmanager --sdk_root="$HOME/Library/Android/sdk" "platform-tools" "platforms;android-34" "build-tools;34.0.0"
```

AGP 8.7.3 要 **JDK 17**,Gradle 由 wrapper 锁在 8.9。PATH 上有更新的 JDK 是这里
最常见的诡异报错来源,所以用脚本固定:

```bash
source scripts/env.sh    # 找 JDK 17,设 JAVA_HOME 和 ANDROID_HOME
```

Go 后端要 **Go 1.26+**(`go-webauthn` 要求的)。

## 2. 编译和测试

```bash
source scripts/env.sh
./gradlew :core-rules:test     # 87 个测试,纯 JVM,几百毫秒
./gradlew assembleDebug        # → app/build/outputs/apk/debug/app-debug.apk
./gradlew lintDebug            # 0 errors(剩下的 warning 都是刻意为之,见下)
```

剩的 lint warning 分三类,都是有意的:依赖版本故意钉住(升上去会强制 compileSdk 35)、
`AcceptsUserCertificates`(自签 CA 方案需要,见 TLS.md)、以及 minSdk 26 下不适用的
`VectorRaster` 之类。

```bash
cd server && go test ./... && go vet ./...
```

测了什么:

| 位置 | 覆盖 |
|---|---|
| `core-rules/src/test` | 判定优先级、三种配额、跨零点宵禁、授权覆盖、离线行为、`nextBoundary`、用量跨天切分、RFC 6238 向量、base32、应急码防重放、协议往返 |
| `server/internal/store` | 原子写、重开后状态保持、配对码单次有效、用量合并语义、过期清理 |
| `server/internal/api` | 设备配对到同步全流程、token 只存 hash、鉴权拒绝、家长端未登录拒绝、规则校验 |
| `server/internal/totp` | RFC 6238 向量(和 Kotlin、JS 三份实现对齐) |

## 3. 部署后端到树莓派

先读 **[docs/TLS.md](docs/TLS.md)** —— HTTPS 必须在第一次注册 passkey 之前就位,
因为 **`BG_ORIGINS` 改了,已注册的 passkey 全部失效**。最省事的是
**Cloudflare Tunnel**:不用证书、不用续期、不用端口转发。

部署用的那些值放在 `/etc/braviaguard/braviaguard.env`(从
`server/deploy/braviaguard.env.example` 复制),两种部署方式读同一个文件,**不进 git**。

**选项 A:交叉编译后直接跑**(最省事,Pi 上不用装 Go)

```bash
./scripts/build-server.sh pi@pi5.local
# 脚本会把二进制和 systemd/avahi 的 unit 拷过去,并打印 Pi 上要执行的命令
```

**选项 B:rootless podman-compose**(podman 4.3 也能用)

```bash
# Pi 上
git clone https://github.com/CroesusOng/SonyTVParentControl.git ~/braviaguard
cd ~/braviaguard/server
cp .env.example .env && $EDITOR .env       # 至少改 BG_ORIGINS 和 BG_TZ
mkdir -p data

podman-compose build
podman-compose up -d
podman logs braviaguard | tail -20         # 从这里读 SETUP CODE
```

挂到 systemd 上(开机自启):

```bash
install -Dm644 /dev/stdin ~/.config/systemd/user/braviaguard.service <<'EOF'
[Unit]
Description=BraviaGuard podman-compose stack
Requires=podman.socket
After=podman.socket network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=%h/braviaguard/server
ExecStart=/usr/bin/podman-compose up -d
ExecStop=/usr/bin/podman-compose down

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now braviaguard
loginctl enable-linger "$USER"             # 没登录时也保持运行
```

**选项 C:podman Quadlet**(需要 podman ≥ 4.4)

`deploy/braviaguard.container` 是现成的,拷到 `~/.config/containers/systemd/`
再 `systemctl --user daemon-reload` 即可。podman 4.3 不支持 Quadlet,用选项 B。

然后让电视能自动发现它:

```bash
sudo install -m644 deploy/avahi-braviaguard.service /etc/avahi/services/braviaguard.service
sudo systemctl reload avahi-daemon
```

**环境变量**

| 变量 | 默认 | 说明 |
|---|---|---|
| `BG_ORIGINS` | **必填** | 手机上打开的那个地址,一字不差。443 上服务就**不要带端口**。WebAuthn 把 passkey 绑死在它上面 |
| `BG_DATA_DIR` | `/var/lib/braviaguard` | `state.json` 和 `usage.json` 放哪 |
| `BG_LISTEN` | `:8443` | |
| `BG_TLS_CERT` / `BG_TLS_KEY` | 空 | 由本进程终结 TLS 时才填。前面有 Cloudflare Tunnel 或反代时**留空** —— 服务端会发现 origin 是 https,照样把 cookie 标成 Secure |
| `BG_RP_ID` | 从 origin 推导 | WebAuthn RP ID,就是域名 |
| `BG_TZ` | `Local` | 日/周边界按这个时区算 |
| `BG_SETUP_CODE` | 随机生成 | 不填就每次启动随机一个,打在日志里 |

## 4. 注册家长端

1. iPhone 的 Safari 打开 `https://tv.example.com:8443`
2. 填名字 + 日志里的 **SETUP CODE** → 「用 Face ID 注册」
3. **分享 → 添加到主屏幕** —— 之后用起来和 app 一样
4. 妈妈的手机:同一个网址 →「这台手机还没注册过」→ 先用你的 Face ID 授权,
   再给她的手机注册一个 passkey

## 5. 推到电视上

完整步骤(含电视设置里的每一项、授权弹窗、日志排查)在
**[docs/SIDELOAD.md](docs/SIDELOAD.md)**。概括:

```bash
source scripts/env.sh
adb connect 192.168.1.50:5555          # 电视上会弹授权框,勾「一直允许」
adb install -r app/build/outputs/apk/debug/app-debug.apk
```

然后在电视设置里打开无障碍,在手机上生成配对码,在电视上输进去。

---

## 目录结构

```
core-rules/          纯 Kotlin/JVM:规则引擎、TOTP、用量账本、线上协议。没有 Android 依赖
app/                 Android TV app
  enforce/           AccessibilityService、拦截 overlay、EnforcementController
  data/              DataStore + Keystore 加密存储
  net/               后端客户端、mDNS 发现
  usage/             UsageStats 读取、app 名称、权限检查
  ui/                Compose for TV 面板
server/              Go 后端
  internal/api/      设备 API + 家长 API
  internal/auth/     WebAuthn / passkey 会话
  internal/store/    JSON 文件存储
  internal/totp/     应急码生成
  web/               家长端 PWA(编译进二进制)
  deploy/            Quadlet、systemd、avahi 的 unit 文件
approver-ios/        原生 iOS 审批 App:Secure Enclave 签名(工程文件要自己在 Xcode 建)
docs/                架构理由、TLS 方案、sideload 步骤
scripts/             env.sh(固定工具链)、build-server.sh(交叉编译)
```

## 已知的坑和没做的事

- **没在真机上验证过。** 最可能需要调的两处:accessibility 事件里
  `rootInActiveWindow.packageName` 在 BRAVIA 上的准确度,以及 overlay 窗口能不能
  可靠抢到 D-pad 焦点。两处都在 `app/src/main/kotlin/dev/homelab/braviaguard/enforce/`。
- **无障碍能被关掉。** 已知代价,见上面和 ARCHITECTURE.md 第 1 节。
- **`adb uninstall` 能卸掉。** 所以调试完把电视的网络调试关掉。
- **`state.json` 是明文的。** 在 Pi 上有 shell 的人能看到 TOTP 密钥。
  设备 token 存的是 SHA-256,这个没问题。
- **改时钟能影响宵禁。** 配额不受影响(累计用量),但宵禁看的是本地时间。
  Android TV 默认走 NTP,手动改时间需要进设置。
- **只有一套规则,不分用户。** Android TV 的多用户在 BRAVIA 上基本不可用,
  所以没做。规则是整台电视的。
- **没做内容分级过滤。** 这个得靠各个 app 自己的设置(Netflix/YouTube Kids 的
  儿童档案),app 层面拦不了。
- **第一次启动的 setup code 只在那次进程里有效。** 重启了就看新日志。
