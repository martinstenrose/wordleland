package main

import (
	"errors"
	"flag"
	"fmt"
	"text/tabwriter"

	"github.com/martinstenrose/wordleland/internal/store"
)

func runPlayer(e *env, args []string) error {
	return dispatch(e, "player", []subcommand{
		{"add", "create a scoreboard entry", playerAdd},
		{"update", "change name, slug or membership", playerUpdate},
		{"link", "attach a login, granting self-report", playerLink},
		{"unlink", "detach the login", playerUnlink},
		{"list", "list players", playerList},
	}, args)
}

func playerAdd(e *env, args []string) error {
	fs := flagSet(e, "player add")
	name := fs.String("name", "", "display name")
	slug := fs.String("slug", "", "URL and CLI identifier; derived from the name if omitted")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*name, "name"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	player, err := store.CreatePlayer(e.ctx, e.db, actor, *name, *slug)
	switch {
	case errors.Is(err, store.ErrSlugTaken):
		return fmt.Errorf("slug %q is already taken", *slug)
	case errors.Is(err, store.ErrInvalidSlug):
		return fmt.Errorf("slug %q must be lowercase letters, digits and hyphens", *slug)
	case errors.Is(err, store.ErrUnslugifiable):
		// Deriving one would drop characters, so ask rather than guess: the
		// slug goes in URLs and every later command that names this player.
		return fmt.Errorf("%w\nPass --slug to choose one, for example: "+
			"wordleland player add --name %q --slug <slug>", err, *name)
	case err != nil:
		return err
	}

	fmt.Fprintf(e.out, "Created player %s (%s, id %d).\n", player.Name, player.Slug, player.ID)
	return nil
}

func playerUpdate(e *env, args []string) error {
	fs := flagSet(e, "player update")
	player := fs.String("player", "", "slug of the player to change")
	name := fs.String("name", "", "new display name")
	slug := fs.String("slug", "", "new slug")
	active := fs.Bool("active", true, "whether the player is still in the group")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}

	// Only the flags actually passed may reach the database. An unset
	// bool is false in Go's flag package, so reading *active directly would
	// retire a player as a side effect of renaming them. flag.Visit reports
	// what was set, which is the distinction the flag values cannot carry.
	var update store.PlayerUpdate
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "name":
			update.Name = name
		case "slug":
			update.Slug = slug
		case "active":
			update.Active = active
		}
	})
	if update.IsEmpty() {
		return errors.New("nothing to change: pass --name, --slug or --active")
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	existing, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}

	updated, err := store.UpdatePlayer(e.ctx, e.db, actor, existing.ID, update)
	switch {
	case errors.Is(err, store.ErrSlugTaken):
		return fmt.Errorf("slug %q is already taken", *slug)
	case errors.Is(err, store.ErrInvalidSlug):
		return fmt.Errorf("slug %q must be lowercase letters, digits and hyphens", *slug)
	case err != nil:
		return err
	}

	fmt.Fprintf(e.out, "Updated player %s (%s).\n", updated.Name, updated.Slug)
	if update.Active != nil && !*update.Active {
		// Retirement is membership, not recency, and it is not deletion:
		// the history stays and the player stays on the board.
		fmt.Fprintln(e.out, "They are marked as having left the group. Their results are kept.")
	}
	return nil
}

func playerLink(e *env, args []string) error {
	fs := flagSet(e, "player link")
	player := fs.String("player", "", "slug of the player")
	user := fs.String("user", "", "email of the login to attach")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}
	if err := requireFlag(*user, "user"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	target, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}
	login, err := e.lookupUser(*user)
	if err != nil {
		return err
	}

	if _, err := store.LinkPlayer(e.ctx, e.db, actor, target.ID, &login.ID); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Linked %s to %s. They can now self-report their own results.\n",
		target.Slug, login.Email)
	return nil
}

func playerUnlink(e *env, args []string) error {
	fs := flagSet(e, "player unlink")
	player := fs.String("player", "", "slug of the player")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*player, "player"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	target, err := e.lookupPlayer(*player)
	if err != nil {
		return err
	}
	if target.UserID == nil {
		return fmt.Errorf("player %s has no login linked", target.Slug)
	}

	if _, err := store.LinkPlayer(e.ctx, e.db, actor, target.ID, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Unlinked %s. Their results are unchanged.\n", target.Slug)
	return nil
}

func playerList(e *env, args []string) error {
	fs := flagSet(e, "player list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	players, err := store.ListPlayers(e.ctx, e.db)
	if err != nil {
		return err
	}
	if len(players) == 0 {
		fmt.Fprintln(e.out, "No players yet.")
		return nil
	}

	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SLUG\tNAME\tMEMBER\tLOGIN")
	for _, p := range players {
		member := "yes"
		if !p.Active {
			member = "left"
		}
		login := "-"
		if p.UserID != nil {
			user, err := store.UserByID(e.ctx, e.db, *p.UserID)
			if err != nil {
				return err
			}
			login = user.Email
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", p.Slug, p.Name, member, login)
	}
	return w.Flush()
}

func (e *env) lookupPlayer(slug string) (store.Player, error) {
	player, err := store.PlayerBySlug(e.ctx, e.db, slug)
	if errors.Is(err, store.ErrPlayerNotFound) {
		return store.Player{}, fmt.Errorf("no player with slug %q", slug)
	}
	return player, err
}
