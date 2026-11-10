function attachmentName(disposition: string | null): string | undefined {
  if (!disposition) return undefined;
  const start = /^\s*attachment\s*(?=;|$)/i.exec(disposition);
  if (!start) return undefined;

  const parameters = new Map<string, string>();
  let remaining = disposition.slice(start[0].length);
  while (remaining) {
    const parameter = /^;\s*([A-Za-z0-9!#$%&'*+.^_`|~-]+)\s*=\s*(?:"((?:[^"\\]|\\.)*)"|([^;\s"]+))\s*/.exec(remaining);
    const name = parameter?.[1]?.toLowerCase();
    if (!parameter || !name || parameters.has(name)) return undefined;
    parameters.set(name, parameter[2] ?? parameter[3] ?? '');
    remaining = remaining.slice(parameter[0].length);
  }

  const extended = parameters.get('filename*');
  if (extended === undefined) return parameters.get('filename');
  const encoded = /^utf-8'[A-Za-z0-9-]*'(.*)$/i.exec(extended)?.[1];
  if (!encoded) return undefined;
  try {
    return decodeURIComponent(encoded);
  } catch {
    return undefined;
  }
}

function downloadName(disposition: string | null, fallback: string): string {
  const name = attachmentName(disposition);
  const extension = /\.[a-z0-9]+$/i.exec(fallback)?.[0];
  if (!name || !extension || /[/\\:*?"<>|\p{Cc}\p{Cf}]/u.test(name)) return fallback;
  const trimmed = name.trim();
  if (!trimmed.toLowerCase().endsWith(extension.toLowerCase())) return fallback;
  const stem = trimmed.slice(0, -extension.length).trim();
  return !stem || stem === '.' || stem === '..' ? fallback : trimmed;
}

export function downloadBlob(blob: Blob, disposition: string | null, fallback: string): void {
  const link = document.createElement('a');
  link.download = downloadName(disposition, fallback);
  link.hidden = true;
  const url = URL.createObjectURL(blob);
  try {
    link.href = url;
    document.body.append(link);
    link.click();
  } finally {
    link.remove();
    // 浏览器接手下载后再释放对象地址。
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
}
