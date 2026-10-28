# 診断専用。時点の相関だけを記録し、所有認可・成功判定・終了操作は行わない。
function Get-StartupIdentity($Recorded, $Snapshot) {
    if ($null -eq $Recorded) { return 'unrecorded' }
    $matches = @($Snapshot | Where-Object ProcessId -eq $Recorded.ProcessId)
    if ($matches.Count -eq 0) { return 'absent' }
    if ($matches.Count -ne 1) { return 'ambiguous' }
    if ($Recorded.CreationDate -isnot [DateTime] -or $matches[0].CreationDate -isnot [DateTime]) { return 'unavailable' }
    if (Same-Process $Recorded $matches[0]) { return 'same-identity' }
    return 'different-identity'
}
function Get-StartupFacts($Driver, $Edge, [string]$AppPath, $Snapshot, $Listeners) {
    if ($null -eq $Snapshot -or @($Snapshot).Count -gt 4096) { throw 'NATIVE_STARTUP_SNAPSHOT_UNAVAILABLE' }
    $driverIdentity = Get-StartupIdentity $Driver $Snapshot
    $edgeIdentity = Get-StartupIdentity $Edge $Snapshot
    $candidates = @($Snapshot | Where-Object ExecutablePath -eq $AppPath)
    $correlation = 'unconfirmed'
    $apps = @()
    $webviews = @()
    if ($driverIdentity -eq 'same-identity' -and $edgeIdentity -eq 'same-identity') {
        try {
            $descendants = @(Get-Descendants $Driver.ProcessId $Snapshot)
            $apps = @($descendants | Where-Object ExecutablePath -eq $AppPath)
            if ($apps.Count -eq 1) {
                $webviews = @(Get-Descendants $apps[0].ProcessId $Snapshot | Where-Object Name -eq 'msedgewebview2.exe')
            }
            $correlation = 'consistent'
        } catch { $correlation = 'unconfirmed'; $apps = @(); $webviews = @() }
    }
    $listenerRows = @($Listeners | Where-Object LocalPort -in @(4444, 4445))
    $listenerFacts = @($listenerRows | Select-Object -First 8 | ForEach-Object {
        $family = 'unavailable'
        $loopback = $false
        try {
            $address = [Net.IPAddress]::Parse($_.LocalAddress)
            $family = if ($address.AddressFamily -eq [Net.Sockets.AddressFamily]::InterNetwork) { 'ipv4' } else { 'ipv6' }
            $loopback = [Net.IPAddress]::IsLoopback($address)
        } catch { }
        $recorded = if ($_.LocalPort -eq 4444) { $Driver } else { $Edge }
        [pscustomobject]@{ port = $_.LocalPort; family = $family; loopback = $loopback
            ownerMatchesRecordedPid = ($null -ne $recorded -and $_.OwningProcess -eq $recorded.ProcessId) }
    })
    return [pscustomobject]@{
        driverIdentity = $driverIdentity; edgeIdentity = $edgeIdentity
        appCandidates = $candidates.Count; lineageCorrelation = $correlation
        correlatedApps = $(if ($correlation -eq 'consistent') { $apps.Count } else { $null })
        correlatedWebviews = $(if ($correlation -eq 'consistent' -and $apps.Count -eq 1) { $webviews.Count } else { $null })
        listeners = $listenerFacts; omittedListeners = [Math]::Max(0, $listenerRows.Count - 8)
    }
}
