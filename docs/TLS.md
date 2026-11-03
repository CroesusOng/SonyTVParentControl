# 为什么家长端必须走 HTTPS,以及怎么配

## 为什么

家长端用 **WebAuthn / passkey** 调 Face ID。WebAuthn 只在
[secure context](https://developer.mozilla.org/en-US/docs/Web/Security/Secure_Contexts)
里可用,而且 **Relying Party ID 必须是域名,不能是 IP**。所以:

- `http://192.168.1.10:8443` —— **不行**,不是 secure context,而且 IP 不能当 RP ID
- `https://192.168.1.10:8443` —— **不行**,IP 不能当 RP ID
- `https://pi5.local:8443` —— 不保险,`.local` 是 special-use 域名,Safari 对它的
  WebAuthn 支持不稳定
- `https://tv.example.com:8443`(证书有效)—— **可以**

电视端不受这个限制:TV ↔ Pi 是机器对机器的 bearer token,debug 版本直接走
`http://` 就能跑(见 `app/src/debug/res/xml/network_security_config.xml`)。
**只有手机浏览器需要 HTTPS。**

## 方案一:自有域名 + Let's Encrypt DNS-01(推荐)

不需要端口转发,不需要公网能访问到 Pi,也不用在 iPhone 上装任何证书。
做法是:公网 DNS 的 A 记录指向内网 IP,证书用 DNS-01 签。

```bash
# Pi 上,以 Cloudflare 为例
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

然后在 DNS 里加一条 A 记录 `tv.example.com → 192.168.1.10`(Pi 的内网 IP)。
这条记录在公网上解析出来是个私有地址 —— 外面的人拿它什么也做不了,家里就正常。

续期挂个 hook,把新证书拷过去并重启服务:

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

**没有自己的域名?** DuckDNS 之类的免费动态 DNS 也支持 TXT 记录,
`certbot-dns-duckdns` 插件能走同一套 DNS-01 流程,拿到的证书同样是公信的。

## 方案二:Tailscale

```bash
sudo tailscale up
sudo tailscale cert pi5.tailxxxx.ts.net
```

Tailscale 直接给 `*.ts.net` 签有效证书。好处是**出门在外也能管**,不限于家里 WiFi;
代价是每台手机都要装 Tailscale 并登录。

`BG_ORIGINS=https://pi5.tailxxxx.ts.net:8443`

## 方案三:自签 CA + 在 iPhone 上手动信任(备选)

```bash
brew install mkcert
mkcert -install
mkcert tv.home.arpa
```

把 `rootCA.pem`(`mkcert -CAROOT` 能看到路径)AirDrop 到 iPhone,然后:

1. **设置 → 已下载的描述文件 → 安装**
2. **设置 → 通用 → 关于本机 → 证书信任设置 → 打开对该根证书的完全信任**
3. 在手机上给 `tv.home.arpa` 做个 DNS 解析(路由器的 hosts 或 dnsmasq)

能用,但每台手机都要走一遍,而且 iOS 对自签根证书的策略时不时会变。
除非不想碰 DNS,否则还是方案一省事。

## 换过 origin 之后

**BG_ORIGINS 改了,已经注册的 passkey 全部失效。** WebAuthn 把每个凭据绑死在一个
origin 上,换域名就等于换了一个站点。所以:

- 第一次启动之前就把域名定下来
- 真要改的话,改完删掉 `state.json` 里的 `parents` 数组,用新的 setup code 重新注册

## 电视端的证书

release 版本的 `network_security_config.xml` 同时信任 **system** 和 **user** 证书库:

- Let's Encrypt 签的 → 系统库里就有,开箱可用
- 自签 CA → 把 CA 装到电视上:**设置 → 设备偏好设置 → 安全和限制 → 凭据 → 从存储设备安装**

debug 版本额外放开了 cleartext,所以在把 TLS 配好之前,电视可以先用
`http://192.168.1.10:8443` 跑通。
