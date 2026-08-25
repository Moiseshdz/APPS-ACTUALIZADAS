from pathlib import Path
import re

root=Path('/tmp/chatlan/chat_lan_v11_project')
htmlp=root/'app/src/main/assets/index.html'
t=htmlp.read_text(encoding='utf-8')

# Android v15: fuerza siempre el diseño móvil con menú lateral tipo drawer.
css="""
html.chatlanAndroid .app{grid-template-columns:1fr!important;width:100%!important;overflow:hidden!important}
html.chatlanAndroid .side{position:fixed!important;left:0!important;top:0!important;bottom:auto!important;height:var(--app-h,100dvh)!important;width:min(330px,88vw)!important;max-width:88vw!important;z-index:80!important;box-shadow:0 12px 32px rgba(24,34,48,.22)!important;transform:translate3d(-105%,0,0)!important;transition:transform .22s ease!important;will-change:transform!important;visibility:visible!important}
html.chatlanAndroid .side.open{transform:translate3d(0,0,0)!important}
html.chatlanAndroid .menuBtn,html.chatlanAndroid .sideClose{display:block!important;touch-action:manipulation!important;-webkit-tap-highlight-color:transparent}
html.chatlanAndroid .menuBtn{position:relative!important;z-index:4!important;min-width:42px!important;min-height:42px!important}
html.chatlanAndroid .sideBackdrop{display:none!important;position:fixed!important;inset:0!important;background:rgba(14,23,34,.45)!important;z-index:75!important}
html.chatlanAndroid .sideBackdrop.show{display:block!important}
html.chatlanAndroid .main{width:100%!important;min-width:0!important;height:var(--app-h,100dvh)!important;max-height:var(--app-h,100dvh)!important}
"""
if 'html.chatlanAndroid .side{' not in t:
    t=t.replace('</style>',css+'</style>',1)

# Marca la interfaz como Android antes de pintar el layout.
script_marker='<script type="module">'
if "document.documentElement.classList.add('chatlanAndroid')" not in t:
    t=t.replace(script_marker,script_marker+"\ndocument.documentElement.classList.add('chatlanAndroid');",1)

# Unifica abrir/cerrar para que no dependa de reglas CSS del ancho de pantalla.
t=re.sub(
    r"function closeSide\(\)\{\$\('side'\)\.classList\.remove\('open'\);\$\('sideBackdrop'\)\.classList\.remove\('show'\)\}\s*function toggleSide\(\)\{let open=!\$\('side'\)\.classList\.contains\('open'\);\$\('side'\)\.classList\.toggle\('open',open\);\$\('sideBackdrop'\)\.classList\.toggle\('show',open\)\}",
    "function setSideOpen(open){let s=$('side'),b=$('sideBackdrop'),m=$('menuBtn');if(!s||!b)return;s.classList.toggle('open',!!open);b.classList.toggle('show',!!open);if(m)m.setAttribute('aria-expanded',open?'true':'false')}\nfunction closeSide(){setSideOpen(false)}\nfunction toggleSide(){setSideOpen(!$('side').classList.contains('open'))}",
    t,
    count=1
)

# Refuerza click/touch del botón hamburguesa y evita el doble evento touch+click.
old="$('menuBtn').onclick=toggleSide;$('sideClose').onclick=closeSide;$('sideBackdrop').onclick=closeSide;window.addEventListener('resize',()=>{if(window.innerWidth>780)closeSide()});"
new="let sideTapLock=0;function sideToggleEvent(e){if(e){e.preventDefault();e.stopPropagation()}let now=Date.now();if(now-sideTapLock<280)return;sideTapLock=now;toggleSide()}function sideCloseEvent(e){if(e){e.preventDefault();e.stopPropagation()}let now=Date.now();if(now-sideTapLock<220)return;sideTapLock=now;closeSide()}$('menuBtn').onclick=sideToggleEvent;$('menuBtn').ontouchend=sideToggleEvent;$('sideClose').onclick=sideCloseEvent;$('sideClose').ontouchend=sideCloseEvent;$('sideBackdrop').onclick=sideCloseEvent;$('sideBackdrop').ontouchend=sideCloseEvent;window.addEventListener('resize',()=>{if(APP_PLATFORM!=='android'&&window.innerWidth>780)closeSide()});"
if old in t:
    t=t.replace(old,new,1)
else:
    raise SystemExit('No se encontró el bloque de eventos del menú lateral')

# Permite cerrar el drawer con Escape y deja una función global útil para el host nativo.
if 'window.ChatLANCloseSide=closeSide' not in t:
    anchor="window.addEventListener('resize',()=>{if(APP_PLATFORM!=='android'&&window.innerWidth>780)closeSide()});"
    t=t.replace(anchor,anchor+"window.ChatLANCloseSide=closeSide;document.addEventListener('keydown',e=>{if(e.key==='Escape')closeSide()});",1)

# Versión Android 15.
t=re.sub(r"const APP_PLATFORM='android',APP_VERSION_CODE=\d+,APP_VERSION_NAME='[^']+';", "const APP_PLATFORM='android',APP_VERSION_CODE=15,APP_VERSION_NAME='15.0';", t, count=1)

# Validaciones de regresión.
assert "APP_VERSION_CODE=15" in t
assert "html.chatlanAndroid .side.open" in t
assert "$('menuBtn').ontouchend=sideToggleEvent" in t
assert "function setSideOpen(open)" in t

htmlp.write_text(t,encoding='utf-8')

gp=root/'app/build.gradle'
s=gp.read_text(encoding='utf-8')
s=re.sub(r'versionCode\s+\d+', 'versionCode 15', s)
s=re.sub(r'versionName\s+\"[^\"]+\"', 'versionName \"15.0\"', s)
gp.write_text(s,encoding='utf-8')
