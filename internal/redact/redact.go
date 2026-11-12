// Package redact masks credentials in log lines and error strings before the monitor shows them. It follows the
// worker's own rules without the length cut, and adds the command-line forms the connectors log
// (curl-like -H 'Authorization: …' and -b 'cookie…'). It masks too much rather than too little.
package redact

import "regexp"

// names of headers and fields whose value is a secret
const secretNames = `authorization|proxy-authorization|cookie|set-cookie|x-api-key|x-auth-token|api[-_]?key|token|password|passwd|secret`

// a rule replaces every match of re with repl (regexp.ReplaceAllString syntax)
type rule struct {
	re   *regexp.Regexp
	repl string
}

// The order matters: the command-line forms come first, they mask the whole quoted header; the later rules then
// only see "***" there, which they leave as it is.
var rules = []rule{
	// -H 'Authorization: Bearer x' and --header="Cookie: a=1" (Go's regexp has no back-references, hence one rule
	// per quote)
	{regexp.MustCompile(`(?i)((?:-H|--header)[\s=]+')(\s*(?:` + secretNames + `)\s*:)[^']*'`), `${1}${2} ***'`},
	{regexp.MustCompile(`(?i)((?:-H|--header)[\s=]+")(\s*(?:` + secretNames + `)\s*:)[^"]*"`), `${1}${2} ***"`},
	{regexp.MustCompile(`(?i)((?:-H|--header)[\s=]+)((?:` + secretNames + `)\s*:)\s*[^\s'"]+`), `${1}${2} ***`},

	// -b 'session=abc' and --cookie session=abc
	{regexp.MustCompile(`((?:\s|^)(?:-b|--cookie)[\s=]+')[^']*'`), `${1}***'`},
	{regexp.MustCompile(`((?:\s|^)(?:-b|--cookie)[\s=]+")[^"]*"`), `${1}***"`},
	{regexp.MustCompile(`((?:\s|^)(?:-b|--cookie)[\s=]+)[^\s'"]+`), `${1}***`},

	// scheme://user:pass@host
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`), `${1}***@`},

	// Authorization: Bearer x — the scheme stays readable, the credential after it goes
	{regexp.MustCompile(`(?i)((?:proxy-)?authorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic|digest|token|negotiate)\s+)?)[^\s"',;]+`), `${1}***`},

	// Cookie: a=1; b=2 — every cookie of the header, up to a quote or the end of the line
	{regexp.MustCompile(`(?i)((?:set-)?cookie["']?\s*[:=]\s*)[^"'\n]+`), `${1}***`},

	// the other names, in headers (X-Api-Key: x), env style (TOKEN=x) and JSON ("password":"x"); an unquoted value
	// stops before a quote or a closing bracket, so that the quotes of a header already masked above are kept
	{regexp.MustCompile(`(?i)(x-api-key|x-auth-token|api[-_]?key|token|password|passwd|secret)(["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,;'"}\]]+)`), `${1}${2}***`},
}

// Line returns s with its credentials masked.
func Line(s string) string {
	for _, r := range rules {
		s = r.re.ReplaceAllString(s, r.repl)
	}
	return s
}
