package providers

import (
	"os"
	"strings"
)

// interpolate expands ${env:VAR} and ${env:VAR:-default} references in s
// against the process environment. An unset (or empty) VAR with no default
// expands to "". Unknown ${...} forms are left verbatim. The function makes a
// single left-to-right pass and does not re-expand substituted text.
func interpolate(s string) string {
	return interpolateWith(s, os.Getenv)
}

// interpolateDefaults expands the same references as interpolate but ignores
// the process environment, always falling back to the embedded ${env:VAR:-def}
// default (or "" when there is none). It is used to validate the shipped
// providers.json structurally — independent of the user's environment — so a
// runtime env override can never turn embedded-file validation into a panic.
func interpolateDefaults(s string) string {
	return interpolateWith(s, func(string) string { return "" })
}

// interpolateWith is the shared engine: lookup resolves a variable name to its
// value (an empty result falls back to the inline default, if any).
func interpolateWith(s string, lookup func(string) string) string {
	const open = "${env:"
	if !strings.Contains(s, open) {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, open)
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		rest := s[i+len(open):]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			// Unterminated reference: emit the rest verbatim and stop.
			b.WriteString(s[i:])
			break
		}
		expr := rest[:j]
		b.WriteString(resolveEnvExpr(expr, lookup))
		s = rest[j+1:]
	}
	return b.String()
}

// resolveEnvExpr resolves the inside of a ${env:...} reference: either "VAR"
// or "VAR:-default". An unset or empty VAR falls back to the default (or "").
func resolveEnvExpr(expr string, lookup func(string) string) string {
	name := expr
	def := ""
	hasDef := false
	if k := strings.Index(expr, ":-"); k >= 0 {
		name = expr[:k]
		def = expr[k+2:]
		hasDef = true
	}
	if v := lookup(name); v != "" {
		return v
	}
	if hasDef {
		return def
	}
	return ""
}

// ResolveInference returns a copy of in with every string field expanded via
// interpolate and empty query-param values dropped. The inference layer calls
// this when constructing a client so env-driven overrides (base URLs, region,
// attribution headers, group id) take effect without per-provider Go code.
func (in InferenceSpec) Resolve() InferenceSpec {
	out := InferenceSpec{
		BaseURL:     interpolate(in.BaseURL),
		AuthScheme:  in.AuthScheme,
		AuthHeader:  in.AuthHeader,
		EffortStyle: in.EffortStyle,
		JSONSet:     in.JSONSet, // values are not interpolated (non-string)
	}
	if len(in.Headers) > 0 {
		out.Headers = make(map[string]string, len(in.Headers))
		for k, v := range in.Headers {
			rv := interpolate(v)
			if rv == "" {
				continue
			}
			out.Headers[k] = rv
		}
	}
	if len(in.QueryParams) > 0 {
		out.QueryParams = make(map[string]string, len(in.QueryParams))
		for k, v := range in.QueryParams {
			rv := interpolate(v)
			if rv == "" {
				// Drop unset query params (e.g. MiniMax GroupId when the env
				// var is absent) — matches the prior middleware behavior.
				continue
			}
			out.QueryParams[k] = rv
		}
	}
	return out
}
