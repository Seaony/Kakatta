const status = document.querySelector('#status');
const frame = document.querySelector('#preview-frame');
const copies = document.querySelector('#preview-copy').content;
const browserLanguage = document.documentElement.lang;
const url = new URL('vnc', location.href);
url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
const socket = new WebSocket(url);
socket.binaryType = 'arraybuffer';
let imageURL;
let connection = 'connecting';

function showCopy() {
    const preference = new URLSearchParams(location.hash.slice(1)).get('lang');
    const language = preference === 'en' || preference === 'zh-CN' ? preference : browserLanguage;
    const { dataset: copy } = copies.querySelector(`[lang="${language}"]`);
    document.documentElement.lang = language;
    document.title = `${copy.title} · Kakatta`;
    document.querySelector('#preview-heading').textContent = copy.heading;
    document.querySelector('#help').textContent = copy.help;
    frame.alt = copy.alt;
    status.textContent = copy[connection];
}

function clearFrame() {
    frame.hidden = true;
    frame.removeAttribute('src');
    if (imageURL) URL.revokeObjectURL(imageURL);
    imageURL = undefined;
}

socket.addEventListener('message', event => {
    if (!(event.data instanceof ArrayBuffer)) return;
    const nextURL = URL.createObjectURL(new Blob([event.data], { type: 'image/jpeg' }));
    frame.src = nextURL;
    frame.hidden = false;
    if (connection !== 'connected') {
        connection = 'connected';
        showCopy();
    }
    if (imageURL) URL.revokeObjectURL(imageURL);
    imageURL = nextURL;
});
socket.addEventListener('close', () => {
    clearFrame();
    connection = 'disconnected';
    showCopy();
});
window.addEventListener('pagehide', () => {
    socket.close();
    clearFrame();
});
window.addEventListener('hashchange', showCopy);
showCopy();
