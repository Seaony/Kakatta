import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('./assets/preview.js', import.meta.url), 'utf8');

test('备用页只收同源图片，换帧和断线释放旧截图', () => {
    const listeners = {};
    const windowListeners = {};
    const frame = { hidden: true, removeAttribute() { delete this.src; } };
    const status = { textContent: '' };
    const heading = { textContent: '' };
    const help = { textContent: '' };
    const copies = {
        en: { title: 'Fallback view', heading: 'Read only', help: 'Check the logs', alt: 'Page', connecting: 'Connecting', connected: 'Connected', disconnected: 'Disconnected' },
        'zh-CN': { title: '备用画面', heading: '无法操作', help: '查看日志', alt: '网页', connecting: '正在连接', connected: '已连接', disconnected: '备用画面已断开' },
    };
    const nodes = {
        '#status': status, '#preview-frame': frame, '#preview-heading': heading, '#help': help,
        '#preview-copy': { content: { querySelector: selector => ({ dataset: copies[selector.match(/"([^"]+)"/)[1]] }) } },
    };
    const document = { documentElement: { lang: 'zh-CN' }, querySelector: id => nodes[id] };
    const location = { href: 'https://example.invalid/remote/sessions/1/#lang=en', protocol: 'https:', hash: '#lang=en' };
    const created = [];
    const revoked = [];
    let closed = false;
    const socket = {
        addEventListener(type, handler) { listeners[type] = handler; },
        close() { closed = true; },
    };
    class ImageURL extends URL {
        static createObjectURL(blob) {
            assert.equal(blob.type, 'image/jpeg');
            const url = `blob:image-${created.length + 1}`;
            created.push(url);
            return url;
        }
        static revokeObjectURL(url) { revoked.push(url); }
    }
    vm.runInNewContext(source, {
        document, location,
        window: { addEventListener(type, handler) { windowListeners[type] = handler; } },
        URL: ImageURL, URLSearchParams, Blob, ArrayBuffer,
        WebSocket: function(url) {
            assert.equal(url.href, 'wss://example.invalid/remote/sessions/1/vnc');
            return socket;
        },
    });
    assert.equal(socket.binaryType, 'arraybuffer');
    assert.equal(document.documentElement.lang, 'en');
    assert.equal(document.title, 'Fallback view · Kakatta');
    assert.equal(heading.textContent, 'Read only');
    assert.equal(help.textContent, 'Check the logs');
    assert.equal(frame.alt, 'Page');
    assert.equal(status.textContent, 'Connecting');
    listeners.message({ data: '非截图数据' });
    assert.equal(frame.hidden, true);
    assert.deepEqual(created, []);
    for (let count = 1; count <= 2; count++) {
        listeners.message({ data: new ArrayBuffer(4) });
        assert.equal(frame.src, `blob:image-${count}`);
        assert.equal(frame.hidden, false);
    }
    assert.deepEqual(revoked, ['blob:image-1']);
    assert.equal(status.textContent, 'Connected');
    location.hash = '#lang=zh-CN';
    windowListeners.hashchange();
    assert.equal(document.documentElement.lang, 'zh-CN');
    assert.equal(status.textContent, '已连接');
    listeners.close();
    assert.equal(frame.hidden, true);
    assert.equal(frame.src, undefined);
    assert.equal(status.textContent, '备用画面已断开');
    assert.deepEqual(revoked, ['blob:image-1', 'blob:image-2']);
    windowListeners.pagehide();
    assert.equal(closed, true);
});
