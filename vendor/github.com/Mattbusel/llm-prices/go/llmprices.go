// Package llmprices holds dated, source-cited LLM API prices (Anthropic, OpenAI, Google
// Gemini, DeepSeek, Mistral, xAI, Cohere, Groq, Together AI and Fireworks AI) in USD per 1M
// tokens, with model-id lookup and per-request costs.
//
// The table is prices.json from https://github.com/Mattbusel/llm-prices, embedded at build
// time: no network access, no dependencies. Every row carries the provider's pricing page
// (SourceURL) and the date it was read (Checked). It is the same table, with the same lookup
// rules, as the Rust crate, the Python package and the JavaScript module.
//
//	p, ok := llmprices.LookupProvider("google_genai", "gemini-2.5-flash")
//	// p.Input = 0.3, p.Output = 2.5 (USD per 1M tokens)
//	usd, ok := llmprices.Cost("claude-sonnet-4-6", 12_000, 800)
//
// Lookup accepts aliases, any ASCII case, dated snapshots ("gpt-4o-2024-08-06" finds
// "gpt-4o" unless the snapshot has its own row), "-latest" ids, Bedrock ids
// ("us.anthropic.claude-sonnet-4-20250514-v1:0"), Vertex "@date" ids and provider prefixes
// ("models/", "openai/", "together_ai/", ...). When the caller knows which provider served a
// request, LookupProvider only searches the rows that provider sells.
//
// A false ok means the table has no price: keep your own fallback, never treat it as zero.
package llmprices

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
)

//go:embed prices.json
var pricesJSON []byte

// Generated is the date the table was generated (ISO 8601).
var Generated string

// LongContext holds the prices that apply when a request is over a provider's long-context
// threshold. Nil fields mean the page lists no price.
type LongContext struct {
	// Requests whose prompt is over this many tokens pay these prices. The prompt counts
	// uncached input, cache reads and cache writes together.
	ThresholdTokens int64
	// Uncached input price above the threshold, USD per 1M tokens.
	Input *float64
	// Output price above the threshold (reasoning tokens included), USD per 1M tokens.
	Output *float64
	// Price of input tokens served from the prompt cache above the threshold.
	CacheRead *float64
	// Cache write price per cache lifetime, for example {"5m": 0.625}.
	CacheWrite map[string]float64
	// Batch API input price above the threshold, USD per 1M tokens.
	BatchInput *float64
	// Batch API output price above the threshold, USD per 1M tokens.
	BatchOutput *float64
}

// Price is one model's prices in USD per 1M tokens. A nil price field means the provider's
// page lists no price for it.
type Price struct {
	// Provider is the company that sells the model at these prices: "anthropic", "openai",
	// "google", "deepseek", "mistral", "xai", "cohere", "groq", "together" or "fireworks".
	Provider string
	// Model is the API model id.
	Model string
	// Name is the model's display name on the pricing page ("" when it has none).
	Name string
	// Aliases are other ids the provider documents for the same model.
	Aliases []string
	// Status is "listed", "retired" (still sold on some cloud platforms) or "limited".
	Status string
	// Input is the uncached input price, USD per 1M tokens.
	Input *float64
	// Output is the output price, USD per 1M tokens. Reasoning tokens bill at this price.
	// Nil for embedding models.
	Output *float64
	// CacheRead is the price of input tokens served from the prompt cache.
	CacheRead *float64
	// CacheWrite is the cache write price per cache lifetime, for example
	// {"5m": 3.75, "1h": 6}. Empty when the provider does not charge for cache writes.
	CacheWrite map[string]float64
	// BatchInput is the Batch API input price, USD per 1M tokens.
	BatchInput *float64
	// BatchOutput is the Batch API output price, USD per 1M tokens.
	BatchOutput *float64
	// LongContext holds prices for requests over a long-context threshold, or nil.
	LongContext *LongContext
	// SourceURL is the provider's official pricing page this row was read from.
	SourceURL string
	// Checked is the ISO date the row was checked against SourceURL.
	Checked string
	// Note holds notes from the table (tiers, conditions, why a price is missing).
	Note string
}

