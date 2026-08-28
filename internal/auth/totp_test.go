package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// codeAt generates the valid code for a secret at an instant.
func codeAt(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(secret, at, totp.ValidateOpts{
		Period:    totpPeriod,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		t.Fatalf("generate code: %v", err)
	}
	return code
}

func TestGenerateTOTPSecret(t *testing.T) {
	secret, uri, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	if secret == "" {
		t.Fatal("secret is empty")
	}
	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("uri = %q, want an otpauth:// URI", uri)
	}
	// The issuer and account are what an authenticator app shows, so they
	// must identify which login this code belongs to.
	if !strings.Contains(uri, Issuer) {
		t.Errorf("uri = %q, want it to name the issuer", uri)
	}
	if !strings.Contains(uri, "martin%40example.tld") && !strings.Contains(uri, "martin@example.tld") {
		t.Errorf("uri = %q, want it to name the account", uri)
	}
}

func TestGenerateTOTPSecretIsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		secret, _, err := GenerateTOTPSecret("martin@example.tld")
		if err != nil {
			t.Fatalf("GenerateTOTPSecret() failed: %v", err)
		}
		if seen[secret] {
			t.Fatal("GenerateTOTPSecret() repeated a secret")
		}
		seen[secret] = true
	}
}

func TestValidateTOTP(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	now := time.Now()

	step, err := ValidateTOTP(secret, codeAt(t, secret, now), now)
	if err != nil {
		t.Fatalf("ValidateTOTP() rejected a valid code: %v", err)
	}
	if step != CurrentStep(now) {
		t.Errorf("step = %d, want %d", step, CurrentStep(now))
	}
}

func TestValidateTOTPRejectsWrongCode(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	now := time.Now()

	for _, code := range []string{"", "000000", "12345", "1234567", "abcdef"} {
		if _, err := ValidateTOTP(secret, code, now); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("ValidateTOTP(%q) error = %v, want ErrInvalidCode", code, err)
		}
	}
}

// One step either side covers ordinary clock drift between phone and server.
func TestValidateTOTPAcceptsSkew(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	now := time.Now()

	for _, offset := range []time.Duration{-totpPeriod * time.Second, 0, totpPeriod * time.Second} {
		at := now.Add(offset)
		code := codeAt(t, secret, at)

		step, err := ValidateTOTP(secret, code, now)
		if err != nil {
			t.Errorf("ValidateTOTP() rejected a code from offset %v: %v", offset, err)
			continue
		}
		// The step returned must be the one that actually matched, not the
		// current one: recording the wrong step would either permit a replay
		// or reject the next legitimate code.
		if want := CurrentStep(at); step != want {
			t.Errorf("offset %v: step = %d, want %d", offset, step, want)
		}
	}
}

func TestValidateTOTPRejectsOutsideSkew(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	now := time.Now()

	for _, offset := range []time.Duration{-3 * totpPeriod * time.Second, 3 * totpPeriod * time.Second} {
		code := codeAt(t, secret, now.Add(offset))
		if _, err := ValidateTOTP(secret, code, now); !errors.Is(err, ErrInvalidCode) {
			t.Errorf("ValidateTOTP() accepted a code from offset %v", offset)
		}
	}
}

func TestValidateTOTPRejectsOtherSecret(t *testing.T) {
	first, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	second, _, err := GenerateTOTPSecret("alex@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}
	now := time.Now()

	if _, err := ValidateTOTP(first, codeAt(t, second, now), now); !errors.Is(err, ErrInvalidCode) {
		t.Error("a code from a different secret was accepted")
	}
}

// The step is what makes replay rejection possible: a code stays valid for its
// whole window, so without recording the step an observed code would work
// again.
func TestValidateTOTPStepIsStableWithinAWindow(t *testing.T) {
	secret, _, err := GenerateTOTPSecret("martin@example.tld")
	if err != nil {
		t.Fatalf("GenerateTOTPSecret() failed: %v", err)
	}

	// Two instants inside the same step.
	base := time.Unix((time.Now().Unix()/totpPeriod)*totpPeriod, 0)
	early, late := base.Add(time.Second), base.Add((totpPeriod-2)*time.Second)
	code := codeAt(t, secret, early)

	firstStep, err := ValidateTOTP(secret, code, early)
	if err != nil {
		t.Fatalf("ValidateTOTP() failed: %v", err)
	}
	secondStep, err := ValidateTOTP(secret, code, late)
	if err != nil {
		t.Fatalf("ValidateTOTP() failed: %v", err)
	}

	if firstStep != secondStep {
		t.Errorf("the same code reported steps %d and %d; replay rejection depends on this being stable",
			firstStep, secondStep)
	}
}

func TestCurrentStepAdvances(t *testing.T) {
	now := time.Now()
	if CurrentStep(now.Add(totpPeriod*time.Second)) != CurrentStep(now)+1 {
		t.Error("CurrentStep() does not advance by one per period")
	}
}

// A code is fixed-length, grouped, and drawn from an alphabet with no
// look-alike characters — all three matter for something read off paper.
func TestGenerateRecoveryCodeShape(t *testing.T) {
	seen := make(map[string]bool)
	for range 200 {
		code, err := GenerateRecoveryCode()
		if err != nil {
			t.Fatalf("GenerateRecoveryCode() failed: %v", err)
		}
		if got, want := len(code), 19; got != want { // 16 characters, 3 dashes
			t.Fatalf("code %q has length %d, want %d", code, got, want)
		}
		for _, group := range strings.Split(code, "-") {
			if len(group) != 4 {
				t.Fatalf("code %q is not grouped in fours", code)
			}
		}
		for _, r := range NormalizeRecoveryCode(code) {
			if !strings.ContainsRune("0123456789ABCDEFGHJKMNPQRSTVWXYZ", r) {
				t.Fatalf("code %q contains %q, which is not in the alphabet", code, r)
			}
		}
		if seen[code] {
			t.Fatalf("GenerateRecoveryCode() repeated %q within 200 draws", code)
		}
		seen[code] = true
	}
}

// A person types what they can read. Grouping, case and stray spaces must
// not be the difference between getting back in and not.
func TestNormalizeRecoveryCode(t *testing.T) {
	code, err := GenerateRecoveryCode()
	if err != nil {
		t.Fatalf("GenerateRecoveryCode() failed: %v", err)
	}
	want := NormalizeRecoveryCode(code)

	for _, typed := range []string{
		code,
		strings.ToLower(code),
		strings.ReplaceAll(code, "-", ""),
		strings.ReplaceAll(code, "-", " "),
		"  " + code + "\n",
	} {
		if got := NormalizeRecoveryCode(typed); got != want {
			t.Errorf("NormalizeRecoveryCode(%q) = %q, want %q", typed, got, want)
		}
	}
}
