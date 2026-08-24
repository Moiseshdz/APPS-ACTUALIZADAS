from pathlib import Path
import re

root=Path('/tmp/chatlan/chat_lan_v11_project')
htmlp=root/'app/src/main/assets/index.html'
t=htmlp.read_text(encoding='utf-8')

t=re.sub(r"const APP_PLATFORM='android',APP_VERSION_CODE=\d+,APP_VERSION_NAME='[^']+';","const APP_PLATFORM='android',APP_VERSION_CODE=12,APP_VERSION_NAME='12.0';",t)
t=t.replace('uploadBytesResumable, getDownloadURL','uploadBytesResumable, uploadBytes, getDownloadURL')
t=re.sub(r'<input type="file" id="fileInput" accept="[^"]*" hidden>','<input type="file" id="fileInput" accept="*/*,.apk,application/vnd.android.package-archive" hidden>',t)

if '.statusReplyBar{' not in t:
    css="""\n.statusReplyBar{display:none;gap:8px;align-items:center;padding:10px 0 0;border-top:1px solid rgba(255,255,255,.15);margin-top:10px}.statusReplyBar.show{display:flex}.statusReplyBar input{flex:1;min-width:0;border:1px solid rgba(255,255,255,.22);background:rgba(255,255,255,.12);color:#fff;border-radius:999px;padding:10px 13px;outline:none}.statusReplyBar input::placeholder{color:rgba(255,255,255,.72)}.statusReplyBar button{border:0;background:#fff;color:#17212b;border-radius:999px;padding:10px 14px;font-weight:800;cursor:pointer}.statusReplyQuote{border-left:4px solid #8f5cff;background:rgba(143,82,204,.09);border-radius:9px;padding:7px 9px;margin-bottom:7px}.statusReplyQuoteTitle{font-size:11px;font-weight:800;color:#7a52cc}.statusReplyQuoteText{font-size:12px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;max-width:360px}.fileTypeBadge{font-size:10px;font-weight:800;border:1px solid rgba(0,0,0,.1);border-radius:6px;padding:2px 5px;background:rgba(255,255,255,.55)}\n"""
    t=t.replace('</style>',css+'</style>')

t=t.replace('<div class="statusContent" id="statusContent"></div><div class="statusNav">','<div class="statusContent" id="statusContent"></div><div class="statusReplyBar" id="statusReplyBar"><input id="statusReplyInput" maxlength="1000" placeholder="Responder a este estado…"><button id="statusReplyBtn">Enviar</button></div><div class="statusNav">')

marker="function sendSticker(sticker){if(!uid||!sticker)return;sendSpecialSticker(sticker)}"
if 'function renderStatusReplyQuote' not in t:
    add="""function renderStatusReplyQuote(m){if(!m.statusReply)return null;let s=m.statusReply,q=document.createElement('div');q.className='statusReplyQuote';let label=s.type==='image'?'📷 Foto del estado':(s.text||'Estado');q.innerHTML='<div class="statusReplyQuoteTitle">↩ Respondió al estado de '+esc(s.userName||'Usuario')+'</div><div class="statusReplyQuoteText">'+esc(label)+'</div>';q.onclick=()=>{let owner=s.userId||'';if(owner&&statuses[owner]&&statuses[owner][s.statusId])viewStatusUser(owner,Math.max(0,activeStatusesForUser(owner).findIndex(x=>x.id===s.statusId)));else toast('Estado no disponible','El estado pudo haber caducado o eliminado.');};return q}\n"""
    t=t.replace(marker,add+marker)

