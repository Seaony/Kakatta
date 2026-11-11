import keysyms from './novnc/core/input/keysymdef.js';
import KeyTable from './novnc/core/input/keysym.js';

export function attachTextInput(elements, sendKey, copy = { sent: '文字已发送，本地输入已清空。' }) {
    const { toggle, panel, input, send, backspace, enter, close, status } = elements;
    let connected = false;
    let composing = false;
    let sent = false;
    const listeners = [];
    const listen = (node, name, handler) => {
        node.addEventListener(name, handler);
        listeners.push(() => node.removeEventListener(name, handler));
    };
    const update = () => {
        toggle.disabled = !connected;
        send.disabled = !connected || composing || input.value.length === 0;
        backspace.disabled = enter.disabled = !connected || composing;
    };
    const hide = () => {
        input.value = '';
        composing = false;
        status.textContent = '';
        sent = false;
        panel.hidden = true;
        toggle.setAttribute('aria-expanded', 'false');
        update();
    };
    listen(toggle, 'click', () => {
        if (!connected) return;
        if (!panel.hidden) { hide(); return; }
        panel.hidden = false;
        toggle.setAttribute('aria-expanded', 'true');
        input.focus();
    });
    listen(input, 'compositionstart', () => { composing = true; update(); });
    listen(input, 'compositionend', () => { composing = false; update(); });
    listen(input, 'input', () => { status.textContent = ''; sent = false; update(); });
    listen(send, 'click', () => {
        if (!connected || composing || input.value.length === 0) return;
        const text = input.value.replace(/\r\n?/g, '\n');
        input.value = '';
        update();
        for (const character of text) {
            const point = character.codePointAt(0);
            if (character === '\n') sendKey(KeyTable.XK_Return, 'Enter');
            else if (character === '\t') sendKey(KeyTable.XK_Tab, 'Tab');
            else if (point >= 0x20 && (point < 0x7f || point > 0x9f)) sendKey(keysyms.lookup(point));
        }
        sent = true;
        status.textContent = copy.sent;
    });
    listen(backspace, 'click', () => { if (connected && !composing) sendKey(KeyTable.XK_BackSpace, 'Backspace'); });
    listen(enter, 'click', () => { if (connected && !composing) sendKey(KeyTable.XK_Return, 'Enter'); });
    listen(close, 'click', () => { hide(); toggle.focus(); });
    hide();
    return {
        setConnected(value) { connected = value; if (!value) hide(); else update(); },
        setCopy(value) { copy = value; if (sent) status.textContent = copy.sent; },
        dispose() { connected = false; hide(); listeners.forEach(remove => remove()); },
    };
}
