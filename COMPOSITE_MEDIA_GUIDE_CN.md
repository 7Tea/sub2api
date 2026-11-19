# 一把 Composite Key 调用语音和数字人视频

## 配置

1. 保留现有上游账号和各自 Base URL：TokenHub、MediaKit 注册为 OpenAI API Key 账号，HeyGen 注册为 HeyGen API Key 账号。
2. 把这三个账号加入同一个 Composite 分组，原有分组可同时保留。账号需启用各自的 `tokenhub_speech` / `mediakit_asr` 能力，HeyGen 无需 OpenAI 能力开关。
3. 为该 Composite 分组配置 TTS 每百万字符价、STT 每小时价、HeyGen 720p / 1080p 每秒价。请求使用组合分组的价格，不继承账号其他分组的价格。
4. API Key 绑定这个 Composite 分组。如果开启分组模型白名单，加入下面实际使用的模型。

| 请求模型 | 请求路径 | 账号平台 |
| --- | --- | --- |
| `minimax-speech-2.8-hd` 等已支持的 `minimax-speech-*` | `POST /v1/tts` | OpenAI / TokenHub |
| `minimax-voice-clone` | `POST /v1/custom-voices` | OpenAI / TokenHub |
| `volcengine-asr-subtitles` | `POST /v1/audio/transcriptions/tasks` | OpenAI / MediaKit |
| `heygen-avatar-video` | `POST /v1/videos` | HeyGen |

这些标准模型有内置平台识别；显式 Composite 路由和账号模型归属仍具有更高优先级。若配置显式路由，TokenHub 语音模型的目标应选 **OpenAI**，不是文本 MiniMax 平台；HeyGen 目标选 **HeyGen**。自定义别名使用 endpoint=`any` 的显式路由映射到表中的原生模型。

## 异步查询

- ASR：`GET /v1/audio/transcriptions/tasks/{id}`。
- HeyGen：`GET /v1/videos/{video_id}`。
- 查询继续使用创建任务的同一把 Key。服务从用户、Key、分组和任务 ID 绑定中找回原账号；视频查询根据绑定账号的平台分发，不猜测平台、不重新选择另一家账号。
- 所有权绑定与现有任务缓存一样保留 24 小时；缓存丢失或绑定过期时不能仅凭第三方任务 ID 恢复访问。

## 计费边界

沿用已有 Sub2API 语音与视频计算、原分组倍率、任务去重和 Kox 结算链路。本补丁不改 Kox 核心、事务、outbox、webhook、数据库或 Docker 配置；不重新计算已结算的历史任务。

HeyGen 继续按上游完成时长向上取整到秒，例如 68.7281 秒按 69 秒。ASR 按结果时长折算小时，TTS 按上游字符用量折算百万字符。启用 Composite 后要在组合分组重新配置价格，不能假定原分组价会自动继承。
