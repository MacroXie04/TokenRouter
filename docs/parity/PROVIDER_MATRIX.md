# Provider Matrix

Channel types and provider adapters in the reference system.

| # | Channel / Provider | Detail | Status | Target Evidence |
|---|---|---|---|---|
| 1 | ChannelTypeUnknown | Channel type 0 (reserved/unknown); no dedicated adapter, falls back to openai.Adaptor | NOT_STARTED |  |
| 2 | ChannelTypeOpenAI | Channel type 1; maps to APITypeOpenAI -> relay/channel/openai.Adaptor | NOT_STARTED |  |
| 3 | ChannelTypeMidjourney | Channel type 2; Midjourney proxy channel, no Go adaptor (handled via mjproxy/RelayFormatMjProxy) | NOT_STARTED |  |
| 4 | ChannelTypeAzure | Channel type 3; Azure OpenAI, uses openai.Adaptor fallback with Azure base URL/headers | NOT_STARTED |  |
| 5 | ChannelTypeOllama | Channel type 4; maps to APITypeOllama -> relay/channel/ollama.Adaptor | NOT_STARTED |  |
| 6 | ChannelTypeMidjourneyPlus | Channel type 5; Midjourney Plus proxy channel, handled via mjproxy path | NOT_STARTED |  |
| 7 | ChannelTypeOpenAIMax | Channel type 6; OpenAI-Max reseller, openai.Adaptor fallback (base https://api.openaimax.com) | NOT_STARTED |  |
| 8 | ChannelTypeOhMyGPT | Channel type 7; OhMyGPT reseller, openai.Adaptor fallback (base https://api.ohmygpt.com) | NOT_STARTED |  |
| 9 | ChannelTypeCustom | Channel type 8; custom OpenAI-compatible endpoint, openai.Adaptor fallback | NOT_STARTED |  |
| 10 | ChannelTypeAILS | Channel type 9; AILS reseller, openai.Adaptor fallback | NOT_STARTED |  |
| 11 | ChannelTypeAIProxy | Channel type 10; AIProxy reseller, openai.Adaptor fallback | NOT_STARTED |  |
| 12 | ChannelTypePaLM | Channel type 11; maps to APITypePaLM -> relay/channel/palm.Adaptor | NOT_STARTED |  |
| 13 | ChannelTypeAPI2GPT | Channel type 12; API2GPT reseller, openai.Adaptor fallback | NOT_STARTED |  |
| 14 | ChannelTypeAIGC2D | Channel type 13; AIGC2D reseller, openai.Adaptor fallback | NOT_STARTED |  |
| 15 | ChannelTypeAnthropic | Channel type 14; maps to APITypeAnthropic -> relay/channel/claude.Adaptor | NOT_STARTED |  |
| 16 | ChannelTypeBaidu | Channel type 15; maps to APITypeBaidu -> relay/channel/baidu.Adaptor | NOT_STARTED |  |
| 17 | ChannelTypeZhipu | Channel type 16; maps to APITypeZhipu -> relay/channel/zhipu.Adaptor | NOT_STARTED |  |
| 18 | ChannelTypeAli | Channel type 17; maps to APITypeAli -> relay/channel/ali.Adaptor | NOT_STARTED |  |
| 19 | ChannelTypeXunfei | Channel type 18; maps to APITypeXunfei -> relay/channel/xunfei.Adaptor | NOT_STARTED |  |
| 20 | ChannelType360 | Channel type 19; 360 AI; package relay/channel/ai360 holds only constants (no Adaptor), openai fallback | NOT_STARTED |  |
| 21 | ChannelTypeOpenRouter | Channel type 20; maps to APITypeOpenRouter -> relay/channel/openai.Adaptor | NOT_STARTED |  |
| 22 | ChannelTypeAIProxyLibrary | Channel type 21; maps to APITypeAIProxyLibrary, which has no GetAdaptor case (returns nil) | NOT_STARTED |  |
| 23 | ChannelTypeFastGPT | Channel type 22; FastGPT channel, openai.Adaptor fallback | NOT_STARTED |  |
| 24 | ChannelTypeTencent | Channel type 23; maps to APITypeTencent -> relay/channel/tencent.DispatchAdaptor | NOT_STARTED |  |
| 25 | ChannelTypeGemini | Channel type 24; maps to APITypeGemini -> relay/channel/gemini.Adaptor | NOT_STARTED |  |
| 26 | ChannelTypeMoonshot | Channel type 25; maps to APITypeMoonshot -> relay/channel/moonshot.Adaptor (Claude API) | NOT_STARTED |  |
| 27 | ChannelTypeZhipu_v4 | Channel type 26; maps to APITypeZhipuV4 -> relay/channel/zhipu_4v.Adaptor | NOT_STARTED |  |
| 28 | ChannelTypePerplexity | Channel type 27; maps to APITypePerplexity -> relay/channel/perplexity.Adaptor | NOT_STARTED |  |
| 29 | ChannelTypeLingYiWanWu | Channel type 31; 01.AI/LingYiWanWu; package relay/channel/lingyiwanwu holds only constants (no Adaptor), openai fallback | NOT_STARTED |  |
| 30 | ChannelTypeAws | Channel type 33; maps to APITypeAws -> relay/channel/aws.Adaptor | NOT_STARTED |  |
| 31 | ChannelTypeCohere | Channel type 34; maps to APITypeCohere -> relay/channel/cohere.Adaptor | NOT_STARTED |  |
| 32 | ChannelTypeMiniMax | Channel type 35; maps to APITypeMiniMax -> relay/channel/minimax.Adaptor | NOT_STARTED |  |
| 33 | ChannelTypeSunoAPI | Channel type 36; Suno music, task-based -> relay/channel/task/suno.TaskAdaptor | NOT_STARTED |  |
| 34 | ChannelTypeDify | Channel type 37; maps to APITypeDify -> relay/channel/dify.Adaptor | NOT_STARTED |  |
| 35 | ChannelTypeJina | Channel type 38; maps to APITypeJina -> relay/channel/jina.Adaptor | NOT_STARTED |  |
| 36 | ChannelCloudflare | Channel type 39; maps to APITypeCloudflare -> relay/channel/cloudflare.Adaptor | NOT_STARTED |  |
| 37 | ChannelTypeSiliconFlow | Channel type 40; maps to APITypeSiliconFlow -> relay/channel/siliconflow.Adaptor | NOT_STARTED |  |
| 38 | ChannelTypeVertexAi | Channel type 41; maps to APITypeVertexAi -> relay/channel/vertex.Adaptor | NOT_STARTED |  |
| 39 | ChannelTypeMistral | Channel type 42; maps to APITypeMistral -> relay/channel/mistral.Adaptor | NOT_STARTED |  |
| 40 | ChannelTypeDeepSeek | Channel type 43; maps to APITypeDeepSeek -> relay/channel/deepseek.Adaptor | NOT_STARTED |  |
| 41 | ChannelTypeMokaAI | Channel type 44; maps to APITypeMokaAI -> relay/channel/mokaai.Adaptor | NOT_STARTED |  |
| 42 | ChannelTypeVolcEngine | Channel type 45; maps to APITypeVolcEngine -> relay/channel/volcengine.Adaptor | NOT_STARTED |  |
| 43 | ChannelTypeBaiduV2 | Channel type 46; maps to APITypeBaiduV2 -> relay/channel/baidu_v2.Adaptor | NOT_STARTED |  |
| 44 | ChannelTypeXinference | Channel type 47; maps to APITypeXinference -> relay/channel/openai.Adaptor | NOT_STARTED |  |
| 45 | ChannelTypeXai | Channel type 48; maps to APITypeXai -> relay/channel/xai.Adaptor | NOT_STARTED |  |
| 46 | ChannelTypeCoze | Channel type 49; maps to APITypeCoze -> relay/channel/coze.Adaptor | NOT_STARTED |  |
| 47 | ChannelTypeKling | Channel type 50; Kling video, task-based -> relay/channel/task/kling.TaskAdaptor | NOT_STARTED |  |
| 48 | ChannelTypeJimeng | Channel type 51; maps to APITypeJimeng -> relay/channel/jimeng.Adaptor (also task/jimeng) | NOT_STARTED |  |
| 49 | ChannelTypeVidu | Channel type 52; Vidu video, task-based -> relay/channel/task/vidu.TaskAdaptor | NOT_STARTED |  |
| 50 | ChannelTypeSubmodel | Channel type 53; maps to APITypeSubmodel -> relay/channel/submodel.Adaptor | NOT_STARTED |  |
| 51 | ChannelTypeDoubaoVideo | Channel type 54; Doubao video, task-based -> relay/channel/task/doubao.TaskAdaptor | NOT_STARTED |  |
| 52 | ChannelTypeSora | Channel type 55; Sora video, task-based -> relay/channel/task/sora.TaskAdaptor | NOT_STARTED |  |
| 53 | ChannelTypeReplicate | Channel type 56; maps to APITypeReplicate -> relay/channel/replicate.Adaptor | NOT_STARTED |  |
| 54 | ChannelTypeCodex | Channel type 57; ChatGPT subscription (Codex); maps to APITypeCodex -> relay/channel/codex.Adaptor | NOT_STARTED |  |
| 55 | ChannelTypeAdvancedCustom | Channel type 58; maps to APITypeAdvancedCustom -> relay/channel/advancedcustom.Adaptor | NOT_STARTED |  |
| 56 | ChannelTypeSub2API | Channel type 59; maps to APITypeSub2API -> relay/channel/sub2api.Adaptor | NOT_STARTED |  |
| 57 | ChannelTypeNewAPI | Channel type 60; New API gateway-to-gateway; maps to APITypeNewAPI -> relay/channel/newapi.Adaptor | NOT_STARTED |  |
| 58 | ChannelTypeDummy | Channel type 61; sentinel for count only, not a real channel | NOT_STARTED |  |
| 59 | APITypeOpenAI | API type 0 (iota); adapter openai.Adaptor | NOT_STARTED |  |
| 60 | APITypeAnthropic | API type 1; adapter claude.Adaptor | NOT_STARTED |  |
| 61 | APITypePaLM | API type 2; adapter palm.Adaptor | NOT_STARTED |  |
| 62 | APITypeBaidu | API type 3; adapter baidu.Adaptor | NOT_STARTED |  |
| 63 | APITypeZhipu | API type 4; adapter zhipu.Adaptor | NOT_STARTED |  |
| 64 | APITypeAli | API type 5; adapter ali.Adaptor | NOT_STARTED |  |
| 65 | APITypeXunfei | API type 6; adapter xunfei.Adaptor | NOT_STARTED |  |
| 66 | APITypeAIProxyLibrary | API type 7; no GetAdaptor case (returns nil) | NOT_STARTED |  |
| 67 | APITypeTencent | API type 8; adapter tencent.DispatchAdaptor | NOT_STARTED |  |
| 68 | APITypeGemini | API type 9; adapter gemini.Adaptor | NOT_STARTED |  |
| 69 | APITypeZhipuV4 | API type 10; adapter zhipu_4v.Adaptor | NOT_STARTED |  |
| 70 | APITypeOllama | API type 11; adapter ollama.Adaptor | NOT_STARTED |  |
| 71 | APITypePerplexity | API type 12; adapter perplexity.Adaptor | NOT_STARTED |  |
| 72 | APITypeAws | API type 13; adapter aws.Adaptor | NOT_STARTED |  |
| 73 | APITypeCohere | API type 14; adapter cohere.Adaptor | NOT_STARTED |  |
| 74 | APITypeDify | API type 15; adapter dify.Adaptor | NOT_STARTED |  |
| 75 | APITypeJina | API type 16; adapter jina.Adaptor | NOT_STARTED |  |
| 76 | APITypeCloudflare | API type 17; adapter cloudflare.Adaptor | NOT_STARTED |  |
| 77 | APITypeSiliconFlow | API type 18; adapter siliconflow.Adaptor | NOT_STARTED |  |
| 78 | APITypeVertexAi | API type 19; adapter vertex.Adaptor | NOT_STARTED |  |
| 79 | APITypeMistral | API type 20; adapter mistral.Adaptor | NOT_STARTED |  |
| 80 | APITypeDeepSeek | API type 21; adapter deepseek.Adaptor | NOT_STARTED |  |
| 81 | APITypeMokaAI | API type 22; adapter mokaai.Adaptor | NOT_STARTED |  |
| 82 | APITypeVolcEngine | API type 23; adapter volcengine.Adaptor | NOT_STARTED |  |
| 83 | APITypeBaiduV2 | API type 24; adapter baidu_v2.Adaptor | NOT_STARTED |  |
| 84 | APITypeOpenRouter | API type 25; adapter openai.Adaptor | NOT_STARTED |  |
| 85 | APITypeXinference | API type 26; adapter openai.Adaptor | NOT_STARTED |  |
| 86 | APITypeXai | API type 27; adapter xai.Adaptor | NOT_STARTED |  |
| 87 | APITypeCoze | API type 28; adapter coze.Adaptor | NOT_STARTED |  |
| 88 | APITypeJimeng | API type 29; adapter jimeng.Adaptor | NOT_STARTED |  |
| 89 | APITypeMoonshot | API type 30; adapter moonshot.Adaptor | NOT_STARTED |  |
| 90 | APITypeSubmodel | API type 31; adapter submodel.Adaptor | NOT_STARTED |  |
| 91 | APITypeMiniMax | API type 32; adapter minimax.Adaptor | NOT_STARTED |  |
| 92 | APITypeReplicate | API type 33; adapter replicate.Adaptor | NOT_STARTED |  |
| 93 | APITypeCodex | API type 34; adapter codex.Adaptor | NOT_STARTED |  |
| 94 | APITypeAdvancedCustom | API type 35; adapter advancedcustom.Adaptor | NOT_STARTED |  |
| 95 | APITypeSub2API | API type 36; adapter sub2api.Adaptor | NOT_STARTED |  |
| 96 | APITypeNewAPI | API type 37; adapter newapi.Adaptor | NOT_STARTED |  |
| 97 | APITypeDummy | API type 38; sentinel for count only | NOT_STARTED |  |
| 98 | adapter/advancedcustom | Provider adapter package relay/channel/advancedcustom (Adaptor for APITypeAdvancedCustom) | NOT_STARTED |  |
| 99 | adapter/ai360 | Provider package relay/channel/ai360 (constants only, no Adaptor; 360 AI OpenAI-compatible) | NOT_STARTED |  |
| 100 | adapter/ali | Provider adapter package relay/channel/ali (Alibaba DashScope) | NOT_STARTED |  |
| 101 | adapter/aws | Provider adapter package relay/channel/aws (Amazon Bedrock) | NOT_STARTED |  |
| 102 | adapter/baidu | Provider adapter package relay/channel/baidu (Baidu Qianfan/Wenxin) | NOT_STARTED |  |
| 103 | adapter/baidu_v2 | Provider adapter package relay/channel/baidu_v2 (Baidu v2) | NOT_STARTED |  |
| 104 | adapter/claude | Provider adapter package relay/channel/claude (Anthropic Claude) | NOT_STARTED |  |
| 105 | adapter/cloudflare | Provider adapter package relay/channel/cloudflare (Cloudflare Workers AI) | NOT_STARTED |  |
| 106 | adapter/codex | Provider adapter package relay/channel/codex (ChatGPT subscription/Codex) | NOT_STARTED |  |
| 107 | adapter/cohere | Provider adapter package relay/channel/cohere | NOT_STARTED |  |
| 108 | adapter/coze | Provider adapter package relay/channel/coze (ByteDance Coze) | NOT_STARTED |  |
| 109 | adapter/deepseek | Provider adapter package relay/channel/deepseek | NOT_STARTED |  |
| 110 | adapter/dify | Provider adapter package relay/channel/dify | NOT_STARTED |  |
| 111 | adapter/gemini | Provider adapter package relay/channel/gemini (Google Gemini) | NOT_STARTED |  |
| 112 | adapter/jimeng | Provider adapter package relay/channel/jimeng (ByteDance Jimeng) | NOT_STARTED |  |
| 113 | adapter/jina | Provider adapter package relay/channel/jina (Jina AI, rerank/embeddings) | NOT_STARTED |  |
| 114 | adapter/lingyiwanwu | Provider package relay/channel/lingyiwanwu (constants only, no Adaptor; 01.AI) | NOT_STARTED |  |
| 115 | adapter/minimax | Provider adapter package relay/channel/minimax | NOT_STARTED |  |
| 116 | adapter/mistral | Provider adapter package relay/channel/mistral | NOT_STARTED |  |
| 117 | adapter/mokaai | Provider adapter package relay/channel/mokaai | NOT_STARTED |  |
| 118 | adapter/moonshot | Provider adapter package relay/channel/moonshot (Moonshot/Kimi, Claude API) | NOT_STARTED |  |
| 119 | adapter/newapi | Provider adapter package relay/channel/newapi (New API gateway-to-gateway) | NOT_STARTED |  |
| 120 | adapter/ollama | Provider adapter package relay/channel/ollama | NOT_STARTED |  |
| 121 | adapter/openai | Provider adapter package relay/channel/openai (also fallback for OpenAI-compatible channels) | NOT_STARTED |  |
| 122 | adapter/openrouter | Provider package relay/channel/openrouter (constants/dto only; routed through openai.Adaptor) | NOT_STARTED |  |
| 123 | adapter/palm | Provider adapter package relay/channel/palm (Google PaLM) | NOT_STARTED |  |
| 124 | adapter/perplexity | Provider adapter package relay/channel/perplexity | NOT_STARTED |  |
| 125 | adapter/replicate | Provider adapter package relay/channel/replicate | NOT_STARTED |  |
| 126 | adapter/siliconflow | Provider adapter package relay/channel/siliconflow | NOT_STARTED |  |
| 127 | adapter/sub2api | Provider adapter package relay/channel/sub2api | NOT_STARTED |  |
| 128 | adapter/submodel | Provider adapter package relay/channel/submodel (LLM SubModel) | NOT_STARTED |  |
| 129 | adapter/tencent | Provider adapter package relay/channel/tencent (Tencent Hunyuan, DispatchAdaptor) | NOT_STARTED |  |
| 130 | adapter/vertex | Provider adapter package relay/channel/vertex (Google Vertex AI) | NOT_STARTED |  |
| 131 | adapter/volcengine | Provider adapter package relay/channel/volcengine (ByteDance Volcano Engine/Ark) | NOT_STARTED |  |
| 132 | adapter/xai | Provider adapter package relay/channel/xai (x.AI Grok) | NOT_STARTED |  |
| 133 | adapter/xinference | Provider package relay/channel/xinference (constants/dto only; routed through openai.Adaptor) | NOT_STARTED |  |
| 134 | adapter/xunfei | Provider adapter package relay/channel/xunfei (iFlytek Spark) | NOT_STARTED |  |
| 135 | adapter/zhipu | Provider adapter package relay/channel/zhipu (Zhipu BigModel GLM) | NOT_STARTED |  |
| 136 | adapter/zhipu_4v | Provider adapter package relay/channel/zhipu_4v (Zhipu v4) | NOT_STARTED |  |
| 137 | adapter/task | Task adaptor parent package relay/channel/task defining TaskAdaptor sub-packages | NOT_STARTED |  |
| 138 | adapter/task/ali | Task adaptor relay/channel/task/ali (Ali video/task) | NOT_STARTED |  |
| 139 | adapter/task/doubao | Task adaptor relay/channel/task/doubao (Doubao video) | NOT_STARTED |  |
| 140 | adapter/task/gemini | Task adaptor relay/channel/task/gemini (Gemini video/task) | NOT_STARTED |  |
| 141 | adapter/task/hailuo | Task adaptor relay/channel/task/hailuo (MiniMax Hailuo video) | NOT_STARTED |  |
| 142 | adapter/task/jimeng | Task adaptor relay/channel/task/jimeng (Jimeng task) | NOT_STARTED |  |
| 143 | adapter/task/kling | Task adaptor relay/channel/task/kling (Kling video) | NOT_STARTED |  |
| 144 | adapter/task/sora | Task adaptor relay/channel/task/sora (Sora video) | NOT_STARTED |  |
| 145 | adapter/task/suno | Task adaptor relay/channel/task/suno (Suno music) | NOT_STARTED |  |
| 146 | adapter/task/taskcommon | Shared task helpers package relay/channel/task/taskcommon | NOT_STARTED |  |
| 147 | adapter/task/vertex | Task adaptor relay/channel/task/vertex (Vertex AI video/task) | NOT_STARTED |  |
| 148 | adapter/task/vidu | Task adaptor relay/channel/task/vidu (Vidu video) | NOT_STARTED |  |
| 149 | RelayFormatOpenAI | RelayFormat string "openai" | NOT_STARTED |  |
| 150 | RelayFormatClaude | RelayFormat string "claude" | NOT_STARTED |  |
| 151 | RelayFormatGemini | RelayFormat string "gemini" | NOT_STARTED |  |
| 152 | RelayFormatOpenAIResponses | RelayFormat string "openai_responses" | NOT_STARTED |  |
| 153 | RelayFormatOpenAIResponsesCompaction | RelayFormat string "openai_responses_compaction" | NOT_STARTED |  |
| 154 | RelayFormatOpenAIAlphaSearch | RelayFormat string "openai_alpha_search" | NOT_STARTED |  |
| 155 | RelayFormatOpenAIAudio | RelayFormat string "openai_audio" | NOT_STARTED |  |
| 156 | RelayFormatOpenAIImage | RelayFormat string "openai_image" | NOT_STARTED |  |
| 157 | RelayFormatOpenAIRealtime | RelayFormat string "openai_realtime" | NOT_STARTED |  |
| 158 | RelayFormatRerank | RelayFormat string "rerank" | NOT_STARTED |  |
| 159 | RelayFormatEmbedding | RelayFormat string "embedding" | NOT_STARTED |  |
| 160 | RelayFormatTask | RelayFormat string "task" | NOT_STARTED |  |
| 161 | RelayFormatMjProxy | RelayFormat string "mj_proxy" | NOT_STARTED |  |
| 162 | EndpointTypeOpenAI | EndpointType string "openai" | NOT_STARTED |  |
| 163 | EndpointTypeOpenAIResponse | EndpointType string "openai-response" | NOT_STARTED |  |
| 164 | EndpointTypeOpenAIResponseCompact | EndpointType string "openai-response-compact" | NOT_STARTED |  |
| 165 | EndpointTypeOpenAIAlphaSearch | EndpointType string "openai-alpha-search" | NOT_STARTED |  |
| 166 | EndpointTypeAnthropic | EndpointType string "anthropic" | NOT_STARTED |  |
| 167 | EndpointTypeGemini | EndpointType string "gemini" | NOT_STARTED |  |
| 168 | EndpointTypeJinaRerank | EndpointType string "jina-rerank" | NOT_STARTED |  |
| 169 | EndpointTypeImageGeneration | EndpointType string "image-generation" | NOT_STARTED |  |
| 170 | EndpointTypeEmbeddings | EndpointType string "embeddings" | NOT_STARTED |  |
| 171 | EndpointTypeOpenAIVideo | EndpointType string "openai-video" | NOT_STARTED |  |
| 172 | RelayModeUnknown | RelayMode 0 (iota) | NOT_STARTED |  |
| 173 | RelayModeChatCompletions | RelayMode 1 | NOT_STARTED |  |
| 174 | RelayModeCompletions | RelayMode 2 | NOT_STARTED |  |
| 175 | RelayModeEmbeddings | RelayMode 3 | NOT_STARTED |  |
| 176 | RelayModeModerations | RelayMode 4 | NOT_STARTED |  |
| 177 | RelayModeImagesGenerations | RelayMode 5 | NOT_STARTED |  |
| 178 | RelayModeImagesEdits | RelayMode 6 | NOT_STARTED |  |
| 179 | RelayModeEdits | RelayMode 7 | NOT_STARTED |  |
| 180 | RelayModeMidjourneyImagine | RelayMode 8 | NOT_STARTED |  |
| 181 | RelayModeMidjourneyDescribe | RelayMode 9 | NOT_STARTED |  |
| 182 | RelayModeMidjourneyBlend | RelayMode 10 | NOT_STARTED |  |
| 183 | RelayModeMidjourneyChange | RelayMode 11 | NOT_STARTED |  |
| 184 | RelayModeMidjourneySimpleChange | RelayMode 12 | NOT_STARTED |  |
| 185 | RelayModeMidjourneyNotify | RelayMode 13 | NOT_STARTED |  |
| 186 | RelayModeMidjourneyTaskFetch | RelayMode 14 | NOT_STARTED |  |
| 187 | RelayModeMidjourneyTaskImageSeed | RelayMode 15 | NOT_STARTED |  |
| 188 | RelayModeMidjourneyTaskFetchByCondition | RelayMode 16 | NOT_STARTED |  |
| 189 | RelayModeMidjourneyAction | RelayMode 17 | NOT_STARTED |  |
| 190 | RelayModeMidjourneyModal | RelayMode 18 | NOT_STARTED |  |
| 191 | RelayModeMidjourneyShorten | RelayMode 19 | NOT_STARTED |  |
| 192 | RelayModeSwapFace | RelayMode 20 | NOT_STARTED |  |
| 193 | RelayModeMidjourneyUpload | RelayMode 21 | NOT_STARTED |  |
| 194 | RelayModeMidjourneyVideo | RelayMode 22 | NOT_STARTED |  |
| 195 | RelayModeMidjourneyEdits | RelayMode 23 | NOT_STARTED |  |
| 196 | RelayModeAudioSpeech | RelayMode 24 (TTS) | NOT_STARTED |  |
| 197 | RelayModeAudioTranscription | RelayMode 25 (Whisper) | NOT_STARTED |  |
| 198 | RelayModeAudioTranslation | RelayMode 26 (Whisper) | NOT_STARTED |  |
| 199 | RelayModeSunoFetch | RelayMode 27 | NOT_STARTED |  |
| 200 | RelayModeSunoFetchByID | RelayMode 28 | NOT_STARTED |  |
| 201 | RelayModeSunoSubmit | RelayMode 29 | NOT_STARTED |  |
| 202 | RelayModeVideoFetchByID | RelayMode 30 | NOT_STARTED |  |
| 203 | RelayModeVideoSubmit | RelayMode 31 | NOT_STARTED |  |
| 204 | RelayModeRerank | RelayMode 32 | NOT_STARTED |  |
| 205 | RelayModeResponses | RelayMode 33 | NOT_STARTED |  |
| 206 | RelayModeRealtime | RelayMode 34 | NOT_STARTED |  |
| 207 | RelayModeGemini | RelayMode 35 | NOT_STARTED |  |
| 208 | RelayModeResponsesCompact | RelayMode 36 | NOT_STARTED |  |
| 209 | RelayModeAlphaSearch | RelayMode 37 | NOT_STARTED |  |
| 210 | TaskPlatformSuno | TaskPlatform string "suno" | NOT_STARTED |  |
| 211 | TaskPlatformMidjourney | TaskPlatform string "mj" | NOT_STARTED |  |
