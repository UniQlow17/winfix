# Сборка WinFix: один исполняемый файл bin\winfix.exe.
#   powershell -ExecutionPolicy Bypass -File build.ps1            собрать
#   powershell -ExecutionPolicy Bypass -File build.ps1 -Install   собрать и установить на этот компьютер
param(
    [string]$Version = '1.0.0',
    [switch]$Install
)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root
New-Item -ItemType Directory -Force bin | Out-Null

Write-Host '== Проверка ==' -ForegroundColor Cyan
& { $ErrorActionPreference = 'Continue'; go vet ./... 2>&1 | Out-Host }
if ($LASTEXITCODE -ne 0) { throw 'go vet нашёл проблемы' }

Write-Host '== Сборка ==' -ForegroundColor Cyan
go build -trimpath -ldflags "-s -w -X main.version=$Version" -o bin\winfix.exe .\cmd\winfix
if ($LASTEXITCODE -ne 0) { throw 'Ошибка сборки' }
Write-Host ("  OK  {0}  ({1:N1} МБ)" -f (Join-Path $root 'bin\winfix.exe'), ((Get-Item bin\winfix.exe).Length / 1MB))

# Удаляем ярлыки прежних отдельных утилит («WinFix - ...»), если они указывали на эту папку.
$desktop = [Environment]::GetFolderPath('Desktop')
$ws = New-Object -ComObject WScript.Shell
Get-ChildItem $desktop -Filter 'WinFix - *.lnk' -ErrorAction SilentlyContinue | ForEach-Object {
    if ($ws.CreateShortcut($_.FullName).TargetPath -like "$root\bin\*") {
        Remove-Item $_.FullName -Force
        Write-Host "  удалён старый ярлык: $($_.Name)"
    }
}

if ($Install) {
    Write-Host '== Установка ==' -ForegroundColor Cyan
    & "$root\bin\winfix.exe" install
}
Write-Host 'Готово.' -ForegroundColor Green
