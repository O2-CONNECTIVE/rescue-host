package guestaccount

// 無効・資格情報なしのstaging候補だけ。enable/SSH認証方式を採用しない。
// New-LocalUser返却objectのSIDを用い、名前の再lookupで来歴を補わない。
func creationScript(module string) string {
	return sidScriptRead + `  $pattern='\A\{"protocol":"rescue-host-guest-create/1","operation":"create-disabled","epoch":"[0-9a-f]{64}","intent":"[0-9a-f]{64}","policy":"[0-9a-f]{64}","generation":"(?<generation>[1-9][0-9]{0,19})","machine":"(?<machine>S-1-5-21-[0-9]+-[0-9]+-[0-9]+)","operator":"(?<operator>S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+)","service":"(?<service>S-1-5-(?:18|19|20|21-[0-9]+-[0-9]+-[0-9]+-[0-9]+|80-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+))","name":"(?<name>rhg_[0-9a-f]{16})","sid":""\}\n\z'
  $match=[regex]::Match($text,$pattern,[System.Text.RegularExpressions.RegexOptions]::CultureInvariant)
  if (!$match.Success) { throw 'frame-shape' }
  $null=[uint64]::Parse($match.Groups['generation'].Value,[Globalization.CultureInfo]::InvariantCulture)
` + sidScriptImport(module) + `  $created=@(Microsoft.PowerShell.LocalAccounts\New-LocalUser -Name $match.Groups['name'].Value -Disabled -NoPassword -ErrorAction Stop -WhatIf:$false -Confirm:$false)
  if ($created.Count -ne 1 -or $null -eq $created[0].SID -or $created[0].Enabled -isnot [bool]) { throw 'create-result' }
  $sid=$created[0].SID
  if ($sid -isnot [System.Security.Principal.SecurityIdentifier] -or $sid.AccountDomainSid.Value -cne $match.Groups['machine'].Value -or $sid.Value -ceq $match.Groups['operator'].Value -or $sid.Value -ceq $match.Groups['service'].Value) { throw 'sid-binding' }
  $disabled=(!$created[0].Enabled).ToString().ToLowerInvariant()
  $nameMatches=($created[0].Name -ceq $match.Groups['name'].Value).ToString().ToLowerInvariant()
  $reply='{"protocol":"rescue-host-guest-created/1","request":'+$text.Substring(0,$text.Length-1)+',"sid":"'+$sid.Value+'","disabled":'+$disabled+',"nameMatches":'+$nameMatches+'}'+[char]10
  $bytes=[System.Text.UTF8Encoding]::new($false,$true).GetBytes($reply)
  $outputStream=[Console]::OpenStandardOutput()
  $outputStream.Write($bytes,0,$bytes.Length)
  $outputStream.Flush()
  exit 0
` + sidScriptEnd
}
