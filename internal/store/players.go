package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

var (
	// ErrPlayerNotFound is returned when no player matches.
	ErrPlayerNotFound = errors.New("player not found")
	// ErrSlugTaken is returned when a slug is already in use.
	ErrSlugTaken = errors.New("slug is already taken")
	// ErrInvalidSlug is returned for a slug outside [a-z0-9-].
	ErrInvalidSlug = errors.New("slug must be lowercase letters, digits and hyphens")

	// ErrUserLinkedElsewhere is returned when a login already belongs to a
	// different player. A sentinel rather than a bare message because
	// callers need to recognise this case and say something useful about
	// it, and matching on error text is the kind of check that keeps
	// working right up until someone rewords the message.
	ErrUserLinkedElsewhere = errors.New("that login is already linked to another player")
)

// slugPattern is the shape requires: lowercase [a-z0-9-].
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Player is a scoreboard entity, independent of whether a login exists.
type Player struct {
	ID     int64
	Slug   string
	Name   string
	UserID *int64
	Active bool
}

// ValidSlug reports whether s is an acceptable slug.
func ValidSlug(s string) bool { return slugPattern.MatchString(s) }

// transliterations are letters that Unicode normalization cannot decompose,
// because they are letters in their own right rather than an accented base
// plus a mark. NFD turns Å into A + ring, but leaves Æ and ß untouched.
//
// This map is a convenience for the letters with an unambiguous conventional
// spelling, not the safety net. Anything not listed here is caught by Slugify
// refusing to drop it — a map alone would keep failing silently for whichever
// letter was not thought of.
var transliterations = strings.NewReplacer(
	"æ", "ae", "Æ", "ae",
	"ø", "o", "Ø", "o",
	"ß", "ss",
	"œ", "oe", "Œ", "oe",
	"đ", "d", "Đ", "d",
	"ð", "d", "Ð", "d",
	"þ", "th", "Þ", "th",
	"ł", "l", "Ł", "l",
)

// ErrUnslugifiable reports that a name cannot become a slug without losing
// characters. The caller is expected to pass an explicit slug instead.
var ErrUnslugifiable = errors.New("cannot derive a slug from this name")

// Slugify derives a slug from a display name.
//
// It transliterates what maps cleanly — accents are folded so "Åsa" becomes
// "asa" rather than "sa" — and returns ErrUnslugifiable rather than dropping
// anything it cannot represent. A slug is a stable identifier that ends up in
// URLs and in every CLI invocation, so quietly losing a letter is the wrong
// failure: a name in Cyrillic or Chinese would slugify to nothing at all, and
// Ø or Þ would lose a letter without saying so. --slug exists for these.
func Slugify(name string) (string, error) {
	folded, _, err := transform.String(
		transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC),
		transliterations.Replace(name),
	)
	if err != nil {
		folded = name
	}

	var b strings.Builder
	lastHyphen := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(folded) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false

		// A letter or digit that is not ASCII survived transliteration, so
		// keeping going would drop it. Separators and punctuation are safe to
		// discard; alphanumerics carry meaning.
		case unicode.IsLetter(r), unicode.IsDigit(r):
			return "", fmt.Errorf("%w: %q cannot be written as [a-z0-9-]", ErrUnslugifiable, r)

		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "", fmt.Errorf("%w: nothing usable in %q", ErrUnslugifiable, name)
	}
	return slug, nil
}

const playerColumns = `id, slug, name, user_id, active`

func scanPlayer(row interface{ Scan(...any) error }) (Player, error) {
	var p Player
	err := row.Scan(&p.ID, &p.Slug, &p.Name, &p.UserID, &p.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return Player{}, ErrPlayerNotFound
	}
	if err != nil {
		return Player{}, fmt.Errorf("scan player: %w", err)
	}
	return p, nil
}

// PlayerBySlug looks a player up by slug.
func PlayerBySlug(ctx context.Context, q Querier, slug string) (Player, error) {
	return scanPlayer(q.QueryRowContext(ctx, `SELECT `+playerColumns+` FROM players WHERE slug = ?`, slug))
}

// PlayerByID looks a player up by id.
func PlayerByID(ctx context.Context, q Querier, id int64) (Player, error) {
	return scanPlayer(q.QueryRowContext(ctx, `SELECT `+playerColumns+` FROM players WHERE id = ?`, id))
}

// PlayerByUserID finds the player a login is attached to, if any.
func PlayerByUserID(ctx context.Context, q Querier, userID int64) (Player, error) {
	return scanPlayer(q.QueryRowContext(ctx,
		`SELECT `+playerColumns+` FROM players WHERE user_id = ?`, userID))
}

