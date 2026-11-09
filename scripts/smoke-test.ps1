param(
    [string]$Pipe = '\\.\pipe\wslc_engine',
    [string]$Image = 'nginx:alpine'
)

$ErrorActionPreference = 'Stop'
$maximumResponseBytes = 64MB
$pipePrefix = '\\.\pipe\'
if ($Pipe.StartsWith('npipe:////./pipe/', [StringComparison]::OrdinalIgnoreCase)) {
    $pipeName = $Pipe.Substring('npipe:////./pipe/'.Length)
} elseif ($Pipe.StartsWith($pipePrefix, [StringComparison]::OrdinalIgnoreCase)) {
    $pipeName = $Pipe.Substring($pipePrefix.Length)
} else {
    $pipeName = $Pipe
}

function Read-PipeLine {
    param([System.IO.Stream]$Stream)

    $lineBytes = [System.Collections.Generic.List[byte]]::new()
    while ($true) {
        $value = $Stream.ReadByte()
        if ($value -lt 0) {
            if ($lineBytes.Count -eq 0) { return $null }
            throw 'The named pipe closed in the middle of an HTTP line.'
        }
        if ($value -eq 10) { break }
        if ($value -ne 13) { $lineBytes.Add([byte]$value) }
        if ($lineBytes.Count -gt 16KB) { throw 'The bridge returned an overlong HTTP line.' }
    }
    return [System.Text.Encoding]::ASCII.GetString($lineBytes.ToArray())
}

function Read-PipeExact {
    param(
        [System.IO.Stream]$Stream,
        [int]$Count
    )

    if ($Count -lt 0 -or $Count -gt $script:maximumResponseBytes) {
        throw "Invalid or oversized HTTP response body length: $Count"
    }
    $buffer = [byte[]]::new($Count)
    $offset = 0
    while ($offset -lt $Count) {
        $read = $Stream.Read($buffer, $offset, $Count - $offset)
        if ($read -le 0) { throw 'The named pipe closed before the HTTP response body was complete.' }
        $offset += $read
    }
    return ,$buffer
}

function Read-PipeToEnd {
    param([System.IO.Stream]$Stream)

    $body = [System.IO.MemoryStream]::new()
    $buffer = [byte[]]::new(8192)
    while ($true) {
        try {
            $read = $Stream.Read($buffer, 0, $buffer.Length)
        } catch [System.IO.IOException] {
            break
        }
        if ($read -eq 0) { break }
        if ($body.Length + $read -gt $script:maximumResponseBytes) {
            throw 'The bridge response exceeded the 64 MiB smoke-test limit.'
        }
        $body.Write($buffer, 0, $read)
    }
    $result = $body.ToArray()
    $body.Dispose()
    return ,$result
}

function Read-PipeChunkedBody {
    param([System.IO.Stream]$Stream)

    $body = [System.IO.MemoryStream]::new()
    while ($true) {
        $sizeLine = Read-PipeLine $Stream
        if ($null -eq $sizeLine) { throw 'The named pipe closed before the chunked response completed.' }
        $sizeToken = ($sizeLine -split ';', 2)[0].Trim()
        $size = [Convert]::ToInt64($sizeToken, 16)
        if ($size -eq 0) {
            while ($true) {
                $trailer = Read-PipeLine $Stream
                if ([string]::IsNullOrEmpty($trailer)) { break }
            }
            break
        }
        if ($body.Length + $size -gt $script:maximumResponseBytes) {
            throw 'The bridge response exceeded the 64 MiB smoke-test limit.'
        }
        $chunk = Read-PipeExact $Stream ([int]$size)
        $body.Write($chunk, 0, $chunk.Length)
        $terminator = Read-PipeLine $Stream
        if ($terminator -ne '') { throw 'Malformed HTTP chunk terminator from the bridge.' }
    }
    $result = $body.ToArray()
    $body.Dispose()
    return ,$result
}

