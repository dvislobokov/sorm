// Comparative benchmarks of sorm against GORM, Ent and raw database/sql.
// The DBMS is in-memory SQLite (pure-Go drivers, no cgo or network):
// we measure library overhead, not the DB.
//
// Run: cd benchmarks && go test -bench . -benchmem
package benchmarks

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/glebarez/go-sqlite"      // registers driver "sqlite"
	gormsqlite "github.com/glebarez/sqlite" // pure-Go driver for gorm
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	entclient "sorm-benchmarks/ent"
	"sorm-benchmarks/models"
	_ "sorm-benchmarks/models/sormgen" // registers BenchUser metadata

	"github.com/dvislobokov/sorm"
	"github.com/dvislobokov/sorm/dialect/lite"
	"github.com/dvislobokov/sorm/driver/sqld"
)

const (
	seedRows = 1000
	bulkRows = 100
)

const ddl = `CREATE TABLE bench_users (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	email TEXT NOT NULL UNIQUE,
	age INTEGER NOT NULL,
	active BOOLEAN NOT NULL
)`

func rawDB(b *testing.B, seed int) *sql.DB {
	b.Helper()
	sdb, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	sdb.SetMaxOpenConns(1)
	b.Cleanup(func() { sdb.Close() })
	if _, err := sdb.Exec(ddl); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < seed; i++ {
		if _, err := sdb.Exec(`INSERT INTO bench_users (name, email, age, active) VALUES (?, ?, ?, 1)`,
			fmt.Sprintf("user-%d", i), fmt.Sprintf("u%d@b.c", i), 20+i%50); err != nil {
			b.Fatal(err)
		}
	}
	return sdb
}

func sormDB(b *testing.B, seed int) sorm.DB {
	return sqld.Wrap(rawDB(b, seed), lite.Dialect{})
}

func gormDB(b *testing.B, seed int) *gorm.DB {
	b.Helper()
	gdb, err := gorm.Open(gormsqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		b.Fatal(err)
	}
	sdb, _ := gdb.DB()
	sdb.SetMaxOpenConns(1)
	b.Cleanup(func() { sdb.Close() })
	if err := gdb.Exec(ddl).Error; err != nil {
		b.Fatal(err)
	}
	for i := 0; i < seed; i += 100 {
		var batch []GormUser
		for j := i; j < i+100 && j < seed; j++ {
			batch = append(batch, GormUser{Name: fmt.Sprintf("user-%d", j), Email: fmt.Sprintf("u%d@b.c", j), Age: 20 + j%50, Active: true})
		}
		if len(batch) > 0 {
			if err := gdb.Create(&batch).Error; err != nil {
				b.Fatal(err)
			}
		}
	}
	return gdb
}

// GormUser — same shape; a separate type so gorm tags don't live in the shared models.
type GormUser struct {
	ID     int64 `gorm:"primaryKey"`
	Name   string
	Email  string `gorm:"uniqueIndex"`
	Age    int
	Active bool
}

func (GormUser) TableName() string { return "bench_users" }

func entDB(b *testing.B, seed int) *entclient.Client {
	b.Helper()
	sdb := rawDB(b, 0)
	client := entclient.NewClient(entclient.Driver(entsql.OpenDB("sqlite3", sdb)))
	b.Cleanup(func() { client.Close() })
	ctx := context.Background()
	for i := 0; i < seed; i += 100 {
		var creates []*entclient.BenchUserCreate
		for j := i; j < i+100 && j < seed; j++ {
			creates = append(creates, client.BenchUser.Create().
				SetName(fmt.Sprintf("user-%d", j)).SetEmail(fmt.Sprintf("u%d@b.c", j)).
				SetAge(20+j%50).SetActive(true))
		}
		if len(creates) > 0 {
			if _, err := client.BenchUser.CreateBulk(creates...).Save(ctx); err != nil {
				b.Fatal(err)
			}
		}
	}
	return client
}

// --- Query: 1000 rows ---

func BenchmarkQuery1000_sorm(b *testing.B) {
	db := sormDB(b, seedRows)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users, err := sorm.Query[models.BenchUser](db).All(ctx)
		if err != nil || len(users) != seedRows {
			b.Fatal(err, len(users))
		}
	}
}

func BenchmarkQuery1000_gorm(b *testing.B) {
	db := gormDB(b, seedRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var users []GormUser
		if err := db.Find(&users).Error; err != nil || len(users) != seedRows {
			b.Fatal(err, len(users))
		}
	}
}

func BenchmarkQuery1000_ent(b *testing.B) {
	client := entDB(b, seedRows)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		users, err := client.BenchUser.Query().All(ctx)
		if err != nil || len(users) != seedRows {
			b.Fatal(err, len(users))
		}
	}
}

func BenchmarkQuery1000_raw(b *testing.B) {
	sdb := rawDB(b, seedRows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := sdb.Query(`SELECT id, name, email, age, active FROM bench_users`)
		if err != nil {
			b.Fatal(err)
		}
		var users []*models.BenchUser
		for rows.Next() {
			u := new(models.BenchUser)
			if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Age, &u.Active); err != nil {
				b.Fatal(err)
			}
			users = append(users, u)
		}
		rows.Close()
		if len(users) != seedRows {
			b.Fatal(len(users))
		}
	}
}

// --- Bulk insert: 100 rows per operation ---

func BenchmarkInsert100_sorm(b *testing.B) {
	db := sormDB(b, 0)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := sorm.NewSession(db)
		for j := 0; j < bulkRows; j++ {
			sorm.Add(s, &models.BenchUser{
				Name: "n", Email: fmt.Sprintf("i%d-%d@b.c", i, j), Age: 30, Active: true,
			})
		}
		if err := s.SaveChanges(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInsert100_gorm(b *testing.B) {
	db := gormDB(b, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := make([]GormUser, bulkRows)
		for j := range batch {
			batch[j] = GormUser{Name: "n", Email: fmt.Sprintf("i%d-%d@b.c", i, j), Age: 30, Active: true}
		}
		if err := db.Create(&batch).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInsert100_ent(b *testing.B) {
	client := entDB(b, 0)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		creates := make([]*entclient.BenchUserCreate, bulkRows)
		for j := range creates {
			creates[j] = client.BenchUser.Create().
				SetName("n").SetEmail(fmt.Sprintf("i%d-%d@b.c", i, j)).SetAge(30).SetActive(true)
		}
		if _, err := client.BenchUser.CreateBulk(creates...).Save(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

// --- Update: one field of one row ---

func BenchmarkUpdateOne_sorm(b *testing.B) {
	db := sormDB(b, 1)
	ctx := context.Background()
	s := sorm.NewSession(db)
	u, err := sorm.Track[models.BenchUser](s).One(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		u.Age = 20 + i%50
		if err := s.SaveChanges(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateOne_gorm(b *testing.B) {
	db := gormDB(b, 1)
	var u GormUser
	if err := db.First(&u).Error; err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := db.Model(&u).Update("age", 20+i%50).Error; err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateOne_ent(b *testing.B) {
	client := entDB(b, 1)
	ctx := context.Background()
	u, err := client.BenchUser.Query().First(ctx)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := u.Update().SetAge(20 + i%50).Save(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