// Usage holds the token counts of one API response, the input to Price.CostUsage. Fields you
// do not set stay zero.
//
// From provider responses: Input is Anthropic's input_tokens, OpenAI's prompt_tokens minus
// cached_tokens, or Gemini's promptTokenCount minus cachedContentTokenCount. CacheRead is
// cache_read_input_tokens, prompt_tokens_details.cached_tokens or cachedContentTokenCount.
// CacheWrite5m and CacheWrite1h are Anthropic's cache_creation.ephemeral_5m_input_tokens and
// ephemeral_1h_input_tokens (a bare cache_creation_input_tokens is the 5-minute lifetime).
// Output is output_tokens, completion_tokens, or candidatesTokenCount plus
// thoughtsTokenCount.
type Usage struct {
	// Uncached input tokens. Do not include tokens counted in CacheRead or the cache writes.
	Input int64
	// Output tokens, reasoning or thinking tokens included.
	Output int64
	// Input tokens served from the prompt cache.
	CacheRead int64
	// Input tokens written to the cache with a 5-minute lifetime.
	CacheWrite5m int64
	// Input tokens written to the cache with a 1-hour lifetime.
	CacheWrite1h int64
	// The request went through the provider's Batch API.
	Batch bool
}

// PromptTokens is uncached input plus cache reads plus cache writes: the number compared
// against a model's long-context threshold.
func (u Usage) PromptTokens() int64 {
	return u.Input + u.CacheRead + u.CacheWrite5m + u.CacheWrite1h
}

// CostBreakdown is the cost in USD of one response, split by token kind.
type CostBreakdown struct {
	Input      float64 // uncached input tokens
	Output     float64 // output tokens
	CacheRead  float64 // input tokens served from the prompt cache
	CacheWrite float64 // cache writes, all lifetimes together
	Total      float64 // sum of the other fields
	// LongContext is true when the prompt was over the model's long-context threshold, so
	// long-context prices applied.
	LongContext bool
}

const perM = 1_000_000.0

// InputPerToken is Input in USD per token. ok is false when the page lists no price.
func (p Price) InputPerToken() (usd float64, ok bool) {
	if p.Input == nil {
		return 0, false
	}
	return *p.Input / perM, true
}

// OutputPerToken is Output in USD per token. ok is false when the page lists no price
// (embedding models).
func (p Price) OutputPerToken() (usd float64, ok bool) {
	if p.Output == nil {
		return 0, false
	}
	return *p.Output / perM, true
}

// Cost is the standard (non-batch, uncached) cost in USD of one request. It uses the
// long-context prices when inputTokens is over the model's threshold. ok is false when a
// needed price is not listed or a token count is negative.
//
// When the response reports cached tokens use CostUsage: cached tokens count toward the
// long-context threshold and bill at their own price.
func (p Price) Cost(inputTokens, outputTokens int64) (usd float64, ok bool) {
	c, ok := p.CostUsage(Usage{Input: inputTokens, Output: outputTokens})
	return c.Total, ok
}

