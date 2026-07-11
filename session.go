package sorm

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
)

// Session — Unit of Work: identity map + snapshot change tracking.
// Загруженные через Track сущности мутируются обычным Go-кодом;
// SaveChanges вычисляет минимальный дифф и применяет его батчами
// в одной транзакции.
//
// Не потокобезопасна (как DbContext в EF); живёт один юнит работы.
type Session struct {
	db     DB
	stores map[reflect.Type]anyStore
}

func NewSession(db DB) *Session {
	return &Session{db: db, stores: map[reflect.Type]anyStore{}}
}

// Track — тот же билдер запросов, но материализованные сущности попадают
// в трекер. Повторная загрузка той же строки возвращает уже отслеживаемый
// указатель; данные из БД не перезатирают локальные изменения (семантика EF).
func Track[E any](s *Session) QueryBuilder[E] {
	q := Query[E](s.db)
	q.sess = s
	return q
}

// Add регистрирует новые сущности на INSERT. FK нового графа задаётся
// навигацией (p.Author = u) — значение FK-колонки проставит рантайм после
// вставки родителя.
func Add[E any](s *Session, entities ...*E) {
	st := storeOf[E](s)
	for _, e := range entities {
		if _, dup := st.added[e]; dup {
			continue
		}
		st.added[e] = struct{}{}
		st.addedOrder = append(st.addedOrder, e)
	}
}

// Remove помечает сущности на DELETE. Сущность должна быть отслеживаемой
// или иметь заполненный PK (проверяется в SaveChanges).
func Remove[E any](s *Session, entities ...*E) {
	st := storeOf[E](s)
	for _, e := range entities {
		if _, dup := st.removed[e]; dup {
			continue
		}
		st.removed[e] = struct{}{}
		st.removedOrder = append(st.removedOrder, e)
	}
}

// SaveChanges открывает транзакцию, применяет дифф и коммитит.
// Порядок: DELETE (дети раньше родителей) → UPDATE (только изменённые
// колонки) → INSERT по уровням зависимостей (RETURNING → FK-fixup детей).
// DELETE+UPDATE уходят одним pgx.Batch (один roundtrip), каждый уровень
// вставок — ещё одним.
func (s *Session) SaveChanges(ctx context.Context) error {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return errors.New("sorm: session db cannot begin transactions; use SaveChangesTx with an explicit pgx.Tx")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("sorm: begin: %w", err)
	}
	post, err := s.flush(ctx, tx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("sorm: commit: %w", err)
	}
	post()
	return nil
}

// SaveChangesTx применяет дифф внутри внешней транзакции; commit — за вызывающим.
// Внимание: состояние трекера обновляется сразу после успешного flush.
func (s *Session) SaveChangesTx(ctx context.Context, tx pgx.Tx) error {
	post, err := s.flush(ctx, tx)
	if err != nil {
		return err
	}
	post()
	return nil
}

// --- хранилище per-type ---

type anyStore interface {
	collectAdded(set map[any]bool)
	buildPlan(p *flushPlan) error
}

type rec[E any] struct {
	e    *E
	snap any
}

type tracker[E any] struct {
	meta         *Meta[E]
	byPK         map[any]*rec[E]
	trackOrder   []*rec[E]
	added        map[*E]struct{}
	addedOrder   []*E
	removed      map[*E]struct{}
	removedOrder []*E
}

func storeOf[E any](s *Session) *tracker[E] {
	t := reflect.TypeFor[E]()
	if st, ok := s.stores[t]; ok {
		return st.(*tracker[E])
	}
	tr := &tracker[E]{
		meta:    metaFor[E](),
		byPK:    map[any]*rec[E]{},
		added:   map[*E]struct{}{},
		removed: map[*E]struct{}{},
	}
	s.stores[t] = tr
	return tr
}

// trackScanned — identity map: та же строка → тот же указатель.
func (t *tracker[E]) trackScanned(e *E) *E {
	pk := t.meta.PKValue(e)
	if existing, ok := t.byPK[pk]; ok {
		return existing.e
	}
	r := &rec[E]{e: e, snap: t.meta.Snapshot(e)}
	t.byPK[pk] = r
	t.trackOrder = append(t.trackOrder, r)
	return e
}

func (t *tracker[E]) collectAdded(set map[any]bool) {
	for _, e := range t.addedOrder {
		if _, cancelled := t.removed[e]; !cancelled {
			set[any(e)] = true
		}
	}
}
