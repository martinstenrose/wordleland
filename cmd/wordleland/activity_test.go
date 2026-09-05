package main

import (
	"strings"
	"testing"
)

func TestActivityListShowsResultsAndPlayers(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")
	c.mustRun("", "--as", "admin@example.tld", "results", "set",
		"--player", "martin", "--puzzle", "1893", "--guesses", "4")

	out := c.mustRun("", "activity", "list")
	if !strings.Contains(out, "ID") || !strings.Contains(out, "ACTION") {
		t.Errorf("list output has no header:\n%s", out)
	}
	if !strings.Contains(out, "player.created") {
		t.Errorf("list output missing the player creation:\n%s", out)
	}
	if !strings.Contains(out, "result.created") {
		t.Errorf("list output missing the result:\n%s", out)
	}
	if !strings.Contains(out, "martin") {
		t.Errorf("list output does not name the subject:\n%s", out)
	}
	if !strings.Contains(out, "admin@example.tld") {
		t.Errorf("list output does not name the acting admin:\n%s", out)
	}
}

func TestActivityListFiltersByKind(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	out := c.mustRun("", "activity", "list", "--kind", "users")
	if strings.Contains(out, "player.created") {
		t.Errorf("--kind users still shows a player event:\n%s", out)
	}

	if _, err := c.run("", "activity", "list", "--kind", "bogus"); err == nil {
		t.Fatal("an unknown --kind was accepted")
	}
}

func TestActivityShowPrintsDetail(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")
	c.mustRun("", "--as", "admin@example.tld", "player", "add", "--name", "Martin")

	list := c.mustRun("", "activity", "list", "--kind", "players")
	lines := strings.Split(strings.TrimSpace(list), "\n")
	id := strings.Fields(lines[len(lines)-1])[0]

	out := c.mustRun("", "activity", "show", "--id", id)
	if !strings.Contains(out, "player.created") {
		t.Errorf("show output missing the action:\n%s", out)
	}
	if !strings.Contains(out, "martin") {
		t.Errorf("show output missing the subject:\n%s", out)
	}
	if !strings.Contains(out, "Detail:") {
		t.Errorf("show output missing the raw detail:\n%s", out)
	}
}

func TestActivityShowMissingID(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")

	if _, err := c.run("", "activity", "show", "--id", "999"); err == nil {
		t.Fatal("showing a nonexistent event succeeded")
	}
}

func TestActivityListIsReadOnly(t *testing.T) {
	c := newCLI(t)
	c.mustRun("correct horse battery staple\n",
		"user", "create", "--email", "admin@example.tld", "--admin")

	// No --as: listing needs no acting admin.
	if _, err := c.run("", "activity", "list"); err != nil {
		t.Errorf("activity list without --as failed: %v", err)
	}
}
