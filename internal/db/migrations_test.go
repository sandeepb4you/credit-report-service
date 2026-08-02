package db

import (
	"io/fs"
	"regexp"
	"strconv"
	"testing"

	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// The embedded migration set must be loadable. iofs rejects duplicate version
// numbers, so two branches each claiming the same NNNN prefix takes the
// service down at boot rather than failing in CI. Git cannot catch it on its
// own: different filenames never conflict, so both sides merge cleanly.
func TestMigrations_SourceLoads(t *testing.T) {
	if _, err := iofs.New(migrationsFS, "migrations"); err != nil {
		t.Fatalf("embedded migrations do not load: %v", err)
	}
}

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

// Every migration needs both directions and a unique version, and versions
// must run 1..N with no gaps so the ordering is unambiguous.
func TestMigrations_VersionsAreUniqueAndSequential(t *testing.T) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}

	type pair struct{ up, down string }
	byVersion := map[int]*pair{}

	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if m == nil {
			t.Errorf("%s does not match NNNN_name.(up|down).sql", e.Name())
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if byVersion[v] == nil {
			byVersion[v] = &pair{}
		}
		p := byVersion[v]
		name := m[2]
		if m[3] == "up" {
			if p.up != "" {
				t.Errorf("version %04d claimed by two up migrations: %q and %q", v, p.up, name)
			}
			p.up = name
		} else {
			if p.down != "" {
				t.Errorf("version %04d claimed by two down migrations: %q and %q", v, p.down, name)
			}
			p.down = name
		}
	}

	for v, p := range byVersion {
		if p.up == "" {
			t.Errorf("version %04d has a down migration but no up", v)
		}
		if p.down == "" {
			t.Errorf("version %04d (%s) has no down migration", v, p.up)
		}
		if p.up != "" && p.down != "" && p.up != p.down {
			t.Errorf("version %04d up/down names disagree: %q vs %q", v, p.up, p.down)
		}
	}

	for i := 1; i <= len(byVersion); i++ {
		if byVersion[i] == nil {
			t.Errorf("missing migration version %04d — versions must be contiguous", i)
		}
	}
}
