# TokenHub MiniMax TTS 与音色复刻

## 上游账号

可复用已有的 TokenHub OpenAI API Key 账号，也可单独建账号：

- Base URL：`https://tokenhub.tencentmaas.com`
- API Key：TokenHub API Key
- OpenAI Endpoint Capabilities：启用 **TokenHub MiniMax TTS / Voice Clone**
- 将账号加入 OpenAI 分组

调用方使用关联到该分组的 Sub2API API Key。若分组开启模型白名单，将对应模型 ID 加入白名单。

## MiniMax TTS

接口：`POST /v1/tts`

模型：

- `minimax-speech-2.8-hd`
- `minimax-speech-2.8-turbo`
- `minimax-speech-2.6-hd`
- `minimax-speech-2.6-turbo`
- `minimax-speech-02-hd`
- `minimax-speech-02-turbo`

请求使用 TokenHub MiniMax 原生 JSON 字段，响应也保留原生 JSON。`output_format` 可选 `url` 或 `hex`；推荐 `url` 以免把较大的音频十六进制内容塞进响应。

```bash
curl "$BASE_URL/v1/tts" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "minimax-speech-2.8-hd",
    "text": "你好，这是 MiniMax 语音合成测试。",
    "voice_setting": {
      "voice_id": "minimax_55e81114-ce7e-4ca1",
      "speed": 1,
      "vol": 1,
      "pitch": 0
    },
    "audio_setting": {
      "sample_rate": 32000,
      "bitrate": 128000,
      "format": "mp3",
      "channel": 1
    },
    "output_format": "url"
  }'
```

`stream: true` 暂不支持；当前适配调用 TokenHub 同步语音合成端点。

## 音色复刻

接口：`POST /v1/custom-voices`

`model` 固定为 `minimax-voice-clone`。输入音频可用公网 `audio_url`、Base64 `audio_data` 或 `file_id`。响应中的 `voice_id` 可用于后续 TTS。

```bash
curl "$BASE_URL/v1/custom-voices" \
  -H "Authorization: Bearer $SUB2API_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "minimax-voice-clone",
    "audio_url": "https://example.com/voice-sample.wav",
    "tts_model": "minimax-speech-2.8-hd",
    "text": "你好，这是复刻音色试听。",
    "output_format": "url"
  }'
```

本地将合成与克隆作为两个独立模型收费：成功创建并返回 `voice_id` 的 `minimax-voice-clone` 请求按一次克隆收费，使用该模型的「按次」默认价卡；即使不传试听文字、上游报告 `use_count=0`，也不跳过本地克隆售价。包含试听的克隆请求仍按同一条克隆价卡收一次，不额外添加 TTS 字符账单。失败、缺少有效 `voice_id` 或缺少克隆价卡时不按成功克隆结算。

普通 `minimax-speech-*` 合成继续按原有字符价收费，使用克隆音色也不自动附加本地克隆费。上游可能延后收取的激活成本与这里的客户售价是两件事；这里明确选择“成功创建克隆即向客户收费”的本地规则。

配置方法：在 Key 所绑定分组的「分组逐模型定价」新增 `minimax-voice-clone`，选择「按次」，填写默认单次价格（USD），不加层级。所提供的上游价表为 `990000 / 1000000 × 10 = 9.9 元/音色`；若按示例汇率 6.7 换算，对应约 `$1.4776/次`。实际售价由管理员决定，代码不硬编码金额，也不自动换汇。余额不足一整次克隆费用时，在创建上游音色前拒绝请求；历史已成功的克隆不追扣。

## 计费

在 Key 所绑定的 OpenAI 或 Composite 分组中，打开已有的「分组逐模型定价 → 添加模型价格」，填写实际请求的 TTS 模型，选择「按次」，默认单次价格填写 **每百万字符的美元价格**。此处复用按次价卡存储单价，实际计费单位是百万字符，并非每次请求。不同模型可以各建一条价卡；详细步骤见 [逐模型定价教程](MEDIA_MODEL_PRICING_CN.md)。

TTS 响应的 `usage.chars` 或 `extra_info.usage_characters` 除以 1,000,000 后进入现有语音结算，不生成虚拟 token。已配置的逐模型价格优先于旧的分组 TTS 通用价格；没有逐模型价卡时保留原有通用价格及默认值回退行为。

账号级模型映射可把自定义公开模型名映射到上述 MiniMax 模型 ID；音色复刻使用固定模型 ID `minimax-voice-clone`。
