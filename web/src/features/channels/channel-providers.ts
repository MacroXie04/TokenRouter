export const CHANNEL_PROVIDER_OPTIONS = [
  { value: 1, label: 'OpenAI' },
  { value: 14, label: 'Anthropic' },
  { value: 33, label: 'AWS' },
  { value: 24, label: 'Gemini' },
  { value: 43, label: 'DeepSeek' },
  { value: 3, label: 'Azure' },
  { value: 41, label: 'Vertex AI' },
  { value: 48, label: 'xAI' },
  { value: 60, label: 'NewAPI' },
  { value: 58, label: 'Advanced Custom' },
  { value: 42, label: 'Mistral' },
  { value: 34, label: 'Cohere' },
  { value: 20, label: 'OpenRouter' },
  { value: 4, label: 'Ollama' },
  { value: 40, label: 'SiliconFlow' },
  { value: 27, label: 'Perplexity' },
  { value: 25, label: 'Moonshot' },
  { value: 17, label: 'Aliyun' },
  { value: 26, label: 'Zhipu v4' },
  { value: 15, label: 'Baidu' },
  { value: 46, label: 'Baidu V2' },
  { value: 23, label: 'Tencent' },
  { value: 18, label: 'Xunfei' },
  { value: 45, label: 'VolcEngine' },
  { value: 31, label: 'LingYiWanWu' },
  { value: 35, label: 'MiniMax' },
  { value: 49, label: 'Coze' },
  { value: 19, label: '360' },
  { value: 47, label: 'Xinference' },
  { value: 37, label: 'Dify' },
  { value: 38, label: 'Jina' },
  { value: 39, label: 'Cloudflare' },
  { value: 8, label: 'Custom' },
  { value: 57, label: 'Codex' },
  { value: 59, label: 'Sub2API' },
  { value: 22, label: 'FastGPT' },
  { value: 44, label: 'MokaAI' },
  { value: 2, label: 'Midjourney' },
  { value: 5, label: 'Midjourney Plus' },
  { value: 36, label: 'Suno' },
  { value: 50, label: 'Kling' },
  { value: 51, label: 'Jimeng' },
  { value: 52, label: 'Vidu' },
  { value: 53, label: 'Submodel' },
  { value: 54, label: 'Doubao Video' },
  { value: 55, label: 'Sora' },
  { value: 56, label: 'Replicate' },
  { value: 7, label: 'OhMyGPT' },
  { value: 16, label: 'Zhipu' },
] as const;

export type ChannelProviderType = (typeof CHANNEL_PROVIDER_OPTIONS)[number]['value'];

const channelProviderTypes = new Set<number>(CHANNEL_PROVIDER_OPTIONS.map(({ value }) => value));

export function isChannelProviderType(value: number): value is ChannelProviderType {
  return channelProviderTypes.has(value);
}

export function channelProviderLabel(value: number): string {
  return CHANNEL_PROVIDER_OPTIONS.find((option) => option.value === value)?.label ?? `Type ${value}`;
}
