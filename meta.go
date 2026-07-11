package sorm

import (
	"fmt"
	"reflect"
	"sync"
)

// Meta описывает сущность для рантайма. В боевом коде генерируется `sorm gen`;
// рукописная мета допустима в тестах и на переходный период.
// Все функции пополевые, без рефлексии — единственная рефлексия рантайма
// это один lookup типа в реестре при построении запроса.
type Meta[E any] struct {
	Table      string
	PK         string // имя PK-колонки
	Auto       bool   // PK генерируется БД (identity) → INSERT ... RETURNING
	VersionCol string // "" — без optimistic concurrency

	SelectCols []string // порядок соответствует Scan
	InsertCols []string // без auto-PK; порядок соответствует InsertValues

	// Scan возвращает указатели на поля в порядке SelectCols.
	Scan func(*E) []any
	// InsertValues возвращает значения в порядке InsertCols.
	InsertValues func(*E) []any
	// ValuesFor возвращает значения колонок по индексам из SelectCols (мост дифф → UPDATE).
	ValuesFor func(*E, []int) []any
	// Snapshot/Diff — снапшот-трекинг (PR №4). Снапшот — типизированная
	// генерируемая структура, боксится на границе меты.
	Snapshot func(*E) any
	Diff     func(any, *E) []int
	// SetPK проставляет auto-PK после RETURNING.
	SetPK func(*E, int64)
}

var registry sync.Map // reflect.Type -> *Meta[E] (как any)

// Register регистрирует мету сущности. Вызывается из init() сгенерированного пакета.
func Register[E any](m Meta[E]) {
	registry.Store(reflect.TypeFor[E](), &m)
}

// MetaOf возвращает зарегистрированную мету сущности — для тестов и
// продвинутых сценариев (инструментирование, собственные executors).
// Паникует, если мета не зарегистрирована.
func MetaOf[E any]() *Meta[E] { return metaFor[E]() }

func metaFor[E any]() *Meta[E] {
	v, ok := registry.Load(reflect.TypeFor[E]())
	if !ok {
		panic(fmt.Sprintf(
			"sorm: no Meta registered for %v — import the generated sormgen package (or run `sorm gen`)",
			reflect.TypeFor[E](),
		))
	}
	return v.(*Meta[E])
}
