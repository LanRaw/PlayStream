# PlayStream

PlayStream 是通过 Docker Compose 部署的局域网服务，管理界面用浏览器打开。在 PS5 上用系统自带的 Twitch 功能开播，PlayStream 接收游戏画面和声音，为 OBS 提供固定的 RTMP 拉流地址。关闭管理网页后，接收服务仍会运行。

## 使用条件

- PS5 已登录 PlayStation Network，并绑定开启了两步验证的 Twitch 账号。[PlayStation 开播指南](https://www.playstation.com/en-us/support/games/ps5-stream-gameplay-broadcast/) · [Twitch 开播排查](https://www.playstation.com/en-us/support/games/ps4-broadcast-and-share-play-troubleshooting/)
- PS5 和运行接收器的电脑在同一局域网，PS5 能访问电脑的 DNS `53/UDP`、`53/TCP` 和 RTMP `1935/TCP`。接收器可用 Docker Compose 部署在 macOS、Linux 或使用 Linux 容器的 Windows。OBS 可以装在接收器电脑上，也可以装在局域网内另一台能访问接收器的电脑上。
- PS5 开播前需要访问 Twitch 的 HTTPS 接口，包括 `api.twitch.tv:443`。如果 PS5 用原本的网络设置就能正常开播，可以用直连方案。在本次中国大陆家庭宽带测试中，直连在 TLS 握手阶段失败。使用 **Mac 版 UU 主机加速**并关闭 Clash 后，已验证 PS5 能重新开播且中继能收流。随后也确认了 OBS 能播放中继的画面和声音。

## 启动接收器

在项目目录里，从 `.env.example` 创建 `.env`，并在文件中填写自己的 `RELAY_PASSWORD`。已有 `.env` 时可以直接启动。

macOS 和 Linux 可以运行启动脚本。脚本会读取电脑的局域网 IP，并打印管理地址。

```sh
./start.sh
```

Windows 或希望直接使用 Compose 时，运行以下命令。用运行 Docker 的电脑的局域网 IPv4 和第二条命令返回的端口打开管理页，在「总览」点「刷新」。

```sh
docker compose up -d --build
docker compose port playstream 8080
```

用用户名 `driver` 和 `.env` 中设置的密码登录。在「直播对象」里新建对象，例如别名为 `gt7_ps5`。第一个启用的对象会自动选中。需要换对象时点「切换」，新选择在下次 PS5 开播时生效。

## 方案一 · Mac 版 UU 主机加速

适用于 PS5 直连 Twitch 开播失败、使用 Mac 版 UU 可以开播的网络。以下组合已实测。接收器和 UU 运行在同一台 Mac，路由器端的 UU 不开启，PS5 的「代理服务器」选「不使用」。

1. 在 Mac 版 UU 启动对应的 PlayStation 主机加速。按 UU 界面显示的值，在 PS5 的「设置 → 网络 → 设定互联网连接 → 当前网络 → 高级设置」里填写手动 IP、子网掩码和默认网关。初次设置可以先用 UU 给的 DNS，确认能打开 Twitch 开播界面，再按以下步骤接入中继。
2. 用 UU 显示的**默认网关地址**访问管理页，端口仍用 `./start.sh` 打印的管理端口。例如网关是 `172.22.31.42`，管理端口是 `32769`，就打开 `http://172.22.31.42:32769`。在「总览」点「刷新」，确认「PS5 首选 DNS」显示这个地址。
3. 在中继的「设置 → 高级网络设置 → 上游 DNS 覆盖」填入 **UU 给出的 DNS**，然后保存。这是中继转发普通 DNS 查询使用的地址。
4. 在 PS5 的同一网络设置里，将**首选 DNS**填为中继总览的地址，将**备用 DNS**填为 UU 给出的 DNS。IP、掩码和默认网关沿用第 1 步。
5. 在 PS5 选择 Twitch 并开始直播，确认中继的直播对象显示「接收中」。

本次实测参数如下。实际配置以自己的 UU 界面为准。

| 位置 | 本次实测值 |
| --- | --- |
| PS5 手动 IP | `172.22.32.42` |
| PS5 子网掩码 | `255.255.0.0` |
| PS5 默认网关、首选 DNS | `172.22.31.42` |
| PS5 备用 DNS、中继上游 DNS | `6.6.6.6` |
| PS5 代理服务器 | 不使用 |

开播时保持 UU 主机加速和中继运行。重启中继后若总览显示的是 Mac 的普通局域网地址，再按第 2 步刷新。也可以用 `RELAY_HOST_IP=172.22.31.42 ./start.sh` 启动，将示例 IP 换成当前 UU 网关。UU 重新加速后如果给出了不同参数，重新核对 PS5 的网络设置与中继总览地址。[网易 UU macOS 主机加速帮助](https://uu.baike.netease.com/gonglue/357.html)

## 方案二 · PS5 可直连 Twitch

适用于 PS5 在当前网络上能直接开播的情况，无需 UU。以下是按当前接收器实现给出的配置流程，本次家庭宽带无法实测直连开播。

1. 先让 PS5 用原有网络设置确认 Twitch 可以开播，再停播并启动中继。
2. 在管理页「总览」确认「PS5 首选 DNS」是电脑在当前局域网的 IPv4 地址。如果显示的是之前 UU 的网段或旧地址，用电脑当前的局域网 IP 打开管理页并点「刷新」。在「设置 → 高级网络设置」将「上游 DNS 覆盖」留空，沿用系统或 Docker 的解析器。从方案一切换过来时，清除原来填写的 UU DNS。
3. 在 PS5 的网络高级设置里，将 IP 地址恢复为「自动」（家里原先使用固定 IP 的，恢复原来的 IP、掩码和网关）。代理服务器选「不使用」，DNS 选「手动」，首选填中继总览的地址。备用 DNS 留空。如果主机要求填写且接受相同地址，可以两项都填中继地址，避免查询绕过中继。
4. 重新在 PS5 开启 Twitch 直播，确认中继显示「接收中」。

## 在 OBS 里接收画面和声音

1. 在 OBS 主窗口底部的「源」面板点 **＋**，在左侧选**媒体**，然后点右侧「添加新的 媒体」。有些版本会把这个选项叫作「媒体源」（Media Source）。可以把新来源命名为「PS5」。
2. 在媒体设置中取消勾选 **「本地文件」**，这时才会出现 **「输入」**。将中继直播对象卡片上的 RTMP 地址粘贴到这里，「输入格式」留空，再点「确定」。
3. OBS 和中继在同一台电脑时，可以用 `rtmp://127.0.0.1:1935/gt7_ps5`，将 `gt7_ps5` 换成实际对象别名。OBS 在另一台电脑时，使用卡片上的地址，并确认那台电脑能访问该 IP 和端口。
4. 在 PS5 开播后检查 OBS 预览和混音器的媒体音量电平。若中继已显示「接收中」但 OBS 仍无画面，可在 OBS 选中该来源并重新开始播放。需要录本地视频时点「开始录制」。[OBS 媒体源文档](https://obsproject.com/kb/media-sources)

## 排查与恢复

- **PS5 选 Twitch 就报 `api.twitch.tv:443` 握手失败**。先验证当前方案的开播网络是否可用。用 UU 时核对 Mac 加速状态、PS5 的手动地址和两项 DNS。切回直连时，可以先将 PS5 DNS 恢复为自动，再验证原生 Twitch 开播。
- **PS5 显示直播中，中继却没有接收**。在中继打开「诊断」，开始检测后重新开播，检查有没有「已重写」推流域名和「已收到 RTMP 视频流」。确认 PS5 的首选 DNS 是当前接收器地址，宿主机的 DNS 和 RTMP 端口也可访问。
- **启动服务失败**。运行 `docker compose logs playstream` 查看原因。管理页默认由 Docker 分配空闲端口，可以用 `RELAY_WEB_PORT` 指定。DNS 和 RTMP 分别需要 `53` 和 `1935`。
- **停用接收器**。先将 PS5 网络恢复成使用前的设置。若继续用 Mac 版 UU，可以把 PS5 DNS 恢复成 UU 给出的值。若也停用 UU，将 PS5 恢复原来的 IP、网关和 DNS，再停止接收器。

## 开发

使用 Go 1.25 或更新版本，RTMP 由 [MediaMTX](https://github.com/bluenviron/mediamtx/tree/v1.21.3) 提供。运行配置保存在 `data/config.yaml`，可在管理页导入或导出。

PlayStream 按 [Twitch 官方推流入口列表](https://dev.twitch.tv/docs/video-broadcast/reference/ingest) 识别 RTMP 推流域名，并兼容 `live.twitch.tv`。
