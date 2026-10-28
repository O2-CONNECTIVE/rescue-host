# 診断専用。合否や所有子の集合を決めず、元の観測を匿名化して並記する。
function Save-NativeReport($Report, [string]$OutputDirectory) {
    try { $Report | ConvertTo-Json -Depth 6 | Set-Content (Join-Path $OutputDirectory 'native-result.json') -Encoding utf8 }
    catch { throw 'NATIVE_CLEANUP_EVIDENCE_WRITE_FAILED' }
}
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
function Get-ChildCloseFacts($Children, $App, $Snapshot, $Handles, $WaitStartedUtc = $null, $ExpectedProfile = "") {
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
        $exitRelativeMs = $null
        if ($handleState -eq 'exited' -and $WaitStartedUtc -is [DateTime]) {
            try { $exitRelativeMs = [Math]::Round(($handlesForSlot[0].Process.ExitTime.ToUniversalTime() - $WaitStartedUtc).TotalMilliseconds, 3) } catch { }
        }
        $profileMatch = $null
        $webviewType = 'unavailable'
        if ($ExpectedProfile -and $known -and $App.CreationDate -is [DateTime] -and $child.CreationDate -ge $App.CreationDate -and $child.Name -eq 'msedgewebview2.exe' -and $handlesForSlot.Count -eq 1 -and $handlesForSlot[0].Identity -eq 'verified') {
            try {
                $types = [Regex]::Matches($child.CommandLine, '(?:^|\s)--type=(?:"([^"]*)"|(\S+))(?=\s|$)')
                if ($types.Count -eq 0) { $webviewType = 'no-type-flag' }
                elseif ($types.Count -eq 1) {
                    $type = if ($types[0].Groups[1].Success) { $types[0].Groups[1].Value } else { $types[0].Groups[2].Value }
                    $webviewType = if ($type -in @('renderer', 'gpu-process', 'utility', 'crashpad-handler')) { $type } else { 'other' }
                }
                $profileArguments = [Regex]::Matches($child.CommandLine, '(?:^|\s)--user-data-dir=(?:"([^"]*)"|(\S+))(?=\s|$)')
                if ($profileArguments.Count -eq 1) {
                    $value = if ($profileArguments[0].Groups[1].Success) { $profileArguments[0].Groups[1].Value } else { $profileArguments[0].Groups[2].Value }
                    $profileMatch = [String]::Equals($value.Replace('/', '\').TrimEnd('\'), $ExpectedProfile.Replace('/', '\').TrimEnd('\'), [StringComparison]::OrdinalIgnoreCase)
                }
            } catch { }
        }
        $role = switch ($child.Name) { 'msedgewebview2.exe' { 'webview' } 'conhost.exe' { 'console-host' } 'crashpad_handler.exe' { 'crash-reporter' } 'WerFault.exe' { 'windows-error-reporting' } default { 'other' } }
        $rows += [pscustomobject]@{ handleIdentityVerified = ($handlesForSlot.Count -eq 1 -and $handlesForSlot[0].Identity -eq 'verified'); webviewType = $webviewType; exitRelativeMs = $exitRelativeMs; profileMatch = $profileMatch; cimCreationMatch = ($known -and $same.Count -eq 1); slot = $slot; role = $role; parentSlot = $parentSlot; creationKnown = $known; parentOrder = $order; snapshotState = $snapshotState; handleState = $handleState }
    }
    return [pscustomobject]@{ observed = $childrenArray.Count; remainingSnapshot = $remaining; omittedRows = [Math]::Max(0, $childrenArray.Count - 32); rows = @($rows) }
}

# 既存の通常close verdictを変更せず、cleanup例外と所有handle解放を分離する。
function Complete-NativeCleanup($Node, $Driver, $Children, $App, $Handles, $Report, $OutputDirectory, $WaitStartedUtc = $null, $ExpectedProfile = "") {
    $Report.cleanupStarted = $true
    $Report.cleanupFailures = 0
    $Report.cleanupCodes = @()
    try {
        try { if ($Node -and -not $Node.HasExited) { $Node.Kill() } }
        catch { $Report.cleanupFailures += 1; $Report.cleanupCodes += 'node-kill-failed' }
        try { if ($Driver -and -not $Driver.HasExited) { $Driver.Kill() } }
        catch { $Report.cleanupFailures += 1; $Report.cleanupCodes += 'driver-kill-failed' }
        try {
            if ($Driver) {
                $Report.driverExitObservedAfterCleanup = $Driver.WaitForExit(2000)
                if (-not $Report.driverExitObservedAfterCleanup) { $Report.cleanupFailures += 1; $Report.cleanupCodes += 'driver-exit-unconfirmed' }
            }
        } catch { $Report.cleanupFailures += 1; $Report.cleanupCodes += 'driver-wait-failed' }
        if (@($Children).Count -gt 0) {
            try { $Report.afterDriverCleanupFacts = Get-ChildCloseFacts $Children $App (Get-Snapshot) $Handles $WaitStartedUtc $ExpectedProfile }
            catch { $Report.afterDriverCleanupObservation = 'unavailable' }
        }
    } finally {
        try {
            $Report.childHandleDisposeFailures = Close-ChildObservationHandles $Handles
            $Report.cleanupFailures += $Report.childHandleDisposeFailures
        } catch { $Report.cleanupFailures += 1; $Report.cleanupCodes += 'handle-dispose-failed' }
        finally {
            Save-NativeReport $Report $OutputDirectory
        }
    }
}
