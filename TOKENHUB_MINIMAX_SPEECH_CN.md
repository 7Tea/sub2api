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

复刻请求不含 `text` 时，TokenHub 文档说明不会立即收取复刻费用，适配器也不会按 TTS 字符计费；传入试听文本时，试听字符按分组 TTS 字符价格计费。复刻音色首次用于正式 TTS 的独立一次性费用没有单独映射到 Sub2API 价卡，需与 TokenHub 账单核对。

## 计费

在「分组管理 → 编辑 OpenAI 分组 → ASR / TTS 语音定价」设置 **TTS 每百万字符价格**。TTS 响应的 `usage.chars` 或 `extra_info.usage_characters` 进入 Sub2API TTS 字符计费。MediaKit ASR 使用同一区域的 STT 每小时价格。

账号级模型映射可把自定义公开模型名映射到上述 MiniMax 模型 ID；音色复刻使用固定模型 ID `minimax-voice-clone`。
