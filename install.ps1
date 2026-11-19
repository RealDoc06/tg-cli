# Installs tg, the Telegram CLI and TUI, on Windows from PowerShell:
#
#   irm https://raw.githubusercontent.com/RealDoc06/tg-cli/main/install.ps1 | iex
#
# The binary goes to %LOCALAPPDATA%\Programs\tg, which is added to the user
# PATH. tg keeps itself up to date afterwards.
$ErrorActionPreference = 'Stop'

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "tg_windows_$arch.exe"
$base = 'https://github.com/RealDoc06/tg-cli/releases/latest/download'
$dir = Join-Path $env:LOCALAPPDATA 'Programs\tg'
$target = Join-Path $dir 'tg.exe'
$download = Join-Path $dir 'tg.exe.download'

New-Item -ItemType Directory -Force -Path $dir | Out-Null
Write-Host "Downloading $asset"
Invoke-WebRequest "$base/$asset" -OutFile $download -UseBasicParsing

$sums = (Invoke-WebRequest "$base/checksums.txt" -UseBasicParsing).Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$want = ($sums -split "`n" | Where-Object { $_ -match "\s$([regex]::Escape($asset))\s*$" }) -split '\s+' | Select-Object -First 1
$got = (Get-FileHash $download -Algorithm SHA256).Hash.ToLower()
if (-not $want -or $want -ne $got) {
    Remove-Item $download
    throw "checksum mismatch for $asset"
}
Move-Item -Force $download $target

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
    $env:Path += ";$dir"
}
Write-Host "Installed $(& $target --version) to $target"
Write-Host 'Next: run "tg login", then "tg". Open a new terminal if tg is not found.'
