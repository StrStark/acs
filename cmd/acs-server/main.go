// Command acs-server runs the storage server: web panel, management API and
// the S3-compatible API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"acs/internal/alerts"
	"acs/internal/api"
	"acs/internal/audit"
	"acs/internal/auth"
	"acs/internal/config"
	"acs/internal/db"
	"acs/internal/metrics"
	"acs/internal/object"
	"acs/internal/replica"
	"acs/internal/s3"
	"acs/internal/secret"
	"acs/internal/settings"
	"acs/internal/share"
	"acs/internal/storage"
	"acs/internal/version"
	"acs/internal/web"
	"acs/internal/webhook"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck(cfg))
	}

	setupLogger(cfg.LogLevel)
	if err := run(cfg); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	// A restore staged from the setup screen is installed before anything opens.
	if applied, err := replica.ApplyStaged(cfg.DataDir); err != nil {
		return fmt.Errorf("apply restore: %w", err)
	} else if applied {
		slog.Info("restored installation from storage")
	}
	restored := replica.TakeRestoredMarker(cfg.DataDir)

	database, err := db.Open(ctx, filepath.Join(cfg.DataDir, "acs.db"))
	if err != nil {
		return err
	}
	defer database.Close()

	box, err := secret.LoadOrCreate(filepath.Join(cfg.DataDir, "master.key"), cfg.MasterKey)
	if err != nil {
		return err
	}
	authSvc, err := auth.NewService(database, box, cfg.SessionTTL, cfg.SetupToken)
	if err != nil {
		return err
	}
	objects, err := object.Open(filepath.Join(cfg.DataDir, "meta"), filepath.Join(cfg.DataDir, "objects"))
	if err != nil {
		return fmt.Errorf("open object store: %w", err)
	}
	defer objects.Close()

	backends, err := storage.Open(ctx, database, box, cfg.DataDir)
	if err != nil {
		return fmt.Errorf("open storage backends: %w", err)
	}
	objects.SetBackends(backends)
	backends.Relocate(ctx, cfg.DrivesRoot)

	settingsStore, err := settings.Open(ctx, database)
	if err != nil {
		return err
	}
	hooks, err := webhook.New(ctx, database, box)
	if err != nil {
		return err
	}
	shares := share.New(database, box.DeriveKey("share-unlock"))
	auditLog := audit.New(database)
	reg := metrics.New()
	alertSvc := alerts.New(database, hooks)

	// ---- metadata replication to every storage backend ----
	installation, err := replica.EnsureInstallation(ctx, database)
	if err != nil {
		return err
	}
	rep := replica.New(replica.Options{
		Objects: objects, DB: database, DataDir: cfg.DataDir, Installation: installation,
		SiteName: func() string { return settingsStore.Get().SiteName },
		Keyring:  func() []byte { return replica.LoadKeyring(context.Background(), database) },
		OnError: func(id, name string, err error) {
			fp := alerts.KindReplicaFailed + ":" + id
			if err == nil {
				alertSvc.ResolveFingerprint(context.Background(), fp)
				return
			}
			alertSvc.Raise(context.Background(), fp, alerts.Alert{Kind: alerts.KindReplicaFailed, Severity: "warning",
				Title: "Metadata copy on " + name + " is failing", Detail: err.Error(), Backend: id})
		},
	})
	objects.SetJournal(rep.Add)
	syncTargets := func() {
		specs := []replica.TargetSpec{{ID: "", Name: "Built-in disk", Kind: storage.TypeLocal,
			Area: replica.NewLocalArea(filepath.Join(cfg.DataDir, "objects", ".acs")), Interval: time.Second}}
		for _, t := range backends.Targets() {
			interval := time.Second
			if t.Type == storage.TypeS3 {
				interval = 5 * time.Second
			}
			specs = append(specs, replica.TargetSpec{ID: t.ID, Name: t.Name, Kind: t.Type, Area: t.Area, Interval: interval})
		}
		rep.Sync(context.Background(), specs)
		for id, st := range rep.Status() {
			if st.Foreign != "" {
				alertSvc.Raise(context.Background(), alerts.KindForeignStorage+":"+id, alerts.Alert{
					Kind: alerts.KindForeignStorage, Severity: "warning", Backend: id,
					Title:  "Storage holds data from another installation",
					Detail: "It belongs to \"" + st.ForeignSite + "\". Nothing is written to it until you import it under Storage.",
				})
			}
		}
	}
	syncTargets()
	rep.ForgetCleanState()
	backends.OnChange(syncTargets)
	backendName := func(id string) string {
		if id == "" {
			return "Built-in disk"
		}
		if b, err := backends.Get(id); err == nil {
			return b.Name
		}
		return id
	}
	backends.OnStatus(func(b storage.Backend) {
		fp := alerts.KindBackendOffline + ":" + b.ID
		if b.Status.Online {
			alertSvc.ResolveFingerprint(context.Background(), fp)
			return
		}
		alertSvc.Raise(context.Background(), fp, alerts.Alert{Kind: alerts.KindBackendOffline, Severity: "critical",
			Title: b.Name + " is offline", Detail: b.Status.Error, Backend: b.ID})
	})
	checker := alerts.NewChecker(objects, alertSvc, backendName)

	// Fan object events out to webhooks and clean up shares of deleted buckets.
	objects.Subscribe(func(e object.Event) {
		hooks.Publish(webhook.Event{Type: e.Type, Bucket: e.Bucket, Key: e.Key, Data: e})
		if e.Type == object.EventBucketDeleted {
			if err := shares.DeleteForBucket(context.Background(), e.Bucket); err != nil {
				slog.Warn("delete shares of bucket", "bucket", e.Bucket, "err", err)
			}
		}
	})

	objects.Run(ctx)
	hooks.Run(ctx)
	go backends.Run(ctx)
	go checker.Run(ctx)
	go maintenance(ctx, authSvc, auditLog, cfg.AuditRetention)
	go func() {
		// Drives plugged in later are found and re-attached automatically.
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				backends.Relocate(ctx, cfg.DrivesRoot)
			}
		}
	}()
	repCtx, repStop := context.WithCancel(context.Background())
	repDone := make(chan struct{})
	go func() { rep.Run(repCtx); close(repDone) }()
	if restored {
		// Verify every object's data after rebuilding from a metadata copy.
		checker.Start(false)
	}

	panel := api.NewServer(api.Deps{
		Config: cfg, DB: database, Auth: authSvc, Objects: objects, Shares: shares,
		Webhooks: hooks, Audit: auditLog, Settings: settingsStore, Metrics: reg, Storage: backends,
		Recovery: &api.Recovery{
			Replicator: rep, Alerts: alertSvc, Checker: checker, Box: box, Installation: installation,
			Restart: restartSelf,
		},
	})
	servers := []*http.Server{{
		Addr:              cfg.Listen,
		Handler:           panel.Handler(web.Handler()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}}
	if cfg.S3Listen != "" {
		servers = append(servers, &http.Server{
			Addr:              cfg.S3Listen,
			Handler:           s3.New(objects, authSvc, settingsStore, reg, cfg.S3Domain).Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		})
	}

	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() { errc <- srv.ListenAndServe() }()
	}

	slog.Info("acs-server started", "version", version.Version, "panel", cfg.Listen, "s3", cfg.S3Listen,
		"dataDir", cfg.DataDir, "installation", installation)
	if needs, err := authSvc.NeedsSetup(ctx); err == nil && needs {
		slog.Warn("no admin account yet: open the web panel to set the admin password (or restore from a drive)",
			"tokenRequired", authSvc.SetupTokenRequired())
	}

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			repStop()
			<-repDone
			return err
		}
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		for _, srv := range servers {
			if err := srv.Shutdown(shutdownCtx); err != nil {
				slog.Warn("shutdown", "addr", srv.Addr, "err", err)
			}
		}
	}
	// Final metadata flush to every backend before closing the index.
	repStop()
	<-repDone
	return nil
}

// restartSelf re-executes the server so a staged restore is applied.
func restartSelf() {
	time.Sleep(500 * time.Millisecond)
	exe, err := os.Executable()
	if err != nil {
		slog.Error("restart failed", "err", err)
		os.Exit(1)
	}
	slog.Info("restarting to apply restore")
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		slog.Error("restart failed", "err", err)
		os.Exit(1)
	}
}

// maintenance prunes expired sessions and old audit entries hourly.
func maintenance(ctx context.Context, a *auth.Service, l *audit.Log, retention time.Duration) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := a.DeleteExpiredSessions(ctx); err != nil {
				slog.Warn("session cleanup failed", "err", err)
			}
			if err := l.Prune(ctx, retention); err != nil {
				slog.Warn("audit prune failed", "err", err)
			}
		}
	}
}

func setupLogger(level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: l})))
}

// healthcheck is used by the Docker HEALTHCHECK (the runtime image has no curl).
func healthcheck(cfg config.Config) int {
	port := cfg.Listen
	if i := strings.LastIndex(port, ":"); i >= 0 {
		port = port[i+1:]
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/v1/health")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", resp.Status)
		return 1
	}
	return 0
}
