import test from 'node:test';
import assert from 'node:assert/strict';
import { attachTextInput } from './assets/text-entry.js';

class Element extends EventTarget {
    value = '';
    hidden = false;
    disabled = false;
    textContent = '';
    attributes = {};
    focused = false;
    setAttribute(name, value) { this.attributes[name] = value; }
    focus() { this.focused = true; }
}

function setup() {
    const names = ['toggle', 'panel', 'input', 'send', 'backspace', 'enter', 'close', 'status'];
    const nodes = Object.fromEntries(names.map(name => [name, new Element()]));
    const sent = [];
    const entry = attachTextInput(nodes, (keysym, code) => sent.push({ keysym, code }));
    return { nodes, sent, entry };
}
const event = (node, name) => node.dispatchEvent(new Event(name));

test('显式打开可编辑文字框，数字和中文只在点击发送后转发一次', () => {
    const { nodes, sent, entry } = setup();
    assert.equal(nodes.toggle.disabled, true);
    entry.setConnected(true);
    event(nodes.toggle, 'click');
    assert.equal(nodes.panel.hidden, false);
    assert.equal(nodes.input.focused, true);
    nodes.input.value = '123中文é😀';
    event(nodes.input, 'input');
    assert.equal(nodes.send.disabled, false);
    assert.deepEqual(sent, []);
    event(nodes.send, 'click');
    assert.deepEqual(sent.map(key => key.keysym), [0x31, 0x32, 0x33, 0x01004e2d, 0x01006587, 0xe9, 0x0101f600]);
    assert.equal(nodes.input.value, '');
    assert.equal(nodes.send.disabled, true);
    event(nodes.send, 'click');
    assert.equal(sent.length, 7);
    entry.dispose();
});

test('输入法组合期间不发送，组合提交和 input 事件不重复转发', () => {
    const { nodes, sent, entry } = setup();
    entry.setConnected(true);
    event(nodes.input, 'compositionstart');
    nodes.input.value = '中';
    event(nodes.input, 'input');
    event(nodes.send, 'click');
    event(nodes.enter, 'click');
    assert.equal(nodes.send.disabled, true);
    assert.deepEqual(sent, []);
    event(nodes.input, 'compositionend');
    event(nodes.input, 'input');
    assert.deepEqual(sent, []);
    event(nodes.send, 'click');
    event(nodes.input, 'input');
    assert.deepEqual(sent, [{ keysym: 0x01004e2d, code: undefined }]);
    assert.equal(nodes.input.value, '');
    entry.dispose();
});

test('退格回车由独立按钮发送，换行不会重复发送回车', () => {
    const { nodes, sent, entry } = setup();
    entry.setConnected(true);
    event(nodes.backspace, 'click');
    event(nodes.enter, 'click');
    nodes.input.value = '1\r\n2\n';
    event(nodes.send, 'click');
    assert.deepEqual(sent, [
        { keysym: 0xff08, code: 'Backspace' }, { keysym: 0xff0d, code: 'Enter' },
        { keysym: 0x31, code: undefined }, { keysym: 0xff0d, code: 'Enter' },
        { keysym: 0x32, code: undefined }, { keysym: 0xff0d, code: 'Enter' },
    ]);
    entry.dispose();
});

test('关闭、断线和离开页面均清空未发送文字，不残留到下一次打开', () => {
    const { nodes, sent, entry } = setup();
    entry.setConnected(true);
    event(nodes.toggle, 'click');
    nodes.input.value = '短信123456';
    event(nodes.close, 'click');
    assert.equal(nodes.input.value, '');
    assert.equal(nodes.panel.hidden, true);
    event(nodes.toggle, 'click');
    assert.equal(nodes.input.value, '');
    nodes.input.value = '未发送内容';
    entry.setConnected(false);
    assert.equal(nodes.input.value, '');
    assert.equal(nodes.panel.hidden, true);
    nodes.input.value = '离开前内容';
    entry.dispose();
    assert.equal(nodes.input.value, '');
    event(nodes.send, 'click');
    event(nodes.enter, 'click');
    assert.deepEqual(sent, []);
});
