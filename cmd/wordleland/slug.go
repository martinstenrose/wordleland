package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/martinstenrose/wordleland/internal/store"
)

func runSlug(e *env, args []string) error {
	return dispatch(e, "slug", []subcommand{
		{"show", "print the current read-only share link", slugShow},
		{"rotate", "replace the share link, invalidating the old one", slugRotate},
	}, args)
}

func slugShow(e *env, args []string) error {
	fs := flagSet(e, "slug show")
	if err := fs.Parse(args); err != nil {
		return err
	}

	slug, err := store.ShareSlug(e.ctx, e.db)
	if err != nil {
		return describeMissingSettings(err)
	}
	fmt.Fprintln(e.out, shareURL(slug))
	return nil
}

func slugRotate(e *env, args []string) error {
	fs := flagSet(e, "slug rotate")
	if err := fs.Parse(args); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}

	slug, err := store.RotateShareSlug(e.ctx, e.db, actor)
	if err != nil {
		return describeMissingSettings(err)
	}

	fmt.Fprintln(e.out, shareURL(slug))
	// Rotating is the whole point, but the consequence lands on other people,
	// so it is worth stating rather than leaving to be discovered.
	fmt.Fprintln(e.out, "The previous share link no longer works. Password reset links are unaffected.")
	return nil
}

// shareURL renders the full link when APP_URL is set, and the path alone when
// it is not — the path is still what someone needs, just without the origin.
func shareURL(slug string) string {
	path := "/share/" + slug + "/"
	if base := os.Getenv("APP_URL"); base != "" {
		return base + path
	}
	return path
}

func describeMissingSettings(err error) error {
	if errors.Is(err, store.ErrNoSettings) {
		return errors.New("no share link exists yet; run serve once to generate it")
	}
	return err
}
