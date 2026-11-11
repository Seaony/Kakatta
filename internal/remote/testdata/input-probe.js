(() => {
  const atom = value => {
    if (value === undefined) return {type: 'undefined'};
    if (typeof value === 'number' && !Number.isFinite(value)) return {type: 'number', value: String(value)};
    if (typeof value === 'bigint') return {type: 'bigint', value: String(value)};
    if (typeof value === 'symbol') return {type: 'symbol', global: Symbol.keyFor(value) ?? null, description: value.description ?? null};
    return {type: typeof value, value};
  };
  const keyName = key => typeof key === 'symbol' ? `@@${Symbol.keyFor(key) ?? key.description ?? ''}` : key;
  const functionValue = fn => ({type: 'function', name: fn.name, length: fn.length, source: Function.prototype.toString.call(fn)});
  const failure = error => ({name: error.name, message: error.message});
  const sortedKeys = object => Reflect.ownKeys(object).sort((a, b) => keyName(a) < keyName(b) ? -1 : keyName(a) > keyName(b) ? 1 : 0);
  function navigatorSnapshot() {
    const unreadable = [], methodsNotInvoked = [];
    const keysAndDescriptors = object => {
      const inventory = [], first = new Map();
      for (let owner = object, level = 0; owner !== null; owner = Object.getPrototypeOf(owner), level++) {
        for (const key of sortedKeys(owner)) {
          const d = Object.getOwnPropertyDescriptor(owner, key);
          const item = {key: keyName(key), kind: typeof key, level, enumerable: d.enumerable, configurable: d.configurable};
          if ('value' in d) { item.writable = d.writable; item.valueType = typeof d.value; }
          else { item.get = d.get ? functionValue(d.get) : null; item.set = d.set ? functionValue(d.set) : null; }
          inventory.push(item);
          if (!first.has(key)) first.set(key, level);
        }
      }
      return {inventory, first};
    };
    const read = (value, path, expand) => {
      if (typeof value === 'function') { methodsNotInvoked.push(path); return functionValue(value); }
      if (value === null || typeof value !== 'object') return atom(value);
      const result = {type: 'object', tag: Object.prototype.toString.call(value)};
      try { result.json = JSON.parse(JSON.stringify(value)); }
      catch (error) { result.serializationError = failure(error); }
      if (expand) Object.assign(result, objectSnapshot(value, path, false));
      return result;
    };
    const objectSnapshot = (object, path, expand) => {
      const {inventory, first} = keysAndDescriptors(object);
      const properties = {};
      for (const key of [...first.keys()].sort((a, b) => keyName(a) < keyName(b) ? -1 : keyName(a) > keyName(b) ? 1 : 0)) {
        const childPath = `${path}.${keyName(key)}`;
        if (key === '__proto__') { properties[keyName(key)] = {type:'prototype', tag:Object.prototype.toString.call(Object.getPrototypeOf(object))}; continue; }
        try { properties[keyName(key)] = read(Reflect.get(object, key, object), childPath, expand); }
        catch (error) { properties[keyName(key)] = {throws: failure(error)}; unreadable.push({path: childPath, ...failure(error)}); }
      }
      return {inventory, properties};
    };
    const snapshot = objectSnapshot(navigator, 'navigator', true);
    return {...snapshot, unreadable, methodsNotInvoked,
      scope: '完整盘点 Navigator 实例到原型链的属性名和描述符；读取可序列化值，对对象最多展开一层，不调用异步或权限方法、不等待 Promise；JSON 序列化可能调用本地 toJSON'};
  }
  const state = {events: [], navigatorSnapshot};
  for (const type of ['keydown', 'keyup', 'beforeinput', 'input', 'compositionstart', 'compositionupdate', 'compositionend']) {
    document.addEventListener(type, event => {
      const element = event.target;
      state.events.push({type, key: event.key ?? null, code: event.code ?? null,
        keyCode: event.keyCode ?? null, charCode: event.charCode ?? null, which: event.which ?? null,
        isComposing: event.isComposing ?? null, location: event.location ?? null, repeat: event.repeat ?? null,
        ctrlKey: event.ctrlKey ?? null, altKey: event.altKey ?? null, shiftKey: event.shiftKey ?? null, metaKey: event.metaKey ?? null,
        isTrusted: event.isTrusted, data: event.data ?? null, inputType: event.inputType ?? null,
        value: typeof element?.value === 'string' ? element.value : null,
        selectionStart: element?.selectionStart ?? null, selectionEnd: element?.selectionEnd ?? null,
        timeStamp: event.timeStamp});
    }, true);
  }
  state.read = selector => {
    const element = document.querySelector(selector);
    return {events: state.events.slice(), value: element?.value ?? null,
      selectionStart: element?.selectionStart ?? null, selectionEnd: element?.selectionEnd ?? null};
  };
  globalThis.__rfbContract = state;
})();
