import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = await readFile(new URL('./assets/control.js', import.meta.url), 'utf8');

class Element {
    hidden = false;
    disabled = false;
    textContent = '';
    children = [];
    handlers = {};
    src = 'about:blank';
    addEventListener(type, handler) { this.handlers[type] = handler; }
    getAttribute(name) { return this[name]; }
    append(...nodes) { this.children.push(...nodes); }
    replaceChildren(...nodes) { this.children = nodes; }
    querySelector() { return this.child ??= new Element(); }
}

async function page(state, status = 200, actionResponse = null) {
    const nodes = new Map();
    const timers = new Map();
    const requests = [];
    const window = new Element();
    const messages = [];
    const document = {
        querySelector(id) { if (!nodes.has(id)) nodes.set(id, new Element()); return nodes.get(id); },
        createElement() { return new Element(); },
    };
    let timerID = 0;
    vm.runInNewContext(source, {
        document, window, URLSearchParams, FormData, location: { origin: 'http://tool.test' },
        fetch: async (url, options) => { requests.push({ url, options }); return url !== '/status' && actionResponse ? actionResponse : { status, ok: status === 200, json: async () => state }; },
        setTimeout: (callback, delay) => { const id = ++timerID; timers.set(id, { callback, delay }); return id; },
        clearTimeout: id => timers.delete(id),
    });
    await new Promise(resolve => setImmediate(resolve));
    nodes.get('#remote').querySelector('iframe').contentWindow = { postMessage: (message, origin) => messages.push({ message, origin }) };
    return { nodes, timers, requests, window, messages };
}

test('登录画面阻止同时运行搜索，状态每两秒只读本地接口', async () => {
    const { nodes, timers, requests } = await page({ phase: 'ready', running: false, events: [] });
    assert.equal(nodes.get('#run').disabled, true);
    assert.equal(nodes.get('#session').disabled, true);
    assert.equal(nodes.get('#remote').hidden, false);
    assert.equal(nodes.get('#remote').querySelector('iframe').src, '/remote/');
    assert.equal(timers.size, 1);
    assert.equal([...timers.values()][0].delay, 2000);
    assert.equal(requests[0].url, '/status');
    assert.equal(requests[0].options.credentials, 'same-origin');
});

test('验证操作可见，服务端提示与日志按纯文本显示', async () => {
    const { nodes } = await page({ phase: 'verification', running: false, reason: '需要人工验证', next_allowed_at: '等待服务端确认', events: [{ at: '2026-11-10T10:00:00Z', event: '<script>不会执行</script>', fields: { 内容: '<img src=x>' } }] });
    assert.equal(nodes.get('#verify').hidden, false);
    assert.equal(nodes.get('#confirm').hidden, false);
    assert.equal(nodes.get('#verify').disabled, false);
    assert.equal(nodes.get('#run').disabled, true);
    assert.equal(nodes.get('#reason').textContent, '需要人工验证');
    assert.equal(nodes.get('#next-allowed').textContent, '下次可操作时间：等待服务端确认');
    assert.equal(nodes.get('#events').children[0].children[1].textContent, '<script>不会执行</script>');
    assert.equal(nodes.get('#events').children[0].children[2].textContent, '内容：<img src=x>');
});

test('终态关闭画面并停止轮询，离开页面清掉计时器', async () => {
    for (const phase of ['completed', 'failed', 'closed']) {
        const { nodes, timers } = await page({ phase, events: [] });
        assert.equal(timers.size, 0);
        assert.equal(nodes.get('#remote').hidden, true);
        assert.equal(nodes.get('#run').disabled, false);
        assert.equal(nodes.get('#verify').hidden, true);
    }
    const { timers, window } = await page({ phase: 'running', running: true, events: [] });
    assert.equal(timers.size, 1);
    window.handlers.pagehide();
    assert.equal(timers.size, 0);
});

test('失效会话只保留令牌表单，不继续读取状态或加载画面', async () => {
    const { nodes, timers } = await page({}, 401);
    assert.equal(nodes.get('#login').hidden, false);
    assert.equal(nodes.get('#controls').hidden, true);
    assert.equal(nodes.get('#remote').hidden, true);
    assert.equal(nodes.get('#remote').querySelector('iframe').src, 'about:blank');
    assert.equal(timers.size, 0);
});

test('关闭或失败后仍有待验证状态时保留去验证入口', async () => {
    for (const phase of ['closed', 'failed']) {
        const { nodes, timers } = await page({ phase, verification_pending: true, events: [] });
        assert.equal(nodes.get('#verify').hidden, false);
        assert.equal(nodes.get('#verify').disabled, false);
        assert.equal(nodes.get('#confirm').hidden, true);
        assert.equal(timers.size, 0);
    }
});

test('操作冲突显示服务端中文正文，刷新状态不会覆盖错误', async () => {
    const { nodes, requests } = await page({ phase: 'closed', events: [] }, 200, { status: 409, ok: false, text: async () => '这个账号还需等待 3 分钟\n' });
    nodes.get('#run').handlers.click();
    await new Promise(resolve => setImmediate(resolve));
    assert.equal(requests[1].url, '/run');
    assert.equal(nodes.get('#error').textContent, '这个账号还需等待 3 分钟');
    assert.equal(nodes.get('#error').hidden, false);
});

test('运行中验证码位置仅在变化时通知画面，不反复重置用户平移', async () => {
    const state = { phase: 'running', running: true, events: [], viewport: null };
    const { timers, messages } = await page(state);
    const refresh = [...timers.values()][0].callback;
    state.viewport = { x: 500, y: 250, width: 300, height: 200 };
    await refresh();
    assert.equal(messages.length, 1);
    assert.equal(messages[0].message.type, 'kakatta:viewport');
    assert.deepEqual(messages[0].message.region, state.viewport);
    assert.equal(messages[0].origin, 'http://tool.test');
    await refresh();
    assert.equal(messages.length, 1);
    state.viewport = { x: 700, y: 250, width: 300, height: 200 };
    await refresh();
    assert.equal(messages.length, 2);
});