marker='async function viewStatusUser(userId,index=0)'
if 'async function sendStatusReply()' not in t:
    add="""async function sendStatusReply(){if(!uid||!statusViewerUser||statusViewerUser===uid)return;let text=$('statusReplyInput').value.trim();if(!text)return;let ss=activeStatusesForUser(statusViewerUser),s=ss[statusViewerIndex];if(!s){toast('Estado no disponible','Puede haber caducado.');return}let recipientId=statusViewerUser,recipientName=personName(recipientId,s.userName||'Usuario'),cid=dm(uid,recipientId),messageId=crypto.randomUUID().replace(/-/g,''),now=Date.now(),base={id:messageId,senderId:uid,senderName:name,recipientId,recipientName,chatId:cid,chatType:'private',createdAt:now,mode:'normal',text,statusReply:{statusId:s.id||'',userId:recipientId,userName:recipientName,type:s.type||'text',text:s.type==='text'?(s.text||'').slice(0,300):'',path:s.type==='image'?(s.path||''):'',createdAt:s.createdAt||0}};try{let patch={};patch[ROOT+'/privateInbox/'+uid+'/'+cid+'/'+messageId]=base;patch[ROOT+'/privateInbox/'+recipientId+'/'+cid+'/'+messageId]=base;await update(dbRef(db),patch);conversations[cid]={id:cid,name:recipientName,type:'private',userId:recipientId};$('statusReplyInput').value='';closeStatusViewer();selectChat(cid);toast('Respuesta enviada','Se envió como mensaje privado a '+recipientName+'.')}catch(e){toast('No se pudo responder el estado',friendly(e))}}\n"""
    t=t.replace(marker,add+marker)

t=t.replace("$('statusDeleteBtn').style.display=userId===uid?'block':'none';let c=$('statusContent');","$('statusDeleteBtn').style.display=userId===uid?'block':'none';$('statusReplyBar').classList.toggle('show',userId!==uid);$('statusReplyInput').value='';let c=$('statusContent');")
t=t.replace("let b=document.createElement('div');b.className='bubble';let q=renderReplyQuote(m);if(q)b.appendChild(q);","let b=document.createElement('div');b.className='bubble';let sq=renderStatusReplyQuote(m);if(sq)b.appendChild(sq);let q=renderReplyQuote(m);if(q)b.appendChild(q);")

m=re.search(r"function attachmentCard\(a\)\{.*?\nasync function addMedia",t,re.S)
if not m: raise SystemExit('attachmentCard not found')
rep="""function attachmentIcon(a){let n=String(a?.name||'').toLowerCase(),mt=String(a?.type||'').toLowerCase();if(n.endsWith('.apk')||mt==='application/vnd.android.package-archive')return'📦';if(mt.startsWith('image/'))return'🖼️';if(mt.startsWith('video/'))return'🎥';if(mt.startsWith('audio/'))return'🎵';if(n.endsWith('.pdf'))return'📄';return'📎'}
function attachmentCard(a){let d=document.createElement('div');d.className='filecard';let ext=(String(a.name||'archivo').split('.').pop()||'').toUpperCase().slice(0,8);d.innerHTML='<span style="font-size:23px">'+attachmentIcon(a)+'</span><div class="filemeta"><div class="filename">'+esc(a.name||'archivo')+'</div><div class="filesize">'+esc(fmt(a.size||0))+' '+(ext?'<span class="fileTypeBadge">'+esc(ext)+'</span>':'')+'</div></div><button>Abrir</button>';d.querySelector('button').onclick=async()=>{let u=await storageURL(a.path);if(!u){toast('No se pudo abrir','Revisa Firebase Storage y sus reglas.');return}try{if(APP_PLATFORM==='android'&&window.ChatLANPush?.openExternalUrl){window.ChatLANPush.openExternalUrl(u)}else window.open(u,'_blank')}catch{window.open(u,'_blank')}};return d}
async function addMedia"""
t=t[:m.start()]+rep+t[m.end():]

