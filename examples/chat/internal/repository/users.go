// Package repository is the data-access layer. Two styles on purpose:
// users/rooms go through the generated sormgen.Context (the EF-style
// unit of work), messages/audit use the low-level Query/Session API —
// pick whichever reads better for the use case.
//
// Every repository is bound to a sorm.DB and can be re-bound to a
// transaction with With(tx) — that is how services compose repositories
// atomically (see service.Chat.Post).
package repository

import (
	"context"

	"github.com/dvislobokov/sorm"

	"github.com/dvislobokov/sorm/examples/chat/internal/models"
	gen "github.com/dvislobokov/sorm/examples/chat/internal/models/sormgen"
)

type Users struct {
	db sorm.DB
}

func NewUsers(db sorm.DB) Users { return Users{db: db} }

// With re-binds the repository to another connection (usually an open
// transaction inside sorm.RunInTx).
func (r Users) With(db sorm.DB) Users { return Users{db: db} }

// Register inserts a user with a personal API token — one unit of work,
// FK через навигацию, ключи вернутся после SaveChanges.
func (r Users) Register(ctx context.Context, u *models.User, token *models.ApiToken) error {
	c := gen.NewContext(r.db)
	c.Users.Add(u)
	if token != nil {
		token.User = u
		c.ApiTokens.Add(token)
	}
	return c.SaveChanges(ctx)
}

// ByEmail — untracked read (nothing will be mutated).
func (r Users) ByEmail(ctx context.Context, email string) (*models.User, error) {
	c := gen.NewContext(r.db)
	return c.Users.NoTracking().Where(gen.User.Email.Eq(email)).One(ctx)
}

// ByID — Find checks the identity map first, then selects by PK.
func (r Users) ByID(ctx context.Context, id int64) (*models.User, error) {
	return gen.NewContext(r.db).Users.Find(ctx, id)
}

// UpdatePrefs is the EF-style flow: tracked read → plain mutation →
// SaveChanges computes the diff (one UPDATE of the changed columns,
// optimistic-concurrency predicate included for free).
func (r Users) UpdatePrefs(ctx context.Context, id int64, prefs *models.UserPrefs) (*models.User, error) {
	c := gen.NewContext(r.db)
	u, err := c.Users.Find(ctx, id)
	if err != nil {
		return nil, err
	}
	u.Prefs = prefs
	return u, c.SaveChanges(ctx)
}

// Ban — set-based UPDATE without loading the entity (ExecuteUpdate style);
// the version column is bumped automatically.
func (r Users) Ban(ctx context.Context, id int64) (int64, error) {
	return sorm.Update[models.User](r.db).
		Set(gen.User.Status.Set(models.StatusBanned)).
		Where(gen.User.ID.Eq(id)).
		Named("users.ban").
		Exec(ctx)
}

// DarkThemePushUsers shows typed JSON accessors and a PG array predicate
// in one query: prefs->>'theme' = 'dark' AND prefs->notify->>push = true
// AND roles @> '{moderator}'.
func (r Users) DarkThemePushUsers(ctx context.Context) ([]*models.User, error) {
	u := gen.User
	return sorm.Query[models.User](r.db).
		Where(
			u.PrefsDoc.Theme.Eq("dark"),
			u.PrefsDoc.Notify.Push.IsTrue(),
			u.Roles.Has("moderator"),
		).
		OrderBy(u.Name.Asc()).
		Named("users.dark-push-mods").
		All(ctx)
}
