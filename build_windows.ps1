# 検証済みの Upterm バイナリを埋め込み、Windows x64 向けの単一ファイル RescueHost.exe をビルドする。
# 前提条件：Windows 10/11 x64、CPython 3.11–3.15 x64、Python ランチャー 'py'、インターネット接続。
param(
    [string]$UptermVersion = 'v0.39.0',
    [switch]$UseLocalUpterm,
    [string]$LocalUptermSha256 = ''
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if ($UptermVersion -ne 'v0.39.0') { throw 'This development candidate is reviewed only against v0.39.0.' }
if ($UseLocalUpterm -and $LocalUptermSha256 -notmatch '^[0-9a-fA-F]{64}$') {
    throw '-UseLocalUpterm requires an independently verified -LocalUptermSha256.'
}

$Root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $Root
& py -3 -I (Join-Path $Root 'scripts\check_build_lock.py') --target
if ($LASTEXITCODE -ne 0) { throw 'Python build target or dependency lock check failed' }
New-Item -Type Directory -Force (Join-Path $Root 'vendor') | Out-Null
New-Item -Type Directory -Force (Join-Path $Root 'third_party') | Out-Null
$Binary = Join-Path $Root 'vendor\upterm.exe'

if (-not $UseLocalUpterm) {
    $Name = 'upterm_windows_amd64.tar.gz'
    $Base = "https://github.com/owenthereal/upterm/releases/download/$UptermVersion"
    $Tmp = Join-Path $env:TEMP ("rescue-build-" + [guid]::NewGuid().ToString('N'))
    New-Item -Type Directory -Force $Tmp | Out-Null
    try {
        Write-Host "Downloading pinned Upterm $UptermVersion ..."
        Invoke-WebRequest -Uri "$Base/$Name" -OutFile (Join-Path $Tmp $Name)
        Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile (Join-Path $Tmp 'checksums.txt')
        $Expected = $null
        foreach ($Line in (Get-Content (Join-Path $Tmp 'checksums.txt'))) {
            if ($Line -match "^([0-9a-fA-F]{64})\s+\*?$([regex]::Escape($Name))$") {
                $Expected = $Matches[1].ToLowerInvariant()
                break
            }
        }
        if (-not $Expected) { throw "No SHA256 entry for $Name in checksums.txt" }
        $Actual = (Get-FileHash -Algorithm SHA256 (Join-Path $Tmp $Name)).Hash.ToLowerInvariant()
        if ($Expected -ne $Actual) { throw 'SHA256 mismatch for Upterm archive.' }
        tar -xzf (Join-Path $Tmp $Name) -C $Tmp
        if ($LASTEXITCODE -ne 0) { throw 'tar extraction failed' }
        $Found = Get-ChildItem -Path $Tmp -Filter 'upterm.exe' -File -Recurse | Select-Object -First 1
        if (-not $Found) { throw 'The Upterm archive did not contain upterm.exe.' }
        Copy-Item $Found.FullName $Binary -Force
        $LicenseUri = "https://raw.githubusercontent.com/owenthereal/upterm/$UptermVersion/LICENSE"
        Invoke-WebRequest -Uri $LicenseUri -OutFile (Join-Path $Root 'third_party\UPTERM-LICENSE.txt')
        Write-Host "Upterm $UptermVersion archive SHA256 verified: $Actual"
    } finally {
        Remove-Item -Recurse -Force $Tmp -ErrorAction SilentlyContinue
    }
} elseif (-not (Test-Path $Binary)) {
    throw 'vendor/upterm.exe is required when -UseLocalUpterm is set.'
}

if ($UseLocalUpterm) {
    $Actual = (Get-FileHash -Algorithm SHA256 $Binary).Hash
    if ($Actual -ine $LocalUptermSha256) { throw 'Local Upterm SHA256 mismatch.' }
    if (-not (Test-Path (Join-Path $Root 'third_party\UPTERM-LICENSE.txt'))) {
        throw 'Provide the matching Upterm license before packaging a local binary.'
    }
}
# アーカイブと同じ場所から取得するチェックサムは破損を検出するが、公開元の侵害は検出できない。
# ビルドしても新規セッションは有効にならない。v0.2 の実行時リリース制御は閉じたままとする。
$BuildEnv = Join-Path $env:TEMP ('rescue-python-build-' + [guid]::NewGuid().ToString('N'))
try {
    & py -3 -m venv $BuildEnv
    if ($LASTEXITCODE -ne 0) { throw 'Build venv creation failed' }
    $BuildPython = Join-Path $BuildEnv 'Scripts\python.exe'
    & $BuildPython -m pip --isolated --disable-pip-version-check install --index-url https://pypi.org/simple `
        --no-cache-dir --force-reinstall --require-hashes --only-binary=:all: -r (Join-Path $Root 'requirements-build.txt')
    if ($LASTEXITCODE -ne 0) { throw 'Locked PyInstaller setup failed' }
    & $BuildPython -m pip --isolated --disable-pip-version-check check
    if ($LASTEXITCODE -ne 0) { throw 'Build dependency consistency check failed' }
    & $BuildPython -I (Join-Path $Root 'scripts\check_build_lock.py') --installed
    if ($LASTEXITCODE -ne 0) { throw 'Installed build versions differ from lock' }
    & $BuildPython -m PyInstaller --onefile --noconsole --clean --noconfirm --name RescueHost `
        --paths (Join-Path $Root 'src') `
        --add-binary "$Binary;." `
        --add-data "$(Join-Path $Root 'assets\upterm_known_hosts');assets" `
        (Join-Path $Root 'src\rescue_host.py')
    if ($LASTEXITCODE -ne 0) { throw 'PyInstaller build failed' }
} finally {
    if (Test-Path $BuildEnv) { Remove-Item -Recurse -Force $BuildEnv }
}
Write-Host "Built: $(Join-Path $Root 'dist\RescueHost.exe')"
Write-Host 'Distribute the third_party/UPTERM-LICENSE.txt notice alongside the EXE.'
