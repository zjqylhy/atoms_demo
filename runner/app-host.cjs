// Disposable test data host. This exercises the production SDK protocol;
// process/disk persistence is verified separately against the actual backend.
function appHost(initial = {}) {
  return `<!doctype html><html><head><style>html,body,iframe{margin:0;border:0;width:100%;height:100%}</style></head><body><iframe sandbox="allow-scripts allow-forms"></iframe><script>
  let state={schema_version:1,revision:0,value:${JSON.stringify(initial).replace(/</g,'\\u003c')}},receipts=new Map();
  window.savedRevision=0;
  const frame=document.querySelector('iframe'),instance=crypto.randomUUID();
  frame.onload=()=>{
    const channel=new MessageChannel();
    channel.port1.onmessage=({data})=>{
      try {
        let result;
        if(data.op==='load')result=state;
        else if(data.op==='save'){
          const hash=JSON.stringify([data.value,data.expectedRevision]),old=receipts.get(data.requestId);
          if(old){if(old.hash!==hash)throw Error('请求标识重复');result={revision:old.revision,saved:true};}
          else {if(data.expectedRevision!==state.revision)throw Error('数据冲突');state={...state,value:data.value,revision:state.revision+1};receipts.set(data.requestId,{hash,revision:state.revision});result={revision:state.revision,saved:true};window.savedRevision=state.revision;}
        }else throw Error('未知操作');
        channel.port1.postMessage({id:data.id,result});
      }catch(error){channel.port1.postMessage({id:data.id,error:error.message});}
    };
    frame.contentWindow.postMessage({type:'atoms-connect',instance},'*',[channel.port2]);
  };
  frame.src='/app?instance='+instance;
  </script></body></html>`;
}
module.exports={appHost};
