import test from 'node:test';
import assert from 'node:assert/strict';
import { constants, createDeflate } from 'node:zlib';

globalThis.window = { console };
const { default: TightDecoder } = await import('./assets/novnc/core/decoders/tight.js');

async function compressedBlocks(pixels) {
    const stream = createDeflate();
    const chunks = [];
    const blocks = [];
    stream.on('data', chunk => chunks.push(chunk));
    try {
        for (let i = 0; i < 2; i++) {
            stream.write(pixels);
            await new Promise((resolve, reject) => stream.flush(constants.Z_SYNC_FLUSH, error => error ? reject(error) : resolve()));
            blocks.push(Buffer.concat(chunks.splice(0)));
        }
        return blocks;
    } finally { stream.destroy(); }
}

function socket(control, bytes) {
    const size = [];
    let length = bytes.length;
    size.push((length & 127) | (length > 127 ? 128 : 0));
    if (length > 127) {
        length >>= 7;
        size.push((length & 127) | (length > 127 ? 128 : 0));
        if (length > 127) size.push(length >> 7);
    }
    const frame = Buffer.concat([Buffer.from([control, ...size]), bytes]);
    let offset = 0;
    return {
        rQwait(_description, count) { return frame.length - offset < count; },
        rQshift8() { return frame[offset++]; },
        rQshiftBytes(count) { const result = frame.subarray(offset, offset + count); offset += count; return result; },
    };
}

test('Tight 流正常连续解码，重置后仍能使用新流历史解码后续矩形', async () => {
    const width = 20, height = 20;
    const pixels = Uint8Array.from({ length: width * height * 3 }, (_, i) => (i * 29 + (i >> 7)) & 255);
    const [first, second] = await compressedBlocks(pixels);
    let rendered = 0;
    const display = { blitImage(_x, _y, _width, _height, actual) {
        for (let pixel = 0; pixel < width * height; pixel++) {
            assert.deepEqual(Array.from(actual.subarray(pixel * 4, pixel * 4 + 4)), [...pixels.subarray(pixel * 3, pixel * 3 + 3), 255]);
        }
        rendered++;
    } };
    const decoder = new TightDecoder();
    const decode = (control, block) => assert.equal(decoder.decodeRect(0, 0, width, height, socket(control, block), display, 24), true);
    decode(0, first);
    decode(0, second);
    for (let i = 0; i < 3; i++) {
        decode(1, first);
        decode(0, second);
    }
    assert.equal(rendered, 8);
});
