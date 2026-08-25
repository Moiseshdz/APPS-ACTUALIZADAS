from pathlib import Path
import re

root=Path('/tmp/chatlan/chat_lan_v11_project')
htmlp=root/'app/src/main/assets/index.html'
t=htmlp.read_text(encoding='utf-8')

# Android v15: fuerza SIEMPRE el layout móvil tipo drawer, sin depender del ancho que reporte WebView.
css="""
html.chatlanAndroid .app{grid-template-columns:1fr!important;width:100%!important;overflow:hidden!important}
html.chatlanAndroid .side{position:fixed!important;left:0!important;top:0!important;bottom:auto!important;height:var(--app-h,100dvh)!important;width:min(330px,88vw)!important;max-width:88vw!important;z-index:80!important;box-shadow:0 12px 32px rgba(24,34,48,.22)!important;transform:translate3d(-105%,0,0)!important;transition:transform .22s ease!important;will-change:transform!important;visibility:visible!important}
html.chatlanAndroid .side.open{left:0!important;transform:translate3d(0,0,0)!important}
html.chatlanAndroid .menuBtn,html.chatlanAndroid .sideClose{display:flex!important;align-items:center!important;justify-content:center!important;touch-action:manipulation!important;-webkit-tap-highlight-color:transparent}
html.chatlanAndroid .menuBtn{position:relative!important;z-index:4!important;min-width:44px!important;min-height:44px!important;pointer-events:auto!important}
html.chatlanAndroid .sideBackdrop{display:none!important;position:fixed!important;inset:0!important;background:rgba(14,23,34,.45)!important;z-index:75!important;pointer-events:auto!important}
html.chatlanAndroid .sideBackdrop.show{display:block!important}
html.chatlanAndroid .main{width:100%!important;min-width:0!important;height:var(--app-h,100dvh)!important;max-height:var(--app-h,100dvh)!important}
"""
if 'html.chatlanAndroid .side{' not in t:
    if '</style>' not in t: raise SystemExit('No se encontró </style>')
    t=t.replace('</style>',css+'</style>',1)

# Marca el documento como host Android antes de inicializar la interfaz.
script_marker='<script type="module">'
android_mark="document.documentElement.classList.add('chatlanAndroid');"
if android_mark not in t:
    if script_marker not in t: raise SystemExit('No se encontró script module')
    t=t.replace(script_marker,script_marker+'\n'+android_mark,1)

# La lógica closeSide/toggleSide original ya agrega/quita .open/.show. No la reescribimos.
if "function toggleSide()" not in t or "function closeSide()" not in t:
    raise SystemExit('No se encontraron funciones del menú lateral')

# Refuerza eventos táctiles sin depender de click de WebView.
menu_click="$('menuBtn').onclick=toggleSide;"
close_click="$('sideClose').onclick=closeSide;"
back_click="$('sideBackdrop').onclick=closeSide;"
if menu_click not in t: raise SystemExit('No se encontró evento click menuBtn')
if close_click not in t: raise SystemExit('No se encontró evento click sideClose')
if back_click not in t: raise SystemExit('No se encontró evento click sideBackdrop')

if "$('menuBtn').ontouchend=" not in t:
    t=t.replace(menu_click,menu_click+"$('menuBtn').ontouchend=e=>{e.preventDefault();e.stopPropagation();toggleSide()};",1)
if "$('sideClose').ontouchend=" not in t:
    t=t.replace(close_click,close_click+"$('sideClose').ontouchend=e=>{e.preventDefault();e.stopPropagation();closeSide()};",1)
if "$('sideBackdrop').ontouchend=" not in t:
    t=t.replace(back_click,back_click+"$('sideBackdrop').ontouchend=e=>{e.preventDefault();e.stopPropagation();closeSide()};",1)

# No cerrar automáticamente el drawer por un ancho WebView >780 en Android.
t=t.replace("window.addEventListener('resize',()=>{if(window.innerWidth>780)closeSide()});",
            "window.addEventListener('resize',()=>{if(APP_PLATFORM!=='android'&&window.innerWidth>780)closeSide()});",1)

# Versión Android 15.
t=re.sub(r"const APP_PLATFORM='android',APP_VERSION_CODE=\d+,APP_VERSION_NAME='[^']+';",
         "const APP_PLATFORM='android',APP_VERSION_CODE=15,APP_VERSION_NAME='15.0';",t,count=1)

# Validaciones de regresión.
assert "APP_VERSION_CODE=15" in t
assert "html.chatlanAndroid .side.open" in t
assert "document.documentElement.classList.add('chatlanAndroid')" in t
assert "$('menuBtn').ontouchend=" in t
assert "function toggleSide()" in t

htmlp.write_text(t,encoding='utf-8')

gp=root/'app/build.gradle'
s=gp.read_text(encoding='utf-8')
s=re.sub(r'versionCode\s+\d+', 'versionCode 15', s)
s=re.sub(r'versionName\s+\"[^\"]+\"', 'versionName \"15.0\"', s)
gp.write_text(s,encoding='utf-8')
