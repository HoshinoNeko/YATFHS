(() => {
'use strict';
const $ = id => document.getElementById(id);
const admin = location.pathname === '/admin';
const rows = [];
let policy, maxSize = 0, busy = false, controller, adminToken = '';
const storageKey = 'yatfhs-resume-v1';
let saved;
try { saved = JSON.parse(localStorage.getItem(storageKey) || '{}') || {}; if(typeof saved!=='object'||Array.isArray(saved))saved={}; } catch { saved = {}; }
function persist() { try { localStorage.setItem(storageKey, JSON.stringify(saved)); } catch { $('uploadError').textContent = 'Browser storage is unavailable. Keep this tab open to resume.'; } }
function bytes(n) { return n < 1024 ? `${n} B` : n < 1048576 ? `${(n/1024).toFixed(1)} KB` : `${(n/1048576).toFixed(2)} MB`; }
function duration(n) { return n === 0 ? 'Never' : n % 86400 === 0 ? `${n/86400} d` : n % 3600 === 0 ? `${n/3600} h` : n % 60 === 0 ? `${n/60} min` : `${n} s`; }
function shellQuote(text) { return "'" + text.replace(/'/g, "'\\''") + "'"; }
async function api(path, options = {}) {
 const timeout = new AbortController();
 const signal = options.signal;
 const abort = () => timeout.abort();
 if (signal?.aborted) timeout.abort();
 signal?.addEventListener('abort', abort, {once:true});
 const timer = setTimeout(abort, 60000);
 try {
  const response = await fetch(path, {...options, signal:timeout.signal, headers:{...(options.body ? {'Content-Type':'application/json'} : {}), ...(adminToken ? {Authorization:`Bearer ${adminToken}`} : {}), ...options.headers}});
  let data; try { data = await response.json(); } catch { throw new Error(`Invalid server response (HTTP ${response.status})`); }
  if (!response.ok || data.status !== 'success') { const e = new Error(data.message || `HTTP ${response.status}`); e.status = response.status; throw e; }
  return data.data;
 } finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); }
}
function updateButtons() {
 $('uploadBtn').disabled = busy || !policy || !rows.some(r => !r.result) || (admin && !adminToken);
 $('pauseBtn').disabled = !busy;
 $('fileInput').disabled = busy;
 $('customExpire').disabled = busy;
 for (const b of $('expireOptions').children) b.disabled = busy;
 if ($('expireAt')) $('expireAt').disabled = busy;
 for (const r of rows) r.remove.disabled = busy;
}
function updatePolicy(p, limit) {
 policy = p; maxSize = limit;
 $('sizeLabel').textContent = `max ${bytes(maxSize)} per file · 4 MB chunks · resumable for 24 hours`;
 if ($('limitsLabel')) $('limitsLabel').textContent = `temporary file hosting · max ${bytes(maxSize)} · up to ${duration(p.max_expire_sec)}`;
 $('customExpire').min = admin ? 0 : p.min_expire_sec;
 $('customExpire').max = admin ? 315360000 : p.max_expire_sec;
 $('customExpire').value = p.default_expire_sec;
 $('expireOptions').replaceChildren();
 for (const n of [...p.expire_options_sec, ...(admin ? [0] : [])]) {
  const b = document.createElement('button'); b.type = 'button'; b.className = 'expire-btn'; b.textContent = duration(n); b.dataset.secs = n;
  b.addEventListener('click', () => { $('customExpire').value = n; if ($('expireAt')) $('expireAt').value = ''; markExpiry(); }); $('expireOptions').append(b);
 }
 $('expireHint').textContent = admin ? '0 = never; custom expiry up to 10 years' : `${p.min_expire_sec}–${p.max_expire_sec} seconds`;
 markExpiry(); updateDocs(); updateButtons();
}
function markExpiry() { for (const b of $('expireOptions').children) b.classList.toggle('active', Number(b.dataset.secs) === Number($('customExpire').value)); }
$('customExpire').addEventListener('input', () => {if ($('expireAt')) $('expireAt').value = ''; markExpiry();});
$('expireAt')?.addEventListener('input', () => { if ($('expireAt').value) $('customExpire').value = Math.ceil((new Date($('expireAt').value).getTime()-Date.now())/1000); markExpiry(); });
function updateDocs() {
 if (!$('apiUpload')) return;
 const endpoint = new URL('/api/v1/upload', location.href).href;
 const seconds = policy?.default_expire_sec || 3600;
 $('apiUpload').textContent = `# Basic upload\ncurl -F 'file=@/path/to/file.zip' ${shellQuote(endpoint)}\n\n# Custom expiry (seconds)\ncurl -F 'file=@/path/to/file.zip' -F 'expire=${seconds}' ${shellQuote(endpoint)}`;
 $('apiParams').textContent = policy ? `file: required; max ${bytes(maxSize)}\nexpire: optional integer, ${policy.min_expire_sec}–${policy.max_expire_sec} seconds\ndefault: ${policy.default_expire_sec} seconds\nGET ${new URL('/api/v1/stats',location.href).href} for current limits.` : 'Loading limits…';
 $('apiResponse').textContent = JSON.stringify({status:'success',data:{url:new URL('/{id}/{filename}',location.href).href,id:'{id}',name:'file.zip',size:123,expires_at:'2026-10-18T12:00:00Z'}},null,2);
 $('apiChunks').textContent = `# Resumable protocol\nPOST ${new URL('/api/v1/uploads',location.href).href}\nJSON: {"name":"file.zip","size":123,"expire":${seconds},"fingerprint":"client-content-hash"}\nGET /api/v1/uploads/{session_id} → uploaded_chunks, chunk_size\nPUT /api/v1/uploads/{session_id}/chunks/{index} → raw bytes (4 MiB, last chunk may be shorter)\nPOST /api/v1/uploads/{session_id}/complete → download URL\nDELETE /api/v1/uploads/{session_id} → cancel\nSession IDs are secret capabilities; keep them private.\n\n# Batch download\nPOST ${new URL('/api/v1/downloads',location.href).href}\nJSON: {"ids":["file_id_1","file_id_2"]} → streaming ZIP`;
}
updateDocs();
function addFiles(files) {
 if (busy) return;
 for (const file of files) {
  if (rows.some(r => r.file.name === file.name && r.file.size === file.size && r.file.lastModified === file.lastModified)) continue;
  const card = document.createElement('div'); card.className = 'queue-item';
  const head = document.createElement('div'); head.className = 'queue-head';
  const select = document.createElement('input'); select.type = 'checkbox'; select.disabled = true; select.setAttribute('aria-label',`Select ${file.name} for download`);
  const name = document.createElement('span'); name.className = 'queue-name'; name.textContent = `${file.name} · ${bytes(file.size)}`;
  head.append(select, name);
  const progress = document.createElement('progress'); progress.max = 100; progress.value = 0; progress.setAttribute('aria-label',`${file.name} upload progress`);
  const state = document.createElement('p'); state.className = 'queue-state'; state.setAttribute('role','status'); state.textContent = 'Ready';
  const link = document.createElement('a'); link.hidden = true;
  const actions = document.createElement('div'); actions.className = 'queue-actions';
  const copy = document.createElement('button'); copy.className = 'btn-copy'; copy.textContent = 'Copy URL'; copy.hidden = true;
  const curl = document.createElement('button'); curl.className = 'btn-copy'; curl.textContent = 'Copy curl'; curl.hidden = true;
  const remove = document.createElement('button'); remove.className = 'btn-copy'; remove.textContent = 'Remove';
  actions.append(copy, curl, remove); card.append(head,progress,state,link,actions); $('queue').append(card);
  const row = {file,card,select,progress,state,link,copy,curl,remove,result:null}; rows.push(row);
  copy.addEventListener('click', () => copyText(row.result.url, copy));
  curl.addEventListener('click', () => copyText(`curl -OJ ${shellQuote(row.result.url)}`,curl));
  remove.addEventListener('click', async () => {
   remove.disabled = true;
   try { if (row.session && !row.result) await api(`/api/v1/uploads/${row.session}`,{method:'DELETE'}); if (row.key) {delete saved[row.key];persist();} rows.splice(rows.indexOf(row),1);card.remove();updateButtons(); }
   catch(e) { state.textContent = e.message;remove.disabled = false; }
  });
 }
 $('fileLabel').textContent = `${rows.length} file(s) selected`;updateButtons();
}
$('fileInput').addEventListener('change', () => {addFiles($('fileInput').files);$('fileInput').value = '';});
$('dropZone').addEventListener('dragover', e => {e.preventDefault();if (!busy) $('dropZone').classList.add('drag-over');});
$('dropZone').addEventListener('dragleave', () => $('dropZone').classList.remove('drag-over'));
$('dropZone').addEventListener('drop', e => {e.preventDefault();$('dropZone').classList.remove('drag-over');addFiles(e.dataTransfer.files);});
window.addEventListener('dragover',e => e.preventDefault());window.addEventListener('drop',e => e.preventDefault());

// SHA-256 fallback keeps resumable file identity available on plain HTTP intranets.
// Fingerprints cover every chunk, so files with matching names and sizes cannot be mixed.
function sha256(buffer) {
 const input = new Uint8Array(buffer), length = Math.ceil((input.length+9)/64)*64;
 const data = new Uint8Array(length);data.set(input);data[input.length] = 128;
 const view = new DataView(data.buffer);view.setUint32(length-8,Math.floor(input.length/0x20000000));view.setUint32(length-4,(input.length*8)>>>0);
 const k = [0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2];
 const h = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19], w = new Uint32Array(64);
 const rr = (x,n) => (x>>>n)|(x<<(32-n));
 for (let offset=0;offset<length;offset+=64) {
  for (let i=0;i<16;i++) w[i] = view.getUint32(offset+i*4);
  for (let i=16;i<64;i++) {const a=w[i-15],b=w[i-2];w[i]=(w[i-16]+(rr(a,7)^rr(a,18)^(a>>>3))+w[i-7]+(rr(b,17)^rr(b,19)^(b>>>10)))>>>0;}
  let [a,b,c,d,e,f,g,z]=h;
  for (let i=0;i<64;i++){const t1=(z+(rr(e,6)^rr(e,11)^rr(e,25))+((e&f)^(~e&g))+k[i]+w[i])>>>0,t2=((rr(a,2)^rr(a,13)^rr(a,22))+((a&b)^(a&c)^(b&c)))>>>0;z=g;g=f;f=e;e=(d+t1)>>>0;d=c;c=b;b=a;a=(t1+t2)>>>0;}
  [a,b,c,d,e,f,g,z].forEach((n,i) => {h[i]=(h[i]+n)>>>0;});
 }
 return h.map(n => n.toString(16).padStart(8,'0')).join('');
}
async function digest(buffer) { if (globalThis.crypto?.subtle) return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256',buffer)),n => n.toString(16).padStart(2,'0')).join(''); return sha256(buffer); }
function checkPause() {if(controller.signal.aborted) throw new DOMException('Paused','AbortError');}
async function fingerprint(row) {
 if(row.fingerprint) return row.fingerprint;
 const hashes=[];
 for(let offset=0;offset<row.file.size;offset+=4194304){checkPause();hashes.push(await digest(await row.file.slice(offset,offset+4194304).arrayBuffer()));row.state.textContent=`Checking file identity… ${Math.min(100,Math.round((offset+4194304)/row.file.size*100))}%`;await new Promise(resolve => setTimeout(resolve,0));}
 checkPause();row.fingerprint = await digest(new TextEncoder().encode(JSON.stringify([row.file.name,row.file.size,row.file.lastModified,hashes])).buffer);return row.fingerprint;
}
function chunkXHR(path,blob,row,done,total) {
 return new Promise((resolve,reject) => {
  const xhr=new XMLHttpRequest();xhr.open('PUT',path);xhr.timeout=120000;if(adminToken)xhr.setRequestHeader('Authorization',`Bearer ${adminToken}`);
  const abort=()=>xhr.abort();controller.signal.addEventListener('abort',abort,{once:true});
  function finish(error){controller.signal.removeEventListener('abort',abort);error?reject(error):resolve();}
  xhr.upload.onprogress=e=>{if(e.lengthComputable){const n=Math.min(total,done+e.loaded);row.progress.value=total?n/total*100:100;row.state.textContent=`Uploading ${row.progress.value.toFixed(1)}% · ${bytes(n)} / ${bytes(total)}`;}};
  xhr.onload=()=>{if(xhr.status>=200&&xhr.status<300){finish();return;}let message=`HTTP ${xhr.status}`;try{message=JSON.parse(xhr.responseText).message||message;}catch{}const e=new Error(message);e.status=xhr.status;finish(e);};
  xhr.onerror=()=>finish(new Error('Network error'));xhr.ontimeout=()=>finish(new Error('Upload timed out'));xhr.onabort=()=>finish(new DOMException('Paused','AbortError'));
  if(controller.signal.aborted){finish(new DOMException('Paused','AbortError'));return;}xhr.send(blob);
 });
}
function completed(row,result){row.result=result;row.progress.value=100;row.state.textContent=`Complete · ${result.expires_at ? 'expires '+new Date(result.expires_at).toLocaleString() : 'never expires'}`;row.link.href=result.url;row.link.textContent=result.url;row.link.hidden=false;row.select.disabled=false;row.select.checked=true;row.copy.hidden=false;row.curl.hidden=false;}
async function uploadRow(row,expire) {
 if(row.file.size>maxSize)throw new Error(`File exceeds maximum of ${bytes(maxSize)}`);
 const fp=await fingerprint(row);checkPause();row.key=`${admin?'admin':'public'}:${fp}:${expire}`;
 let session;
 if(saved[row.key]){try{session=await api(`/api/v1/uploads/${saved[row.key].id}`,{signal:controller.signal});if(session.result_expired){delete saved[row.key];session=null;}}catch(e){if(e.status===404||e.status===410){delete saved[row.key];}else throw e;}}
 if(!session){session=await api('/api/v1/uploads',{method:'POST',body:JSON.stringify({name:row.file.name,size:row.file.size,expire,fingerprint:fp}),signal:controller.signal});saved[row.key]={id:session.id,expires_at:session.expires_at};persist();}
 row.session=session.id;
 if(session.result){completed(row,session.result);delete saved[row.key];persist();return;}
 if(session.fingerprint!==fp||session.size!==row.file.size||session.expire!==expire)throw new Error('Saved upload does not match this file. Remove it and start again.');
 const uploaded=new Set(session.uploaded_chunks),size=session.chunk_size,count=Math.ceil(row.file.size/size);let done=0;
 for(const i of uploaded)done+=Math.min(size,row.file.size-i*size);
 row.progress.value=row.file.size?done/row.file.size*100:0;row.state.textContent=`Resuming from ${bytes(done)} / ${bytes(row.file.size)}`;
 for(let index=0;index<count;index++){checkPause();if(uploaded.has(index))continue;const blob=row.file.slice(index*size,Math.min(row.file.size,(index+1)*size));
  for(let attempt=0;;attempt++){try{await chunkXHR(`/api/v1/uploads/${session.id}/chunks/${index}`,blob,row,done,row.file.size);break;}catch(e){if(e.name==='AbortError'||attempt>=2||(e.status&&e.status<500))throw e;row.state.textContent=`Retrying chunk ${index+1}…`;await new Promise(resolve=>setTimeout(resolve,500*2**attempt));checkPause();}}
  done+=blob.size;
 }
 checkPause();row.state.textContent='Finalizing file…';const result=await api(`/api/v1/uploads/${session.id}/complete`,{method:'POST',signal:controller.signal});completed(row,result);delete saved[row.key];persist();window.dispatchEvent(new CustomEvent('yatfhs-uploaded'));
}
$('uploadBtn').addEventListener('click',async()=>{
 $('uploadError').textContent='';let expire=Number($('customExpire').value);
 if(admin&&$('expireAt').value){expire=Math.ceil((new Date($('expireAt').value).getTime()-Date.now())/1000);if(expire<=0){$('uploadError').textContent='Choose an expiry time in the future.';return;}}
 if(!Number.isInteger(expire)||$('customExpire').value===''||expire<(admin?0:policy.min_expire_sec)||expire>(admin?315360000:policy.max_expire_sec)){ $('uploadError').textContent='Choose an expiry within the allowed range.';return; }
 busy=true;controller=new AbortController();updateButtons();let failed=0;
 try{for(const row of rows){if(row.result)continue;try{await uploadRow(row,expire);}catch(e){if(controller.signal.aborted){row.state.textContent='Paused. Click Resume uploads, or re-select this file after a refresh.';break;}row.state.textContent=`Upload failed: ${e.message}. Click Resume uploads to retry.`;failed++;}}}
 finally{busy=false;$('uploadBtn').textContent=rows.some(r=>!r.result)?'Resume uploads':'Upload files';$('overall').textContent=`${rows.filter(r=>r.result).length}/${rows.length} complete${failed?` · ${failed} failed`:''}`;updateButtons();}
});
$('pauseBtn').addEventListener('click',()=>{controller?.abort();$('pauseBtn').disabled=true;});
window.addEventListener('beforeunload',e=>{if(busy){e.preventDefault();e.returnValue='';}});
async function copyText(text,button){try{if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(text);}else{const input=document.createElement('textarea');input.value=text;input.style.position='fixed';document.body.append(input);input.select();const copied=document.execCommand('copy');input.remove();if(!copied)throw new Error('Copy unavailable');}const label=button.textContent;button.textContent='Copied!';setTimeout(()=>button.textContent=label,1500);}catch(e){$('uploadError').textContent=e.message;}}
$('downloadBtn').addEventListener('click',()=>{
 $('downloadError').textContent='';
 try{
  const ids=new Set(rows.filter(r=>r.result&&r.select.checked).map(r=>r.result.id));
  for(const input of document.querySelectorAll('#adminFiles input:checked'))ids.add(input.value);
  for(const line of ($('downloadURLs')?.value||'').split(/\n/).map(s=>s.trim()).filter(Boolean)){const u=new URL(line,location.href),parts=u.pathname.split('/').filter(Boolean);if(u.origin!==location.origin||parts.length!==2)throw new Error('Use file URLs from this site.');ids.add(decodeURIComponent(parts[0]));}
  if(!ids.size||ids.size>100)throw new Error('Select between 1 and 100 files.');
  let frame=$('zipFrame');if(!frame){frame=document.createElement('iframe');frame.name='yatfhs-zip';frame.id='zipFrame';frame.hidden=true;frame.onload=()=>{try{const text=frame.contentDocument.body.textContent;if(text){const data=JSON.parse(text);$('downloadError').textContent=data.message||'Download failed';}}catch{}};document.body.append(frame);}
  const form=document.createElement('form');form.method='POST';form.action='/api/v1/downloads';form.target=frame.name;form.hidden=true;const input=document.createElement('input');input.name='ids';input.value=JSON.stringify([...ids]);form.append(input);document.body.append(form);form.submit();form.remove();
 }catch(e){$('downloadError').textContent=e.message;}
});
// Keep only live pending session references in browser storage.
for(const [key,value] of Object.entries(saved)){if(!value||!value.id||new Date(value.expires_at).getTime()<Date.now())delete saved[key];}persist();
window.YATFHS={api,bytes,duration,updatePolicy,setToken(token){adminToken=token;updateButtons();},pause(){controller?.abort();}};
api('/api/v1/stats').then(data=>updatePolicy(data.limits,data.max_file_size)).catch(e=>{$('uploadError').textContent=`Cannot load upload policy: ${e.message}. Reload to retry.`;});
})();
