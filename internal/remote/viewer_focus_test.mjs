import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { attachTextInput } from './assets/text-entry.js';

const source = (await readFile(new URL('./assets/viewer.js', import.meta.url), 'utf8')).replace(/^import .*$/gm, '');
const rfbSource = await readFile(new URL('./assets/novnc/core/rfb.js', import.meta.url), 'utf8');
const transitionStart = rfbSource.indexOf('    _updateConnectionState(state) {');
const transitionEnd = rfbSource.indexOf('\n    /* Print errors and disconnect', transitionStart);
assert.ok(transitionStart >= 0 && transitionEnd > transitionStart);
const transition = rfbSource.slice(transitionStart, transitionEnd).trim().replace('_updateConnectionState(state)', 'function(state)');
const socketClose = rfbSource.slice(rfbSource.indexOf('    _socketClose(e) {'), rfbSource.indexOf('    _socketError(e) {')).trim().replace('_socketClose(e)', 'function(e)');
const failure = rfbSource.slice(rfbSource.indexOf('    _fail(details) {'), rfbSource.indexOf('    _setCapability(cap, val) {')).trim().replace('_fail(details)', 'function(details)');
const region = { x: 400, y: 300, width: 200, height: 200 };
const dictionaries = Object.fromEntries(await Promise.all(['zh-CN', 'en', 'ja'].map(async language => {
    const dictionary = JSON.parse(await readFile(new URL(`../locales/${language}.json`, import.meta.url), 'utf8'));
    const copy = Object.fromEntries(Object.entries(dictionary).filter(([key]) => key.startsWith('remote.viewer.')).map(([key, text]) => [key.slice('remote.viewer.'.length).replace(/_([a-z])/g, (_, letter) => letter.toUpperCase()), text]));
    return [language, copy];
})));

class Element extends EventTarget {
    value = '';
    hidden = false;
    disabled = false;
    textContent = '';
    attributes = {};
    dataset = {};
    focused = false;
    setAttribute(name, value) { this.attributes[name] = value; }
    focus() { this.focused = true; }
}
const event = (node, name) => node.dispatchEvent(new Event(name));

