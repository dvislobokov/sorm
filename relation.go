package sorm

import (
	"context"
	"fmt"
)

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

// Include — eager loading детей (split-стратегия: отдельный запрос
// WHERE fk IN (pks) и раскладка по родителям in-memory).
// preds фильтруют детей, не родителей.
func (r HasMany[E, C]) Include(preds ...Pred[C]) IncludeSpec[E] {
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

		cq := Query[C](db).
			Where(pred[C](inNode{colRef{name: r.fkCol}, keys, false})).
			Where(preds...)
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
		return nil
	}}
}

// IncludeSpec — спецификация eager loading, замкнутая по родительской
// сущности; создаётся методами дескрипторов связей, исполняется билдером.
type IncludeSpec[E any] struct {
	load func(ctx context.Context, db DB, sess *Session, parents []*E) error
}
