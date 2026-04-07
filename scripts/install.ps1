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
  $release = Invoke-RestMethod -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } -Uri "https://api.github.com/repos/$Repo/releases/latest"
  if (-not $release.tag_name) {
    Fail 'Could not determine latest release version from GitHub.'
  }

  return $release.tag_name.TrimStart('v')
}

function Download-File([string]$Url, [string]$Destination) {
  Invoke-WebRequest -Headers @{ 'User-Agent' = 'basecamp-cli-installer' } -Uri $Url -OutFile $Destination
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

  $newPath = if ($userPath) { "$userPath;$Dir" } else { $Dir }
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

    Step 'Extracting...'
    Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force

    $binaryPath = Join-Path $extractDir 'basecamp.exe'
    if (-not (Test-Path $binaryPath)) {
      Fail 'basecamp.exe not found in archive'
    }

    $installedBinary = Join-Path $BinDir 'basecamp.exe'

    New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
    Copy-Item -Force $binaryPath $installedBinary
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
