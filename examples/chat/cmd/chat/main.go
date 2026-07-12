// Chat — the sorm showcase application: Echo + clean layering
// (transport → service → repository → sorm), everything in a dedicated
// "chat" database schema.
//
//	docker run -d -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=chat -p 5432:5432 postgres:17-alpine
//	go run ./examples/chat/cmd/chat
//
// Configuration: appsettings.yaml + CHAT_* env vars (CHAT_DB__DSN, ...).
package main

import (
	"context"
	"database/sql"
	"errors"

	"github.com/dvislobokov/sconf"
	"github.com/dvislobokov/srog"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/driver/pgxd"
	"github.com/dvislobokov/sorm/migrate"

	"github.com/dvislobokov/sorm/examples/chat/internal/config"
	"github.com/dvislobokov/sorm/examples/chat/internal/service"
	"github.com/dvislobokov/sorm/examples/chat/internal/transport/httpapi"
	_ "github.com/dvislobokov/sorm/examples/chat/internal/models/sormgen" // registers entity meta + TableDefs
)

func main() {
	cfg, err := config.Load()
	if errors.Is(err, sconf.ErrHelp) {
		return
	}
	if err != nil {
		srog.NewConsole().Fatal(err, "configuration failed")
	}

	level, err := srog.ParseLevel(cfg.Log.Level)
	if err != nil {
		level = srog.InformationLevel
	}
	log := srog.MustNew(srog.WithConsole(), srog.WithLevel(level))
	defer log.Close()

	if err := run(context.Background(), cfg, log); err != nil {
		log.Fatal(err, "chat failed to start")
	}
}

func run(ctx context.Context, cfg *config.Settings, log *srog.Logger) error {
	// --- database: pool + schema binding ---
	pool, err := pgxpool.New(ctx, cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Everything the app renders is qualified with the schema:
	// "chat"."users", "chat"."messages", ... Models stay schema-agnostic.
	db := sorm.InSchema(pgxd.Wrap(pool), cfg.DB.Schema)

	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS `+cfg.DB.Schema); err != nil {
		return err
	}

	// --- migrations + seed (declarative, scoped to the schema) ---
	sdb, err := sql.Open("pgx", cfg.DB.DSN)
	if err != nil {
		return err
	}
	defer sdb.Close()
	if err := migrate.Apply(ctx, sdb, "postgres", migrate.WithSchema(cfg.DB.Schema)); err != nil {
		return err
	}
	log.Information("schema {Schema} migrated", cfg.DB.Schema)

	// One-time seed: the #general room and its system owner. Recorded in
	// the history table — replicas and redeploys skip it.
	err = migrate.Seed(ctx, sdb, "postgres", "general-room", func(ctx context.Context, tx *sql.Tx) error {
		s := cfg.DB.Schema
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+s+`.users (email, name, status, meta, roles, balance, version, created_at, updated_at)
			 VALUES ('system@chat.local', 'System', 'active', '{}', '{system}', 0, 1, now(), now())`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO `+s+`.rooms (slug, title, owner_id, created_at)
			 SELECT 'general', 'General', id, now() FROM `+s+`.users WHERE email = 'system@chat.local'`)
		return err
	})
	if err != nil {
		return err
	}

	// --- layers ---
	chat := service.New(db, log)

	// --- HTTP ---
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())
	e.Use(requestLog(log))
	httpapi.Register(e, chat)

	log.Information("chat listening on {Addr}", cfg.HTTP.Addr)
	return e.Start(cfg.HTTP.Addr)
}

// requestLog — a minimal Echo middleware over srog.
func requestLog(log *srog.Logger) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			err := next(c)
			log.Information("{Method} {Path} -> {Status}",
				c.Request().Method, c.Request().URL.Path, c.Response().Status)
			return err
		}
	}
}
