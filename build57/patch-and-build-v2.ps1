$ErrorActionPreference = 'Stop'
$srcDir = Join-Path $env:GITHUB_WORKSPACE 'SISTEMA_NOTAS_LOCAL_4_6\codigo_fuente'
$main = Join-Path $srcDir 'main.go'
$appjs = Join-Path $srcDir 'web\app.js'

$s = Get-Content $main -Raw
$s = $s -replace "`r`n", "`n"
$s = $s.Replace('appVersion     = "4.6.0-buscador-resultados-sin-select"', 'appVersion     = "5.7.0-native-file-drag"')

$newBrowser = @'
	if strings.TrimSpace(os.Getenv("SISTEMA_NOTAS_NO_BROWSER")) != "1" {
		go func() {
			time.Sleep(650 * time.Millisecond)
			openBrowser(baseURL)
		}()
	}
'@
$browserPattern = '(?m)^\s*go func\(\) \{\n\s*time\.Sleep\(650 \* time\.Millisecond\)\n\s*openBrowser\(baseURL\)\n\s*\}\(\)'
$before = $s
$s = [regex]::Replace($s, $browserPattern, $newBrowser.TrimEnd(), 1)
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
$rootPattern = '(?s)func applicationDir\(\) \(string, error\) \{.*?\n\}'
$before = $s
$s = [regex]::Replace($s, $rootPattern, $newRoot.TrimEnd(), 1)
if ($s -eq $before) { throw 'No se pudo parchear applicationDir.' }
Set-Content -Path $main -Value $s -Encoding utf8 -NoNewline

$legacy = Get-Content (Join-Path $env:GITHUB_WORKSPACE 'build57\patch-and-build.ps1') -Raw
$marker = '$a = Get-Content $appjs -Raw'
$idx = $legacy.IndexOf($marker)
if ($idx -lt 0) { throw 'No se encontró el bloque de parcheo de app.js.' }
$tail = $legacy.Substring($idx)
Invoke-Expression $tail
