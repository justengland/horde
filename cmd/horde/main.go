package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/justengland/horde/internal/config"
	"github.com/justengland/horde/internal/git"
	"github.com/justengland/horde/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if err := server.EnsureVaultRoot(cfg.Root); err != nil {
		slog.Error("vault", "err", err)
		os.Exit(1)
	}
	var gitRepo *git.Repo
	if cfg.GitEnabled {
		if err := server.CheckGitRepo(cfg.Root); err != nil {
			slog.Error("git", "err", err)
			os.Exit(1)
		}
		gitRepo = &git.Repo{Root: cfg.Root, Push: cfg.GitPush}
	}
	store, err := server.NewStore(cfg.Root, cfg.Ignore, cfg.MaxNoteBytes, gitRepo, cfg.ReadOnly)
	if err != nil {
		slog.Error("store", "err", err)
		os.Exit(1)
	}
	handler := server.NewHTTPServer(server.Deps{Store: store, Token: cfg.Token, PublicURL: cfg.PublicURL})
	slog.Info("horde listening", "addr", cfg.Addr, "root", cfg.Root, "version", server.Version, "readonly", cfg.ReadOnly, "public_url", cfg.PublicURL)
	if err := http.ListenAndServe(cfg.Addr, handler); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
