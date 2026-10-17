(() => {
'use strict';
const $=id=>document.getElementById(id),app=window.YATFHS;
let signedIn=false,dirty=false,logs=[],refreshing=false;
function message(e){$('adminError').textContent=e.message||String(e);if(e.status===401)logout();}
function logout(){signedIn=false;app.pause();app.setToken('');$('dashboard').hidden=true;$('loginPanel').hidden=false;$('token').value='';for(const id of ['adminFiles','adminLogs','statsGrid'])$(id).replaceChildren();logs=[];}
function fillPolicy(p){$('policyMin').value=p.min_expire_sec;$('policyDefault').value=p.default_expire_sec;$('policyMax').value=p.max_expire_sec;$('policyOptions').value=p.expire_options_sec.join(', ');dirty=false;}
function cell(row,text){const td=document.createElement('td');td.textContent=text;row.append(td);return td;}
function renderLogs(){const filter=$('logFilter').value;$('adminLogs').replaceChildren();for(const e of [...logs].reverse()){if(filter&&e.kind!==filter)continue;const row=document.createElement('tr');for(const text of [new Date(e.time).toLocaleString(),e.kind,e.ip||'—',[e.method,e.path,e.message].filter(Boolean).join(' '),e.status||'—',e.bytes??'—',e.duration_ms??'—'])cell(row,text);$('adminLogs').append(row);}}
async function refresh(initial=false){
 if(refreshing||!signedIn)return;refreshing=true;
 try{
  const [overview,files]=await Promise.all([app.api('/api/v1/admin/overview'),app.api('/api/v1/admin/files')]);
  if(!signedIn)return;
  const stats=overview.stats;$('statsGrid').replaceChildren();
  for(const [label,value] of [['Active files',stats.active_files],['Stored',app.bytes(stats.stored_bytes)],['Uptime',app.duration(stats.uptime_seconds)],['Requests',stats.requests],['Errors (4xx/5xx)',stats.errors],['Uploads',stats.uploads],['Downloaded files',stats.downloads],['Uploaded',app.bytes(stats.uploaded_bytes)],['Downloaded',app.bytes(stats.downloaded_bytes)],['Memory',app.bytes(stats.memory_bytes)],['Goroutines',stats.goroutines],['Runtime',stats.go_version]]){const box=document.createElement('div');box.className='stat';const labelNode=document.createElement('span');labelNode.className='small';labelNode.textContent=label;const strong=document.createElement('strong');strong.textContent=value;box.append(labelNode,strong);$('statsGrid').append(box);}
  if(stats.log_error)$('adminError').textContent=`Log persistence error: ${stats.log_error}`;
  if(!dirty)fillPolicy(overview.policy);if(initial)app.updatePolicy(overview.policy,overview.max_file_size);
  const checked=new Set([...document.querySelectorAll('#adminFiles input:checked')].map(i=>i.value));$('adminFiles').replaceChildren();
  for(const entry of files){const m=entry.file,result=entry.download,row=document.createElement('tr');const select=document.createElement('input');select.type='checkbox';select.value=m.id;select.checked=checked.has(m.id);select.setAttribute('aria-label',`Select ${m.orig_name}`);const choose=document.createElement('label');choose.className='file-select';choose.append(select);cell(row,'').append(choose);const link=document.createElement('a');link.href=result.url;link.textContent=m.orig_name;cell(row,'').append(link);cell(row,app.bytes(m.size));cell(row,new Date(m.uploaded_at).toLocaleString());cell(row,result.expires_at?new Date(result.expires_at).toLocaleString():'Never');cell(row,m.uploader_ip);$('adminFiles').append(row);}
  logs=overview.logs;renderLogs();$('lastRefresh').textContent=`Updated ${new Date().toLocaleTimeString()}`;
 }catch(e){message(e);}finally{refreshing=false;}
}
$('loginForm').addEventListener('submit',async e=>{e.preventDefault();$('adminError').textContent='';app.setToken($('token').value);try{await app.api('/api/v1/admin/settings');signedIn=true;$('token').value='';$('loginPanel').hidden=true;$('dashboard').hidden=false;dirty=false;await refresh(true);}catch(err){app.setToken('');message(err);}});
$('logoutBtn').addEventListener('click',logout);
$('refreshAdmin').addEventListener('click',()=>refresh());
$('cleanupBtn').addEventListener('click',async()=>{try{await app.api('/api/v1/admin/cleanup',{method:'POST'});await refresh();}catch(e){message(e);}});
$('policyForm').addEventListener('input',()=>{dirty=true;$('policyStatus').textContent='Unsaved changes';});
$('policyForm').addEventListener('submit',async e=>{
 e.preventDefault();$('adminError').textContent='';
 try{const policy={min_expire_sec:Number($('policyMin').value),default_expire_sec:Number($('policyDefault').value),max_expire_sec:Number($('policyMax').value),expire_options_sec:$('policyOptions').value.split(',').map(s=>Number(s.trim()))};const data=await app.api('/api/v1/admin/settings',{method:'PUT',body:JSON.stringify(policy)});fillPolicy(data);const stats=await app.api('/api/v1/stats');app.updatePolicy(data,stats.max_file_size);$('policyStatus').textContent='Saved; public webpage and API now use this policy.';}catch(err){message(err);}
});
$('logFilter').addEventListener('change',renderLogs);
window.addEventListener('yatfhs-uploaded',()=>refresh());
setInterval(()=>{if(signedIn&&!document.hidden)refresh();},15000);
})();
