package llmprices

import "strings"

// Provider is a company that sells model access: a model maker's own API, a cloud platform
// that resells it, or a host for open-weight models. The value is the canonical lowercase
// name, the Price.Provider of that provider's rows.
type Provider string

// The providers the table knows. Vertex, Bedrock, Azure and OpenRouter have no rows of their
// own; lookups through them fall back to the model makers they resell.
const (
	Anthropic  Provider = "anthropic"
	OpenAI     Provider = "openai"
	Google     Provider = "google"
	Vertex     Provider = "vertex"
	Bedrock    Provider = "bedrock"
	Azure      Provider = "azure"
	DeepSeek   Provider = "deepseek"
	Mistral    Provider = "mistral"
	Groq       Provider = "groq"
	Together   Provider = "together"
	Fireworks  Provider = "fireworks"
	XAI        Provider = "xai"
	Cohere     Provider = "cohere"
	OpenRouter Provider = "openrouter"
)

// Providers lists every Provider, model makers first.
var Providers = []Provider{Anthropic, OpenAI, Google, Vertex, Bedrock, Azure, DeepSeek, Mistral, Groq, Together, Fireworks, XAI, Cohere, OpenRouter}

var providerNames = func() map[string]Provider {
	m := map[string]Provider{}
	for p, names := range map[Provider][]string{
		Anthropic:  {"anthropic", "claude"},
		OpenAI:     {"openai", "open-ai"},
		Google:     {"google", "gemini", "google-ai-studio", "google-gemini", "ai-studio", "google-genai", "google-generativeai", "genai", "google-ai", "googleai"},
		Vertex:     {"vertex", "vertex-ai", "vertexai", "google-vertex", "google-vertex-ai", "vertex-ai-beta", "google-vertexai"},
		Bedrock:    {"bedrock", "aws", "aws-bedrock", "amazon-bedrock", "bedrock-converse", "bedrock-runtime", "amazon"},
		Azure:      {"azure", "azure-openai", "azure-ai", "azure-foundry", "microsoft-foundry", "azure-ai-foundry"},
		DeepSeek:   {"deepseek"},
		Mistral:    {"mistral", "mistralai", "mistral-ai"},
		Groq:       {"groq", "groqcloud", "groq-cloud"},
		Together:   {"together", "together-ai", "togetherai"},
		Fireworks:  {"fireworks", "fireworks-ai", "fireworksai"},
		XAI:        {"xai", "x-ai", "grok"},
		Cohere:     {"cohere", "cohere-chat", "cohere-ai"},
		OpenRouter: {"openrouter", "open-router"},
	} {
		for _, n := range names {
			m[n] = p
		}
	}
	return m
}()

var fallbacks = map[Provider][]Provider{
	Vertex:     {Google, Anthropic},
	Bedrock:    {Anthropic},
	Azure:      {OpenAI},
	OpenRouter: {Anthropic, OpenAI, Google, DeepSeek, Mistral, XAI, Cohere},
}

var platforms = map[string]bool{"vertex": true, "bedrock": true, "azure": true, "openrouter": true}

// ParseProvider parses a provider name, ignoring ASCII case and treating "_", "-" and spaces
// alike. It accepts the canonical names and the spellings LiteLLM, LangChain, OpenRouter and
// the provider SDKs use ("gemini", "google_genai", "vertex_ai", "google_vertexai",
// "bedrock_converse", "azure_openai", "together_ai", "x-ai", ...). ok is false for anything
// else ("ollama", "huggingface").
func ParseProvider(name string) (p Provider, ok bool) {
	name = strings.TrimSpace(name)
	if len(name) > 24 {
		return "", false
	}
	key := strings.NewReplacer("_", "-", " ", "-").Replace(asciiLower(name))
	p, ok = providerNames[key]
	return p, ok
}

// String returns the canonical lowercase name.
func (p Provider) String() string { return string(p) }

// Rows returns the rows this provider sells, in table order (copies). Empty for Vertex,
// Bedrock, Azure and OpenRouter, which have no rows of their own.
func (p Provider) Rows() []Price {
	var out []Price
	for i := range table {
		if table[i].Provider == string(p) {
			out = append(out, table[i].clone())
		}
	}
	return out
}

// FallbackProviders returns the model makers whose list prices Lookup falls back to when this
// provider has no row for a model: Vertex AI to Google and Anthropic, Bedrock to Anthropic,
// Azure to OpenAI, OpenRouter to every model maker in the table. Nil for the others.
func (p Provider) FallbackProviders() []Provider {
	return append([]Provider(nil), fallbacks[p]...)
}

// Lookup finds model among the rows this provider sells, with the same id handling as the
// package-level Lookup. When the provider has no row for it, the rows of its
// FallbackProviders are tried, so Claude on Bedrock gets Anthropic's row.
func (p Provider) Lookup(model string) (Price, bool) {
	i := p.lookupIndex(model)
	if i < 0 {
		return Price{}, false
	}
	return table[i].clone(), true
}

