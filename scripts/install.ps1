$ErrorActionPreference = 'Stop'

try {
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
  # Ignore when the runtime manages TLS defaults.
}

$Repo = 'basecamp/basecamp-cli'
$Version = $env:BASECAMP_VERSION
$SkipSetup = $env:BASECAMP_SKIP_SETUP
$BinDir = $env:BASECAMP_BIN_DIR

function Step([string]$Message) {
  Write-Host "  -> $Message"
}

function Info([string]$Message) {
  Write-Host "  + $Message" -ForegroundColor Green
}

function Fail([string]$Message) {
  throw $Message
}

function Get-PlatformArch {
  $arch = $env:PROCESSOR_ARCHITECTURE
  if ($env:PROCESSOR_ARCHITEW6432) {
    $arch = $env:PROCESSOR_ARCHITEW6432
  }

  switch -Regex ($arch) {
    '^(AMD64|x86_64)$' { return 'amd64' }
    '^ARM64$' { return 'arm64' }
    default { Fail "Unsupported Windows architecture: $arch" }
  }
}

function Get-LatestVersion {
  Step 'Resolving latest release version...'

  # Follow the releases/latest redirect first to avoid GitHub API rate limits.
  # -MaximumRedirection 0 turns the expected 302 into a terminating error, so
  # we read Location off the caught response. Headers.Location is Uri on
  # PowerShell Core and string on Windows PowerShell 5.1, so coerce to string.
  $location = $null
  try {
    $response = Invoke-WebRequest -MaximumRedirection 0 -UseBasicParsing `
      -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
      -Uri "https://github.com/$Repo/releases/latest" -ErrorAction Stop
    $location = $response.Headers.Location
  } catch {
    if ($_.Exception.Response) {
      $location = $_.Exception.Response.Headers.Location
      if (-not $location) {
        $location = $_.Exception.Response.Headers['Location']
      }
    }
  }

  if ($location) {
    $tag = ([string]$location).TrimEnd('/').Split('/')[-1]
    $candidate = $tag.TrimStart('v')
    if ($candidate -match '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
      return $candidate
    }
  }

  # Fall back to the GitHub API if the redirect path didn't yield a semver tag.
  $release = Invoke-RestMethod -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
    -Uri "https://api.github.com/repos/$Repo/releases/latest"
  if (-not $release.tag_name) {
    Fail 'Could not determine latest release version from GitHub.'
  }

  return $release.tag_name.TrimStart('v')
}

function Download-File([string]$Url, [string]$Destination) {
  # -UseBasicParsing avoids initializing IE's MSHTML parser on Windows
  # PowerShell 5.1 — required on Server Core and locked-down installs.
  # No-op on PowerShell 6+, where basic parsing is the only mode.
  Invoke-WebRequest -UseBasicParsing -ErrorAction Stop `
    -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } `
    -Uri $Url -OutFile $Destination
}

function Verify-Checksum([string]$ChecksumsPath, [string]$ArchivePath, [string]$ArchiveName) {
  $expected = $null
  foreach ($line in Get-Content $ChecksumsPath) {
    if ($line -match '^(?<hash>[0-9a-fA-F]{64})\s+\*?(?<name>.+)$') {
      if ($Matches.name -eq $ArchiveName) {
        $expected = $Matches.hash.ToLowerInvariant()
        break
      }
    }
  }

  if (-not $expected) {
    Fail "Could not find checksum entry for $ArchiveName"
  }

  $actual = (Get-FileHash -Algorithm SHA256 -Path $ArchivePath).Hash.ToLowerInvariant()
  if ($actual -ne $expected) {
    Fail "Checksum verification failed for $ArchiveName"
  }

  Info 'Checksum verified'
}

function Verify-CosignSignature([string]$Version, [string]$BaseUrl, [string]$TmpDir) {
  if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
    return
  }

  Step 'Verifying cosign signature...'

  $bundlePath = Join-Path $TmpDir 'checksums.txt.bundle'
  $checksumsPath = Join-Path $TmpDir 'checksums.txt'
  Download-File -Url "$BaseUrl/checksums.txt.bundle" -Destination $bundlePath

  # Native exits don't trigger ErrorActionPreference=Stop on Windows PowerShell 5.1,
  # so check $LASTEXITCODE explicitly — otherwise a verify failure would false-green.
  & cosign verify-blob `
    --bundle $bundlePath `
    --certificate-identity "https://github.com/basecamp/basecamp-cli/.github/workflows/release.yml@refs/tags/v$Version" `
    --certificate-oidc-issuer 'https://token.actions.githubusercontent.com' `
    $checksumsPath
  if ($LASTEXITCODE -ne 0) {
    Fail 'Cosign signature verification failed'
  }

  Info 'Signature verified'
}

function Get-PathEntries {
  param([string]$PathValue)

  if (-not $PathValue) {
    return @()
  }

  return $PathValue -split ';' | Where-Object { $_ }
}

function Normalize-PathEntry([string]$PathValue) {
  if (-not $PathValue) {
    return ''
  }

  return $PathValue.Trim().TrimEnd('\\')
}

function Get-DefaultBinDir {
  $currentPathEntries = Get-PathEntries $env:Path
  $userPathEntries = Get-PathEntries ([Environment]::GetEnvironmentVariable('Path', 'User'))
  $allEntries = @($currentPathEntries + $userPathEntries) | ForEach-Object { Normalize-PathEntry $_ }

  $homeBin = Normalize-PathEntry (Join-Path $HOME 'bin')
  $homeLocalBin = Normalize-PathEntry (Join-Path $HOME '.local\bin')

  if ($allEntries -contains $homeBin) {
    return $homeBin
  }

  if ($allEntries -contains $homeLocalBin) {
    return $homeLocalBin
  }

  return $homeBin
}

function Ensure-UserPath([string]$Dir) {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $segments = Get-PathEntries $userPath

  $normalizedSegments = $segments | ForEach-Object { Normalize-PathEntry $_ }
  $normalizedDir = Normalize-PathEntry $Dir
  if ($normalizedSegments -contains $normalizedDir) {
    return
  }

  $newPath = if ($userPath) { "$Dir;$userPath" } else { $Dir }
  [Environment]::SetEnvironmentVariable('Path', $newPath, 'User')
  $env:Path = "$Dir;$env:Path"
  Info "Added $Dir to your user PATH"
}

function Test-InteractiveSession {
  if ($Host.Name -ne 'ConsoleHost' -and $Host.Name -ne 'Visual Studio Code Host') {
    return $false
  }

  try {
    return -not [Console]::IsInputRedirected -and -not [Console]::IsOutputRedirected
  } catch {
    return $false
  }
}

function Main {
  $arch = Get-PlatformArch
  if (-not $BinDir) {
    $script:BinDir = Get-DefaultBinDir
  }

  $resolvedVersion = if ($Version) { $Version } else { Get-LatestVersion }

  if ($resolvedVersion -notmatch '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
    Fail "Invalid version '$resolvedVersion'. Expected semver format like 1.2.3 or 1.2.3-rc.1."
  }

  $archiveName = "basecamp_${resolvedVersion}_windows_${arch}.zip"
  $baseUrl = "https://github.com/$Repo/releases/download/v$resolvedVersion"

  Step "Downloading basecamp v$resolvedVersion for windows_$arch..."
  $tmpDir = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
  New-Item -ItemType Directory -Path $tmpDir | Out-Null

  try {
    $archivePath = Join-Path $tmpDir $archiveName
    $checksumsPath = Join-Path $tmpDir 'checksums.txt'
    $extractDir = Join-Path $tmpDir 'extract'

    Download-File -Url "$baseUrl/$archiveName" -Destination $archivePath

    Step 'Verifying checksums...'
    Download-File -Url "$baseUrl/checksums.txt" -Destination $checksumsPath
    Verify-Checksum -ChecksumsPath $checksumsPath -ArchivePath $archivePath -ArchiveName $archiveName

    Verify-CosignSignature -Version $resolvedVersion -BaseUrl $baseUrl -TmpDir $tmpDir

    Step 'Extracting...'
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

    $binaryPath = Join-Path $extractDir 'basecamp.exe'
    if (-not (Test-Path $binaryPath)) {
      Fail 'basecamp.exe not found in archive'
    }

    $installedBinary = Join-Path $BinDir 'basecamp.exe'

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    # Windows holds an exclusive lock on running PE files; -Force doesn't help.
    # Generic catch — typed catches miss ActionPreferenceStopException wrapping.
    try {
      Copy-Item -Force $binaryPath $installedBinary -ErrorAction Stop
    } catch {
      Fail "Failed to install basecamp.exe. If it is in use, close any running 'basecamp' processes and re-run the installer. (Original error: $($_.Exception.Message))"
    }
    Ensure-UserPath -Dir $BinDir
    Info "Installed basecamp to $installedBinary"

    $installedVersion = & $installedBinary --version
    Info "$installedVersion installed"

    $isInteractive = Test-InteractiveSession

    Write-Host ''
    if ($SkipSetup -eq '1') {
      Step 'Skipping setup wizard (BASECAMP_SKIP_SETUP=1)'
      Write-Host ''
      Write-Host '  Next steps:'
      Write-Host '    basecamp auth login        Authenticate with Basecamp'
      Write-Host '    basecamp setup             Run interactive setup wizard'
      Write-Host ''
    } elseif ($isInteractive) {
      & $installedBinary setup
      Write-Host ''
      Write-Host '  Next steps:'
      Write-Host '    basecamp auth login        Authenticate with Basecamp'
      Write-Host ''
    } else {
      Info 'Skipping interactive setup because PowerShell is running non-interactively.'
      Write-Host ''
      Write-Host '  Installed executable:'
      Write-Host "    $installedBinary"
      Write-Host ''
      Write-Host '  In this session, use the installed executable path directly for follow-up actions like starting login.'
      Write-Host ''
    }
  }
  finally {
    if (Test-Path $tmpDir) {
      Remove-Item -Recurse -Force $tmpDir
    }
  }
}

Main
