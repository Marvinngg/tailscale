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

echo  [1/5] Stopping services...
taskkill /F /IM tailscale-gui.exe >nul 2>&1
taskkill /F /IM tailscale-ipn.exe >nul 2>&1
taskkill /F /IM tailscale.exe >nul 2>&1
net stop Tailscale >nul 2>&1
timeout /t 3 /nobreak >nul

echo  [2/5] Removing service...
set "TS=C:\Program Files\Tailscale"
if exist "%TS%\tailscaled.exe" "%TS%\tailscaled.exe" uninstall-system-daemon >nul 2>&1
timeout /t 2 /nobreak >nul
sc delete Tailscale >nul 2>&1

echo  [3/5] Removing files...
rmdir /S /Q "%TS%" >nul 2>&1

echo  [4/5] Removing data...
rmdir /S /Q "%ProgramData%\Tailscale" >nul 2>&1
rmdir /S /Q "%APPDATA%\Tailscale" >nul 2>&1

echo  [5/5] Removing startup entries...
reg delete "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v TailscaleVoice /f >nul 2>&1
del "%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\Tailscale.lnk" >nul 2>&1
del "%USERPROFILE%\Desktop\Tailscale.lnk" >nul 2>&1

echo.
echo  Uninstall complete.
echo.
pause
