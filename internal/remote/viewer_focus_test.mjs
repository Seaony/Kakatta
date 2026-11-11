import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import { attachTextInput } from './assets/text-entry.js';

const source = (await readFile(new URL('./assets/viewer.js', import.meta.url), 'utf8')).replace(/^import .*$/gm, '');
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

function viewer(language = 'zh-CN', hash = '') {
    let instances = 0, fetches = 0;
    const rfb = {
        events: {}, keys: [], disconnects: 0,
        addEventListener(type, handler) { this.events[type] = handler; },
        sendKey(keysym, code) { this.keys.push({ keysym, code }); },
        disconnect() { this.disconnects++; },
    };
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
        RFB: function(_host, url) { instances++; assert.equal(url, `${origin.replace('http', 'ws')}/remote/vnc`); return rfb; }, document,
        window, location, URL, URLSearchParams,
        matchMedia: () => ({ matches: true }), requestAnimationFrame: callback => frames.push(callback),
        attachTouch: () => () => {}, attachTextInput,
        fetch: () => { fetches++; return response; },
    });
    return { scroll, help, frames, receive, rfb, nodes, canvas, document, location, connect: rfb.events.connect,
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
        v.rfb.events.disconnect();
        assert.equal(v.nodes.get('#status').textContent, copy.disconnected);
        assert.equal(v.help.textContent, copy.helpDisconnected);
        assert.equal(v.nodes.get('#text-entry').hidden, true);
        assert.equal(v.nodes.get('#text-toggle').disabled, true);
        assert.equal(v.instances, 1);
        assert.equal(v.fetches, 1);
        v.leave();
        assert.equal(v.rfb.disconnects, 1);
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
    v.rfb.events.disconnect();
    v.language('ja');
    assert.equal(v.nodes.get('#status').textContent, dictionaries.ja.disconnected);
    assert.equal(v.nodes.get('#text-toggle').disabled, true);
    assert.equal(v.instances, 1);
    assert.equal(v.fetches, 1);
});
