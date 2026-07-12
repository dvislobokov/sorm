package sorm

import (
	"context"
	"fmt"
)

// inChunkSize — предел ключей в одном WHERE ... IN (...) при eager loading:
// десятки тысяч родителей не упрутся в лимиты плейсхолдеров БД.
const inChunkSize = 1000

func chunked(keys []any) [][]any {
	if len(keys) <= inChunkSize {
		return [][]any{keys}
	}
	var out [][]any
	for start := 0; start < len(keys); start += inChunkSize {
		end := min(start+inChunkSize, len(keys))
		out = append(out, keys[start:end])
	}
	return out
}

// HasMany — дескриптор связи «один ко многим». Знает оба типа, поэтому всё
// «двухтиповое» (Include, Any) — его методы: методы билдера не могут вводить
// новые type parameters.
//
// Функции доступа генерируются `sorm gen` — рантайм не использует рефлексию.
type HasMany[E, C any] struct {
	fkCol       string
	parentKey   func(*E) any
	childKey    func(*C) any
	initSlice   func(*E)      // помечает навигацию «загружено, но пусто»
	appendChild func(*E, *C)
}

func NewHasMany[E, C any](
	fkCol string,
	parentKey func(*E) any,
	childKey func(*C) any,
	initSlice func(*E),
	appendChild func(*E, *C),
) HasMany[E, C] {
	return HasMany[E, C]{fkCol, parentKey, childKey, initSlice, appendChild}
}

// Any — фильтр РОДИТЕЛЯ по детям: EXISTS (SELECT 1 FROM children WHERE fk = parent.pk AND preds).
// Возвращает Pred[E], подаётся в обычный Where.
func (r HasMany[E, C]) Any(preds ...Pred[C]) Pred[E] {
	return r.exists(preds, false)
}

// None — NOT EXISTS: родители, у которых нет подходящих детей.
func (r HasMany[E, C]) None(preds ...Pred[C]) Pred[E] {
	return r.exists(preds, true)
}

func (r HasMany[E, C]) exists(preds []Pred[C], not bool) Pred[E] {
	pm, cm := metaFor[E](), metaFor[C]()
	return pred[E](existsNode{
		childTable:  cm.Table,
		fkCol:       r.fkCol,
		parentTable: pm.Table,
		parentPK:    pm.PK,
		preds:       nodesOf(preds),
		not:         not,
	})
}

// ChildOpt — опция Include: предикат по детям (Pred[C]), сортировка детей
// (Order[C]) или вложенная спецификация (IncludeSpec[C] — аналог ThenInclude):
//
//	q.With(u.Posts.Include(
//	    p.Title.HasPrefix("Go"),      // фильтр детей
//	    p.CreatedAt.Desc(),           // порядок детей
//	    p.Comments.Include(),         // вложенная загрузка
//	))
type ChildOpt[C any] interface {
	applyChild(cfg *childCfg[C])
}

type childCfg[C any] struct {
	preds  []Pred[C]
	orders []Order[C]
	specs  []IncludeSpec[C]
}

func (p Pred[C]) applyChild(cfg *childCfg[C])        { cfg.preds = append(cfg.preds, p) }
func (o Order[C]) applyChild(cfg *childCfg[C])       { cfg.orders = append(cfg.orders, o) }
func (s IncludeSpec[C]) applyChild(cfg *childCfg[C]) { cfg.specs = append(cfg.specs, s) }

func childConfig[C any](opts []ChildOpt[C]) childCfg[C] {
	var cfg childCfg[C]
	for _, o := range opts {
		o.applyChild(&cfg)
	}
	return cfg
}

// Include — eager loading детей (split-стратегия: отдельный запрос
// WHERE fk IN (pks) и раскладка по родителям in-memory).
// Опции: Pred[C] (фильтр детей), Order[C] (их порядок), IncludeSpec[C]
// (вложенная загрузка — аналог ThenInclude).
func (r HasMany[E, C]) Include(opts ...ChildOpt[C]) IncludeSpec[E] {
	cfg := childConfig(opts)
	return IncludeSpec[E]{load: func(ctx context.Context, db DB, sess *Session, parents []*E) error {
		if len(parents) == 0 {
			return nil
		}
		keys := make([]any, 0, len(parents))
		byKey := make(map[any][]*E, len(parents))
		for _, p := range parents {
			k := r.parentKey(p)
			if _, seen := byKey[k]; !seen {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], p)
			r.initSlice(p) // загруженная пустая связь = пустой слайс, не nil
		}

		var all []*C
		for _, chunk := range chunked(keys) {
			cq := Query[C](db).
				Where(pred[C](inNode{colRef{name: r.fkCol}, chunk, false})).
				Where(cfg.preds...).
				OrderBy(cfg.orders...)
			cq.sess = sess // Track трекает и детей
			children, err := cq.All(ctx)
			if err != nil {
				return fmt.Errorf("include: %w", err)
			}
			for _, c := range children {
				for _, p := range byKey[r.childKey(c)] {
					r.appendChild(p, c)
				}
			}
			all = append(all, children...)
		}
		return runChildSpecs(ctx, db, sess, cfg.specs, all)
	}}
}

// IncludeSpec — спецификация eager loading, замкнутая по родительской
// сущности; создаётся методами дескрипторов связей, исполняется билдером.
type IncludeSpec[E any] struct {
	load func(ctx context.Context, db DB, sess *Session, parents []*E) error
}