func (p Provider) lookupIndex(model string) int {
	name := string(p)
	if i := resolve(model, func(r *Price) bool { return r.Provider == name }); i >= 0 {
		return i
	}
	makers := fallbacks[p]
	if len(makers) == 0 {
		return -1
	}
	return resolve(model, func(r *Price) bool {
		for _, m := range makers {
			if r.Provider == string(m) {
				return true
			}
		}
		return false
	})
}

func lookupIndex(model string) int {
	lower := asciiLower(strings.TrimSpace(model))
	if strings.HasPrefix(lower, "accounts/fireworks/") {
		if i := Fireworks.lookupIndex(lower); i >= 0 {
			return i
		}
	} else if head, rest, found := strings.Cut(lower, "/"); found {
		if p, ok := ParseProvider(head); ok {
			if i := p.lookupIndex(rest); i >= 0 {
				return i
			}
		}
	}
	if i := resolve(lower, func(r *Price) bool { return !platforms[r.Provider] }); i >= 0 {
		return i
	}
	return resolve(lower, func(r *Price) bool { return platforms[r.Provider] })
}

// -- model ids --------------------------------------------------------------------------------

var prefixes = []string{
	"models/",
	"anthropic/",
	"openai/",
	"google/",
	"gemini/",
	"vertex_ai/",
	"bedrock/",
	"azure/",
	"openrouter/",
	"deepseek/",
	"mistral/",
	"mistralai/",
	"xai/",
	"x-ai/",
	"cohere/",
	"groq/",
	"together/",
	"together_ai/",
	"fireworks/",
	"fireworks_ai/",
	"accounts/fireworks/models/",
	// Bedrock's vendor prefix with an optional cross-region inference profile.
	"us.anthropic.",
	"eu.anthropic.",
	"apac.anthropic.",
	"global.anthropic.",
	"anthropic.",
}

func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; 'A' <= c && c <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

func eqCI(a, b string) bool {
	return len(a) == len(b) && asciiLower(a) == asciiLower(b)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func stripPrefix(s string) string {
	for {
		before := s
		for _, p := range prefixes {
			if len(s) >= len(p) && eqCI(s[:len(p)], p) {
				s = s[len(p):]
			}
		}
		if s == before {
			return s
		}
	}
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

// stripDate returns id without a trailing -YYYYMMDD (Anthropic) or -YYYY-MM-DD (OpenAI).
func stripDate(id string) (string, bool) {
	head, tail, found := cutLast(id, "-")
	if !found {
		return "", false
	}
	if len(tail) == 8 && strings.HasPrefix(tail, "20") && allDigits(tail) {
		return head, true
	}
	rest, month, found := cutLast(head, "-")
	if !found {
		return "", false
	}
	base, year, found := cutLast(rest, "-")
	if !found {
		return "", false
	}
	shape := len(tail) == 2 && len(month) == 2 && len(year) == 4 && strings.HasPrefix(year, "20")
	if shape && allDigits(tail) && allDigits(month) && allDigits(year) && base != "" {
		return base, true
	}
	return "", false
}

// stripVersion returns id without a trailing -latest, then without a trailing snapshot date.
func stripVersion(id string) string {
	s := id
	if len(id) > 7 && eqCI(id[len(id)-7:], "-latest") {
		s = id[:len(id)-7]
	}
	if d, ok := stripDate(s); ok {
		return d
	}
	return s
}

// normalizeID returns the id without provider prefixes, without Bedrock's -v1:0 suffix, and
// with Vertex's @ turned into -.
func normalizeID(lower string) string {
	s := stripPrefix(lower)
	if i := strings.Index(s, "-v"); i >= 0 {
		rest := s[i+2:]
		if rest != "" && rest[0] >= '0' && rest[0] <= '9' && !strings.Contains(rest, "-") {
			s = s[:i]
		}
	}
	return strings.Replace(s, "@", "-", 1)
}

func ids(p *Price) []string {
	return append([]string{p.Model}, p.Aliases...)
}

func resolve(id string, keep func(*Price) bool) int {
	var rows []int
	for i := range table {
		if keep(&table[i]) {
			rows = append(rows, i)
		}
	}
	lower := asciiLower(strings.TrimSpace(id))
	for _, i := range rows {
		for _, m := range ids(&table[i]) {
			if eqCI(m, lower) {
				return i
			}
		}
	}
	s := normalizeID(lower)
	if s == "" {
		return -1
	}
	strippedEq := func(want string) int {
		for _, i := range rows {
			for _, m := range ids(&table[i]) {
				if eqCI(stripPrefix(m), want) {
					return i
				}
			}
		}
		return -1
	}
	if i := strippedEq(s); i >= 0 {
		return i
	}
	base := stripVersion(s)
	if base != s {
		if i := strippedEq(base); i >= 0 {
			return i
		}
	}
	for _, i := range rows {
		for _, m := range ids(&table[i]) {
			if u, ok := stripDate(stripPrefix(m)); ok && eqCI(u, base) {
				return i
			}
		}
	}
	return -1
}