m=re.search(r"async function uploadFile\(f,messageId,scope,recipientId=''\)\{.*?\}\nasync function send\(\)",t,re.S)
if not m: raise SystemExit('uploadFile not found')
rep="""function fileMime(f){let n=String(f?.name||'').toLowerCase(),mt=String(f?.type||'').trim();if(mt)return mt;if(n.endsWith('.apk'))return'application/vnd.android.package-archive';if(n.endsWith('.mp4'))return'video/mp4';if(n.endsWith('.mov'))return'video/quicktime';if(n.endsWith('.m4v'))return'video/x-m4v';if(n.endsWith('.jpg')||n.endsWith('.jpeg'))return'image/jpeg';if(n.endsWith('.png'))return'image/png';if(n.endsWith('.webp'))return'image/webp';if(n.endsWith('.pdf'))return'application/pdf';if(n.endsWith('.txt'))return'text/plain';return'application/octet-stream'}
async function uploadStorageRobust(path,f,progressCb){let r=stRef(storage,path),meta={contentType:fileMime(f)},report=(p,msg)=>{try{progressCb&&progressCb(p,msg)}catch{}};let direct=async(msg='Subiendo archivo…')=>{report(0,msg);await uploadBytes(r,f,meta);report(100,'Subida completada');};if(f.size<=25*1024*1024){await direct('Subiendo archivo…');return}let task=uploadBytesResumable(r,f,meta),stalled=false,sawBytes=false;try{await new Promise((res,rej)=>{let timer=setTimeout(()=>{if(!sawBytes){stalled=true;try{task.cancel()}catch{};rej(new Error('UPLOAD_STALLED'))}},14000);task.on('state_changed',s=>{if(s.bytesTransferred>0)sawBytes=true;let p=s.totalBytes?Math.round(s.bytesTransferred/s.totalBytes*100):0;report(p,'Subiendo archivo… '+p+'%')},e=>{clearTimeout(timer);rej(e)},()=>{clearTimeout(timer);report(100,'Subida completada');res()})})}catch(e){let code=String(e?.code||''),msg=String(e?.message||'');if(stalled||code==='storage/canceled'||msg.includes('UPLOAD_STALLED')){await direct('Reintentando subida compatible…')}else throw e}}
async function uploadFile(f,messageId,scope,recipientId=''){let ext=(f.name.split('.').pop()||'bin').replace(/[^a-z0-9]/gi,'').slice(0,8),base=scope==='group'?'group/general':('private/'+uid+'/'+recipientId),path=ROOT+'/'+base+'/'+messageId+'/'+Date.now()+'_'+safeKey(f.name).slice(0,50)+(ext?'.'+ext:'');$('progress').classList.add('show');await uploadStorageRobust(path,f,(p,msg)=>{$('progress').textContent=msg});return{name:f.name,type:fileMime(f),size:f.size,path}}
async function send()"""
t=t[:m.start()]+rep+t[m.end():]

m=re.search(r"async function publishPhotoStatus\(file\)\{.*?\}\nasync function sendStatusReply",t,re.S)
if not m: raise SystemExit('publishPhotoStatus not found')
rep="""async function publishPhotoStatus(file){if(!uid||!file||statusUploading)return;if(file.size>25*1024*1024){toast('Foto demasiado grande','Máximo 25 MB');return}statusUploading=true;$('statusCreateInfo').textContent='Subiendo foto…';try{let id=crypto.randomUUID().replace(/-/g,''),now=Date.now(),path=ROOT+'/statuses/'+uid+'/'+id+'/'+Date.now()+'_'+safeKey(file.name).slice(0,50);await uploadStorageRobust(path,file,(p,msg)=>{$('statusCreateInfo').textContent=(msg||'Subiendo foto…')+(p>0&&p<100?' '+p+'%':'')});await set(dbRef(db,ROOT+'/statuses/'+uid+'/'+id),{id,userId:uid,userName:name,type:'image',path,createdAt:now,expiresAt:now+86400000});$('statusCreateInfo').textContent='✅ Foto publicada por 24 horas.'}catch(e){$('statusCreateInfo').textContent=friendly(e);toast('No se pudo subir la foto',friendly(e))}finally{statusUploading=false;$('statusFileInput').value=''}}
async function sendStatusReply"""
t=t[:m.start()]+rep+t[m.end():]

