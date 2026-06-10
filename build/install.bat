@echo off
setlocal EnableDelayedExpansion
title Marvin Tailscale - Install

REM ===== Antigravity Tailscale Installer (Windows) =====
REM Mirrors macOS install.sh: trust 263onet CA, write hosts, install MSI +
REM fork binaries, then `tailscale up` with auth-key.
REM
REM Usage (run as admin):
REM   install.bat --key=hskey-auth-xxxx
REM
REM Options:
REM   --key=KEY           Headscale pre-auth key (required for first install)
REM   --server=URL        Headscale URL (default https://hs.263onet.com:8443)
REM   --exit-node=IP      Exit node IP (default 100.96.0.1)
REM   --no-exit-node      Don't set exit node
REM   --no-hosts-write    Skip C:\Windows\System32\drivers\etc\hosts write
REM ======================================================

set "HEADSCALE_URL=https://hs.263onet.com:8443"
set "HEADSCALE_HOST=hs.263onet.com"
set "HEADSCALE_IP=114.141.178.114"
set "EXIT_NODE=100.96.0.1"
set "AUTH_KEY="
set "WRITE_HOSTS=1"

REM ----- parse args -----
:parse
if "%~1"=="" goto parsed
set "ARG=%~1"
if /I "!ARG:~0,6!"=="--key=" set "AUTH_KEY=!ARG:~6!" & shift & goto parse
if /I "!ARG:~0,9!"=="--server=" set "HEADSCALE_URL=!ARG:~9!" & shift & goto parse
if /I "!ARG:~0,12!"=="--exit-node=" set "EXIT_NODE=!ARG:~12!" & shift & goto parse
if /I "!ARG!"=="--no-exit-node" set "EXIT_NODE=" & shift & goto parse
if /I "!ARG!"=="--no-hosts-write" set "WRITE_HOSTS=0" & shift & goto parse
echo Unknown option: !ARG!
exit /b 1
:parsed

REM ----- admin check + auto-elevate -----
net session >nul 2>&1
if errorlevel 1 (
    echo Requesting administrator privileges...
    powershell -NoProfile -Command "Start-Process -Verb RunAs -FilePath '%~f0' -ArgumentList '%*'"
    exit /b
)

set "SRCDIR=%~dp0"
if "%SRCDIR:~-1%"=="\" set "SRCDIR=%SRCDIR:~0,-1%"

echo.
echo  === Marvin Tailscale Install ===
echo.
echo   Server:    %HEADSCALE_URL%
echo   Exit:     %EXIT_NODE%
echo.

REM ----- detect existing install (update mode) -----
set "EXISTING=0"
if exist "%ProgramData%\Tailscale\tailscaled.state" set "EXISTING=1"
if "%EXISTING%"=="0" if "%AUTH_KEY%"=="" (
    echo  ERROR: --key is required for first install ^(no tailscaled.state found^).
    echo  Usage: install.bat --key=hskey-auth-xxxx
    pause
    exit /b 1
)

REM ----- verify bundled files -----
for %%F in (ca.crt setup.exe tailscale.exe tailscaled.exe) do (
    if not exist "%SRCDIR%\%%F" (
        echo  ERROR: missing %%F next to install.bat
        echo  Did you extract the full zip?
        pause
        exit /b 1
    )
)

REM ----- [1/5] trust 263onet CA -----
echo  [1/5] Installing 263onet CA certificate to LocalMachine\Root...
certutil -addstore -f "Root" "%SRCDIR%\ca.crt" >nul
if errorlevel 1 (
    echo    WARN: certutil failed. Check ca.crt and try again as admin.
) else (
    echo    CA trusted.
)

REM ----- [2/5] /etc/hosts equivalent -----
if "%WRITE_HOSTS%"=="1" (
    echo  [2/5] Writing hosts entry...
    set "HOSTSFILE=%SystemRoot%\System32\drivers\etc\hosts"
    findstr /C:"%HEADSCALE_HOST%" "!HOSTSFILE!" >nul 2>&1
    if errorlevel 1 (
        echo.>>"!HOSTSFILE!"
        echo %HEADSCALE_IP% %HEADSCALE_HOST%>>"!HOSTSFILE!"
        echo    Added: %HEADSCALE_IP% %HEADSCALE_HOST%
    ) else (
        echo    Already present.
    )
) else (
    echo  [2/5] Skipping hosts write ^(--no-hosts-write^).
)

REM ----- [3/5] run setup.exe (MSI + binary swap) -----
echo  [3/5] Running setup.exe ^(MSI + fork binary swap^)...
"%SRCDIR%\setup.exe"
if errorlevel 1 (
    echo    ERROR: setup.exe failed. See messages above.
    pause
    exit /b 1
)

REM ----- [4/5] wait for service + tailscale up -----
echo  [4/5] Waiting for Tailscale service...
set "RETRY=0"
:waitsvc
sc query Tailscale | findstr /C:"RUNNING" >nul
if errorlevel 1 (
    set /a RETRY+=1
    if !RETRY! GEQ 30 (
        echo    ERROR: Tailscale service not running after 30s.
        sc query Tailscale
        pause
        exit /b 1
    )
    timeout /t 1 /nobreak >nul
    goto waitsvc
)
echo    Service running.

set "EXITFLAGS="
if not "%EXIT_NODE%"=="" set "EXITFLAGS=--exit-node=%EXIT_NODE% --exit-node-allow-lan-access"

set "TS=C:\Program Files\Tailscale\tailscale.exe"
if not "%AUTH_KEY%"=="" (
    echo    Mode: register with auth-key
    "%TS%" up --login-server=%HEADSCALE_URL% --auth-key=%AUTH_KEY% --accept-routes --accept-dns --reset %EXITFLAGS%
    if errorlevel 1 (
        echo    ERROR: tailscale up failed.
        pause
        exit /b 1
    )
) else (
    echo    Mode: update ^(reuse existing identity, no key needed^)
)

REM ----- [5/5] status -----
echo.
echo  [5/5] Status:
"%TS%" status
echo.
echo  === Installed ===
echo    Daemon:  C:\Program Files\Tailscale\tailscaled.exe
echo    CLI:     C:\Program Files\Tailscale\tailscale.exe
echo    CA:      LocalMachine\Root ^(263onet Internal Headscale CA^)
if "%WRITE_HOSTS%"=="1" echo    Hosts:   %HEADSCALE_IP% %HEADSCALE_HOST%
echo.
pause
endlocal
