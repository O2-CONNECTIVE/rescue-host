param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$OutputDirectory)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$root = (Resolve-Path "$PSScriptRoot/../../..").Path
try {
    $tools = Get-Content "$PSScriptRoot/tools.json" -Raw | ConvertFrom-Json
    $before = Get-Content "$ToolsDirectory/driver-provenance.json" -Raw | ConvertFrom-Json
    $binary = Join-Path $ToolsDirectory 'tauri/bin/tauri-driver.exe'
    if ($before.variant -ne 'tauri-driver-2.1.0-stage-2' -or $before.binarySha256 -cnotmatch '^[0-9a-f]{64}$' -or (Get-FileHash $binary -Algorithm SHA256).Hash.ToLowerInvariant() -cne $before.binarySha256) { throw 'STAGE_BINARY_DRIFT' }
    # 原provenanceはコピーせず、固定source/binaryから再検証して安全な項目だけ再生成。
    & python -B "$root/scripts/apply_driver_trace.py" --archive "$ToolsDirectory/tauri-driver.crate" --source "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)" --patch-folder "$root/tools/native-driver-trace" --provenance "$OutputDirectory/driver-provenance.json" --verify-patched --binary $binary
    if ($LASTEXITCODE -ne 0) { throw 'STAGE_SOURCE_DRIFT' }
    & python -B "$root/scripts/verify_driver_trace.py" --trace "$ToolsDirectory/driver-stage.jsonl" | Set-Content "$OutputDirectory/driver-stage-report.json" -Encoding utf8
    exit $LASTEXITCODE
} catch {
    '{"check":"native-driver-stage","diagnostic":"incomplete","code":"TRACE_PROVENANCE_UNAVAILABLE","events":[],"summary":null}' | Set-Content "$OutputDirectory/driver-stage-report.json" -Encoding utf8
    Write-Output 'NATIVE_STAGE_PROVENANCE_UNAVAILABLE'
    exit 1
}
