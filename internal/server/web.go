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
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 前端资源随 Go 二进制一起发布；认证状态和交互逻辑更新后不能被
		// 浏览器的旧 app.js/index.html 缓存遮住，开发与内网部署统一不缓存。
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}
