export function attachTouch(canvas, sendPointer, scroller, isPanning = () => false) {
    let active = null;
    let last = { x: 0, y: 0 };
    let start = null;
    const listeners = [];
    const listen = (target, name, handler, options = false) => {
        target.addEventListener(name, handler, options);
        listeners.push(() => target.removeEventListener(name, handler, options));
    };
    const position = event => {
        const rect = canvas.getBoundingClientRect();
        return {
            x: Math.max(0, Math.min(rect.width - 1, event.clientX - rect.left)),
            y: Math.max(0, Math.min(rect.height - 1, event.clientY - rect.top)),
        };
    };
    const block = event => { event.preventDefault(); event.stopPropagation(); };
    const release = () => {
        if (active !== null && !start?.panning) sendPointer(last.x, last.y, 0);
        active = null;
        start = null;
    };
    listen(canvas, 'pointerdown', event => {
        if (event.pointerType === 'mouse' && !isPanning()) return;
        block(event);
        if (active !== null) return;
        active = event.pointerId;
        last = position(event);
        start = { x: event.clientX, y: event.clientY, left: scroller.scrollLeft, top: scroller.scrollTop, panning: isPanning() };
        canvas.setPointerCapture?.(active);
        canvas.focus?.({ preventScroll: true });
        if (!start.panning) sendPointer(last.x, last.y, 1);
    });
    listen(canvas, 'pointermove', event => {
        if (event.pointerId !== active) return;
        block(event);
        if (start.panning) {
            scroller.scrollLeft = start.left + start.x - event.clientX;
            scroller.scrollTop = start.top + start.y - event.clientY;
        } else {
            last = position(event);
            sendPointer(last.x, last.y, 1);
        }
    });
    for (const name of ['pointerup', 'pointercancel', 'lostpointercapture']) {
        listen(canvas, name, event => {
            if (event.pointerId !== active) return;
            block(event);
            if (name === 'pointerup' && !start.panning) last = position(event);
            const pointer = active;
            release();
            if (canvas.hasPointerCapture?.(pointer)) canvas.releasePointerCapture(pointer);
        });
    }
    for (const name of ['touchstart', 'touchmove', 'touchend', 'gesturestart', 'gesturechange', 'gestureend']) {
        listen(canvas, name, event => event.preventDefault(), { passive: false });
    }
    listen(canvas, 'wheel', event => {
        if (event.ctrlKey) { event.preventDefault(); event.stopImmediatePropagation(); }
    }, { passive: false, capture: true });
    listen(globalThis, 'blur', release);
    if (globalThis.document) listen(document, 'visibilitychange', () => { if (document.hidden) release(); });
    return () => { release(); listeners.forEach(remove => remove()); };
}
