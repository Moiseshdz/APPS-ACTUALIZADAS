$ErrorActionPreference = 'Stop'
$srcDir = Join-Path $env:GITHUB_WORKSPACE 'SISTEMA_NOTAS_LOCAL_4_6\codigo_fuente'
$main = Join-Path $srcDir 'main.go'
$appjs = Join-Path $srcDir 'web\app.js'

$s = (Get-Content $main -Raw) -replace "`r`n", "`n"
$s = $s.Replace('appVersion     = "4.6.0-buscador-resultados-sin-select"', 'appVersion     = "5.7.0-native-file-drag"')
$newBrowser = @'
	if strings.TrimSpace(os.Getenv("SISTEMA_NOTAS_NO_BROWSER")) != "1" {
		go func() {
			time.Sleep(650 * time.Millisecond)
			openBrowser(baseURL)
		}()
	}
'@
$before = $s
$s = [regex]::Replace($s, '(?m)^\s*go func\(\) \{\n\s*time\.Sleep\(650 \* time\.Millisecond\)\n\s*openBrowser\(baseURL\)\n\s*\}\(\)', $newBrowser.TrimEnd(), 1)
if ($s -eq $before) { throw 'No se pudo parchear openBrowser.' }
$newRoot = @'
func applicationDir() (string, error) {
	if root := strings.TrimSpace(os.Getenv("SISTEMA_NOTAS_ROOT")); root != "" {
		if err := os.MkdirAll(root, 0755); err != nil {
			return "", err
		}
		return root, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}
'@
$before = $s
$s = [regex]::Replace($s, '(?s)func applicationDir\(\) \(string, error\) \{.*?\n\}', $newRoot.TrimEnd(), 1)
if ($s -eq $before) { throw 'No se pudo parchear applicationDir.' }
Set-Content -Path $main -Value $s -Encoding utf8 -NoNewline

$a = (Get-Content $appjs -Raw) -replace "`r`n", "`n"
$addFilesMarker = 'function addFiles(fileList, edit = false) {'
$idx = $a.IndexOf($addFilesMarker)
if ($idx -lt 0) { throw 'No se encontró addFiles.' }
$helpers = @'
function isSupportedImageFile(file) {
  if (!file) return false;
  const type = String(file.type || "").toLowerCase();
  const name = String(file.name || "").toLowerCase();
  return type.startsWith("image/") || /\.(jpe?g|png|webp|gif|bmp|heic|heif)$/i.test(name);
}

function droppedImageFiles(dataTransfer) {
  const all = [];
  for (const file of [...(dataTransfer?.files || [])]) if (file) all.push(file);
  for (const item of [...(dataTransfer?.items || [])]) {
    if (item?.kind !== "file") continue;
    try {
      const file = item.getAsFile();
      if (file) all.push(file);
    } catch {}
  }
  const seen = new Set();
  return all.filter(file => {
    if (!isSupportedImageFile(file)) return false;
    const key = photoKey(file);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

'@
$a = $a.Insert($idx, $helpers)
$a = $a.Replace('const files = [...fileList].filter(file => file && file.type.startsWith("image/"));', 'const files = [...fileList].filter(isSupportedImageFile);')
$a = $a.Replace('zone.addEventListener("drop", event => addFiles(event.dataTransfer.files, edit));', 'zone.addEventListener("drop", event => addFiles(droppedImageFiles(event.dataTransfer), edit));')
$a = $a.Replace('zone.addEventListener("drop", event => addPhotoOnlyFiles(event.dataTransfer.files));', 'zone.addEventListener("drop", event => addPhotoOnlyFiles(droppedImageFiles(event.dataTransfer)));')
$a = $a.Replace('.filter(item => item.kind === "file" && item.type.startsWith("image/"))', '.filter(item => item.kind === "file")')
$a = $a.Replace('.map(item => item.getAsFile()).filter(Boolean);', '.map(item => item.getAsFile()).filter(isSupportedImageFile);')

$dragPattern = '(?s)function setupDraggablePhoto\(img, photo, note = selectedNote\) \{.*?\n\}\n\nfunction renderViewer'
$dragReplacement = @'
function buildNativeDragInfo(photo, note = selectedNote) {
  const absolute = new URL(photo.url, location.origin).href;
  const originalName = photo.name || `foto_${photo.id || "nota"}.jpg`;
  const dot = originalName.lastIndexOf(".");
  const ext = dot > 0 ? originalName.slice(dot) : ".jpg";
  const clean = value => String(value || "").normalize("NFD").replace(/[\u0300-\u036f]/g, "").replace(/[^a-zA-Z0-9_-]+/g, "_").replace(/^_+|_+$/g, "").slice(0, 70);
  const folioPart = clean(note?.folio || "SIN_FOLIO");
  const incidentPart = clean(note?.titulo || "SIN_INCIDENTE");
  const name = `${folioPart}_${incidentPart}_${clean(originalName.slice(0, dot > 0 ? dot : undefined)) || `foto_${photo.id || "nota"}`}${ext}`;
  return { url: absolute, name, mime: photo.mime || "image/jpeg" };
}

function setupDraggablePhoto(img, photo, note = selectedNote) {
  img.draggable = true;
  let preparingNative = null;
  const prepare = () => {
    cachePhotoForDrag(photo);
    if (!window.sistemaNotasDesktop?.preparePhoto) return;
    if (img.dataset.nativeDragPath || preparingNative) return;
    const info = buildNativeDragInfo(photo, note);
    preparingNative = window.sistemaNotasDesktop.preparePhoto(info)
      .then(filePath => {
        if (filePath) img.dataset.nativeDragPath = filePath;
        return filePath;
      })
      .catch(() => "")
      .finally(() => { preparingNative = null; });
  };
  img.addEventListener("mouseenter", prepare);
  img.addEventListener("pointerdown", prepare);
  if (img.complete) setTimeout(prepare, 0);
  else img.addEventListener("load", prepare, { once: true });
  img.addEventListener("dragstart", event => {
    if (window.sistemaNotasDesktop?.startDrag) {
      const nativePath = img.dataset.nativeDragPath;
      event.preventDefault();
      event.stopPropagation();
      img.classList.add("drag-source");
      if (nativePath) {
        window.sistemaNotasDesktop.startDrag(nativePath);
      } else {
        prepare();
        toast("Preparando la fotografía como archivo de Windows. Intenta arrastrarla nuevamente en un instante.");
      }
      return;
    }
    const info = buildNativeDragInfo(photo, note);
    event.dataTransfer.effectAllowed = "copy";
    event.dataTransfer.setData("text/uri-list", info.url);
    event.dataTransfer.setData("text/plain", info.url);
    try { event.dataTransfer.setData("DownloadURL", `${info.mime}:${info.name}:${info.url}`); } catch {}
    const cached = photoBlobCache.get(photo.url);
    if (cached?.file && event.dataTransfer.items?.add) {
      try { event.dataTransfer.items.add(cached.file); } catch {}
    }
    img.classList.add("drag-source");
  });
  img.addEventListener("dragend", () => img.classList.remove("drag-source"));
}

function renderViewer
'@
$patched = [regex]::Replace($a, $dragPattern, $dragReplacement)
if ($patched -eq $a) { throw 'No se pudo reemplazar setupDraggablePhoto.' }
$a = $patched
Set-Content -Path $appjs -Value $a -Encoding utf8 -NoNewline

$buildRoot = Join-Path $env:GITHUB_WORKSPACE '.build57'
New-Item -ItemType Directory -Force -Path $buildRoot | Out-Null
Push-Location $srcDir
go test .\main.go
go build -trimpath -ldflags "-H windowsgui -s -w" -o (Join-Path $buildRoot 'backend.exe') .\main.go
Pop-Location
