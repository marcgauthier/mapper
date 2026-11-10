// Browser-native HTTP decoding handles Content-Encoding: zstd. Uploads and
// WebSocket data use the local Meta Zstandard codec at the exact level 3.
const mapperTransport = (() => {
  const nativeFetch = window.fetch.bind(window);
  const encoder = new TextEncoder(), decoder = new TextDecoder();
  const ready = import('/vendor/zstd/index.web.js').then(async codec => {await codec.init('/vendor/zstd/zstd.wasm');return codec;});
  // A failed codec must not silently send an uncompressed mutation.
  ready.catch(error => console.error('Zstandard transport initialization failed',error));
  async function compressedBlob(blob, signal) {
    const codec = await ready, parts = [], chunk = 1 << 20;
    // Independent frames concatenate into one standard Zstd stream. Blob parts
    // avoid holding a backup-sized ArrayBuffer or Wasm allocation in JS memory.
    for(let offset=0;offset<blob.size;offset+=chunk){
      if(signal?.aborted)throw new DOMException('Request aborted','AbortError');
      const data=new Uint8Array(await blob.slice(offset,offset+chunk).arrayBuffer());
      parts.push(new Blob([codec.compress(data,3)]));
    }
    if(!parts.length)parts.push(new Blob([codec.compress(new Uint8Array(),3)]));
    return new Blob(parts);
  }
  window.fetch=async function(input,init){
    const request=new Request(input,init);
    if(new URL(request.url).origin!==location.origin||!request.body||request.headers.has('Content-Encoding'))return nativeFetch(request);
    const headers=new Headers(request.headers);headers.set('Content-Encoding','zstd');headers.set('X-Mapper-Compression-Level','3');headers.delete('Content-Length');
    const body=await compressedBlob(await request.blob(),request.signal);
    return nativeFetch(request.url,{method:request.method,headers,body,credentials:request.credentials,mode:request.mode,cache:request.cache,redirect:request.redirect,referrer:request.referrer,referrerPolicy:request.referrerPolicy,integrity:request.integrity,keepalive:request.keepalive,signal:request.signal});
  };
  async function encodeMessage(value){const codec=await ready;return codec.compress(encoder.encode(JSON.stringify(value)),3);}
  async function decodeMessage(data){const codec=await ready;const bytes=data instanceof Blob?new Uint8Array(await data.arrayBuffer()):new Uint8Array(data);if(bytes.byteLength>1<<20)throw Error('WebSocket frame exceeds limit');const decoded=codec.decompress(bytes);if(decoded.byteLength>1<<20)throw Error('WebSocket message exceeds limit');return JSON.parse(decoder.decode(decoded));}
  return {ready,encodeMessage,decodeMessage,compressedBlob,webSocketProtocol:'mapper.zstd.v1'};
})();

// Report browser exceptions to the application's system log without recursion.
(() => {
  let recent=[];
  function report(message,stack,source) {
    const now=Date.now(); recent=recent.filter(time=>now-time<60000);
    if(recent.length>=10)return; recent.push(now);
    const payload=JSON.stringify({message:String(message||'Unknown browser error').slice(0,2000),stack:String(stack||'').slice(0,4000),source:String(source||location.pathname).slice(0,1000)});
    fetch('/api/client-errors',{method:'POST',headers:{'Content-Type':'application/json'},body:payload}).catch(()=>{});
  }
  window.addEventListener('error',event=>{if(event.message)report(event.message,event.error?.stack,event.filename)});
  window.addEventListener('unhandledrejection',event=>report(event.reason?.message||event.reason,event.reason?.stack,location.pathname));
})();
