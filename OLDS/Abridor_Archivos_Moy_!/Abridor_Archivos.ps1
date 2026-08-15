
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing

$ErrorActionPreference = "SilentlyContinue"
$baseDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$configFile = Join-Path $baseDir "archivos.json"

function Load-Items {
    if (Test-Path $configFile) {
        try {
            $data = Get-Content $configFile -Raw | ConvertFrom-Json
            if ($null -eq $data) { return @() }
            if ($data -is [System.Array]) { return @($data) }
            return @($data)
        } catch {
            return @()
        }
    }
    return @()
}

function Save-Items {
    param([object[]]$Items)
    try {
        @($Items) | ConvertTo-Json -Depth 4 | Set-Content -Path $configFile -Encoding UTF8
    } catch {
        [System.Windows.Forms.MessageBox]::Show(
            "No se pudo guardar la configuración.`n`n$($_.Exception.Message)",
            "Error",
            "OK",
            "Error"
        ) | Out-Null
    }
}

$form = New-Object System.Windows.Forms.Form
$form.Text = "Abridor de archivos"
$form.Size = New-Object System.Drawing.Size(760, 520)
$form.MinimumSize = New-Object System.Drawing.Size(700, 450)
$form.StartPosition = "CenterScreen"
$form.BackColor = [System.Drawing.Color]::FromArgb(245,247,250)
$form.Font = New-Object System.Drawing.Font("Segoe UI", 10)
$form.FormBorderStyle = "Sizable"

$title = New-Object System.Windows.Forms.Label
$title.Text = "ABRIDOR DE ARCHIVOS"
$title.Font = New-Object System.Drawing.Font("Segoe UI Semibold", 18)
$title.AutoSize = $true
$title.Location = New-Object System.Drawing.Point(24, 18)
$title.ForeColor = [System.Drawing.Color]::FromArgb(25,35,50)
$form.Controls.Add($title)

$subtitle = New-Object System.Windows.Forms.Label
$subtitle.Text = "Agrega los archivos que utilizas al llegar al trabajo. La configuración queda guardada."
$subtitle.AutoSize = $true
$subtitle.Location = New-Object System.Drawing.Point(27, 58)
$subtitle.ForeColor = [System.Drawing.Color]::FromArgb(85,95,110)
$form.Controls.Add($subtitle)

$list = New-Object System.Windows.Forms.ListView
$list.Location = New-Object System.Drawing.Point(28, 94)
$list.Size = New-Object System.Drawing.Size(686, 286)
$list.Anchor = "Top,Bottom,Left,Right"
$list.View = "Details"
$list.FullRowSelect = $true
$list.GridLines = $false
$list.HideSelection = $false
$list.MultiSelect = $true
$list.BackColor = [System.Drawing.Color]::White
[void]$list.Columns.Add("Archivo", 235)
[void]$list.Columns.Add("Ruta", 425)
$form.Controls.Add($list)

function Refresh-List {
    $list.Items.Clear()
    $items = Load-Items
    foreach ($path in $items) {
        if ([string]::IsNullOrWhiteSpace([string]$path)) { continue }
        $name = [System.IO.Path]::GetFileName([string]$path)
        $li = New-Object System.Windows.Forms.ListViewItem($name)
        [void]$li.SubItems.Add([string]$path)
        if (-not (Test-Path ([string]$path))) {
            $li.ForeColor = [System.Drawing.Color]::Firebrick
            $li.ToolTipText = "No se encontró este archivo."
        }
        [void]$list.Items.Add($li)
    }
}

$btnAdd = New-Object System.Windows.Forms.Button
$btnAdd.Text = "＋ Agregar archivos"
$btnAdd.Location = New-Object System.Drawing.Point(28, 397)
$btnAdd.Size = New-Object System.Drawing.Size(160, 42)
$btnAdd.Anchor = "Bottom,Left"
$btnAdd.FlatStyle = "Flat"
$btnAdd.BackColor = [System.Drawing.Color]::White
$form.Controls.Add($btnAdd)

$btnRemove = New-Object System.Windows.Forms.Button
$btnRemove.Text = "Quitar seleccionado"
$btnRemove.Location = New-Object System.Drawing.Point(198, 397)
$btnRemove.Size = New-Object System.Drawing.Size(160, 42)
$btnRemove.Anchor = "Bottom,Left"
$btnRemove.FlatStyle = "Flat"
$btnRemove.BackColor = [System.Drawing.Color]::White
$form.Controls.Add($btnRemove)

$btnClear = New-Object System.Windows.Forms.Button
$btnClear.Text = "Vaciar lista"
$btnClear.Location = New-Object System.Drawing.Point(368, 397)
$btnClear.Size = New-Object System.Drawing.Size(120, 42)
$btnClear.Anchor = "Bottom,Left"
$btnClear.FlatStyle = "Flat"
$btnClear.BackColor = [System.Drawing.Color]::White
$form.Controls.Add($btnClear)

