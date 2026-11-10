export const providerPresets = [
  { value: 'custom', labelKey: 'web.ai.preset_custom', baseURL: null },
  { value: 'openai', labelKey: 'web.ai.preset_openai', baseURL: 'https://api.openai.com/v1' },
  { value: 'deepseek', labelKey: 'web.ai.preset_deepseek', baseURL: 'https://api.deepseek.com' },
  { value: 'modelscope', labelKey: 'web.ai.preset_modelscope', baseURL: 'https://api-inference.modelscope.cn/v1' },
  { value: 'siliconflow', labelKey: 'web.ai.preset_siliconflow', baseURL: 'https://api.siliconflow.cn/v1' },
  { value: 'openrouter', labelKey: 'web.ai.preset_openrouter', baseURL: 'https://openrouter.ai/api/v1' },
  { value: 'aliyun', labelKey: 'web.ai.preset_aliyun', baseURL: 'https://dashscope.aliyuncs.com/compatible-mode/v1' },
  { value: 'anthropic', labelKey: 'web.ai.preset_anthropic', baseURL: 'https://api.anthropic.com/v1' },
  { value: 'ollama', labelKey: 'web.ai.preset_ollama', baseURL: 'http://host.docker.internal:11434/v1' },
] as const;
