import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

const source = (await readFile(new URL('./assets/viewer.js', import.meta.url), 'utf8')).replace(/^import .*$/gm, '');
const region = { x: 400, y: 300, width: 200, height: 200 };

function viewer() {
    const rfb = { events: {}, addEventListener(type, handler) { this.events[type] = handler; } };
    const scroll = { scrollLeft: 0, scrollTop: 0, clientWidth: 200, clientHeight: 100 };
    const host = { firstElementChild: scroll, querySelector: () => ({ setAttribute() {} }) };
    const help = { textContent: '' };
    const nodes = new Map([['#screen', host], ['#help', help]]);
    const window = { parent: {}, events: {}, addEventListener(type, handler) { this.events[type] = handler; } };
    const origin = 'http://localhost';
    const frames = [];
    let receive;
    const response = new Promise(resolve => { receive = resolve; });
    vm.runInNewContext(source, {
        RFB: function() { return rfb; }, document: { querySelector(id) {
            if (!nodes.has(id)) nodes.set(id, { addEventListener() {} });
            return nodes.get(id);
        } },
        window, location: { origin, href: `${origin}/remote/`, protocol: 'http:' }, URL,
        matchMedia: () => ({ matches: true }), requestAnimationFrame: callback => frames.push(callback),
        attachTouch: () => () => {}, attachTextInput: () => ({ setConnected() {} }), fetch: () => response,
    });
    return { scroll, help, frames, receive, connect: rfb.events.connect, send(region) {
        window.events.message({ origin, source: window.parent, data: { type: 'kakatta:viewport', region } });
    } };
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
