# 診断専用。合否や所有子の集合を決めず、元の観測を匿名化して並記する。
function Open-ChildObservationHandles($Children) {
    $slot = 0
    foreach ($child in $Children) {
        $slot += 1
        if ($slot -gt 32) { break }
        $entry = [pscustomobject]@{ Slot = $slot; Identity = 'unavailable'; Process = $null }
        $process = $null
        try {
            if ($child.CreationDate -is [DateTime]) {
                $process = Get-Process -Id $child.ProcessId -ErrorAction Stop
                $null = $process.SafeHandle
                $expected = $child.CreationDate.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')
                $actual = $process.StartTime.ToUniversalTime().ToString('yyyyMMddHHmmss.ffffff')
                if ($process.Id -eq $child.ProcessId -and $process.Path -eq $child.ExecutablePath -and $expected -eq $actual) {
                    $entry.Identity = 'verified'
                    $entry.Process = $process
                } else { $entry.Identity = 'different-identity' }
            }
        } catch { $entry.Identity = 'unavailable' }
        finally { if ($process -and $entry.Identity -ne 'verified') { $process.Dispose() } }
        $entry
    }
}
function Close-ChildObservationHandles($Handles) {
    $failures = 0
    foreach ($entry in $Handles) { try { if ($entry.Process) { $entry.Process.Dispose() } } catch { $failures += 1 } }
    return $failures
}
function Get-ChildCloseFacts($Children, $App, $Snapshot, $Handles) {
    $childrenArray = @($Children)
    $rows = @()
    $remaining = 0
    $slot = 0
    foreach ($child in $childrenArray) {
        $slot += 1
        $same = @($Snapshot | Where-Object { Same-Process $child $_ })
        if ($same.Count -gt 0) { $remaining += 1 }
        if ($slot -gt 32) { continue }
        $pidRows = @($Snapshot | Where-Object ProcessId -eq $child.ProcessId)
        $snapshotState = if ($same.Count -eq 1 -and $pidRows.Count -eq 1) { 'same-identity' } elseif ($pidRows.Count -gt 1) { 'ambiguous' } elseif ($pidRows.Count -eq 1) { 'pid-reused' } else { 'absent' }
        $known = $child.CreationDate -is [DateTime]
        $parents = @(@($App) + $childrenArray | Where-Object ProcessId -eq $child.ParentProcessId)
        $order = 'unknown'
        $parentSlot = -1
        if ($parents.Count -eq 1) {
            if ($App.ProcessId -eq $parents[0].ProcessId) { $parentSlot = 0 }
            else { for ($index = 0; $index -lt $childrenArray.Count; $index += 1) { if ($childrenArray[$index].ProcessId -eq $parents[0].ProcessId) { $parentSlot = $index + 1; break } } }
            if ($known -and $parents[0].CreationDate -is [DateTime]) { $order = if ($child.CreationDate -lt $parents[0].CreationDate) { 'child-older' } else { 'consistent' } }
        }
        $handlesForSlot = @($Handles | Where-Object Slot -eq $slot)
        $handleState = 'unavailable'
        if ($handlesForSlot.Count -eq 1) {
            $handle = $handlesForSlot[0]
            if ($handle.Identity -eq 'verified' -and $handle.Process) {
                try { $handleState = if ($handle.Process.HasExited) { 'exited' } else { 'live' } } catch { $handleState = 'unavailable' }
            } elseif ($handle.Identity -eq 'different-identity') { $handleState = 'different-identity' }
        }
        $rows += [pscustomobject]@{ slot = $slot; role = $(if ($child.Name -eq 'msedgewebview2.exe') { 'webview' } else { 'other' }); parentSlot = $parentSlot; creationKnown = $known; parentOrder = $order; snapshotState = $snapshotState; handleState = $handleState }
    }
    return [pscustomobject]@{ observed = $childrenArray.Count; remainingSnapshot = $remaining; omittedRows = [Math]::Max(0, $childrenArray.Count - 32); rows = @($rows) }
}
