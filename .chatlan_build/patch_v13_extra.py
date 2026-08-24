from pathlib import Path
import re

root=Path('/tmp/chatlan/chat_lan_v11_project')
htmlp=root/'app/src/main/assets/index.html'
t=htmlp.read_text(encoding='utf-8')

t=re.sub(r"const APP_PLATFORM='android',APP_VERSION_CODE=\d+,APP_VERSION_NAME='[^']+';","const APP_PLATFORM='android',APP_VERSION_CODE=13,APP_VERSION_NAME='13.0';",t)
t=re.sub(r"import \{ getStorage[^\n]+firebase-storage\.js';\n",'',t)
t=t.replace("const app=initializeApp(firebaseConfig),auth=getAuth(app),db=getDatabase(app),storage=getStorage(app),ROOT='chatlan',G='group:general';","const app=initializeApp(firebaseConfig),auth=getAuth(app),db=getDatabase(app),ROOT='chatlan',G='group:general';")

inject="""
const SUPABASE_URL='https://mowzkwpdqhnkvruhxbts.supabase.co',SUPABASE_KEY='sb_publishable_Wvy2wasIv8ooRRHYqBTOTw_K0QZj7-B',SUPABASE_BUCKET='chatlan-files';
function supaObjectPath(path){return String(path||'').split('/').filter(Boolean).map(encodeURIComponent).join('/')}
function supaPublicURL(path){return SUPABASE_URL+'/storage/v1/object/public/'+encodeURIComponent(SUPABASE_BUCKET)+'/'+supaObjectPath(path)}
async function supaDelete(path){if(!path)return;let r=await fetch(SUPABASE_URL+'/storage/v1/object/'+encodeURIComponent(SUPABASE_BUCKET)+'/'+supaObjectPath(path),{method:'DELETE',headers:{apikey:SUPABASE_KEY,Authorization:'Bearer '+SUPABASE_KEY}});if(!r.ok&&r.status!==404)throw new Error('Supabase Storage HTTP '+r.status)}
async function supaUpload(path,f,progressCb){if(!f)throw new Error('Archivo vacío');if(f.size>50*1024*1024)throw new Error('El plan gratuito permite máximo 50 MB por archivo.');return await new Promise((resolve,reject)=>{let xhr=new XMLHttpRequest(),url=SUPABASE_URL+'/storage/v1/object/'+encodeURIComponent(SUPABASE_BUCKET)+'/'+supaObjectPath(path);xhr.open('POST',url,true);xhr.setRequestHeader('apikey',SUPABASE_KEY);xhr.setRequestHeader('Authorization','Bearer '+SUPABASE_KEY);xhr.setRequestHeader('x-upsert','true');xhr.setRequestHeader('Content-Type',fileMime(f));xhr.upload.onprogress=e=>{if(e.lengthComputable){let p=Math.round(e.loaded/e.total*100);try{progressCb&&progressCb(p,'Subiendo archivo… '+p+'%')}catch{}}};xhr.onerror=()=>reject(new Error('No se pudo conectar con Supabase Storage'));xhr.onload=()=>{if(xhr.status>=200&&xhr.status<300){try{progressCb&&progressCb(100,'Subida completada')}catch{};resolve()}else reject(new Error('Supabase Storage HTTP '+xhr.status+': '+(xhr.responseText||'')))};xhr.send(f)})}
"""
marker="const USER_COLORS="
if 'const SUPABASE_URL=' not in t:
    t=t.replace(marker,inject+'\n'+marker,1)

t=t.replace("async function deleteCurrentStatus(){if(statusViewerUser!==uid)return;let ss=activeStatusesForUser(uid),s=ss[statusViewerIndex];if(!s||!confirm('¿Eliminar este estado?'))return;try{if(s.path)try{await deleteObject(stRef(storage,s.path))}catch{}await remove(dbRef(db,ROOT+'/statuses/'+uid+'/'+s.id));","async function deleteCurrentStatus(){if(statusViewerUser!==uid)return;let ss=activeStatusesForUser(uid),s=ss[statusViewerIndex];if(!s||!confirm('¿Eliminar este estado?'))return;try{if(s.path)try{await supaDelete(s.path)}catch{}await remove(dbRef(db,ROOT+'/statuses/'+uid+'/'+s.id));")

