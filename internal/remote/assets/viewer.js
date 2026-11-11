import RFB from './novnc/core/rfb.js';
import { attachTouch } from './touch.js';
import { attachTextInput } from './text-entry.js';

const host = document.querySelector('#screen');
const status = document.querySelector('#status');
const help = document.querySelector('#help');
const actual = document.querySelector('#actual');
const fit = document.querySelector('#fit');
const pan = document.querySelector('#pan');
const copies = document.querySelector('#viewer-copy').content;
const browserLanguage = document.documentElement.lang;
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
let pendingRegion;
let connected = false;
let connection = 'connecting';
let helpState = 'initialHelp';
let copy;

function showCopy() {
    const preference = new URLSearchParams(location.hash.slice(1)).get('lang');
    const language = preference === 'en' || preference === 'zh-CN' || preference === 'ja' ? preference : browserLanguage;
    copy = copies.querySelector(`[lang="${language}"]`).dataset;
    document.documentElement.lang = language;
    document.title = `${copy.title} · Kakatta`;
    for (const [id, key] of [
        ['viewer-heading', 'title'], ['actual', 'actual'], ['fit', 'fit'], ['text-toggle', 'textToggle'],
        ['text-label', 'textLabel'], ['text-help', 'textHelp'], ['text-send', 'textSend'],
        ['text-backspace', 'textBackspace'], ['text-enter', 'textEnter'], ['text-close', 'textClose'],
    ]) document.querySelector(`#${id}`).textContent = copy[key];
    document.querySelector('#viewer-toolbar').setAttribute('aria-label', copy.toolbar);
    document.querySelector('#text-entry').setAttribute('aria-label', copy.textEntry);
    host.setAttribute('aria-label', copy.screen);
    const canvas = host.querySelector('canvas');
    if (canvas) canvas.setAttribute('aria-label', copy.canvas);
    pan.textContent = copy[panning ? 'resume' : 'pan'];
    status.textContent = copy[connection];
    help.textContent = copy[helpState];
    textInput.setCopy({ sent: copy.textSent });
}

function showControlHelp() {
    helpState = panning ? 'helpPan' : 'helpControl';
    help.textContent = copy[helpState];
}

function focusRegion(region) {
    pendingRegion = region;
    if (!connected) return;
    rfb.scaleViewport = false;
    const scroll = host.firstElementChild;
    if (region && Number.isFinite(region.x) && Number.isFinite(region.y) && region.width > 0 && region.height > 0) {
        requestAnimationFrame(() => {
            if (!connected || pendingRegion !== region) return;
            scroll.scrollLeft = Math.max(0, region.x + region.width / 2 - scroll.clientWidth / 2);
            scroll.scrollTop = Math.max(0, region.y + region.height / 2 - scroll.clientHeight / 2);
        });
        helpState = 'helpVerification';
        help.textContent = copy[helpState];
    } else {
        scroll.scrollLeft = 0;
        scroll.scrollTop = 0;
        showControlHelp();
    }
}

actual.addEventListener('click', () => focusRegion(null));
fit.addEventListener('click', () => { rfb.scaleViewport = true; });
pan.addEventListener('click', () => {
    panning = !panning;
    pan.setAttribute('aria-pressed', String(panning));
    host.dataset.panning = String(panning);
    pan.textContent = copy[panning ? 'resume' : 'pan'];
    showControlHelp();
});
rfb.addEventListener('connect', async () => {
    connected = true;
    connection = 'connected';
    status.textContent = copy.connected;
    actual.disabled = false;
    fit.disabled = mobile();
    fit.hidden = mobile();
    pan.disabled = false;
    textInput.setConnected(true);
    const canvas = host.querySelector('canvas');
    canvas.tabIndex = 0;
    canvas.setAttribute('aria-label', copy.canvas);
    detachTouch = attachTouch(canvas, (x, y, mask) => rfb.sendPointer(x, y, mask), host.firstElementChild, () => panning);
    if (pendingRegion !== undefined) {
        if (pendingRegion !== null) focusRegion(pendingRegion);
        return;
    }
    try {
        const response = await fetch(new URL('viewport', location.href));
        if (response.ok) {
            const region = await response.json();
            if (region && pendingRegion === undefined && connected) focusRegion(region);
        }
    } catch {
        if (pendingRegion === undefined && connected) {
            helpState = 'helpPositionUnavailable';
            help.textContent = copy[helpState];
        }
    }
});
rfb.addEventListener('disconnect', () => {
    connected = false;
    detachTouch();
    textInput.setConnected(false);
    connection = 'disconnected';
    helpState = 'helpDisconnected';
    status.textContent = copy[connection];
    help.textContent = copy[helpState];
    for (const button of [actual, fit, pan]) button.disabled = true;
});
rfb.addEventListener('securityfailure', () => { connection = 'connectionFailed'; status.textContent = copy[connection]; });
window.addEventListener('message', event => {
    if (event.origin === location.origin && event.source === window.parent && event.data?.type === 'kakatta:viewport') focusRegion(event.data.region);
});
window.addEventListener('pagehide', () => { detachTouch(); textInput.dispose(); if (connection !== 'disconnected') rfb.disconnect(); });
window.addEventListener('hashchange', showCopy);
showCopy();
