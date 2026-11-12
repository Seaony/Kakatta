const remote = document.querySelector('#remote');
const viewer = remote.querySelector('iframe');
const error = document.querySelector('#error');
const events = document.querySelector('#events');
const logSection = document.querySelector('#log-section');
const session = document.querySelector('#session');
const run = document.querySelector('#run');
const close = document.querySelector('#close');
const verify = document.querySelector('#verify');
const confirm = document.querySelector('#confirm');
const labels = { idle: '还未开始', opening: '正在打开浏览器', ready: '画面已就绪', running: '正在跑一次流程', verification: '请手动完成验证', completed: '本次流程已完成', failed: '本次流程已停止', closed: '浏览器已关闭' };
const terminal = new Set(['completed', 'failed', 'closed']);
let timer = null;
let busy = false;
let previousPhase = '';
let pageActive = true;
let previousViewport = 'null';

function showError(message) { error.textContent = message || ''; error.hidden = !message; }
function stopPoll() { clearTimeout(timer); timer = null; }
function requestPoll() { stopPoll(); if (pageActive) timer = setTimeout(refresh, 2000); }
function showHint(id, prefix, value) {
    const node = document.querySelector(id);
    node.hidden = !value;
    node.textContent = value ? prefix + value : '';
}
function show(state) {
    const phase = state.phase || 'idle';
    if (phase !== previousPhase) document.querySelector('#phase').textContent = labels[phase] || '等待下一步';
    previousPhase = phase;
    showError(state.error);
    showHint('#reason', '', state.reason);
    showHint('#next-allowed', '下次可操作时间：', state.next_allowed_at);
    const live = ['ready', 'running', 'verification'].includes(phase);
    remote.hidden = !live;
    if (live && viewer.getAttribute('src') !== '/remote/') viewer.src = '/remote/';
    if (!live && viewer.getAttribute('src') !== 'about:blank') viewer.src = 'about:blank';
    if (live) {
        const region = state.viewport || null;
        const signature = JSON.stringify(region);
        if (signature !== previousViewport) {
            viewer.contentWindow?.postMessage({ type: 'kakatta:viewport', region }, location.origin);
            previousViewport = signature;
        }
    } else previousViewport = 'null';
    session.disabled = busy || ['opening', 'ready', 'running', 'verification'].includes(phase);
    run.disabled = busy || state.running || !['idle', 'completed', 'failed', 'closed'].includes(phase);
    close.disabled = busy || !live;
    verify.hidden = phase !== 'verification' && !state.verification_pending;
    confirm.hidden = phase !== 'verification';
    verify.disabled = confirm.disabled = busy;
    const entries = state.events || [];
    logSection.hidden = entries.length === 0;
    events.replaceChildren(...entries.slice(-100).map(entry => {
        const row = document.createElement('li');
        const time = document.createElement('time');
        const date = new Date(entry.at);
        time.textContent = Number.isNaN(date.valueOf()) ? '' : date.toLocaleTimeString('zh-CN', { hour12: false });
        const label = document.createElement('span');
        label.textContent = entry.event;
        row.append(time, label);
        if (entry.fields && Object.keys(entry.fields).length) {
            const details = document.createElement('pre');
            details.textContent = Object.entries(entry.fields).map(([key, value]) => `${key}：${typeof value === 'object' ? JSON.stringify(value) : value}`).join(' · ');
            row.append(details);
        }
        return row;
    }));
    if (!terminal.has(phase)) requestPoll(); else stopPoll();
}
async function refresh() {
    stopPoll();
    try {
        const response = await fetch('/status', { credentials: 'same-origin' });
        if (!response.ok) throw new Error('读取实验状态失败，请刷新页面重试。');
        show(await response.json());
    } catch (failure) { showError(failure.message); }
}
async function action(path) {
    busy = true;
    for (const button of [session, run, close, verify, confirm]) button.disabled = true;
    showError('');
    let message = '';
    try {
        const response = await fetch(path, { method: 'POST', credentials: 'same-origin' });
        if (!response.ok) {
            const reason = response.status === 409 ? (await response.text()).trim() : '';
            throw new Error(reason || '操作未完成，请查看实验记录后重试。');
        }
    } catch (failure) { message = failure.message; }
    finally { busy = false; await refresh(); if (message) showError(message); }
}
session.addEventListener('click', () => action('/session'));
run.addEventListener('click', () => action('/run'));
close.addEventListener('click', () => action('/close'));
verify.addEventListener('click', () => action('/verify'));
confirm.addEventListener('click', () => action('/confirm'));
window.addEventListener('pagehide', () => { pageActive = false; stopPoll(); });
refresh();