// runChildSpecs выполняет вложенные Include над загруженными детьми.
func runChildSpecs[C any](ctx context.Context, db DB, sess *Session, specs []IncludeSpec[C], children []*C) error {
	for _, sp := range specs {
		if err := sp.load(ctx, db, sess, children); err != nil {
			return err
		}
	}
	return nil
}

// BelongsTo — дескриптор связи «многие к одному» (ребёнок → родитель).
// Функции доступа генерируются `sorm gen`.
type BelongsTo[C, P any] struct {
	fkCol     string
	childFK   func(*C) any
	setParent func(*C, *P)
}

func NewBelongsTo[C, P any](
	fkCol string,
	childFK func(*C) any,
	setParent func(*C, *P),
) BelongsTo[C, P] {
	return BelongsTo[C, P]{fkCol, childFK, setParent}
}

// Is — фильтр РЕБЁНКА по атрибутам родителя:
// EXISTS (SELECT 1 FROM parents WHERE parents.pk = children.fk AND preds).
func (r BelongsTo[C, P]) Is(preds ...Pred[P]) Pred[C] {
	cm, pm := metaFor[C](), metaFor[P]()
	return pred[C](existsNode{
		childTable:  pm.Table,
		fkCol:       pm.PK,
		parentTable: cm.Table,
		parentPK:    r.fkCol,
		preds:       nodesOf(preds),
	})
}

// Include — eager loading родителя: один запрос WHERE pk IN (fk детей)
// и раскладка по детям. Pred[P]-опции фильтруют родителей; у ребёнка, чей
// родитель отфильтрован, навигация остаётся nil. IncludeSpec[P]-опции —
// вложенная загрузка связей родителя.
func (r BelongsTo[C, P]) Include(opts ...ChildOpt[P]) IncludeSpec[C] {
	cfg := childConfig(opts)
	return IncludeSpec[C]{load: func(ctx context.Context, db DB, sess *Session, children []*C) error {
		if len(children) == 0 {
			return nil
		}
		pm := metaFor[P]()

		keys := make([]any, 0, len(children))
		seen := map[any]bool{}
		for _, c := range children {
			k := r.childFK(c)
			if isZero(k) || seen[k] {
				continue
			}
			seen[k] = true
			keys = append(keys, k)
		}
		if len(keys) == 0 {
			return nil
		}

		byPK := map[any]*P{}
		var all []*P
		for _, chunk := range chunked(keys) {
			pq := Query[P](db).
				Where(pred[P](inNode{colRef{name: pm.PK}, chunk, false})).
				Where(cfg.preds...)
			pq.sess = sess // Track трекает и родителей
			parents, err := pq.All(ctx)
			if err != nil {
				return fmt.Errorf("include: %w", err)
			}
			for _, p := range parents {
				byPK[pm.PKValue(p)] = p
			}
			all = append(all, parents...)
		}
		for _, c := range children {
			if p, ok := byPK[r.childFK(c)]; ok {
				r.setParent(c, p)
			}
		}
		return runChildSpecs(ctx, db, sess, cfg.specs, all)
	}}
}

// HasOne — связь «один к одному» (FK на стороне ребёнка C, навигация *C у E).
// Отсутствие ребёнка после Include неотличимо от «не загружено» (указатель
// остаётся nil) — в отличие от hasMany, где пустой слайс ≠ nil.
type HasOne[E, C any] struct {
	fkCol     string
	parentKey func(*E) any
	childKey  func(*C) any
	setChild  func(*E, *C)
}

func NewHasOne[E, C any](
	fkCol string,
	parentKey func(*E) any,
	childKey func(*C) any,
	setChild func(*E, *C),
) HasOne[E, C] {
	return HasOne[E, C]{fkCol, parentKey, childKey, setChild}
}

// Any — фильтр родителя по ребёнку (EXISTS), None — отсутствие ребёнка.
func (r HasOne[E, C]) Any(preds ...Pred[C]) Pred[E]  { return r.exists(preds, false) }
func (r HasOne[E, C]) None(preds ...Pred[C]) Pred[E] { return r.exists(preds, true) }

func (r HasOne[E, C]) exists(preds []Pred[C], not bool) Pred[E] {
	pm, cm := metaFor[E](), metaFor[C]()
	return pred[E](existsNode{
		childTable:  cm.Table,
		fkCol:       r.fkCol,
		parentTable: pm.Table,
		parentPK:    pm.PK,
		preds:       nodesOf(preds),
		not:         not,
	})
}

// Include — eager loading единственного ребёнка (WHERE fk IN (pks)).
func (r HasOne[E, C]) Include(opts ...ChildOpt[C]) IncludeSpec[E] {
	cfg := childConfig(opts)
	return IncludeSpec[E]{load: func(ctx context.Context, db DB, sess *Session, parents []*E) error {
		if len(parents) == 0 {
			return nil
		}
		keys := make([]any, 0, len(parents))
		byKey := make(map[any][]*E, len(parents))
		for _, p := range parents {
			k := r.parentKey(p)
			if _, seen := byKey[k]; !seen {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], p)
		}

		var all []*C
		for _, chunk := range chunked(keys) {
			cq := Query[C](db).
				Where(pred[C](inNode{colRef{name: r.fkCol}, chunk, false})).
				Where(cfg.preds...)
			cq.sess = sess
			children, err := cq.All(ctx)
			if err != nil {
				return fmt.Errorf("include: %w", err)
			}
			for _, c := range children {
				for _, p := range byKey[r.childKey(c)] {
					r.setChild(p, c)
				}
			}
			all = append(all, children...)
		}
		return runChildSpecs(ctx, db, sess, cfg.specs, all)
	}}
}
