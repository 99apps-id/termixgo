@echo off
rem Double-click entry point for the Termixgo installer.
rem Extract the whole zip first, then run this file.
powershell -NoProfile -ExecutionPolicy Bypass -File "%~dp0install.ps1"
