package guestaccount

import (
	"strings"

	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childjob"
	"github.com/O2-CONNECTIVE/rescue-host/backend/internal/childstdio"
)

// private provisioning候補。値だけでは配置/主体/依存解決の保護proofにならない。
// 正規producerと公開入口への接続は未設定。tokenは借用し、取得/昇格しない。
type sidNativeProfile struct {
	image, manifest, assembly childjob.Artifact
	token                     uintptr
	stdio                     childstdio.Setup
}

func newSIDNativeFactory(p sidNativeProfile) (func() (sidExecutor, error), error) {
	return newGuestNativeFactory(p, sidScript(p.manifest.Path))
}

// scriptはこのpackageの固定処理だけが渡す。任意UI/argv/envから選択しない。
func newGuestNativeFactory(p sidNativeProfile, script string) (func() (sidExecutor, error), error) {
	const manifest = `\Microsoft.PowerShell.LocalAccounts.psd1`
	const assembly = `\Microsoft.PowerShell.LocalAccounts.dll`
	if !strings.HasSuffix(p.manifest.Path, manifest) || !strings.HasSuffix(p.assembly.Path, assembly) || strings.TrimSuffix(p.manifest.Path, manifest) != strings.TrimSuffix(p.assembly.Path, assembly) {
		return nil, ErrUnconfigured
	}
	command, err := childjob.NewFixedCommand([]string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script}, []childjob.Artifact{p.manifest, p.assembly})
	if err != nil {
		return nil, ErrUnconfigured
	}
	config := sidRunConfig{spec: childjob.Spec{Artifact: p.image, PrimaryToken: p.token, CleanupTimeout: p.stdio.CleanupTimeout, Command: command}, stdio: p.stdio}
	// constructorはnative取得を行わない。issued ownerはsidSourceが保持する。
	if _, err = newNativeSIDExecutor(config); err != nil {
		return nil, err
	}
	return func() (sidExecutor, error) { return newNativeSIDExecutor(config) }, nil
}

// 入力をコードへ埋め込まない。唯一のliteralは保護側の固定module path。
// shell/環境/PATH/module searchを経由せず、実EOF前にimport/mutationしない。
func sidScript(module string) string {
	return sidScriptRead + `  $pattern='\A\{"protocol":"rescue-host-guest-sid/1","operation":"(?<op>disable|delete)","epoch":"[0-9a-f]{64}","intent":"[0-9a-f]{64}","policy":"[0-9a-f]{64}","generation":"(?<generation>[1-9][0-9]{0,19})","machine":"(?<machine>S-1-5-21-[0-9]+-[0-9]+-[0-9]+)","operator":"(?<operator>S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+)","service":"(?<service>S-1-5-(?:18|19|20|21-[0-9]+-[0-9]+-[0-9]+-[0-9]+|80-[0-9]+-[0-9]+-[0-9]+-[0-9]+-[0-9]+))","name":"rhg_[0-9a-f]{16}","sid":"(?<sid>S-1-5-21-[0-9]+-[0-9]+-[0-9]+-[0-9]+)"\}\n\z'
  $match=[regex]::Match($text,$pattern,[System.Text.RegularExpressions.RegexOptions]::CultureInvariant)
  if (!$match.Success) { throw 'frame-shape' }
  $null=[uint64]::Parse($match.Groups['generation'].Value,[Globalization.CultureInfo]::InvariantCulture)
  $sid=[System.Security.Principal.SecurityIdentifier]::new($match.Groups['sid'].Value)
  if ($sid.Value -cne $match.Groups['sid'].Value -or $sid.AccountDomainSid.Value -cne $match.Groups['machine'].Value -or $sid.Value -ceq $match.Groups['operator'].Value -or $sid.Value -ceq $match.Groups['service'].Value) { throw 'sid-binding' }
` + sidScriptImport(module) + `  if ($match.Groups['op'].Value -ceq 'disable') {
    Microsoft.PowerShell.LocalAccounts\Disable-LocalUser -SID $sid -ErrorAction Stop -WhatIf:$false -Confirm:$false
  } else {
    Microsoft.PowerShell.LocalAccounts\Remove-LocalUser -SID $sid -ErrorAction Stop -WhatIf:$false -Confirm:$false
  }
  $reply='{"protocol":"rescue-host-guest-result/1","request":'+$text.Substring(0,$text.Length-1)+',"outcome":"ok"}'+[char]10
  $bytes=[System.Text.UTF8Encoding]::new($false,$true).GetBytes($reply)
  $outputStream=[Console]::OpenStandardOutput()
  $outputStream.Write($bytes,0,$bytes.Length)
  $outputStream.Flush()
  exit 0
` + sidScriptEnd
}
