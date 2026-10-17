// Full browser regression. Requires Go and Chromium (or CHROME_PATH).
import assert from 'node:assert/strict';
import {mkdtemp,readFile,writeFile,mkdir,rm,access} from 'node:fs/promises';
import {tmpdir} from 'node:os';
import {join,resolve} from 'node:path';
import {execFileSync,spawn} from 'node:child_process';
import net from 'node:net';
import vm from 'node:vm';
import {createHash} from 'node:crypto';
import {chromium} from 'playwright';

const root=resolve(import.meta.dirname,'..');
const script=await readFile(join(root,'internal/handler/uploader.js'),'utf8');
const shaSource=script.slice(script.indexOf('function sha256('),script.indexOf('async function digest('));
for(const size of [0,1,55,56,63,64,65,1024,4194304]){
 const data=Buffer.alloc(size,0xab),buffer=data.buffer.slice(data.byteOffset,data.byteOffset+data.byteLength);
 const actual=vm.runInNewContext(`${shaSource};sha256(buffer)`,{buffer});
 assert.equal(actual,createHash('sha256').update(data).digest('hex'),`SHA-256 HTTP fallback, ${size} bytes`);
}
const temp=await mkdtemp(join(tmpdir(),'yatfhs-e2e-'));
const token='browser-test-admin-token-with-32-characters';
const socket=net.createServer();await new Promise(resolve=>socket.listen(0,'127.0.0.1',resolve));const port=socket.address().port;await new Promise(resolve=>socket.close(resolve));
const cfg=JSON.parse(await readFile(join(root,'config.json'),'utf8'));Object.assign(cfg,{host:'127.0.0.1',port:String(port),base_url:'',storage_dir:join(temp,'data')});
await writeFile(join(temp,'config.json'),JSON.stringify(cfg));
execFileSync('go',['build','-o',join(temp,'yatfhs'),'.'],{cwd:root});
const server=spawn(join(temp,'yatfhs'),['-config',join(temp,'config.json')],{env:{...process.env,YATFHS_ADMIN_TOKEN:token}});let serverLog='';server.stderr.on('data',b=>serverLog+=b);
const base=`http://127.0.0.1:${port}`;
let browser;
try{
 let ready=false;for(let i=0;i<100;i++){try{const r=await fetch(base+'/api/v1/stats');if(r.ok){ready=true;break;}}catch{}await new Promise(resolve=>setTimeout(resolve,100));}assert(ready,serverLog);
 let executablePath=process.env.CHROME_PATH;
 if(!executablePath&&process.platform==='darwin'){const chrome='/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';try{await access(chrome);executablePath=chrome;}catch{}}
 browser=await chromium.launch({headless:true,...(executablePath?{executablePath}:{})});
 const context=await browser.newContext({acceptDownloads:true,viewport:{width:1200,height:1000}});const page=await context.newPage();const errors=[];page.on('pageerror',e=>errors.push(e.message));
 async function checkLayout(target,label) {
  const issues=await target.evaluate(async()=>{
   await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
   const visible=selector=>[...document.querySelectorAll(selector)].filter(e=>e.getClientRects().length);
   const errors=[],mobile=innerWidth<=640;
   const equal=(elements,dimension,name)=>{elements=elements.filter(e=>e.getClientRects().length);if(elements.length<2)return;const sizes=elements.map(e=>e.getBoundingClientRect()[dimension]);if(Math.max(...sizes)-Math.min(...sizes)>1)errors.push(`${name}: inconsistent ${dimension} (${sizes.join(', ')})`);};
   if(document.documentElement.scrollWidth>innerWidth)errors.push('page overflows horizontally');
   const buttons=visible('.btn-upload,.btn-copy');equal(buttons,'height','action buttons');
   const fields=visible('input[type=text],input[type=password],input[type=number],input[type=datetime-local],select');equal([...buttons,...fields],'height','buttons and fields');
   equal(visible('.admin-toolbar button'),'width','admin toolbar');
   equal(visible('.settings-grid input[type=number]'),'width','policy number fields');
   equal(visible('.expire-btn'),'width','expiry choices');equal(visible('.expire-btn'),'height','expiry choices');
   for(const group of document.querySelectorAll('.queue-actions')){equal([...group.children],'width','file actions');equal([...group.children],'height','file actions');}
   for(const button of visible('button')){
    const rect=button.getBoundingClientRect(),style=getComputedStyle(button),range=document.createRange();range.selectNodeContents(button);const text=range.getBoundingClientRect();
    if(text.left<rect.left+parseFloat(style.paddingLeft)-1||text.right>rect.right-parseFloat(style.paddingRight)+1)errors.push(`${button.textContent}: label exceeds available width`);
    if(mobile&&(rect.height<44||rect.width<44))errors.push(`${button.textContent}: mobile target is too small`);
   }
   for(const heading of visible('th')){const range=document.createRange();range.selectNodeContents(heading);if(range.getBoundingClientRect().height>parseFloat(getComputedStyle(heading).lineHeight)+1)errors.push(`${heading.textContent}: table heading wraps mid-word`);}
   const pause=document.getElementById('pauseBtn');if(!mobile&&pause.getBoundingClientRect().width>200)errors.push('pause action stretches across the toolbar');
   return errors;
  });assert.deepEqual(issues,[],`${label} layout`);
 }
 async function checkWidths(target,label,widths=[1200,768,390,320]){for(const width of widths){await target.setViewportSize({width,height:1000});await checkLayout(target,`${label} at ${width}px`);}await target.setViewportSize({width:1200,height:1000});}
 async function drop(target,files){await target.evaluate(files=>{const transfer=new DataTransfer();for(const f of files){const raw=Uint8Array.from(atob(f.base64),c=>c.charCodeAt(0));transfer.items.add(new File([raw],f.name,{lastModified:f.modified}));}document.getElementById('dropZone').dispatchEvent(new DragEvent('drop',{bubbles:true,cancelable:true,dataTransfer:transfer}));},files);}
 const large=Buffer.alloc(9*1024*1024,0x5a);large.write('last chunk',large.length-10);
 const files=[{name:'resumable.bin',modified:1700000000000,base64:large.toString('base64')},{name:'dropped.txt',modified:1700000000001,base64:Buffer.from('drag and drop works').toString('base64')}];
 await page.goto(base);await page.waitForFunction(()=>document.getElementById('customExpire').value==='3600');
 assert((await page.locator('#apiUpload').textContent()).includes(base+'/api/v1/upload'));
 assert.equal(await page.evaluate(()=>getComputedStyle(document.body).backgroundColor),'rgb(13, 13, 15)');
 let reached,release;const reachedGate=new Promise(resolve=>reached=resolve),gate=new Promise(resolve=>release=resolve);
 await page.route('**/api/v1/uploads/*/chunks/1',async route=>{reached();await gate;await route.abort('aborted').catch(()=>{});});
 await drop(page,files);assert.equal(await page.locator('.queue-item').count(),2);assert.equal(await page.locator('#uploadBtn').isEnabled(),true);
 await page.locator('#uploadBtn').click();await reachedGate;await page.locator('#pauseBtn').click();release();await page.waitForFunction(()=>document.querySelector('.queue-state').textContent.startsWith('Paused'));
 await checkWidths(page,'paused public upload');
 const saved=await page.evaluate(()=>JSON.parse(localStorage.getItem('yatfhs-resume-v1')));assert.equal(Object.keys(saved).length,1);
 const id=Object.values(saved)[0].id;const status=await (await fetch(base+'/api/v1/uploads/'+id)).json();assert.deepEqual(status.data.uploaded_chunks,[0]);
 await page.unrouteAll({behavior:'ignoreErrors'});await page.reload();await page.waitForFunction(()=>document.getElementById('customExpire').value==='3600');
 const sent=[];page.on('request',r=>{if(r.method()==='PUT')sent.push(r.url());});
 await drop(page,files);await page.locator('#uploadBtn').click();await page.waitForFunction(()=>document.querySelectorAll('.queue-item a:not([hidden])').length===2,null,{timeout:60000});
 assert(!sent.some(u=>u.includes(id+'/chunks/0')),'resume resent completed chunk');
 const urls=await page.locator('.queue-item a').evaluateAll(links=>links.map(l=>l.href));assert.deepEqual(Buffer.from(await (await fetch(urls[0])).arrayBuffer()),large);
 const downloadPromise=page.waitForEvent('download');await page.locator('#downloadBtn').click();const download=await downloadPromise;assert.equal(download.suggestedFilename(),'yatfhs-files.zip');assert((await readFile(await download.path())).length>large.length);
 await checkWidths(page,'completed public upload');
 const artifacts=resolve(process.env.TEST_ARTIFACTS_DIR||join(root,'test-results'));await mkdir(artifacts,{recursive:true});await page.screenshot({path:join(artifacts,'upload-desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.screenshot({path:join(artifacts,'upload-mobile.png'),fullPage:true});
 await page.setViewportSize({width:1200,height:1000});await page.goto(base+'/admin');await checkWidths(page,'admin sign-in');await page.locator('#token').fill(token);await page.getByRole('button',{name:'Sign in',exact:true}).click();await page.locator('#dashboard').waitFor({state:'visible'});await page.waitForFunction(()=>document.querySelectorAll('#statsGrid .stat').length>0);
 await page.locator('#policyMin').fill('120');await page.locator('#policyDefault').fill('300');await page.locator('#policyMax').fill('7200');await page.locator('#policyOptions').fill('300, 600, 7200');await page.getByRole('button',{name:'Save policy'}).click();await page.waitForFunction(()=>document.getElementById('policyStatus').textContent.startsWith('Saved'));
 const publicPage=await context.newPage();publicPage.on('pageerror',e=>errors.push(e.message));await publicPage.goto(base);await publicPage.waitForFunction(()=>document.getElementById('customExpire').value==='300');assert.equal(await publicPage.locator('.expire-btn').count(),3);
 const forbidden=await fetch(base+'/api/v1/uploads',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name:'bad',size:0,expire:60})});assert.equal(forbidden.status,400);
 await drop(page,[{name:'permanent.txt',modified:1700000000002,base64:Buffer.from('admin forever').toString('base64')}]);await page.locator('#customExpire').fill('0');await page.locator('#uploadBtn').click();await page.waitForFunction(()=>document.querySelector('.queue-state').textContent.includes('never expires'));await page.waitForFunction(()=>document.getElementById('adminFiles').textContent.includes('permanent.txt'));
 await drop(page,[{name:'custom-date.txt',modified:1700000000004,base64:Buffer.from('expires at selected date').toString('base64')}]);
 await page.locator('#expireAt').fill('2000-01-01T00:00');await page.locator('#uploadBtn').click();assert((await page.locator('#uploadError').textContent()).includes('future'));
 const future=new Date(Date.now()+600000-new Date().getTimezoneOffset()*60000).toISOString().slice(0,16);await page.locator('#expireAt').fill(future);await page.locator('#uploadBtn').click();await page.waitForFunction(()=>document.querySelectorAll('.queue-item a:not([hidden])').length===2);assert(!(await page.locator('.queue-state').nth(1).textContent()).includes('never'));
 await page.locator('#logFilter').selectOption('upload');assert((await page.locator('#adminLogs tr').count())>0);
 assert.equal(await page.evaluate(token=>JSON.stringify(localStorage).includes(token),token),false);
 assert((await page.locator('.queue-head input').first().boundingBox()).width<=24,'admin checkbox stretched');
 await checkWidths(page,'admin dashboard');
 await page.screenshot({path:join(artifacts,'admin-desktop.png'),fullPage:true});
 await page.setViewportSize({width:390,height:844});assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false);await page.screenshot({path:join(artifacts,'admin-mobile.png'),fullPage:true});await page.setViewportSize({width:1200,height:1000});
 assert.equal((await fetch(base+'/api/v1/admin/overview')).status,401);
 await page.getByRole('button',{name:'Sign out',exact:true}).click();assert.equal(await page.locator('#dashboard').isHidden(),true);
 // Plain HTTP environments use the tested SHA-256 fallback.
 let retries=0;await publicPage.route('**/api/v1/uploads/*/chunks/0',route=>++retries===1?route.abort('failed'):route.continue());
 await publicPage.addInitScript(()=>Object.defineProperty(globalThis,'crypto',{value:{}}));await publicPage.reload();await publicPage.waitForFunction(()=>document.getElementById('customExpire').value==='300');await drop(publicPage,[{name:'http-fallback.txt',modified:1700000000003,base64:Buffer.from('HTTP fallback').toString('base64')}]);await publicPage.locator('#uploadBtn').click();await publicPage.waitForFunction(()=>document.querySelector('.queue-state').textContent.startsWith('Complete'));
 assert.equal(retries,2,'chunk did not automatically retry after a network error');
 assert.deepEqual(errors,[]);console.log('Browser checks passed: drag/drop, multiple files, pause, refresh/resume, skipped chunks, integrity, ZIP, policy, admin authentication, permanent and custom date uploads, logs, consistent controls at 320/390/768/1200px, HTTP hashing fallback.');
 console.log(`Screenshots: ${artifacts}`);
}finally{await browser?.close();server.kill('SIGTERM');await new Promise(resolve=>server.exitCode!==null?resolve():server.once('exit',resolve));await rm(temp,{recursive:true,force:true});}
