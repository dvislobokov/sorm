// Package schema — Ent-схема, эквивалентная моделям бенчмарка.
package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type BenchUser struct {
	ent.Schema
}

func (BenchUser) Fields() []ent.Field {
	return []ent.Field{
		field.String("name"),
		field.String("email").Unique(),
		field.Int("age"),
		field.Bool("active"),
	}
}
