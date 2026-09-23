@echo off
echo Starting my-eino-app server...
cd /d E:\11\my-eino-app
go run ./cmd/server -addr 127.0.0.1:18181
pause
