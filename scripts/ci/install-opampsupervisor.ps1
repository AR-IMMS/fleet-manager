$ErrorActionPreference = 'Stop'
$version = '0.160.0'
$asset = "opampsupervisor_${version}_windows_amd64.exe"
$base = "https://github.com/open-telemetry/opentelemetry-collector-releases/releases/download/cmd%2Fopampsupervisor%2Fv${version}"
$destination = Join-Path $env:RUNNER_TEMP $asset

Invoke-WebRequest -Uri "$base/$asset" -OutFile $destination
Invoke-WebRequest -Uri "$base/$asset.sha256" -OutFile "$destination.sha256"
$expected = ((Get-Content -LiteralPath "$destination.sha256" -Raw).Trim() -split '\s+')[0].TrimStart('*').ToLowerInvariant()
if ($expected -notmatch '^[0-9a-f]{64}$') {
    throw 'Invalid upstream Supervisor checksum file'
}
$actual = (Get-FileHash -LiteralPath $destination -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -ne $expected) {
    throw "Supervisor v$version checksum mismatch"
}
$buildInfo = [string]::Join("`n", (go version -m $destination 2>&1))
$buildInfoExit = $LASTEXITCODE
$moduleMarker = "mod`tgithub.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor`tv0.160.0"
if ($buildInfoExit -ne 0 -or -not $buildInfo.Contains($moduleMarker)) {
    throw "Downloaded Supervisor does not contain the pinned module v$version. Build info: $buildInfo"
}
"OPAMP_SUPERVISOR_BIN=$destination" | Out-File -FilePath $env:GITHUB_ENV -Encoding utf8 -Append
Write-Host "Verified official OpAMP Supervisor v$version SHA-256 $actual"
