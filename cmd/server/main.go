package main

import (
	"context"
	"log"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/httpapi"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	c := config.Load()
	if c.PublicURL != "" && !httpapi.ValidPublicURL(c.PublicURL) {
		log.Fatal("ATOMS_PUBLIC_URL 无效")
	}
	s, err := store.Open(filepath.Join(c.DataDir, "atoms.db"))
	if err != nil {
		log.Fatal("数据库初始化失败：", err)
	}
	defer s.Close()
	if err = s.Recover(); err != nil {
		log.Fatal("状态恢复失败：", err)
	}
	engine := agent.New(s, c, model.New(c))
	server := &http.Server{Addr: c.Addr, Handler: httpapi.New(s, engine, c), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 * 1024}
	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stopped
		engine.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("Atoms Lab listening on %s; model configured: %t; runner configured: %t", c.Addr, c.ModelError() == nil, c.RunnerURL != "")
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