// CostUsage is the cost in USD of one response from its token counts, split by token kind.
//
// Rules (the same as the Rust crate's Price::cost_usage):
//   - Long context: when the model has a LongContext tier and u.PromptTokens() is strictly
//     greater than its threshold, every token bills at the long-context prices.
//   - Batch: when u.Batch is set, input and output bill at the batch prices; cache reads and
//     writes get the same discount as input (batch input price over standard input price).
//   - Cache reads bill at the cache-read price, or as ordinary input when none is listed.
//   - Cache writes bill at CacheWrite["5m"] and CacheWrite["1h"]. Other lifetimes (the
//     OpenAI rows' "30m" and "12h") are not priced here: add tokens * CacheWrite["30m"] / 1e6.
//
// ok is false when a price it needs is not listed (input or output, batch prices for a batch
// request, long-context prices above the threshold, the cache-write price for a lifetime
// with a non-zero token count) or when a token count is negative.
func (p Price) CostUsage(u Usage) (c CostBreakdown, ok bool) {
	if u.Input < 0 || u.Output < 0 || u.CacheRead < 0 || u.CacheWrite5m < 0 || u.CacheWrite1h < 0 {
		return CostBreakdown{}, false
	}
	t := tier{p.Input, p.Output, p.CacheRead, p.CacheWrite, p.BatchInput, p.BatchOutput}
	lc := false
	if l := p.LongContext; l != nil && u.PromptTokens() > l.ThresholdTokens {
		t = tier{l.Input, l.Output, l.CacheRead, l.CacheWrite, l.BatchInput, l.BatchOutput}
		lc = true
	}
	if t.input == nil {
		return CostBreakdown{}, false
	}
	standardInput := *t.input
	var inRate, outRate, discount float64
	if u.Batch {
		if t.batchInput == nil || t.batchOutput == nil {
			return CostBreakdown{}, false
		}
		inRate, outRate = *t.batchInput, *t.batchOutput
		discount = 1.0
		if standardInput > 0 {
			discount = inRate / standardInput
		}
	} else {
		if t.output == nil {
			return CostBreakdown{}, false
		}
		inRate, outRate, discount = standardInput, *t.output, 1.0
	}
	readRate := inRate
	if t.cacheRead != nil {
		readRate = float64(*t.cacheRead * discount)
	}
	write := func(ttl string, tokens int64) (float64, bool) {
		if tokens == 0 {
			return 0, true
		}
		rate, ok := t.cacheWrite[ttl]
		if !ok {
			return 0, false
		}
		return float64(float64(float64(tokens)*rate) * discount), true
	}
	w5, ok5 := write("5m", u.CacheWrite5m)
	w1, ok1 := write("1h", u.CacheWrite1h)
	if !ok5 || !ok1 {
		return CostBreakdown{}, false
	}
	// Each product is tokens x USD-per-1M; the total adds the products before dividing once,
	// in the same order as the Rust crate and the Python package. The float64 conversions
	// keep the compiler from fusing a multiply and an add, so results are bit-for-bit equal
	// to theirs on every architecture.
	pInput := float64(float64(u.Input) * inRate)
	pRead := float64(float64(u.CacheRead) * readRate)
	pWrite := float64(w5 + w1)
	pOutput := float64(float64(u.Output) * outRate)
	return CostBreakdown{
		Input:       pInput / perM,
		Output:      pOutput / perM,
		CacheRead:   pRead / perM,
		CacheWrite:  pWrite / perM,
		Total:       float64(float64(float64(pInput+pRead)+pWrite)+pOutput) / perM,
		LongContext: lc,
	}, true
}

type tier struct {
	input, output, cacheRead *float64
	cacheWrite               map[string]float64
	batchInput, batchOutput  *float64
}

// All returns every row of the table, in prices.json order: Anthropic, OpenAI, Google and
// DeepSeek first, then Mistral, xAI, Cohere, Groq, Together AI and Fireworks AI. The slice
// and its rows are copies; changing them does not change the table.
func All() []Price {
	out := make([]Price, len(table))
	for i := range table {
		out[i] = table[i].clone()
	}
	return out
}

// Lookup finds a model by id across every provider in the table. See the package doc for
// the id forms it accepts. When the id starts with a provider name and a slash ("groq/",
// "together_ai/", ...), that provider is tried first; otherwise the first matching row in
// table order wins, model makers before the hosts that resell open-weight models. Use
// LookupProvider whenever you know which provider served the request.
func Lookup(model string) (Price, bool) {
	i := lookupIndex(model)
	if i < 0 {
		return Price{}, false
	}
	return table[i].clone(), true
}

// LookupProvider finds model as sold by provider. provider is any name ParseProvider
// accepts ("openai", "google_genai", "vertex_ai", "bedrock_converse", "azure_openai",
// "together_ai", ...). Only rows that provider sells are searched; Vertex AI, Bedrock, Azure
// and OpenRouter, which have no rows of their own, fall back to the list price of the model
// makers they resell, and the returned row's Provider says whose price it is. ok is false
// when the provider name is unknown ("ollama") or the table has no price for the model there.
func LookupProvider(provider, model string) (Price, bool) {
	pv, ok := ParseProvider(provider)
	if !ok {
		return Price{}, false
	}
	return pv.Lookup(model)
}

// Cost is the standard (non-batch, uncached) cost in USD of one request to model, found
// with Lookup. ok is false when the model is not in the table or a needed price is not
// listed.
func Cost(model string, inputTokens, outputTokens int64) (usd float64, ok bool) {
	p, ok := Lookup(model)
	if !ok {
		return 0, false
	}
	return p.Cost(inputTokens, outputTokens)
}

