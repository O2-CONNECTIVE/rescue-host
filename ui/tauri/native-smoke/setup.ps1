param([Parameter(Mandatory)][string]$ToolsDirectory, [Parameter(Mandatory)][string]$InventoryPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$tools = Get-Content "$PSScriptRoot/tools.json" -Raw | ConvertFrom-Json
$inventory = Get-Content $InventoryPath -Raw | ConvertFrom-Json
if ($inventory.runtimeVersions.Count -ne 1 -or $inventory.runtimeVersions[0] -ne $tools.runtimeVersion) { throw 'NATIVE_ENV_RUNTIME_DRIFT_NO_UPDATE_ATTEMPTED' }
New-Item -ItemType Directory -Path $ToolsDirectory -ErrorAction Stop | Out-Null
function Assert-Hash([string]$File, [string]$Expected) {
    if ((Get-FileHash $File -Algorithm SHA256).Hash.ToLowerInvariant() -ne $Expected) { throw 'NATIVE_TOOL_HASH_MISMATCH' }
}
$zip = Join-Path $ToolsDirectory 'edge-driver.zip'
Invoke-WebRequest "https://msedgedriver.microsoft.com/$($tools.driverVersion)/edgedriver_win64.zip" -OutFile $zip -TimeoutSec 60
Assert-Hash $zip $tools.driverZipSha256
Expand-Archive $zip -DestinationPath "$ToolsDirectory/edge"
$edge = Join-Path $ToolsDirectory 'edge/msedgedriver.exe'
Assert-Hash $edge $tools.driverExeSha256
$signature = Get-AuthenticodeSignature $edge
if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'Microsoft Corporation') { throw 'NATIVE_TOOL_SIGNATURE_INVALID' }
$version = (& $edge --version | Out-String).Trim()
if ($version -notlike "Microsoft Edge WebDriver $($tools.driverVersion) (*)") { throw 'NATIVE_TOOL_VERSION_MISMATCH' }
$crate = Join-Path $ToolsDirectory 'tauri-driver.crate'
Invoke-WebRequest "https://static.crates.io/crates/tauri-driver/tauri-driver-$($tools.tauriDriverVersion).crate" -OutFile $crate -TimeoutSec 60
Assert-Hash $crate $tools.tauriDriverCrateSha256
& tar -xzf $crate -C $ToolsDirectory
if ($LASTEXITCODE -ne 0) { throw 'NATIVE_TOOL_EXTRACT_FAILED' }
$root = (Resolve-Path "$PSScriptRoot/../../..").Path
& python "$root/scripts/apply_driver_trace.py" --archive $crate --source "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)" --patch-folder "$root/tools/native-driver-trace" --provenance "$ToolsDirectory/driver-provenance.json"
if ($LASTEXITCODE -ne 0) { throw 'NATIVE_DIAGNOSTIC_PATCH_REJECTED' }
& cargo +1.99.1 test --manifest-path "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)/Cargo.toml" --locked --release --target-dir "$ToolsDirectory/target"
if ($LASTEXITCODE -ne 0) { throw 'NATIVE_DIAGNOSTIC_FIXTURE_FAILED' }
& cargo +1.99.1 install --path "$ToolsDirectory/tauri-driver-$($tools.tauriDriverVersion)" --locked --root "$ToolsDirectory/tauri" --target-dir "$ToolsDirectory/target"
if ($LASTEXITCODE -ne 0) { throw 'NATIVE_TOOL_BUILD_FAILED' }
Write-Output 'NATIVE_PINNED_TOOLS_READY_IN_RUNNER_TEMP'
