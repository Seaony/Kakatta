import RFB from './novnc/core/rfb.js';
import { attachTouch } from './touch.js';
import { attachTextInput } from './text-entry.js';

const host = document.querySelector('#screen');
const status = document.querySelector('#status');
const help = document.querySelector('#help');
const actual = document.querySelector('#actual');
const fit = document.querySelector('#fit');
const pan = document.querySelector('#pan');
const mobile = () => matchMedia('(max-width: 767px), (pointer: coarse)').matches;
const ws = new URL('vnc', location.href);
ws.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
const rfb = new RFB(host, ws.href, { shared: true, touchGestures: false });
const textInput = attachTextInput({
    toggle: document.querySelector('#text-toggle'), panel: document.querySelector('#text-entry'),
    input: document.querySelector('#text-input'), send: document.querySelector('#text-send'),
    backspace: document.querySelector('#text-backspace'), enter: document.querySelector('#text-enter'),
    close: document.querySelector('#text-close'), status: document.querySelector('#text-status'),
}, (keysym, code) => rfb.sendKey(keysym, code));
rfb.resizeSession = false;
rfb.scaleViewport = !mobile();
rfb.clipViewport = false;
let detachTouch = () => {};
let panning = false;
let pendingRegion = null;
let connected = false;

function focusRegion(region) {
    if (!connected) { pendingRegion = region; return; }
    rfb.scaleViewport = false;
    const scroll = host.firstElementChild;
    if (region && Number.isFinite(region.x) && Number.isFinite(region.y) && region.width > 0 && region.height > 0) {
        requestAnimationFrame(() => {
            scroll.scrollLeft = Math.max(0, region.x + region.width / 2 - scroll.clientWidth / 2);
            scroll.scrollTop = Math.max(0, region.y + region.height / 2 - scroll.clientHeight / 2);
        });
        help.textContent = '已定位到验证区域。请自己完成验证；需要看其他位置时，切换到“移动画面”。';
    } else {
        scroll.scrollLeft = 0;
        scroll.scrollTop = 0;
    }
}

actual.addEventListener('click', () => focusRegion(null));
fit.addEventListener('click', () => { rfb.scaleViewport = true; });
pan.addEventListener('click', () => {
    panning = !panning;
    pan.setAttribute('aria-pressed', String(panning));
    host.dataset.panning = String(panning);
    pan.textContent = panning ? '继续操控' : '移动画面';
    help.textContent = panning ? '拖动画面查看其他位置，不会向远程浏览器发送鼠标操作。' : '现在可以直接点击、输入或拖动远程浏览器。';
});
rfb.addEventListener('connect', async () => {
    connected = true;
    status.textContent = '已连接';
    actual.disabled = false;
    fit.disabled = mobile();
    fit.hidden = mobile();
    pan.disabled = false;
    textInput.setConnected(true);
    const canvas = host.querySelector('canvas');
    canvas.tabIndex = 0;
    canvas.setAttribute('aria-label', '远程浏览器，可用键盘输入');
    detachTouch = attachTouch(canvas, (x, y, mask) => rfb.sendPointer(x, y, mask), host.firstElementChild, () => panning);
    try {
        const response = await fetch(new URL('viewport', location.href));
        if (response.ok) {
            const region = pendingRegion || await response.json();
            if (region) focusRegion(region);
        }
    } catch { help.textContent = '位置提示暂时不可用，仍可在画面里手动操作。'; }
});
rfb.addEventListener('disconnect', () => {
    connected = false;
    detachTouch();
    textInput.setConnected(false);
    status.textContent = '连接已结束';
    help.textContent = '画面会在会话结束时关闭。如需继续，请回到实验工具重新打开。';
    for (const button of [actual, fit, pan]) button.disabled = true;
});
rfb.addEventListener('securityfailure', () => { status.textContent = '无法连接，请回到实验工具重新打开画面'; });
window.addEventListener('message', event => {
    if (event.origin === location.origin && event.source === window.parent && event.data?.type === 'kakatta:viewport') focusRegion(event.data.region);
});
window.addEventListener('pagehide', () => { detachTouch(); textInput.dispose(); rfb.disconnect(); });
