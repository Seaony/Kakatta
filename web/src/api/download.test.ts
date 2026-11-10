import { expect, spyOn, test } from 'bun:test';
import { downloadBlob } from './download';

test('附件下载保留中文和带引号的安全文件名，拒绝路径与错扩展并释放对象地址', async () => {
  const csv = 'kakatta-results-7.csv';
  const samples: Array<[string | null, string, string]> = [
    ["attachment; filename=ascii.csv; filename*=UTF-8'zh-CN'%E7%9B%B8%E6%9C%BA-2026-11-11.csv", csv, '相机-2026-11-11.csv'],
    ['attachment; filename="camera; daily 7.csv"', csv, 'camera; daily 7.csv'],
    ['ATTACHMENT; Filename=regular.CSV', csv, 'regular.CSV'],
    ['attachment; filename="../../untrusted.csv"', csv, csv],
    ['attachment; filename="folder\\untrusted.csv"', csv, csv],
    ["attachment; filename*=utf-8''..%2Funtrusted.csv", csv, csv],
    ["attachment; filename*=utf-8''line%0Aname.csv", csv, csv],
    ["attachment; filename*=utf-8''name%E2%80%AE.csv", csv, csv],
    ["attachment; filename*=utf-8''invalid%ZZ.csv", csv, csv],
    ['attachment; filename=untrusted.html', csv, csv],
    ['attachment; filename=first.csv; filename=second.csv', csv, csv],
    ['inline; filename=regular.csv', csv, csv],
    ['attachment; filename="kakatta backup.zip"', 'kakatta.zip', 'kakatta backup.zip'],
    [null, 'kakatta.txt', 'kakatta.txt'],
  ];
  const urls: string[] = [];
  const revoked: string[] = [];
  const clicks: HTMLAnchorElement[] = [];
  const blob = new Blob(['内容']);
  const create = spyOn(URL, 'createObjectURL').mockImplementation((value) => {
    expect(value === blob).toBe(true);
    const url = `blob:http://localhost/download-helper-test-${urls.length}`;
    urls.push(url);
    return url;
  });
  const originalRevoke = URL.revokeObjectURL.bind(URL);
  const revoke = spyOn(URL, 'revokeObjectURL').mockImplementation((url) => {
    if (urls.includes(url)) revoked.push(url);
    else originalRevoke(url);
  });
  const click = spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    expect(this.isConnected).toBe(true);
    clicks.push(this);
  });
  try {
    for (const [disposition, fallback, name] of samples) {
      downloadBlob(blob, disposition, fallback);
      expect(clicks.at(-1)?.download).toBe(name);
      expect(clicks.at(-1)?.getAttribute('href')).toBe(urls.at(-1));
      expect(clicks.at(-1)?.isConnected).toBe(false);
    }
    click.mockImplementationOnce(() => { throw new Error('浏览器无法接手下载'); });
    expect(() => downloadBlob(blob, null, csv)).toThrow('浏览器无法接手下载');
    expect(document.querySelector('a[download]') === null).toBe(true);
    expect(revoked).toHaveLength(0);
    await new Promise((resolve) => setTimeout(resolve, 1050));
    expect(revoked).toEqual(urls);
  } finally {
    click.mockRestore();
    revoke.mockRestore();
    create.mockRestore();
  }
});
