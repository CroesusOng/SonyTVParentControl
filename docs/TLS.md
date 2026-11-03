# 家长端的 HTTPS

## 为什么躲不掉

家长端用 **WebAuthn / passkey** 调 Face ID。WebAuthn 只在
[secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts)
里可用,而且 **Relying Party ID 必须是域名,不能是 IP**:

- `http://192.168.1.10:8443` —— 不行,不是 secure context,IP 也不能当 RP ID
- `https://192.168.1.10:8443` —— 不行,IP 不能当 RP ID
- `https://pi5.local:8443` —— 不保险,`.local` 是 special-use 域名,Safari 对它的
  WebAuthn 支持不稳定
- `https://tv.example.com` —— 可以

**电视端不受这个限制。** TV ↔ Pi 是机器对机器的 bearer token,debug 版本直接走
`http://` 就行。只有手机浏览器需要 HTTPS。

---

## 方案一:Cloudflare Tunnel(推荐)

最省事的一条:Cloudflare 在边缘终结 TLS,你这边完全不用碰证书。
**不用签证书、不用续期、不用端口转发、不用 A 记录**,而且出门在外也能管。

### Pi 上

跑 `cloudflared`,在 Zero Trust 面板里加一条 Public Hostname:

| 字段 | 值 |
|---|---|
| Subdomain | `tv` |
| Domain | `example.com` |
| Service Type | `HTTP` |
| URL | `localhost:8443` |

cloudflared 会自动建好 CNAME,你不需要自己加 DNS 记录。

### 对应的 env

```bash
BG_ORIGINS=https://tv.example.com    # 注意:没有端口
BG_LISTEN=:8443
BG_TLS_CERT=                          # 留空
BG_TLS_KEY=                           # 留空
```

**`BG_ORIGINS` 不要带端口。** Cloudflare 在 443 上服务,浏览器发的 `Origin` 头
就是 `https://tv.example.com`,带了 `:8443` 会对不上,所有 passkey 都认证失败。

证书留空不代表不安全 —— 服务端会发现 origin 是 https,照样把会话 cookie 标成
`Secure`(见 `internal/config.CookiesSecure`),启动日志里会打
`serving plain HTTP behind a TLS terminator`。

### 三个坑

**1. 电视不要走隧道。** 让电视直接连 Pi 的内网地址 `http://<pi内网IP>:8443`。
走隧道的话,流量要绕到 Cloudflare 再回来,**而且宽带一断电视就连不上 Pi 了** ——
明明两台机器就在同一个交换机上。所以 `BG_LISTEN=:8443`(监听所有网卡)而不是
只听 loopback:cloudflared 走 localhost,电视走内网 IP。

**2. 这个 hostname 上不要挂 Cloudflare Access 策略,或者设成 bypass。**
挂了 Access 之后,Access 会话一过期,API 请求拿回来的就是一个 HTML 登录页而不是
JSON。家长端现在能认出这种情况并提示「刷新页面重新登录」,但体验上是多了一道门,
而且妈妈也要过。这个 app 自己的 passkey 已经是强认证了。

> 要是你坚持要 Access 当第二层:把会话时长设长一点(24 小时以上),
> 否则每次会话过期都要先刷一次页面。

**3. 家长端现在暴露在公网了。** 和「A 记录指向内网 IP」那种方案不同,
隧道意味着任何人都能打到那个 URL。挡在前面的只有 passkey ——
这是强认证(没有密码可以撞、没有 OTP 可以钓),但值得你心里有数。
好处是出门在外也能解锁,这比原来只能在家里 WiFi 上管要实用。

### release 包的注意事项

release 版本的 `network_security_config.xml` 关掉了 cleartext,所以 release 包
**不能**走 `http://内网IP`。你要么继续用 debug 包(sideload 场景下正常做法),
要么让电视走隧道,要么在 release 的 config 里加一条 `domain-config` 放行 Pi 的主机名。

---

## 方案二:自有域名 + Let's Encrypt DNS-01

不想把家长端暴露在公网的话走这条。公网 DNS 的 A 记录指向**内网 IP**,
证书用 DNS-01 签 —— 不需要端口转发,外面的人解析出来是个私有地址,拿不到任何东西。
代价是只能在家里的网络上用。

```bash
# Pi 上,以 Cloudflare DNS 为例
sudo apt install certbot python3-certbot-dns-cloudflare
sudo install -d -m700 /etc/braviaguard
sudo tee /etc/braviaguard/cloudflare.ini >/dev/null <<'EOF'
dns_cloudflare_api_token = <一个只有 Zone:DNS:Edit 权限的 token>
EOF
sudo chmod 600 /etc/braviaguard/cloudflare.ini

sudo certbot certonly \
  --dns-cloudflare \
  --dns-cloudflare-credentials /etc/braviaguard/cloudflare.ini \
  -d tv.example.com

sudo install -d -m755 /etc/braviaguard/certs
sudo cp /etc/letsencrypt/live/tv.example.com/{fullchain,privkey}.pem /etc/braviaguard/certs/
```

A 记录要设成 **DNS only(灰云)**,不能是 proxied(橙云)—— 代理一个私有地址是走不通的。

env 里填 `BG_ORIGINS=https://tv.example.com:8443` 和两个证书路径。

续期 hook:

```bash
sudo tee /etc/letsencrypt/renewal-hooks/deploy/braviaguard.sh >/dev/null <<'EOF'
#!/bin/sh
cp /etc/letsencrypt/live/tv.example.com/fullchain.pem /etc/braviaguard/certs/
cp /etc/letsencrypt/live/tv.example.com/privkey.pem   /etc/braviaguard/certs/
chmod 644 /etc/braviaguard/certs/fullchain.pem
chmod 640 /etc/braviaguard/certs/privkey.pem
systemctl restart braviaguard 2>/dev/null || systemctl --user restart braviaguard
EOF
sudo chmod +x /etc/letsencrypt/renewal-hooks/deploy/braviaguard.sh
```

## 方案三:Tailscale

```bash
sudo tailscale up
sudo tailscale cert pi5.tailxxxx.ts.net
```

`*.ts.net` 直接有有效证书,出门也能用,而且不暴露在公网(只有你的 tailnet 能访问)。
代价是每台手机都要装 Tailscale 并登录 —— 妈妈那台也要。

## 方案四:自签 CA + iPhone 手动信任

`mkcert` 签一张,把 `rootCA.pem` AirDrop 到 iPhone,
**设置 → 已下载的描述文件 → 安装**,再到
**通用 → 关于本机 → 证书信任设置**里打开完全信任。能用,但每台手机都要走一遍,
而且 iOS 对自签根证书的策略时不时会变。

---

## 换过 origin 之后

**`BG_ORIGINS` 改了,已经注册的 passkey 全部失效。** WebAuthn 把每个凭据绑死在一个
origin 上,换域名、换端口、从带端口改成不带端口,都等于换了一个站点。

- 第一次启动之前就把它定下来
- 真要改:改完删掉 `state.json` 里的 `parents` 数组,用新的 setup code 重新注册

## 电视端的证书

release 版本的 `network_security_config.xml` 同时信任 **system** 和 **user** 证书库:

- Cloudflare 或 Let's Encrypt 签的 → 系统库里就有,开箱可用
- 自签 CA → 把 CA 装到电视上:**设置 → 设备偏好设置 → 安全和限制 → 凭据 → 从存储设备安装**

debug 版本额外放开了 cleartext,所以电视可以直接用 `http://<pi内网IP>:8443`。
