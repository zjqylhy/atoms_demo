// Platform-owned runtime. The sandbox receives a private message port, never credentials.
(() => {
  if (window.Atoms) return;
  let port, connect, failedSave;
  const waiting = new Map();
  const ready = new Promise(resolve => { connect = resolve; });
  const instance = new URL(location.href).searchParams.get('instance');
  let parentOrigin;
  // The preview is served from the host's URL origin. A sandbox self-reload may
  // clear document.referrer, so it is not a stable handshake authority.
  try { parentOrigin = new URL(location.href).origin; } catch { parentOrigin = ''; }
  addEventListener('message', event => {
    if (event.source !== parent || event.origin !== parentOrigin || !instance || event.data?.type !== 'atoms-connect' || event.data.instance !== instance || event.ports.length !== 1 || port) return;
    port = event.ports[0];
    port.onmessage = ({data}) => {
      const pending = waiting.get(data?.id);
      if (!pending) return;
      waiting.delete(data.id); clearTimeout(pending.timer);
      if (data.error) pending.reject(Object.assign(new Error(data.error), {status:data.status}));
      else pending.resolve(data.result);
    };
    port.start(); connect();
  });
  async function request(op, args) {
    await Promise.race([ready, new Promise((_,reject) => setTimeout(() => reject(new Error('应用数据服务未连接，请刷新预览')),15000))]);
    const id = crypto.randomUUID();
    return new Promise((resolve,reject) => {
      const timer = setTimeout(() => { waiting.delete(id); reject(new Error('保存结果未知，请使用相同请求重试；不要关闭页面')); },20000);
      waiting.set(id,{resolve,reject,timer}); port.postMessage({id,op,...args});
    });
  }
  const data = Object.freeze({
    load: () => request('load',{}),
    save: async (value, options = {}) => {
      const signature = JSON.stringify([value,options.expectedRevision]);
      const requestId = options.requestId || (failedSave?.signature === signature ? failedSave.requestId : crypto.randomUUID());
      failedSave = {signature,requestId};
      const result = await request('save',{value,expectedRevision:options.expectedRevision,requestId});
      failedSave = undefined; return result;
    }
  });
  Object.defineProperty(window,'Atoms',{value:Object.freeze({data}),writable:false,configurable:false});
})();
