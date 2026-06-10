@echo off
title Marvin Tailscale - Uninstall
net session >nul 2>&1
if %errorlevel% neq 0 (
    echo Please right-click and Run as administrator.
    pause
    exit /b 1
)
echo.
echo  === Marvin Tailscale Uninstall ===
echo.

echo  [1/8] Stopping services...
taskkill /F /IM tailscale-gui.exe >nul 2>&1
taskkill /F /IM tailscale-ipn.exe >nul 2>&1
taskkill /F /IM tailscale.exe >nul 2>&1
net stop Tailscale >nul 2>&1
timeout /t 3 /nobreak >nul

echo  [2/8] MSI uninstall (clean Windows Installer state)...
for %%f in ("%~dp0tailscale-setup.msi" "C:\Users\*\Desktop\*\tailscale-setup.msi") do (
    if exist "%%f" msiexec /x "%%f" /quiet /norestart >nul 2>&1
)
wmic product where "name like '%%Tailscale%%'" call uninstall /nointeractive >nul 2>&1
timeout /t 3 /nobreak >nul

echo  [3/8] Removing service...
set "TS=C:\Program Files\Tailscale"
if exist "%TS%\tailscaled.exe" "%TS%\tailscaled.exe" uninstall-system-daemon >nul 2>&1
timeout /t 2 /nobreak >nul
sc delete Tailscale >nul 2>&1

echo  [4/8] Removing files...
rmdir /S /Q "%TS%" >nul 2>&1

echo  [5/8] Removing data...
rmdir /S /Q "%ProgramData%\Tailscale" >nul 2>&1
rmdir /S /Q "%APPDATA%\Tailscale" >nul 2>&1

echo  [6/8] Removing startup entries...
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v TailscaleVoice /f >nul 2>&1
del "%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\Tailscale.lnk" >nul 2>&1
del "%USERPROFILE%\Desktop\Tailscale.lnk" >nul 2>&1

echo  [7/8] Removing 263onet CA from LocalMachine\Root...
certutil -delstore "Root" "263onet Internal Headscale CA" >nul 2>&1

echo  [8/8] Removing hosts entry...
set "HOSTSFILE=%SystemRoot%\System32\drivers\etc\hosts"
findstr /V /C:"hs.263onet.com" "%HOSTSFILE%" > "%HOSTSFILE%.tmp" 2>nul
if exist "%HOSTSFILE%.tmp" (
    move /Y "%HOSTSFILE%.tmp" "%HOSTSFILE%" >nul
)

echo.
echo  Uninstall complete.
echo.
pause