function Read-HttpResponse {
    param([System.IO.Stream]$Stream)

    $statusLine = Read-PipeLine $Stream
    if ($null -eq $statusLine -or $statusLine -notmatch '^HTTP/1\.[01]\s+(\d{3})') {
        throw "Invalid HTTP status line from bridge: $statusLine"
    }
    $statusCode = [int]$Matches[1]
    $headers = @{}
    while ($true) {
        $line = Read-PipeLine $Stream
        if ($null -eq $line) { throw 'The named pipe closed in the middle of HTTP response headers.' }
        if ($line -eq '') { break }
        $separator = $line.IndexOf(':')
        if ($separator -le 0) { throw "Malformed HTTP response header: $line" }
        $headers[$line.Substring(0, $separator).Trim()] = $line.Substring($separator + 1).Trim()
    }

    if ($statusCode -eq 101) {
        $body = Read-PipeToEnd $Stream
    } elseif ($headers.ContainsKey('Transfer-Encoding') -and $headers['Transfer-Encoding'] -match 'chunked') {
        $body = Read-PipeChunkedBody $Stream
    } elseif ($headers.ContainsKey('Content-Length')) {
        $body = Read-PipeExact $Stream ([int]$headers['Content-Length'])
    } else {
        $body = Read-PipeToEnd $Stream
    }
    return [pscustomobject]@{ StatusCode = $statusCode; Headers = $headers; Body = $body }
}

function Invoke-BridgeApi {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Path,
        [object]$RequestBody,
        [switch]$Upgrade
    )

    $bodyBytes = [byte[]]::new(0)
    if ($null -ne $RequestBody) {
        $json = ConvertTo-Json -InputObject $RequestBody -Depth 50 -Compress
        $bodyBytes = [System.Text.UTF8Encoding]::new($false).GetBytes($json)
    }

    $connectionHeader = if ($Upgrade) { "Connection: Upgrade`r`nUpgrade: tcp`r`n" } else { "Connection: close`r`n" }
    $requestHeader = "$Method $Path HTTP/1.1`r`nHost: docker`r`n${connectionHeader}Content-Length: $($bodyBytes.Length)`r`n"
    if ($null -ne $RequestBody) { $requestHeader += "Content-Type: application/json`r`n" }
    $requestHeader += "`r`n"
    $headerBytes = [System.Text.Encoding]::ASCII.GetBytes($requestHeader)
    $requestBytes = [byte[]]::new($headerBytes.Length + $bodyBytes.Length)
    [Array]::Copy($headerBytes, 0, $requestBytes, 0, $headerBytes.Length)
    if ($bodyBytes.Length -gt 0) { [Array]::Copy($bodyBytes, 0, $requestBytes, $headerBytes.Length, $bodyBytes.Length) }

    $pipe = [System.IO.Pipes.NamedPipeClientStream]::new('.', $script:pipeName, [System.IO.Pipes.PipeDirection]::InOut)
    try {
        $pipe.ReadTimeout = 180000
        $pipe.WriteTimeout = 30000
        $pipe.Connect(5000)
        $pipe.Write($requestBytes, 0, $requestBytes.Length)
        $pipe.Flush()
        return Read-HttpResponse $pipe
    } finally {
        $pipe.Dispose()
    }
}

function Get-JsonResponse {
    param([Parameter(Mandatory = $true)]$Response)
    $text = [System.Text.Encoding]::UTF8.GetString($Response.Body)
    if ($Response.StatusCode -lt 200 -or $Response.StatusCode -ge 300) {
        throw "Docker API returned HTTP $($Response.StatusCode): $text"
    }
    if ([string]::IsNullOrWhiteSpace($text)) { return $null }
    return ConvertFrom-Json -InputObject $text -ErrorAction Stop
}

function Get-ExecOutput {
    param([byte[]]$Frames)

    $stdout = [System.Text.StringBuilder]::new()
    $stderr = [System.Text.StringBuilder]::new()
    $offset = 0
    while ($offset -lt $Frames.Length) {
        if ($Frames.Length - $offset -lt 8) { throw 'Truncated Docker exec stream header.' }
        $streamType = $Frames[$offset]
        $length = ($Frames[$offset + 4] -shl 24) -bor ($Frames[$offset + 5] -shl 16) -bor ($Frames[$offset + 6] -shl 8) -bor $Frames[$offset + 7]
        $offset += 8
        if ($length -lt 0 -or $length -gt ($Frames.Length - $offset)) { throw 'Invalid Docker exec stream frame length.' }
        $text = [System.Text.UTF8Encoding]::new($false).GetString($Frames, $offset, $length)
        if ($streamType -eq 1) { [void]$stdout.Append($text) }
        elseif ($streamType -eq 2) { [void]$stderr.Append($text) }
        else { throw "Unexpected Docker exec stream type: $streamType" }
        $offset += $length
    }
    return [pscustomobject]@{ Stdout = $stdout.ToString(); Stderr = $stderr.ToString() }
}

