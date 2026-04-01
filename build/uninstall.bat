@echo off
title Tailscale Uninstall
net session >/dev/null 2>&1
if %errorlevel% neq 0 (
    echo Please right-click and Run as administrator.
    pause
    exit /b 1
)
echo.
echo  Tailscale Uninstall
echo.
taskkill /F /IM tailscale-gui.exe >/dev/null 2>&1
taskkill /F /IM tailscale-ipn.exe >/dev/null 2>&1
net stop Tailscale >/dev/null 2>&1
timeout /t 2 /nobreak >nul
set "TS=C:\Program Files\Tailscale"
if exist "%TS%\tailscaled.exe" "%TS%\tailscaled.exe" uninstall-system-daemon >/dev/null 2>&1
timeout /t 2 /nobreak >nul
sc delete Tailscale >/dev/null 2>&1
rmdir /S /Q "%TS%" >/dev/null 2>&1
del "%APPDATA%\Microsoft\Windows\Start Menu\Programs\Startup\Tailscale.lnk" >/dev/null 2>&1
del "%USERPROFILE%\Desktop\Tailscale.lnk" >/dev/null 2>&1
echo  Done.
echo.
pause