$btnOpen = New-Object System.Windows.Forms.Button
$btnOpen.Text = "ABRIR TODO"
$btnOpen.Location = New-Object System.Drawing.Point(548, 397)
$btnOpen.Size = New-Object System.Drawing.Size(166, 42)
$btnOpen.Anchor = "Bottom,Right"
$btnOpen.FlatStyle = "Flat"
$btnOpen.BackColor = [System.Drawing.Color]::FromArgb(33, 115, 220)
$btnOpen.ForeColor = [System.Drawing.Color]::White
$btnOpen.Font = New-Object System.Drawing.Font("Segoe UI Semibold", 10)
$form.Controls.Add($btnOpen)

$status = New-Object System.Windows.Forms.Label
$status.Text = "Listo."
$status.AutoSize = $true
$status.Location = New-Object System.Drawing.Point(29, 452)
$status.Anchor = "Bottom,Left"
$status.ForeColor = [System.Drawing.Color]::FromArgb(85,95,110)
$form.Controls.Add($status)

$btnAdd.Add_Click({
    $dlg = New-Object System.Windows.Forms.OpenFileDialog
    $dlg.Title = "Selecciona uno o varios archivos"
    $dlg.Multiselect = $true
    $dlg.Filter = "Todos los archivos (*.*)|*.*"
    if ($dlg.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
        $current = @()
        foreach ($x in (Load-Items)) { if ($x) { $current += [string]$x } }

        foreach ($file in $dlg.FileNames) {
            if ($current -notcontains $file) {
                $current += $file
            }
        }
        Save-Items $current
        Refresh-List
        $status.Text = "Configuración guardada: $($current.Count) archivo(s)."
    }
})

$btnRemove.Add_Click({
    if ($list.SelectedItems.Count -eq 0) {
        [System.Windows.Forms.MessageBox]::Show(
            "Selecciona uno o varios archivos de la lista.",
            "Abridor de archivos"
        ) | Out-Null
        return
    }

    $removePaths = @()
    foreach ($selected in $list.SelectedItems) {
        $removePaths += [string]$selected.SubItems[1].Text
    }

    $newItems = @()
    foreach ($x in (Load-Items)) {
        if ($removePaths -notcontains [string]$x) {
            $newItems += [string]$x
        }
    }

    Save-Items $newItems
    Refresh-List
    $status.Text = "Archivo(s) retirado(s) de la lista."
})

$btnClear.Add_Click({
    $answer = [System.Windows.Forms.MessageBox]::Show(
        "¿Quieres borrar toda la configuración guardada?",
        "Confirmar",
        [System.Windows.Forms.MessageBoxButtons]::YesNo,
        [System.Windows.Forms.MessageBoxIcon]::Question
    )
    if ($answer -eq [System.Windows.Forms.DialogResult]::Yes) {
        Save-Items @()
        Refresh-List
        $status.Text = "Lista vacía."
    }
})

$btnOpen.Add_Click({
    $items = Load-Items
    if ($items.Count -eq 0) {
        [System.Windows.Forms.MessageBox]::Show(
            "Primero agrega los archivos que quieres abrir.",
            "Abridor de archivos"
        ) | Out-Null
        return
    }

    $opened = 0
    $missing = @()

    foreach ($path in $items) {
        $p = [string]$path
        if (Test-Path $p) {
            try {
                Start-Process -FilePath $p
                $opened++
                Start-Sleep -Milliseconds 250
            } catch {
                $missing += $p
            }
        } else {
            $missing += $p
        }
    }

    if ($missing.Count -gt 0) {
        $status.Text = "Se abrieron $opened. No se encontraron $($missing.Count)."
        [System.Windows.Forms.MessageBox]::Show(
            "Se abrieron $opened archivo(s).`n`nNo se pudieron abrir o no se encontraron:`n`n" +
            ($missing -join "`n"),
            "Resultado"
        ) | Out-Null
    } else {
        $status.Text = "Se abrieron correctamente $opened archivo(s)."
    }

    Refresh-List
})

$list.Add_DoubleClick({
    if ($list.SelectedItems.Count -eq 1) {
        $p = [string]$list.SelectedItems[0].SubItems[1].Text
        if (Test-Path $p) {
            Start-Process -FilePath $p
        } else {
            [System.Windows.Forms.MessageBox]::Show(
                "No se encontró el archivo:`n`n$p",
                "Archivo no encontrado"
            ) | Out-Null
        }
    }
})

Refresh-List
[void]$form.ShowDialog()
