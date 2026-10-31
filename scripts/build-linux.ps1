$ErrorActionPreference = "Stop"
$root = (Get-Item $PSScriptRoot).Parent.FullName
Set-Location $root

$version = git describe --tags --always --dirty
$commit = git rev-parse --short HEAD
$buildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

$envFile = Join-Path $root ".env.local"
if (Test-Path $envFile) {
    foreach ($line in Get-Content $envFile) {
        $trimmed = $line.Trim()
        if (-not $trimmed -or $trimmed.StartsWith("#")) { continue }
        $eq = $trimmed.IndexOf("=")
        if ($eq -lt 1) { continue }
        $key = $trimmed.Substring(0, $eq).Trim()
        $value = $trimmed.Substring($eq + 1).Trim().Trim('"')
        Set-Item -Path "env:$key" -Value $value
    }
}

$verMod = "github.com/99apps-id/termixgo/internal/version"
$oauthMod = "github.com/99apps-id/termixgo/internal/oauth"

$ldflags = "-s -w -X $verMod.Version=$version -X $verMod.Commit=$commit -X $verMod.BuildTime=$buildTime"
if ($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID) {
    $ldflags += " -X $oauthMod.AntigravityClientID=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_ID)"
}
if ($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET) {
    $ldflags += " -X $oauthMod.AntigravityClientSecret=$($env:TERMIXGO_ANTIGRAVITY_CLIENT_SECRET)"
}

$dist = Join-Path $root "dist"
if (-not (Test-Path $dist)) {
    New-Item -ItemType Directory -Path $dist | Out-Null
}

$outputPath = Join-Path $dist "termixgo-linux-amd64"
Write-Host "Compiling $outputPath (version: $version, commit: $commit)..."
$env:GOOS = "linux"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
go build -ldflags $ldflags -o $outputPath ./cmd/termixgo
$size = (Get-Item $outputPath).Length
Write-Host "Success: $outputPath ($size bytes)"