function viewer(language = 'zh-CN', hash = '', options = {}) {
    let instances = 0, fetches = 0;
    const errors = [], trace = [];
    const stateContext = {
        Log: { Debug() {}, Error(message) { errors.push(message); } },
        setTimeout: () => 1, clearTimeout() {}, DISCONNECT_TIMEOUT: 3,
        CustomEvent: class { constructor(type, options) { this.type = type; this.detail = options.detail; } },
    };
    const updateConnectionState = vm.runInNewContext(`(${transition})`, stateContext);
    const onSocketClose = vm.runInNewContext(`(${socketClose})`, stateContext);
    const fail = vm.runInNewContext(`(${failure})`, stateContext);
    const rfb = {
        events: {}, keys: [], disconnects: 0,
        _rfbConnectionState: 'connecting', _disconnTimer: null, _rfbCleanDisconnect: true,
        _sock: { off(type) { if (type === 'close') socket.onclose = null; } }, _disconnect() {},
        _fail(details) { return fail.call(this, details); },
        _updateConnectionState(state) { trace.push(`${this._rfbConnectionState}->${state}`); updateConnectionState.call(this, state); },
        dispatchEvent(event) { this.lastEvent = this.events[event.type]?.(event); },
        addEventListener(type, handler) { this.events[type] = handler; },
        sendKey(keysym, code) { this.keys.push({ keysym, code }); },
        disconnect() { this.disconnects++; this._updateConnectionState('disconnecting'); },
    };
    let socket;
    function makeSocket(url) {
        assert.equal(url, 'ws://localhost/remote/vnc');
        socket = { handlers: [], closes: 0,
            addEventListener(type, listener) { assert.equal(type, 'close'); this.handlers.push(listener); },
            close() { this.closes++; },
            emitClose(code, reason = '') { const event = { code, reason }; for (const listener of this.handlers) listener(event); this.onclose?.(event); },
        };
        options.socket?.(socket);
        return socket;
    }
    const scroll = { scrollLeft: 0, scrollTop: 0, clientWidth: 200, clientHeight: 100 };
    const canvas = new Element();
    const host = Object.assign(new Element(), { firstElementChild: scroll, querySelector: () => canvas });
    const help = new Element();
    const nodes = new Map([['#screen', host], ['#help', help], ['#viewer-copy', { content: { querySelector: selector => ({ dataset: dictionaries[selector.match(/"([^"]+)"/)[1]] }) } }]]);
    const window = { parent: {}, events: {}, addEventListener(type, handler) { this.events[type] = handler; } };
    const origin = 'http://localhost';
    const location = { origin, href: `${origin}/remote/${hash}`, protocol: 'http:', hash };
    const document = { documentElement: { lang: language }, querySelector(id) {
        if (!nodes.has(id)) nodes.set(id, new Element());
        return nodes.get(id);
    } };
    const frames = [];
    let receive;
    const response = new Promise(resolve => { receive = resolve; });
    vm.runInNewContext(source, {
        RFB: function(_host, channel) {
            instances++;
            if (options.error) throw options.error;
            if (typeof channel === 'string') channel = makeSocket(channel);
            channel.onclose = event => onSocketClose.call(rfb, event);
            return rfb;
        }, WebSocket: function(url) { return makeSocket(url); }, document,
        window, location, URL, URLSearchParams,
        matchMedia: () => ({ matches: true }), requestAnimationFrame: callback => frames.push(callback),
        attachTouch: () => () => {}, attachTextInput,
        fetch: () => { fetches++; return response; },
    });
    return { scroll, help, frames, receive, rfb, nodes, canvas, document, location, errors, trace,
        connect() { rfb._updateConnectionState('connected'); return rfb.lastEvent; },
        serverDisconnect() { rfb._updateConnectionState('disconnecting'); rfb._updateConnectionState('disconnected'); },
        finishDisconnect() { rfb._updateConnectionState('disconnected'); },
        socketClose(code, reason) { socket.emitClose(code, reason); },
        get instances() { return instances; }, get fetches() { return fetches; },
        language(value) { location.hash = `#lang=${value}`; window.events.hashchange(); },
        leave() { window.events.pagehide(); },
        send(region) { window.events.message({ origin, source: window.parent, data: { type: 'kakatta:viewport', region } }); },
    };
}

test('验证框消失时清除定位提示和已排队的滚动', async () => {
    const v = viewer();
    const connected = v.connect();
    v.receive({ ok: true, json: async () => null });
    await connected;
    v.send(region);
    assert.match(v.help.textContent, /已定位/);
    v.send(null);
    for (const frame of v.frames) frame();
    assert.doesNotMatch(v.help.textContent, /已定位/);
    assert.equal(v.scroll.scrollLeft, 0);
    assert.equal(v.scroll.scrollTop, 0);
});

test('连接期间收到的位置清除优先于较早读取的位置', async () => {
    const v = viewer();
    const connected = v.connect();
    v.send(null);
    v.receive({ ok: true, json: async () => region });
    await connected;
    for (const frame of v.frames) frame();
    assert.doesNotMatch(v.help.textContent, /已定位/);
    assert.equal(v.scroll.scrollLeft, 0);
    assert.equal(v.scroll.scrollTop, 0);
});

test('连接后用有效区域定位到验证框中心', async () => {
    const v = viewer();
    const connected = v.connect();
    v.receive({ ok: true, json: async () => region });
    await connected;
    for (const frame of v.frames) frame();
    assert.match(v.help.textContent, /已定位/);
    assert.equal(v.scroll.scrollLeft, 400);
    assert.equal(v.scroll.scrollTop, 350);
});

for (const language of ['zh-CN', 'en', 'ja']) {
    test(`正式画面用共享${language}副本显示控件、状态和输入回执`, async () => {
        const v = viewer(language);
        const copy = dictionaries[language];
        assert.ok(copy.title);
        assert.equal(v.document.documentElement.lang, language);
        assert.equal(v.document.title, `${copy.title} · Kakatta`);
        for (const [id, key] of [
            ['viewer-heading', 'title'], ['actual', 'actual'], ['fit', 'fit'], ['pan', 'pan'], ['text-toggle', 'textToggle'],
            ['text-label', 'textLabel'], ['text-help', 'textHelp'], ['text-send', 'textSend'],
            ['text-backspace', 'textBackspace'], ['text-enter', 'textEnter'], ['text-close', 'textClose'],
        ]) assert.equal(v.nodes.get(`#${id}`).textContent, copy[key]);
        assert.equal(v.nodes.get('#status').textContent, copy.connecting);
        assert.equal(v.nodes.get('#viewer-toolbar').attributes['aria-label'], copy.toolbar);
        assert.equal(v.nodes.get('#text-entry').attributes['aria-label'], copy.textEntry);
        assert.equal(v.nodes.get('#screen').attributes['aria-label'], copy.screen);
        assert.equal(v.help.textContent, copy.initialHelp);
        const connected = v.connect();
        v.receive({ ok: true, json: async () => region });
        await connected;
        assert.equal(v.nodes.get('#status').textContent, copy.connected);
        assert.equal(v.canvas.attributes['aria-label'], copy.canvas);
        assert.equal(v.help.textContent, copy.helpVerification);
        event(v.nodes.get('#text-toggle'), 'click');
        v.nodes.get('#text-input').value = '中文かな123😀';
        event(v.nodes.get('#text-input'), 'input');
        assert.equal(v.rfb.keys.length, 0);
        event(v.nodes.get('#text-send'), 'click');
        assert.equal(v.nodes.get('#text-status').textContent, copy.textSent);
        assert.equal(v.nodes.get('#text-input').value, '');
        assert.deepEqual(v.rfb.keys.map(key => key.keysym), [0x01004e2d, 0x01006587, 0x0100304b, 0x0100306a, 0x31, 0x32, 0x33, 0x0101f600]);
        v.rfb.events.securityfailure();
        assert.equal(v.nodes.get('#status').textContent, copy.connectionFailed);
        v.serverDisconnect();
        assert.equal(v.nodes.get('#status').textContent, copy.disconnected);
        assert.equal(v.help.textContent, copy.helpDisconnected);
        assert.equal(v.nodes.get('#text-entry').hidden, true);
        assert.equal(v.nodes.get('#text-toggle').disabled, true);
        assert.equal(v.instances, 1);
        assert.equal(v.fetches, 1);
        v.leave();
        assert.equal(v.rfb.disconnects, 0);
        assert.deepEqual(v.errors, []);
    });
}

test('Hash语言覆盖请求语言且切换不重连、不重新定位、不发送或清空待发文字', async () => {
    const v = viewer('en', '#lang=ja');
    assert.equal(v.document.documentElement.lang, 'ja');
    assert.equal(v.document.title, `${dictionaries.ja.title} · Kakatta`);
    const connected = v.connect();
    v.receive({ ok: true, json: async () => region });
    await connected;
    for (const frame of v.frames) frame();
    event(v.nodes.get('#text-toggle'), 'click');
    v.nodes.get('#text-input').value = '未发送の文字';
    event(v.nodes.get('#text-input'), 'input');
    event(v.nodes.get('#pan'), 'click');
    for (const language of ['zh-CN', 'en', 'ja']) {
        v.language(language);
        assert.equal(v.nodes.get('#text-input').value, '未发送の文字');
        assert.equal(v.nodes.get('#text-entry').hidden, false);
        assert.equal(v.nodes.get('#pan').textContent, dictionaries[language].resume);
        assert.equal(v.nodes.get('#pan').attributes['aria-pressed'], 'true');
        assert.equal(v.help.textContent, dictionaries[language].helpPan);
        assert.equal(v.canvas.attributes['aria-label'], dictionaries[language].canvas);
        assert.equal(v.nodes.get('#status').textContent, dictionaries[language].connected);
        assert.equal(v.scroll.scrollLeft, 400);
        assert.equal(v.scroll.scrollTop, 350);
        assert.equal(v.instances, 1);
        assert.equal(v.fetches, 1);
        assert.equal(v.rfb.disconnects, 0);
        assert.equal(v.rfb.keys.length, 0);
    }
    event(v.nodes.get('#text-send'), 'click');
    const keys = v.rfb.keys.slice();
    for (const language of ['zh-CN', 'en', 'ja']) {
        v.language(language);
        assert.equal(v.nodes.get('#text-status').textContent, dictionaries[language].textSent);
        assert.deepEqual(v.rfb.keys, keys);
    }
    v.rfb.events.securityfailure();
    v.language('en');
    assert.equal(v.nodes.get('#status').textContent, dictionaries.en.connectionFailed);
    v.language('fr');
    assert.equal(v.document.documentElement.lang, 'en');
    assert.equal(v.location.href, 'http://localhost/remote/#lang=ja');
    v.serverDisconnect();
    v.language('ja');
    assert.equal(v.nodes.get('#status').textContent, dictionaries.ja.disconnected);
    assert.equal(v.nodes.get('#text-toggle').disabled, true);
    assert.equal(v.instances, 1);
    assert.equal(v.fetches, 1);
});

test('服务端已断开后离开画面，不再次切换已终止的RFB状态', async () => {
    const v = viewer();
    const connected = v.connect();
    v.receive({ ok: true, json: async () => null });
    await connected;
    event(v.nodes.get('#text-toggle'), 'click');
    v.nodes.get('#text-input').value = '未发送内容';
    v.serverDisconnect();
    v.leave();
    assert.equal(v.rfb.disconnects, 0, `调用顺序：${v.trace.join(', ')}；错误：${v.errors.join(', ')}`);
    assert.deepEqual(v.errors, []);
    assert.equal(v.nodes.get('#text-input').value, '');
    assert.equal(v.nodes.get('#text-entry').hidden, true);
    assert.equal(v.nodes.get('#text-toggle').disabled, true);
});

test('仍在连接或已连接时离开画面，仍正常关闭RFB并清掉输入', async () => {
    for (const connected of [false, true]) {
        const v = viewer();
        if (connected) {
            const pending = v.connect();
            v.receive({ ok: true, json: async () => null });
            await pending;
            event(v.nodes.get('#text-toggle'), 'click');
            v.nodes.get('#text-input').value = '本地草稿';
        }
        v.leave();
        assert.equal(v.rfb.disconnects, 1);
        assert.equal(v.rfb._rfbConnectionState, 'disconnecting');
        v.finishDisconnect();
        v.leave();
        assert.equal(v.rfb.disconnects, 1);
        assert.deepEqual(v.errors, []);
        assert.equal(v.nodes.get('#text-input').value, '');
    }
});

test('原始noVNC状态机能识别重复关闭终态，错误断言不会空过', () => {
    const v = viewer();
    v.serverDisconnect();
    v.rfb.disconnect();
    assert.deepEqual(v.errors, ['Tried changing state of a disconnected RFB object']);
    assert.equal(v.rfb._rfbConnectionState, 'disconnected');
});

test('只有固定正常会话标记在库处理前关闭当前RFB，未标记关闭仍报错', () => {
    for (const [code, reason, normal] of [
        [1000, 'kakatta-session-ended', true], [1000, '', false], [1006, '', false], [1006, 'kakatta-session-ended', false],
    ]) {
        const v = viewer();
        v.socketClose(code, reason);
        assert.equal(v.rfb._rfbConnectionState, 'disconnected');
        assert.equal(v.rfb.disconnects, normal ? 1 : 0);
        if (normal) assert.deepEqual(v.errors, []);
        else assert.deepEqual(v.errors, [`Failed when connecting: Connection closed (code: ${code}${reason ? ', reason: ' + reason : ''})`]);
        v.leave();
        assert.equal(v.rfb.disconnects, normal ? 1 : 0);
    }
});

test('RFB构造失败时关掉本次WebSocket并保留原错误', () => {
    const error = new Error('本地构造故障');
    let socket;
    assert.throws(() => viewer('en', '', { error, socket: value => { socket = value; } }), value => value === error);
    assert.equal(socket.closes, 1);
});

test('库已移除关闭回调后，正常结束标记也不再关闭终态RFB', () => {
    const v = viewer();
    v.serverDisconnect();
    v.socketClose(1000, 'kakatta-session-ended');
    assert.equal(v.rfb.disconnects, 0);
    assert.deepEqual(v.errors, []);
});
