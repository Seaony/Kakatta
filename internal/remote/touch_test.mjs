import test from 'node:test';
import assert from 'node:assert/strict';
import { attachTouch } from './assets/touch.js';

const windowEvents = new EventTarget();
globalThis.addEventListener = windowEvents.addEventListener.bind(windowEvents);
globalThis.removeEventListener = windowEvents.removeEventListener.bind(windowEvents);
globalThis.document = new EventTarget();
document.hidden = false;

class Canvas extends EventTarget {
    captured = new Set();
    getBoundingClientRect() { return { left: 20, top: 30, width: 800, height: 600 }; }
    setPointerCapture(id) { this.captured.add(id); }
    hasPointerCapture(id) { return this.captured.has(id); }
    releasePointerCapture(id) { this.captured.delete(id); }
    focus() {}
}

function pointer(target, type, values = {}) {
    const event = new Event(type, { cancelable: true });
    Object.assign(event, { pointerId: 1, pointerType: 'touch', clientX: 120, clientY: 130, ...values });
    target.dispatchEvent(event);
    return event;
}

test('触屏第一刻在原点按下，短距离移动也连续发送', () => {
    const canvas = new Canvas();
    const sent = [];
    const detach = attachTouch(canvas, (...point) => sent.push(point), { scrollLeft: 0, scrollTop: 0 });
    try {
        assert.equal(pointer(canvas, 'pointerdown').defaultPrevented, true);
        assert.deepEqual(sent, [[100, 100, 1]]);
        pointer(canvas, 'pointermove', { clientX: 123 });
        pointer(canvas, 'pointermove', { clientX: 130 });
        pointer(canvas, 'pointerup', { clientX: 131 });
        assert.deepEqual(sent, [[100, 100, 1], [103, 100, 1], [110, 100, 1], [111, 100, 0]]);
        assert.equal(canvas.captured.size, 0);
    } finally { detach(); }
});

test('第二触点不改变鼠标，取消与离开页面会松开按键', () => {
    const canvas = new Canvas();
    const sent = [];
    const detach = attachTouch(canvas, (...point) => sent.push(point), { scrollLeft: 0, scrollTop: 0 });
    try {
        pointer(canvas, 'pointerdown');
        pointer(canvas, 'pointerdown', { pointerId: 2, clientX: 600 });
        pointer(canvas, 'pointermove', { pointerId: 2, clientX: 650 });
        pointer(canvas, 'pointerup', { pointerId: 2 });
        assert.deepEqual(sent, [[100, 100, 1]]);
        pointer(canvas, 'pointercancel');
        assert.deepEqual(sent.at(-1), [100, 100, 0]);
        pointer(canvas, 'pointerdown', { pointerId: 3 });
        windowEvents.dispatchEvent(new Event('blur'));
        assert.deepEqual(sent.slice(-2), [[100, 100, 1], [100, 100, 0]]);
        pointer(canvas, 'pointerdown', { pointerId: 4 });
        document.hidden = true;
        document.dispatchEvent(new Event('visibilitychange'));
        document.hidden = false;
        assert.deepEqual(sent.at(-1), [100, 100, 0]);
    } finally { detach(); }
});

test('平移画面只改变滚动位置，不操作远程浏览器', () => {
    const canvas = new Canvas();
    const sent = [];
    const scroll = { scrollLeft: 200, scrollTop: 100 };
    const detach = attachTouch(canvas, (...point) => sent.push(point), scroll, () => true);
    try {
        pointer(canvas, 'pointerdown');
        pointer(canvas, 'pointermove', { clientX: 100, clientY: 110 });
        pointer(canvas, 'pointerup');
        assert.deepEqual(scroll, { scrollLeft: 220, scrollTop: 120 });
        assert.deepEqual(sent, []);
    } finally { detach(); }
});

test('阻止双指缩放和 Ctrl 滚轮，普通鼠标仍由核心库处理', () => {
    const canvas = new Canvas();
    const sent = [];
    const detach = attachTouch(canvas, (...point) => sent.push(point), { scrollLeft: 0, scrollTop: 0 });
    try {
        for (const type of ['touchstart', 'touchmove', 'gesturestart', 'gesturechange', 'gestureend']) {
            assert.equal(pointer(canvas, type).defaultPrevented, true);
        }
        let wheelForwarded = false;
        canvas.addEventListener('wheel', () => { wheelForwarded = true; });
        assert.equal(pointer(canvas, 'wheel', { ctrlKey: true }).defaultPrevented, true);
        assert.equal(wheelForwarded, false);
        assert.equal(pointer(canvas, 'pointerdown', { pointerType: 'mouse' }).defaultPrevented, false);
        assert.deepEqual(sent, []);
    } finally { detach(); }
    pointer(canvas, 'pointerdown');
    assert.deepEqual(sent, []);
});