// ListPlayers returns every player, ordered by slug.
func ListPlayers(ctx context.Context, q Querier) ([]Player, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+playerColumns+` FROM players ORDER BY slug`)
	if err != nil {
		return nil, fmt.Errorf("list players: %w", err)
	}
	defer rows.Close()

	var players []Player
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		players = append(players, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list players: %w", err)
	}
	return players, nil
}

// CreatePlayer inserts a player. An empty slug is derived from the name, with
// a numeric suffix if that is taken.
func CreatePlayer(ctx context.Context, db *sql.DB, actor Actor, name, slug string) (Player, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Player{}, errors.New("player name is empty")
	}

	explicitSlug := slug != ""
	if explicitSlug && !ValidSlug(slug) {
		return Player{}, ErrInvalidSlug
	}

	var player Player
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		if !explicitSlug {
			base, err := Slugify(name)
			if err != nil {
				return err
			}
			if slug, err = uniqueSlug(ctx, tx, base); err != nil {
				return err
			}
		}

		res, err := tx.ExecContext(ctx, `INSERT INTO players (slug, name) VALUES (?, ?)`, slug, name)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrSlugTaken
			}
			return fmt.Errorf("create player: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create player: %w", err)
		}

		if err := Audit(ctx, tx, actor, ActionPlayerCreated, SubjectPlayer, &id,
			map[string]any{"slug": slug, "name": name}); err != nil {
			return err
		}

		player, err = PlayerByID(ctx, tx, id)
		return err
	})
	return player, err
}