$containerName = "wslc-bridge-smoke-$PID"
$networkName = "wslc-bridge-smoke-net-$PID"
$containerId = $null
$containerCreateAttempted = $false
$networkId = $null
$networkCreateAttempted = $false
$workDirectory = Join-Path $env:TEMP "wslc-bridge-smoke-$PID"

try {
    New-Item -ItemType Directory -Force -Path $workDirectory | Out-Null
    Set-Content -Path (Join-Path $workDirectory 'marker.txt') -Value 'WSLC Windows bind mount is working' -NoNewline

    Write-Host 'Checking guest Docker health, version, containers, and images over the Windows named pipe...'
    $pingResponse = Invoke-BridgeApi -Method 'GET' -Path '/_ping'
    if ($pingResponse.StatusCode -ne 200 -or [System.Text.Encoding]::UTF8.GetString($pingResponse.Body) -ne 'OK') {
        throw 'Bridge health check did not return HTTP 200 / OK.'
    }
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path '/version')
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path '/containers/json?all=1')
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path '/images/json')

    $imageParts = $Image.Split(':')
    $tag = if ($imageParts.Length -gt 1) { $imageParts[-1] } else { 'latest' }
    $repository = if ($imageParts.Length -gt 1) { $Image.Substring(0, $Image.Length - $tag.Length - 1) } else { $Image }
    $pullPath = '/images/create?fromImage=' + [Uri]::EscapeDataString($repository) + '&tag=' + [Uri]::EscapeDataString($tag)
    Write-Host "Pulling public image $Image through WSLC..."
    $pullResponse = Invoke-BridgeApi -Method 'POST' -Path $pullPath
    if ($pullResponse.StatusCode -ne 200) { throw "Image pull returned HTTP $($pullResponse.StatusCode)." }
    foreach ($progressLine in [System.Text.Encoding]::UTF8.GetString($pullResponse.Body).Split("`n")) {
        if ([string]::IsNullOrWhiteSpace($progressLine)) { continue }
        $progress = ConvertFrom-Json -InputObject $progressLine -ErrorAction Stop
        if ($progress.error) { throw "Image pull failed: $($progress.error)" }
    }
    $imageInspectPath = '/images/' + [Uri]::EscapeDataString($Image) + '/json'
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path $imageInspectPath)

    Write-Host 'Creating a stopped container with an ephemeral Windows port and read-only Windows bind...'
    $createBody = @{
        Image = $Image
        ExposedPorts = @{ '80/tcp' = @{} }
        HostConfig = @{
            Binds = @("${workDirectory}:/wslc-smoke:ro")
            PortBindings = @{ '80/tcp' = @(@{ HostIp = '127.0.0.1'; HostPort = '0' }) }
        }
    }
    $createPath = '/containers/create?name=' + [Uri]::EscapeDataString($containerName)
    $containerCreateAttempted = $true
    $createResponse = Invoke-BridgeApi -Method 'POST' -Path $createPath -RequestBody $createBody
    $created = Get-JsonResponse $createResponse
    $containerId = [string]$created.Id
    if (-not $containerId) { throw 'Container create returned an empty ID.' }

    $beforeStart = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path "/containers/$containerId/json")
    if ($beforeStart.State.Status -ne 'created') { throw "Create unexpectedly started the container: $($beforeStart.State.Status)" }

    Write-Host 'Starting the container and checking WSLC Windows port relay...'
    $startResponse = Invoke-BridgeApi -Method 'POST' -Path "/containers/$containerId/start"
    if ($startResponse.StatusCode -ne 204) { throw "Container start returned HTTP $($startResponse.StatusCode)." }
    $summaryResponse = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path '/containers/json?all=1')
    $summary = $summaryResponse | Where-Object { $_.Id -eq $containerId } | Select-Object -First 1
    $summaryPort = $summary.Ports | Where-Object { $_.PrivatePort -eq 80 -and $_.Type -eq 'tcp' -and $_.PublicPort -gt 0 } | Select-Object -First 1
    if (-not $summaryPort) { throw 'Container list did not include the native Windows port mapping.' }
    $inspect = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path "/containers/$containerId/json")
    $inspectPort = $inspect.NetworkSettings.Ports.'80/tcp' | Select-Object -First 1
    if ($inspectPort.HostPort -ne [string]$summaryPort.PublicPort) {
        throw "List/inspect Windows ports differ ($($summaryPort.PublicPort) versus $($inspectPort.HostPort))."
    }
    $webResponse = Invoke-WebRequest -Uri "http://127.0.0.1:$($summaryPort.PublicPort)" -UseBasicParsing -TimeoutSec 15
    if ($webResponse.StatusCode -ne 200) { throw "Published port returned HTTP $($webResponse.StatusCode)." }

    Write-Host 'Checking logs, stats, and non-interactive exec over the named pipe...'
    $null = Invoke-BridgeApi -Method 'GET' -Path "/containers/$containerId/logs?stdout=1&stderr=1&tail=10"
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path "/containers/$containerId/stats?stream=false")
    $execCreate = Get-JsonResponse (Invoke-BridgeApi -Method 'POST' -Path "/containers/$containerId/exec" -RequestBody @{
        AttachStdout = $true; AttachStderr = $true; AttachStdin = $false; Tty = $false; Cmd = @('cat', '/wslc-smoke/marker.txt')
    })
    $execStart = Invoke-BridgeApi -Method 'POST' -Path "/exec/$($execCreate.Id)/start" -Upgrade -RequestBody @{ Detach = $false; Tty = $false }
    if ($execStart.StatusCode -ne 101) { throw "Exec start returned HTTP $($execStart.StatusCode), expected 101." }
    $mountOutput = Get-ExecOutput $execStart.Body
    if ($mountOutput.Stdout.Trim() -ne 'WSLC Windows bind mount is working') { throw "Unexpected bind mount output: $($mountOutput.Stdout)" }

    $failureExec = Get-JsonResponse (Invoke-BridgeApi -Method 'POST' -Path "/containers/$containerId/exec" -RequestBody @{
        AttachStdout = $true; AttachStderr = $true; AttachStdin = $false; Tty = $false; Cmd = @('sh', '-c', 'echo bridge-stderr 1>&2; exit 42')
    })
    $failureStart = Invoke-BridgeApi -Method 'POST' -Path "/exec/$($failureExec.Id)/start" -Upgrade -RequestBody @{ Detach = $false; Tty = $false }
    $failureOutput = Get-ExecOutput $failureStart.Body
    $failureStatus = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path "/exec/$($failureExec.Id)/json")
    if ($failureStatus.ExitCode -ne 42 -or $failureOutput.Stderr -notmatch 'bridge-stderr') {
        throw "Non-interactive exec did not preserve stderr/exit code 42 (exit $($failureStatus.ExitCode))."
    }

    Write-Host 'Checking network create, inspect, list, and remove...'
    $networkCreateAttempted = $true
    $networkResponse = Get-JsonResponse (Invoke-BridgeApi -Method 'POST' -Path '/networks/create' -RequestBody @{ Name = $networkName })
    $networkId = [string]$networkResponse.Id
    if (-not $networkId) { throw 'Network create returned an empty ID.' }
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path "/networks/$networkId")
    $null = Get-JsonResponse (Invoke-BridgeApi -Method 'GET' -Path '/networks')
    $removeNetwork = Invoke-BridgeApi -Method 'DELETE' -Path "/networks/$networkId"
    if ($removeNetwork.StatusCode -ne 204) { throw "Network remove returned HTTP $($removeNetwork.StatusCode)." }
    $networkCreateAttempted = $false

    Write-Host 'WSLC bridge smoke test passed.'
} finally {
    if ($containerCreateAttempted) {
        $containerResource = if ($containerId) { $containerId } else { [Uri]::EscapeDataString($containerName) }
        try { $null = Invoke-BridgeApi -Method 'DELETE' -Path "/containers/$containerResource?force=true" } catch { }
    }
    if ($networkCreateAttempted) {
        $networkResource = if ($networkId) { $networkId } else { [Uri]::EscapeDataString($networkName) }
        try { $null = Invoke-BridgeApi -Method 'DELETE' -Path "/networks/$networkResource?force=true" } catch { }
    }
    Remove-Item -Recurse -Force $workDirectory -ErrorAction SilentlyContinue
}
