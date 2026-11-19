# HeyGen 数字人视频接入

HeyGen 已作为独立分组平台接入。当前支持 HeyGen v3 的图片数字人 + 音频异步视频流程；客户端通过 Sub2API API Key 调用，HeyGen API Key 只保存在上游账号中。

## 上游账号与分组

1. 在账号管理中创建 **HeyGen** 平台 API Key 账号。
2. Base URL 使用 `https://api.heygen.com/v3`，API Key 使用 HeyGen 控制台签发的 Key。
3. 将账号加入 HeyGen 分组，为目标分辨率设置视频单价，并把 `heygen-avatar-video` 加入模型白名单（如果分组启用了白名单）。未配置单价时，创建任务会被拒绝。
4. 若该 Sub2API API Key 由 Kox 管理，视频完成时的用量记录会沿用现有结算和 Kox 用量同步流程。

## 创建数字人视频

HeyGen 能访问的公网 URL 可直接作为图片和音频输入。也可传已上传到同一个 HeyGen 账号中的 `asset_id`。
当前网关定价档支持 `720p` 和 `1080p`，每个分辨率都需要先配置分组单价。

```bash
curl "$BASE_URL/v1/videos" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "heygen-avatar-video",
    "type": "image",
    "image": {"type": "url", "url": "https://example.com/avatar.png"},
    "audio_url": "https://example.com/speech.mp3",
    "aspect_ratio": "9:16",
    "resolution": "720p",
    "fit": "contain",
    "expressiveness": "medium",
    "output_format": "mp4",
    "title": "数字人测试"
  }'
```

创建响应返回 HeyGen `video_id`。通过 `GET /v1/videos/{video_id}` 查询，直到 `data.status` 为 `completed` 或 `failed`。成功结果中的 `data.video_url` 是临时视频链接。

任务查询由创建任务的同一把 Sub2API API Key 发起，并固定回创建时选择的 HeyGen 上游账号。

## 计费

成功轮询后，Sub2API 使用现有视频用量记录流程，按输出时长、分辨率和 HeyGen 分组的视频价格结算。Kox 同步复用现有 `RecordUsage` 链路；HeyGen 接入没有新增 Kox 计费逻辑。

HeyGen 按上游完成响应中的 `data.duration` 向上取整到整数秒计费，不受 Grok 的 15 秒上限影响。例如 68.7281 秒按 69 秒计算，分组价格为 `$0.01/秒` 且倍率为 1 时，费用为 `$0.69`。这个取整规则是 Sub2API 的计费口径，不代表 HeyGen 官方扣费规则。失败或缺少有效时长的任务不按默认 8 秒结算。

HeyGen 账号余额查询和实际扣费核对尚未完成；在这些核对完成前，请不要据此指南推定 HeyGen 上游的实际扣费单位或成本。