// uniqueSlug appends -2, -3, ... until the slug is free.
func uniqueSlug(ctx context.Context, q Querier, base string) (string, error) {
	candidate := base
	for n := 2; n < 1000; n++ {
		var exists int
		err := q.QueryRowContext(ctx, `SELECT 1 FROM players WHERE slug = ?`, candidate).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("check slug: %w", err)
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	return "", fmt.Errorf("cannot find a free slug based on %q", base)
}

// PlayerUpdate carries the fields a caller asked to change. A nil pointer
// means "not provided" and leaves the column alone.
//
// Pointers rather than values because Go's flag package cannot distinguish an
// unset boolean from an explicit false: a naive implementation would
// deactivate a player as a side effect of renaming them.
type PlayerUpdate struct {
	Name   *string
	Slug   *string
	Active *bool
}

// IsEmpty reports whether nothing was asked for.
func (u PlayerUpdate) IsEmpty() bool { return u.Name == nil && u.Slug == nil && u.Active == nil }

// UpdatePlayer applies only the fields present in update.
func UpdatePlayer(ctx context.Context, db *sql.DB, actor Actor, playerID int64, update PlayerUpdate) (Player, error) {
	if update.IsEmpty() {
		return Player{}, errors.New("nothing to update")
	}
	if update.Slug != nil && !ValidSlug(*update.Slug) {
		return Player{}, ErrInvalidSlug
	}
	if update.Name != nil && strings.TrimSpace(*update.Name) == "" {
		return Player{}, errors.New("player name is empty")
	}

	var player Player
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		before, err := PlayerByID(ctx, tx, playerID)
		if err != nil {
			return err
		}

		var (
			sets    []string
			args    []any
			changed = map[string]any{}
		)
		if update.Name != nil && *update.Name != before.Name {
			name := strings.TrimSpace(*update.Name)
			sets = append(sets, "name = ?")
			args = append(args, name)
			changed["name"] = map[string]any{"from": before.Name, "to": name}
		}
		if update.Slug != nil && *update.Slug != before.Slug {
			sets = append(sets, "slug = ?")
			args = append(args, *update.Slug)
			changed["slug"] = map[string]any{"from": before.Slug, "to": *update.Slug}
		}
		if update.Active != nil && *update.Active != before.Active {
			sets = append(sets, "active = ?")
			args = append(args, *update.Active)
			changed["active"] = map[string]any{"from": before.Active, "to": *update.Active}
		}

		// Every requested change matched what is already stored.
		if len(sets) == 0 {
			player = before
			return nil
		}

		args = append(args, playerID)
		if _, err := tx.ExecContext(ctx,
			`UPDATE players SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...,
		); err != nil {
			if isUniqueViolation(err) {
				return ErrSlugTaken
			}
			return fmt.Errorf("update player: %w", err)
		}

		// Retirement and return are logged as themselves rather than buried in
		// a generic update, since treats them as membership decisions.
		action := ActionPlayerUpdated
		if update.Active != nil && *update.Active != before.Active {
			if *update.Active {
				action = ActionPlayerReactivated
			} else {
				action = ActionPlayerRetired
			}
		}
		if err := Audit(ctx, tx, actor, action, SubjectPlayer, &playerID, changed); err != nil {
			return err
		}

		player, err = PlayerByID(ctx, tx, playerID)
		return err
	})
	return player, err
}

// LinkPlayer attaches a login to a player, which is how self-report is
// granted. Passing a nil userID unlinks.
func LinkPlayer(ctx context.Context, db *sql.DB, actor Actor, playerID int64, userID *int64) (Player, error) {
	var player Player
	err := InTx(ctx, db, func(tx *sql.Tx) error {
		before, err := PlayerByID(ctx, tx, playerID)
		if err != nil {
			return err
		}

		if userID != nil {
			// One login per player: linking a user who already holds another
			// player would let one account self-report as two people.
			var otherSlug string
			err := tx.QueryRowContext(ctx,
				`SELECT slug FROM players WHERE user_id = ? AND id != ?`, *userID, playerID).Scan(&otherSlug)
			switch {
			case err == nil:
				return fmt.Errorf("%w: %s", ErrUserLinkedElsewhere, otherSlug)
			case !errors.Is(err, sql.ErrNoRows):
				return fmt.Errorf("check existing link: %w", err)
			}
		}

		if _, err := tx.ExecContext(ctx, `UPDATE players SET user_id = ? WHERE id = ?`, userID, playerID); err != nil {
			return fmt.Errorf("link player: %w", err)
		}

		action, detail := ActionPlayerUnlinked, map[string]any{}
		if userID != nil {
			action = ActionPlayerLinked
			detail["user_id"] = *userID
		}
		if before.UserID != nil {
			detail["previous_user_id"] = *before.UserID
		}
		if err := Audit(ctx, tx, actor, action, SubjectPlayer, &playerID, detail); err != nil {
			return err
		}

		player, err = PlayerByID(ctx, tx, playerID)
		return err
	})
	return player, err
}

// ReactivatePlayer marks a player as back in the group.
//
// Called only from live ingest: posting again is evidence of return,
// where a replayed or backfilled result is history and says nothing about the
// present. Logged so the change is visible rather than silent.
func ReactivatePlayer(ctx context.Context, q Querier, actor Actor, playerID int64) error {
	res, err := q.ExecContext(ctx, `UPDATE players SET active = 1 WHERE id = ? AND active = 0`, playerID)
	if err != nil {
		return fmt.Errorf("reactivate player: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		// Already active: nothing happened, so nothing is logged.
		return nil
	}
	return Audit(ctx, q, actor, ActionPlayerReactivated, SubjectPlayer, &playerID,
		map[string]any{"reason": "posted a result after leaving"})
}

// CreatePlayerTx creates a player inside an existing transaction.
func CreatePlayerTx(ctx context.Context, tx *sql.Tx, actor Actor, name, slug string) (Player, error) {
	if slug == "" {
		derived, err := Slugify(name)
		if err != nil {
			return Player{}, err
		}
		if slug, err = uniqueSlug(ctx, tx, derived); err != nil {
			return Player{}, err
		}
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO players (slug, name) VALUES (?, ?)`, slug, name)
	if err != nil {
		if isUniqueViolation(err) {
			return Player{}, ErrSlugTaken
		}
		return Player{}, fmt.Errorf("create player: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Player{}, fmt.Errorf("create player: %w", err)
	}
	if err := Audit(ctx, tx, actor, ActionPlayerCreated, SubjectPlayer, &id,
		map[string]any{"slug": slug, "name": name}); err != nil {
		return Player{}, err
	}
	return PlayerByID(ctx, tx, id)
}

// UpdatePlayerTx applies a PlayerUpdate inside an existing transaction.
func UpdatePlayerTx(ctx context.Context, tx *sql.Tx, actor Actor, playerID int64, update PlayerUpdate) (Player, error) {
	before, err := PlayerByID(ctx, tx, playerID)
	if err != nil {
		return Player{}, err
	}
	if update.Active == nil || *update.Active == before.Active {
		return before, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE players SET active = ? WHERE id = ?`, *update.Active, playerID); err != nil {
		return Player{}, fmt.Errorf("update player: %w", err)
	}
	action := ActionPlayerRetired
	if *update.Active {
		action = ActionPlayerReactivated
	}
	if err := Audit(ctx, tx, actor, action, SubjectPlayer, &playerID,
		map[string]any{"active": map[string]any{"from": before.Active, "to": *update.Active}}); err != nil {
		return Player{}, err
	}
	return PlayerByID(ctx, tx, playerID)
}
