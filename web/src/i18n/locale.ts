export type Locale = 'zh-CN' | 'en' | 'ja';
export type LocalePreference = Locale | 'system';

export function selectLocale(preference: LocalePreference, systemLocale = globalThis.navigator?.language ?? ''): Locale {
  if (preference === 'zh-CN' || preference === 'en' || preference === 'ja') return preference;
  let language = systemLocale;
  if (!/[,;]/.test(language)) language = language.split(/[.@]/, 1)[0]?.replaceAll('_', '-') ?? '';
  const choices = language.split(',').map((part, order) => {
    const [tag = '', ...parameters] = part.trim().split(';');
    const quality = parameters.find((parameter) => /^\s*q\s*=/.test(parameter));
    return { tag, order, quality: quality === undefined ? 1 : Number(quality.split('=')[1]) };
  }).filter((choice) => choice.quality > 0 && choice.quality <= 1 && Number.isFinite(choice.quality))
    .sort((left, right) => right.quality - left.quality || left.order - right.order);
  for (const { tag } of choices) {
    try {
      const primary = new Intl.Locale(tag).language;
      if (primary === 'en' || primary === 'ja') return primary;
      if (primary === 'zh') return 'zh-CN';
    } catch {
      // 无法识别的系统语言不影响界面启动。
    }
  }
  return 'en';
}