t=re.sub(r"function applyBgPath\(p\)\{.*?\}\nfunction clearListeners", "function applyBgPath(p){bgPath=p||'';if(!p){$('messages').style.backgroundImage='';return}let u=supaPublicURL(p);$('messages').style.backgroundImage='url(\\\"'+u.replace(/\\\"/g,'%22')+'\\\")'}\nfunction clearListeners", t, count=1, flags=re.S)
t=re.sub(r"async function storageURL\(path\)\{.*?\}","async function storageURL(path){if(!path)return'';return supaPublicURL(path)}",t,count=1,flags=re.S)
t=re.sub(r"async function uploadStorageRobust\(path,f,progressCb\)\{.*?\}\nasync function uploadFile","async function uploadStorageRobust(path,f,progressCb){return supaUpload(path,f,progressCb)}\nasync function uploadFile",t,count=1,flags=re.S)

t=t.replace("toast('No se pudo abrir','Revisa Firebase Storage y sus reglas.');","toast('No se pudo abrir','Revisa la conexión con Supabase Storage.');")
t=t.replace("if(c.startsWith('storage/'))return 'Firebase Storage no está disponible o sus reglas/plan no permiten esta operación ('+c+').';","if(m.includes('Supabase Storage'))return m;")
t=t.replace('Firebase · mensajes en tiempo real','Firebase RTDB · archivos en Supabase')
t=t.replace("if(f.size>200*1024*1024){toast('Archivo demasiado grande','Máximo 200 MB');","if(f.size>50*1024*1024){toast('Archivo demasiado grande','Máximo 50 MB en el plan gratuito');")

# Background storage migration
start=t.find('async function changeBg(f)')
end=t.find("stickers.forEach",start)
if start>=0 and end>start:
    bg="""async function changeBg(f){if(!f)return;if(f.size>15*1024*1024){toast('Fondo demasiado grande','Máximo 15 MB');return}try{let path=ROOT+'/background/'+Date.now()+'_'+safeKey(f.name).slice(0,60);$('progress').classList.add('show');await supaUpload(path,f,(p)=>$('progress').textContent='Subiendo fondo… '+p+'%');let old=bgPath;await set(dbRef(db,ROOT+'/settings/background'),{path,storageProvider:'supabase',updatedBy:uid,updatedAt:serverTimestamp()});if(old){try{await supaDelete(old)}catch{}}toast('Fondo actualizado','Se cambió para todos los usuarios.')}catch(e){toast('No se pudo cambiar el fondo',friendly(e))}finally{$('progress').classList.remove('show');$('bgInput').value=''}}
async function removeBg(){try{let old=bgPath;await remove(dbRef(db,ROOT+'/settings/background'));if(old){try{await supaDelete(old)}catch{}}applyBgPath('')}catch(e){toast('No se pudo quitar',friendly(e))}}
"""
    t=t[:start]+bg+t[end:]

# Android has no admin publishing UI in normal operation; remove any remaining Firebase Storage symbols defensively.
t=re.sub(r"async function publishUpdate\(\)\{.*?\}\nfunction startSession","async function publishUpdate(){return}\nfunction startSession",t,count=1,flags=re.S)

htmlp.write_text(t,encoding='utf-8')

gp=root/'app/build.gradle'
s=gp.read_text(encoding='utf-8')
s=re.sub(r'versionCode\s+\d+','versionCode 13',s)
s=re.sub(r'versionName\s+\"[^\"]+\"','versionName \"13.0\"',s)
gp.write_text(s,encoding='utf-8')
