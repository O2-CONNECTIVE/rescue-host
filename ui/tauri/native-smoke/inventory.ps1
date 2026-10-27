param([Parameter(Mandatory)][string]$OutputPath)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
if (-not $IsWindows) { throw 'NATIVE_ENV_WINDOWS_REQUIRED' }
$runtimeKey = '{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}'
$versions = @('HKLM:\SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients', 'HKCU:\Software\Microsoft\EdgeUpdate\Clients') | ForEach-Object {
    $key = Join-Path $_ $runtimeKey
    if (Test-Path $key) { (Get-ItemProperty $key -Name pv).pv }
}
$driver = Get-Command msedgedriver.exe -ErrorAction SilentlyContinue
$record = [ordered]@{
    kind = 'read-only-native-environment-inventory'
    runtimeVersions = @($versions)
    edgeDriverVersion = if ($driver) { (& $driver.Source --version | Out-String).Trim() } else { 'not-found' }
    edgeDriverSha256 = if ($driver) { (Get-FileHash $driver.Source -Algorithm SHA256).Hash.ToLowerInvariant() } else { $null }
    runnerImage = $env:ImageVersion
    nativeTestsRun = 0
}
$record | ConvertTo-Json -Depth 4 | Set-Content $OutputPath -Encoding utf8
$record | ConvertTo-Json -Depth 4 | Write-Output
if (@($versions).Count -eq 0) { throw 'NATIVE_ENV_WEBVIEW2_MISSING_NO_INSTALL_ATTEMPTED' }
