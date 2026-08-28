package main

import (
	"errors"
	"fmt"

	"github.com/martinstenrose/wordleland/internal/auth"
	"github.com/martinstenrose/wordleland/internal/store"
)

func runUser(e *env, args []string) error {
	return dispatch(e, "user", []subcommand{
		{"create", "create a login", userCreate},
		{"reset-password", "set a new password and end all sessions", userResetPassword},
		{"reset-2fa", "clear TOTP enrolment, forcing re-enrolment", userReset2FA},
		{"disable", "retire an account and end its sessions", userDisable},
		{"enable", "restore a retired account", userEnable},
	}, args)
}

// firstUserBootstrap allows creating the very first admin without an acting
// admin to attribute it to. Nothing exists to authorise the change yet, and
// the alternative is a database no one can administer.
func (e *env) actorOrBootstrap(subject string) (store.Actor, error) {
	var users int
	if err := e.db.QueryRowContext(e.ctx, `SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		return store.Actor{}, fmt.Errorf("count users: %w", err)
	}
	if users == 0 {
		fmt.Fprintf(e.out, "No users exist yet; recording %s as a system action.\n", subject)
		return store.SystemActor(), nil
	}
	return e.actor()
}

func userCreate(e *env, args []string) error {
	fs := flagSet(e, "user create")
	email := fs.String("email", "", "`address` used as the login identifier")
	admin := fs.Bool("admin", false, "grant system admin rights")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*email, "email"); err != nil {
		return err
	}

	actor, err := e.actorOrBootstrap("the first user")
	if err != nil {
		return err
	}

	password, err := readNewPassword(e)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	user, err := store.CreateUser(e.ctx, e.db, actor, *email, hash, *admin)
	if errors.Is(err, store.ErrEmailTaken) {
		return fmt.Errorf("a user with email %s already exists", store.NormalizeEmail(*email))
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Created user %s (id %d)%s.\n", user.Email, user.ID, adminSuffix(user.IsAdmin))
	if user.IsAdmin {
		// 2FA is mandatory for admins, enforced at login rather than
		// here, so say so now instead of leaving it to be discovered.
		fmt.Fprintln(e.out, "Two-factor authentication is mandatory for admins; "+
			"they will be sent to enrolment on first login.")
	}
	return nil
}

func adminSuffix(isAdmin bool) string {
	if isAdmin {
		return " as an admin"
	}
	return ""
}

func userResetPassword(e *env, args []string) error {
	fs := flagSet(e, "user reset-password")
	email := fs.String("email", "", "email address of the account to reset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*email, "email"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	user, err := e.lookupUser(*email)
	if err != nil {
		return err
	}

	password, err := readNewPassword(e)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := store.SetUserPassword(e.ctx, e.db, actor, user.ID, hash); err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Reset the password for %s. All their sessions have ended.\n", user.Email)
	return nil
}

func userReset2FA(e *env, args []string) error {
	fs := flagSet(e, "user reset-2fa")
	email := fs.String("email", "", "email address of the account to reset")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*email, "email"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	user, err := e.lookupUser(*email)
	if err != nil {
		return err
	}
	if err := store.ResetUserTOTP(e.ctx, e.db, actor, user.ID); err != nil {
		return err
	}

	fmt.Fprintf(e.out, "Cleared two-factor enrolment for %s. All their sessions have ended.\n", user.Email)
	if user.IsAdmin {
		fmt.Fprintln(e.out, "They are an admin, so they must re-enrol at their next login.")
	}
	return nil
}

func userDisable(e *env, args []string) error { return setDisabled(e, args, true) }
func userEnable(e *env, args []string) error  { return setDisabled(e, args, false) }

func setDisabled(e *env, args []string, disabled bool) error {
	verb := "enable"
	if disabled {
		verb = "disable"
	}

	fs := flagSet(e, "user "+verb)
	email := fs.String("email", "", "email address of the account")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := requireFlag(*email, "email"); err != nil {
		return err
	}

	actor, err := e.actor()
	if err != nil {
		return err
	}
	user, err := e.lookupUser(*email)
	if err != nil {
		return err
	}
	if err := store.SetUserDisabled(e.ctx, e.db, actor, user.ID, disabled); err != nil {
		return err
	}

	if disabled {
		fmt.Fprintf(e.out, "Disabled %s. They cannot log in, and their sessions have ended.\n", user.Email)
		// A disabled user is not a deleted one, and cannot become one if they
		// have entered results. Saying so avoids the follow-up question.
		fmt.Fprintln(e.out, "Their entered_by attributions are unchanged.")
	} else {
		fmt.Fprintf(e.out, "Enabled %s. They can log in again.\n", user.Email)
	}
	return nil
}

func (e *env) lookupUser(email string) (store.User, error) {
	user, err := store.UserByEmail(e.ctx, e.db, email)
	if errors.Is(err, store.ErrUserNotFound) {
		return store.User{}, fmt.Errorf("no user with email %s", store.NormalizeEmail(email))
	}
	return user, err
}
