package ai

import (
	"regexp"
	"strings"
)

// Redaction rules for response excerpts sent to the model (only when the user
// ticks "include response excerpt"). They are conservative pattern filters,
// not a guarantee: free text can hold secrets in forms no pattern knows, which
// is why excerpts are opt-in and why docs/security.md says so.
//
//  1. JWTs                       eyJ....eyJ....sig
//  2. Authorization-style values Bearer/Basic/Token <value>
//  3. Provider key patterns      sk_/pk_/rk_/whsec_/ghp_/AKIA/AIza/xox.-
//  4. key=value secrets          password|secret|token|api_key|signature=... (also JSON "key":"...")
//  5. Email addresses
//  6. Card numbers               13-19 digits, optional spaces/dashes, Luhn-valid
//  7. Long opaque strings        32+ chars of hex/base64/url-safe alphabet
var (
	jwtRE      = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{5,}\.eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]*`)
	authRE     = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+[A-Za-z0-9._~+/=-]{8,}`)
	keyPatRE   = regexp.MustCompile(`\b(?:(?:sk|pk|rk|whsec)_[A-Za-z0-9_]{8,}|gh[pousr]_[A-Za-z0-9]{20,}|AKIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,})`)
	kvSecretRE = regexp.MustCompile(`(?i)("?(?:password|passwd|pwd|secret|token|api[_-]?key|apikey|signature|authorization|session|cookie)"?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;&}]+)`)
	emailRE    = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	cardRE     = regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`)
	opaqueRE   = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

// Redacted is the replacement marker.
const Redacted = "[REDACTED]"

// RedactExcerpt removes token, key, email and card patterns from text.
func RedactExcerpt(s string) string {
	s = jwtRE.ReplaceAllString(s, Redacted)
	s = keyPatRE.ReplaceAllString(s, Redacted)
	s = authRE.ReplaceAllString(s, "$1 "+Redacted)
	s = kvSecretRE.ReplaceAllString(s, "${1}"+Redacted)
	s = emailRE.ReplaceAllString(s, Redacted)
	s = cardRE.ReplaceAllStringFunc(s, func(m string) string {
		if luhnValid(m) {
			return Redacted
		}
		return m
	})
	s = opaqueRE.ReplaceAllString(s, Redacted)
	return s
}

func luhnValid(s string) bool {
	var digits []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum, alt := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if alt {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// Truncate cuts s to at most max runes.
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max]))
}
