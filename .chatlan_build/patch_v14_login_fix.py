from pathlib import Path
import re

root=Path('/tmp/chatlan/chat_lan_v11_project')
htmlp=root/'app/src/main/assets/index.html'
t=htmlp.read_text(encoding='utf-8')

# v14: corrige el error de sintaxis que impedía que el módulo JS arrancara y por eso el botón Entrar no hacía nada.
t=t.replace("async function storageURL(path){if(!path)return'';return supaPublicURL(path)}catch{return''}}", "async function storageURL(path){if(!path)return'';return supaPublicURL(path)}")

# Supabase publishable keys deben ir en apikey, no como Bearer. Además usamos rutas únicas, por lo que no necesitamos upsert.
t=t.replace("headers:{apikey:SUPABASE_KEY,Authorization:'Bearer '+SUPABASE_KEY}", "headers:{apikey:SUPABASE_KEY}")
t=t.replace("xhr.setRequestHeader('Authorization','Bearer '+SUPABASE_KEY);", "")
t=t.replace("xhr.setRequestHeader('x-upsert','true');", "")

# Android no publica builds desde la UI; elimina referencias residuales a Firebase Storage que ya no se importa.
t=re.sub(r"async function publishUpdate\(\)\{.*?\}\n\nasync function syncNativePush", "async function publishUpdate(){return}\n\nasync function syncNativePush", t, count=1, flags=re.S)

# Mejora visible del inicio de sesión y evita que parezca que el botón no respondió mientras conecta.
old="async function login(){let v=$('nameInput').value.trim().slice(0,28);if(!v)return;$('loginError').style.display='none';try{await establishSession(v)}catch(e){$('loginError').style.display='block';$('loginError').textContent=friendly(e)}}"
new="async function login(){let v=$('nameInput').value.trim().slice(0,28);if(!v)return;let b=$('enterBtn'),oldText=b.textContent;b.disabled=true;b.textContent='Entrando…';$('loginError').style.display='none';try{await setPersistence(auth,browserLocalPersistence).catch(()=>{});await establishSession(v)}catch(e){$('loginError').style.display='block';$('loginError').textContent=friendly(e)}finally{b.disabled=false;b.textContent=oldText}}"
if old in t:
    t=t.replace(old,new,1)

# Versión Android 14.
t=re.sub(r"const APP_PLATFORM='android',APP_VERSION_CODE=\d+,APP_VERSION_NAME='[^']+';", "const APP_PLATFORM='android',APP_VERSION_CODE=14,APP_VERSION_NAME='14.0';", t, count=1)

# Guardas de validación para no volver a entregar una APK con el fallo de v13.
assert "}catch{return''}}" not in t, 'storageURL quedó roto'
assert "Authorization','Bearer '+SUPABASE_KEY" not in t, 'Bearer publishable sigue presente'
assert "APP_VERSION_CODE=14" in t

htmlp.write_text(t,encoding='utf-8')

gp=root/'app/build.gradle'
s=gp.read_text(encoding='utf-8')
s=re.sub(r'versionCode\s+\d+', 'versionCode 14', s)
s=re.sub(r'versionName\s+"[^"]+"', 'versionName "14.0"', s)
gp.write_text(s,encoding='utf-8')
