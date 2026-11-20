# Volcengine MediaKit ASR 使用说明

本适配对应 `video-tools/workflows/mixclip/api_asr.py` 使用的 MediaKit 字幕识别 API。它与火山 OpenSpeech 原生 AUC submit/query 协议不同。

## 上游账号

在 Sub2API「账号管理」新增 **OpenAI API Key** 账号：

- Base URL：`https://mediakit.cn-beijing.volces.com/api/v1`
- API Key：MediaKit 的 Bearer Key
- OpenAI Endpoint Capabilities：勾选 **Volcengine MediaKit ASR**
- 将账号加入目标 OpenAI 分组

选择能力开关会自动预填 Base URL。此账号不要求启用 Chat Completions，也不应选 Seedance 或 TokenHub Video。

## 调用模型

模型名固定为 `volcengine-asr-subtitles`。若分组启用了模型白名单，将它加入白名单。

输入必须是 MediaKit 可访问的公网 `audio_url` 或 `video_url`。当前不接收本地音频文件上传。可选参数：

- `language`，默认 `cmn-Hans-CN`
- `content_type`，默认 `speech`
- `enable_speaker_info`
- `enable_confidence`
- `callback_args`
- `queue_id`
- `client_token`，不传时由 Sub2API 生成

## 提交任务

```bash
export BASE_URL='https://<sub2api-host>'
export SUB2API_API_KEY='<Sub2API API Key>'

curl "$BASE_URL/v1/audio/transcriptions/tasks" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "volcengine-asr-subtitles",
    "audio_url": "https://example.com/meeting.mp3",
    "language": "cmn-Hans-CN",
    "enable_speaker_info": true
  }'
```

成功时返回任务 `id` 和 `queued` 状态。

## 查询任务

把提交响应中的 `id` 原样带入路径：

```bash
curl "$BASE_URL/v1/audio/transcriptions/tasks/<TASK_ID>" \
  -H "Authorization: Bearer $SUB2API_API_KEY"
```

任务状态为 `queued` 或 `running` 时继续轮询。成功后响应的 `result` 保留 MediaKit 字幕结果（如 `subtitles`、分句时间、说话人和字幕信息）；失败时返回 `error`。任务只能由创建它的同一把 Sub2API API Key 查询，查询会固定回原上游账号。

## 计费

在 Key 所绑定的 OpenAI 或 Composite 分组中，打开已有的「分组逐模型定价 → 添加模型价格」，填写 `volcengine-asr-subtitles`，选择「按次」，默认单次价格填写 **每小时识别音频的美元价格**。这里的单位是音频小时，并非每个任务，也不是等待任务的耗时。详细步骤见 [逐模型定价教程](MEDIA_MODEL_PRICING_CN.md)。

识别成功后，Sub2API 从结果中的音频时长或分句结束时间计算 STT 小时数，通过已有用量结算按模型价卡收费。若上游结果没有可用时长，就不会生成 STT 费用记录。没有逐模型价卡时，保留旧分组 STT 通用价格及现有默认值回退行为。

Sub2API 计算出费用后照常走用量结算；若该 API Key 已关联 Kox，Kox 接收的是 Sub2API 算出的金额。本适配没有修改 Kox 核心。
