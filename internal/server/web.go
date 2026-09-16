package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFiles 将前端资源编译进服务程序，部署时不需要额外复制静态目录。
//
//go:embed web/*
var webFiles embed.FS

func webHandler() http.Handler {
	root, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(root))
}
