# Bark 通知配置与自建服务

通知中心的 Bark 通道由应用 Go 服务端直接调用 Bark 的 `/push` API，邮件正文不会经过浏览器。你可以使用 Bark 官方服务，也可以在自己的服务器运行 `bark-server`，再把服务端地址填入通道配置。

## 一、准备 iPhone 设备密钥

1. 在 iPhone 安装并打开 Bark。
2. 在 Bark 的设备详情页复制 `Device Key`。
3. 如果以后更换 Bark 服务端，需要在 Bark App 中重新注册设备，并使用新服务端生成的 Device Key。

设备密钥相当于推送凭据，只应保存在通知通道配置中，不要提交到 GitHub、截图或公开日志。

## 二、使用官方 Bark 服务

在“通知中心”点击“新建通道”，填写：

- 名称：例如 `Bark 邮件通知`。
- 类型：选择 `Bark`。
- Bark 服务端地址：`https://api.day.app`。
- 设备密钥：粘贴 Bark App 中的 Device Key。
- 分组：可选，例如 `Outlook 邮件`。
- 声音：可选，例如 `minuet`；留空使用系统默认声音。

点击“测试配置”，确认 iPhone 收到测试通知后，再点击“创建通道”。应用会发送标题、发件人、主题和正文预览；正文预览仍遵守应用的长度限制，不会发送完整邮件正文。

## 三、自建 bark-server（Docker）

Bark 官方后端提供 Docker 镜像。下面的命令将服务绑定到本机 `8088`，避免与 Outlook Mail Manager 的 `8080` 冲突：

```bash
mkdir -p /opt/bark-server/bark-data
docker run -d \
  --name bark-server \
  --restart unless-stopped \
  -p 127.0.0.1:8088:8080 \
  -v /opt/bark-server/bark-data:/data \
  ghcr.io/finb/bark-server
```

确认容器和 API 正常：

```bash
docker ps --filter name=bark-server
curl -fsS http://127.0.0.1:8088/ping
```

生产环境不要把 `8088` 直接暴露到公网。使用宝塔 Nginx 为它配置独立 HTTPS 域名，例如 `bark.example.com`，反向代理到 `127.0.0.1:8088`，并在防火墙中只开放 80/443。Bark App 的后端地址也要改成这个 HTTPS 域名，然后重新复制 Device Key。

如果不使用域名，也可以只在 Outlook Mail Manager 所在服务器上使用内网地址；此时在通道中填写 `http://127.0.0.1:8088`，但 HTTP 只适合同机、受控网络，不能用于跨公网传输。

## 四、宝塔反向代理要点

1. 在“网站”中添加 `bark.example.com` 并申请 Let's Encrypt 证书。
2. 在“反向代理”中将目标 URL 设置为 `http://127.0.0.1:8088`。
3. 开启 HTTPS，并勾选 WebSocket 支持（即使当前接口不依赖，也可保持 Bark 默认配置兼容）。
4. 外网测试：

   ```bash
   curl -fsS https://bark.example.com/ping
   ```

5. 在 Outlook Mail Manager 的 Bark 通道中填写 `https://bark.example.com`，不要在地址末尾填写 `/push`。

## 五、API 请求格式

应用服务端发送的是 Bark API V2 JSON 请求，设备密钥放在请求体中：

```bash
curl -X POST https://bark.example.com/push \
  -H 'Content-Type: application/json; charset=utf-8' \
  -d '{
    "device_key": "你的设备密钥",
    "title": "邮箱管理台测试通知",
    "body": "这是一条测试通知",
    "group": "Outlook 邮件",
    "sound": "minuet"
  }'
```

返回 `code: 200` 才算成功。网络错误或非 200 返回会进入通知投递重试队列；连续失败后可在“投递记录”查看错误并重试。

## 六、安全与故障排查

- 优先使用 HTTPS；Bark 服务端地址不能带用户名、密码、查询参数或片段。
- 设备密钥和 Bark 服务端地址会加密保存在应用数据库，列表只显示脱敏设备信息。
- `无法连接 Bark 服务`：检查域名证书、反向代理、容器状态和服务器出站网络。
- `Bark 服务拒绝了推送`：确认 Device Key 属于当前 Bark 服务端，并检查设备是否在线。
- 更换 Bark 服务端后必须重新注册设备；旧 Device Key 不会自动迁移。
- Bark 服务端的 `/data` 目录包含设备和推送相关数据，必须纳入备份，且不要上传到公开仓库。

参考：

- [Bark 官方项目](https://github.com/Finb/Bark)
- [bark-server 自建后端](https://github.com/Finb/bark-server)
- [Bark API V2](https://github.com/Finb/bark-server/blob/master/docs/API_V2.md)