t=t.replace("if(f.size>100*1024*1024){toast('Archivo demasiado grande','Máximo 100 MB');","if(f.size>200*1024*1024){toast('Archivo demasiado grande','Máximo 200 MB');")
anchor="$('statusDeleteBtn').onclick=deleteCurrentStatus;"
t=t.replace(anchor,anchor+"$('statusReplyBtn').onclick=sendStatusReply;$('statusReplyInput').onkeydown=e=>{if(e.key==='Enter'&&!e.shiftKey){e.preventDefault();sendStatusReply()}};")
htmlp.write_text(t,encoding='utf-8')

# Android Java WebView: allow Firebase Storage from local asset page, accept every file type and open downloads externally.
ma=root/'app/src/main/java/com/chatlan/app/MainActivity.java'
s=ma.read_text(encoding='utf-8')
s=s.replace('import android.net.Uri;','import android.net.Uri;\nimport android.content.ActivityNotFoundException;')
s=s.replace('s.setAllowContentAccess(true);\n        s.setMediaPlaybackRequiresUserGesture(false);','s.setAllowContentAccess(true);\n        if (Build.VERSION.SDK_INT >= 16) {\n            s.setAllowFileAccessFromFileURLs(true);\n            s.setAllowUniversalAccessFromFileURLs(true);\n        }\n        s.setMediaPlaybackRequiresUserGesture(false);')
old='''                Intent intent;\n                try {\n                    intent = fileChooserParams.createIntent();\n                } catch (Exception e) {\n                    intent = new Intent(Intent.ACTION_OPEN_DOCUMENT);\n                    intent.addCategory(Intent.CATEGORY_OPENABLE);\n                    intent.setType("*/*");\n                }\n\n                startActivityForResult(intent, FILE_CHOOSER_REQUEST);'''
new='''                Intent intent = new Intent(Intent.ACTION_OPEN_DOCUMENT);\n                intent.addCategory(Intent.CATEGORY_OPENABLE);\n                intent.setType("*/*");\n                intent.putExtra(Intent.EXTRA_MIME_TYPES, new String[]{\n                        "image/*", "video/*", "audio/*", "application/pdf", "text/plain",\n                        "application/vnd.android.package-archive", "application/octet-stream"\n                });\n                intent.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, false);\n\n                try {\n                    startActivityForResult(Intent.createChooser(intent, "Seleccionar archivo"), FILE_CHOOSER_REQUEST);\n                } catch (Exception e) {\n                    fileCallback.onReceiveValue(null);\n                    fileCallback = null;\n                }'''
if old not in s: raise SystemExit('chooser block not found')
s=s.replace(old,new)
helper='''    public void openExternalUrl(String url) {\n        if (url == null || url.trim().isEmpty()) return;\n        runOnUiThread(() -> {\n            try {\n                Intent i = new Intent(Intent.ACTION_VIEW, Uri.parse(url));\n                i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);\n                startActivity(i);\n            } catch (ActivityNotFoundException ignored) {\n            }\n        });\n    }\n\n'''
s=s.replace('    public void refreshFcmToken() {',helper+'    public void refreshFcmToken() {')
ma.write_text(s,encoding='utf-8')

pb=root/'app/src/main/java/com/chatlan/app/PushBridge.java'
s=pb.read_text(encoding='utf-8')
insert='''    @JavascriptInterface\n    public void openExternalUrl(String url) {\n        activity.openExternalUrl(url);\n    }\n\n'''
s=s.replace('    @JavascriptInterface\n    public void refreshToken() {',insert+'    @JavascriptInterface\n    public void refreshToken() {')
pb.write_text(s,encoding='utf-8')

gp=root/'app/build.gradle'
s=gp.read_text(encoding='utf-8')
s=re.sub(r'versionCode\s+\d+','versionCode 12',s)
s=re.sub(r'versionName\s+"[^"]+"','versionName "12.0"',s)
gp.write_text(s,encoding='utf-8')
