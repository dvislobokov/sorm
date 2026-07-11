// Todo-API: net/http + sorm + PostgreSQL.
//
// При старте применяет версионные миграции (migrate.Up), затем поднимает
// HTTP-сервер. Показывает: сессию (Unit of Work) в хендлерах, optimistic
// concurrency → HTTP 409, eager loading, динамическую композицию фильтров.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер "pgx" для database/sql (миграции)
	"github.com/jackc/pgx/v5/pgxpool"

	"sorm"
	"sorm/driver/pgxd"
	"sorm/examples/webapp/models"
	gen "sorm/examples/webapp/models/sormgen"
	"sorm/migrate"
)

func main() {
	ctx := context.Background()
	dsn := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/todo")
	addr := envOr("ADDR", ":8080")
	migrationsDir := envOr("MIGRATIONS_DIR", "migrations")

	if err := migrateUp(ctx, dsn, migrationsDir); err != nil {
		log.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	srv := &server{db: pgxd.Wrap(pool)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", srv.createUser)
	mux.HandleFunc("GET /users", srv.listUsers)
	mux.HandleFunc("POST /tasks", srv.createTask)
	mux.HandleFunc("GET /tasks", srv.listTasks)
	mux.HandleFunc("POST /tasks/{id}/toggle", srv.toggleTask)

	log.Println("listening on", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

// migrateUp применяет неприменённые файлы миграций (с ретраями: в compose
// приложение может стартовать раньше готовности БД).
func migrateUp(ctx context.Context, dsn, dir string) error {
	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer sdb.Close()

	var applied []string
	for attempt := 1; ; attempt++ {
		applied, err = migrate.Up(ctx, sdb, "postgres", dir)
		if err == nil {
			break
		}
		if attempt >= 10 {
			return fmt.Errorf("migrate up: %w", err)
		}
		log.Printf("migrate up (попытка %d): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	for _, f := range applied {
		log.Println("применена миграция", f)
	}
	if len(applied) == 0 {
		log.Println("миграции: всё уже применено")
	}
	return nil
}

type server struct {
	db sorm.DB
}

// POST /users {"name": "...", "email": "..."}
func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" || in.Email == "" {
		httpError(w, http.StatusBadRequest, "нужны name и email")
		return
	}

	sess := sorm.NewSession(s.db)
	u := &models.User{Name: in.Name, Email: in.Email}
	sorm.Add(sess, u)
	if err := sess.SaveChanges(r.Context()); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, u)
}

// GET /users — пользователи с задачами (eager loading, split query).
func (s *server) listUsers(w http.ResponseWriter, r *http.Request) {
	u := gen.User
	users, err := sorm.Query[models.User](s.db).
		With(u.Tasks.Include()).
		OrderBy(u.ID.Asc()).
		All(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// POST /tasks {"user_id": 1, "title": "...", "priority": 2}
func (s *server) createTask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID   int64  `json:"user_id"`
		Title    string `json:"title"`
		Priority int    `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
		httpError(w, http.StatusBadRequest, "нужны user_id и title")
		return
	}

	owner, err := sorm.Query[models.User](s.db).
		Where(gen.User.ID.Eq(in.UserID)).
		One(r.Context())
	if errors.Is(err, sorm.ErrNotFound) {
		httpError(w, http.StatusNotFound, "пользователь не найден")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	sess := sorm.NewSession(s.db)
	t := &models.Task{User: owner, Title: in.Title, Priority: in.Priority, CreatedAt: time.Now()}
	sorm.Add(sess, t) // FK проставится из навигации
	if err := sess.SaveChanges(r.Context()); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.User = nil // не раздуваем ответ
	writeJSON(w, http.StatusCreated, t)
}

// GET /tasks?done=false&min_priority=2 — динамическая композиция фильтров.
func (s *server) listTasks(w http.ResponseWriter, r *http.Request) {
	t := gen.Task
	q := sorm.Query[models.Task](s.db).OrderBy(t.Priority.Desc(), t.ID.Asc())

	if v := r.URL.Query().Get("done"); v != "" {
		done, err := strconv.ParseBool(v)
		if err != nil {
			httpError(w, http.StatusBadRequest, "done: true|false")
			return
		}
		q = q.Where(t.Done.Eq(done)) // done=false — полноценное условие
	}
	if v := r.URL.Query().Get("min_priority"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			httpError(w, http.StatusBadRequest, "min_priority: число")
			return
		}
		q = q.Where(t.Priority.Gte(p))
	}

	tasks, err := q.All(r.Context())
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

// POST /tasks/{id}/toggle — Unit of Work: Track → мутация → SaveChanges.
// Конкурентное изменение (optimistic concurrency) → 409 Conflict.
func (s *server) toggleTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpError(w, http.StatusBadRequest, "bad id")
		return
	}

	sess := sorm.NewSession(s.db)
	task, err := sorm.Track[models.Task](sess).
		Where(gen.Task.ID.Eq(id)).
		One(r.Context())
	if errors.Is(err, sorm.ErrNotFound) {
		httpError(w, http.StatusNotFound, "задача не найдена")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	task.Done = !task.Done // обычная мутация — дифф вычислит sorm
	err = sess.SaveChanges(r.Context())
	var conflict *sorm.ConflictError
	if errors.As(err, &conflict) {
		httpError(w, http.StatusConflict, "задача изменена конкурентно, повторите")
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
