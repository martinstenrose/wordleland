package auth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// Issuer names the account in an authenticator app.
const Issuer = "Wordleland"

// totpPeriod is the standard 30-second step.
const totpPeriod = 30

// totpSkew allows one step either side, covering ordinary clock drift between
// the phone and the server. Wider would multiply the codes valid at any moment
// for no real gain in usability.
const totpSkew = 1

// ErrInvalidCode covers a code that does not match, has already been used, or
// was generated for a different secret.
var ErrInvalidCode = errors.New("invalid code")

// GenerateTOTPSecret creates an enrolment secret and its otpauth:// URI.
func GenerateTOTPSecret(email string) (secret, uri string, err error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      Issuer,
		AccountName: email,
		Period:      totpPeriod,
		Algorithm:   otp.AlgorithmSHA1, // what every authenticator app supports
	})
	if err != nil {
		return "", "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// ValidateTOTP checks a code and returns the time step it matched.
//
// The step is returned so the caller can reject replay: PRD the rule is that a
// code already accepted cannot be used again inside its window. Validation
// alone does not provide that — the same code stays valid for the whole step,
// so an observed code would otherwise work a second time.
func ValidateTOTP(secret, code string, at time.Time) (int64, error) {
	valid, err := totp.ValidateCustom(code, secret, at, totp.ValidateOpts{
		Period:    totpPeriod,
		Skew:      totpSkew,
		Digits:    otp.DigitsSix,
		Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil || !valid {
		return 0, ErrInvalidCode
	}

	// ValidateCustom accepts a window, so recover which step actually matched
	// rather than assuming the current one: recording the wrong step would
	// either let a code be replayed or reject the next legitimate one.
	step, err := matchedStep(secret, code, at)
	if err != nil {
		return 0, err
	}
	return step, nil
}

// matchedStep finds which step within the skew window produced code.
func matchedStep(secret, code string, at time.Time) (int64, error) {
	for offset := -totpSkew; offset <= totpSkew; offset++ {
		candidate := at.Add(time.Duration(offset) * totpPeriod * time.Second)
		generated, err := totp.GenerateCodeCustom(secret, candidate, totp.ValidateOpts{
			Period:    totpPeriod,
			Skew:      0,
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, fmt.Errorf("generate comparison code: %w", err)
		}
		if generated == code {
			return candidate.Unix() / totpPeriod, nil
		}
	}
	// Reached only if ValidateCustom and this loop disagree, which would mean
	// the two are configured differently.
	return 0, ErrInvalidCode
}

// CurrentStep reports the time step for an instant, so callers can compare
// against a stored last-accepted step.
func CurrentStep(at time.Time) int64 { return at.Unix() / totpPeriod }

// RebuildTOTPURI reconstructs the otpauth:// URI for an existing secret, so a
// re-rendered enrolment page can show the same QR code rather than issuing a
// new secret and forcing another scan.
func RebuildTOTPURI(secret, email string) (string, string, error) {
	key, err := otp.NewKeyFromURL(fmt.Sprintf(
		"otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=%d",
		url.PathEscape(Issuer), url.PathEscape(email), secret, url.QueryEscape(Issuer), totpPeriod))
	if err != nil {
		return "", "", fmt.Errorf("rebuild TOTP URI: %w", err)
	}
	return key.Secret(), key.URL(), nil
}

// RecoveryCodeCount is how many codes an enrolment issues.
const RecoveryCodeCount = 10

// recoveryCodeChars is the length of a code. Each character carries 5 bits
// of the 32-letter alphabet below, so 16 of them is 80 bits — which is what
// makes storing only a SHA-256 hash safe. A shorter code would be worth
// grinding offline against a stolen database.
const recoveryCodeChars = 16

// recoveryAlphabet is Crockford base32 without I, L, O and U, so a code read
// off paper cannot be mistyped as a similar-looking character.
const recoveryAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// GenerateRecoveryCode returns one code, grouped for reading aloud.
func GenerateRecoveryCode() (string, error) {
	raw, err := randomBytes(recoveryCodeChars)
	if err != nil {
		return "", fmt.Errorf("generate recovery code: %w", err)
	}

	var b strings.Builder
	for i, c := range raw {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		// One character per random byte rather than packing the bits.
		// 256 divides by 32 exactly, so the modulo is unbiased and each
		// character really does carry its full 5 bits.
		b.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
	}
	return b.String(), nil
}

// NormalizeRecoveryCode puts a typed code into its stored form, so the
// grouping dashes, stray spaces and lower case a person types are not the
// difference between getting back in and not.
func NormalizeRecoveryCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		if strings.ContainsRune(recoveryAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