// CostProvider is Cost for a model found with LookupProvider.
func CostProvider(provider, model string, inputTokens, outputTokens int64) (usd float64, ok bool) {
	p, ok := LookupProvider(provider, model)
	if !ok {
		return 0, false
	}
	return p.Cost(inputTokens, outputTokens)
}

func (p Price) clone() Price {
	q := p
	q.Aliases = append([]string(nil), p.Aliases...)
	q.Input, q.Output, q.CacheRead = clonef(p.Input), clonef(p.Output), clonef(p.CacheRead)
	q.BatchInput, q.BatchOutput = clonef(p.BatchInput), clonef(p.BatchOutput)
	q.CacheWrite = clonem(p.CacheWrite)
	if p.LongContext != nil {
		l := *p.LongContext
		l.Input, l.Output, l.CacheRead = clonef(l.Input), clonef(l.Output), clonef(l.CacheRead)
		l.BatchInput, l.BatchOutput = clonef(l.BatchInput), clonef(l.BatchOutput)
		l.CacheWrite = clonem(l.CacheWrite)
		q.LongContext = &l
	}
	return q
}

func clonef(v *float64) *float64 {
	if v == nil {
		return nil
	}
	x := *v
	return &x
}

func clonem(m map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// -- loading prices.json ----------------------------------------------------------------------

type rawTier struct {
	Input       *json.Number           `json:"input"`
	Output      *json.Number           `json:"output"`
	CacheRead   *json.Number           `json:"cache_read"`
	CacheWrite  map[string]json.Number `json:"cache_write"`
	BatchInput  *json.Number           `json:"batch_input"`
	BatchOutput *json.Number           `json:"batch_output"`
}

type rawRow struct {
	rawTier
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	Name        *string  `json:"name"`
	Aliases     []string `json:"aliases"`
	Status      string   `json:"status"`
	SourceURL   string   `json:"source_url"`
	Checked     string   `json:"checked"`
	Note        *string  `json:"note"`
	LongContext *struct {
		rawTier
		ThresholdTokens int64 `json:"threshold_tokens"`
	} `json:"long_context"`
}

var table = mustLoad(pricesJSON)

func mustLoad(b []byte) []Price {
	var doc struct {
		Generated string   `json:"generated"`
		Models    []rawRow `json:"models"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		panic("llmprices: embedded prices.json: " + err.Error())
	}
	Generated = doc.Generated
	rows := make([]Price, 0, len(doc.Models))
	for _, r := range doc.Models {
		p := Price{
			Provider:  r.Provider,
			Model:     r.Model,
			Aliases:   r.Aliases,
			Status:    r.Status,
			SourceURL: r.SourceURL,
			Checked:   r.Checked,
		}
		if p.Aliases == nil {
			p.Aliases = []string{}
		}
		if r.Name != nil {
			p.Name = *r.Name
		}
		if r.Note != nil {
			p.Note = *r.Note
		}
		where := r.Provider + "/" + r.Model
		p.Input, p.Output, p.CacheRead = num(r.Input, where), num(r.Output, where), num(r.CacheRead, where)
		p.CacheWrite = nums(r.CacheWrite, where)
		p.BatchInput, p.BatchOutput = num(r.BatchInput, where), num(r.BatchOutput, where)
		if l := r.LongContext; l != nil {
			p.LongContext = &LongContext{
				ThresholdTokens: l.ThresholdTokens,
				Input:           num(l.Input, where),
				Output:          num(l.Output, where),
				CacheRead:       num(l.CacheRead, where),
				CacheWrite:      nums(l.CacheWrite, where),
				BatchInput:      num(l.BatchInput, where),
				BatchOutput:     num(l.BatchOutput, where),
			}
		}
		rows = append(rows, p)
	}
	return rows
}

// num parses a price as written in prices.json. strconv.ParseFloat rounds correctly, so the
// float is the one Rust, Python and JavaScript read from the same text.
func num(n *json.Number, where string) *float64 {
	if n == nil {
		return nil
	}
	v, err := strconv.ParseFloat(n.String(), 64)
	if err != nil {
		panic(fmt.Sprintf("llmprices: embedded prices.json: %s: %v", where, err))
	}
	return &v
}

func nums(m map[string]json.Number, where string) map[string]float64 {
	out := make(map[string]float64, len(m))
	for k, n := range m {
		out[k] = *num(&n, where)
	}
	return out
}
