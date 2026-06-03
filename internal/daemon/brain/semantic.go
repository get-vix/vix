package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/get-vix/vix/internal/config"
)

const (
	semanticModel   = "claude-sonnet-4-5-20250929"
	maxInputTokens  = 8000
	maxRetries      = 3
)

func callLLM(ctx context.Context, client anthropic.Client, system, user string) (string, error) {
	for attempt := range maxRetries {
		msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(semanticModel),
			MaxTokens: 2048,
			System:    []anthropic.TextBlockParam{{Text: system}},
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock(user)),
			},
		})
		if err != nil {
			if attempt == maxRetries-1 {
				LogError("LLM call failed after %d retries: %v", maxRetries, err)
				return fmt.Sprintf("(Analysis unavailable: %v)", err), nil
			}
			wait := time.Duration(math.Pow(2, float64(attempt)))*time.Second + time.Duration(rand.Float64()*1000)*time.Millisecond
			LogWarn("LLM call attempt %d failed: %v, retrying in %v", attempt+1, err, wait)
			time.Sleep(wait)
			continue
		}
		if len(msg.Content) > 0 {
			if tb, ok := msg.Content[0].AsAny().(anthropic.TextBlock); ok {
				return tb.Text, nil
			}
		}
		return "", nil
	}
	return "(Analysis unavailable)", nil
}

func generateProjectSummary(ctx context.Context, client anthropic.Client, index *BrainIndex, brainDir string) (string, error) {
	system := "You are a senior software engineer writing concise project documentation. Be dense and precise. No fluff."

	filesByLang := make(map[string]int)
	for _, f := range index.Files {
		lang := f.Language
		if lang == "" {
			lang = "other"
		}
		filesByLang[lang]++
	}

	entryPoints, _ := json.Marshal(index.Project.EntryPoints)
	frameworks, _ := json.Marshal(index.Project.Frameworks)

	maxDeps := 30
	if len(index.Project.ExternalDeps) < maxDeps {
		maxDeps = len(index.Project.ExternalDeps)
	}
	deps, _ := json.Marshal(index.Project.ExternalDeps[:maxDeps])

	maxHubs := 10
	if len(index.HubFiles) < maxHubs {
		maxHubs = len(index.HubFiles)
	}
	hubPaths := make([]string, maxHubs)
	for i := 0; i < maxHubs; i++ {
		hubPaths[i] = index.HubFiles[i].Path
	}
	hubs, _ := json.Marshal(hubPaths)

	maxSyms := 50
	if len(index.Symbols) < maxSyms {
		maxSyms = len(index.Symbols)
	}
	symNames := make([]string, maxSyms)
	for i := 0; i < maxSyms; i++ {
		symNames[i] = index.Symbols[i].Name
	}
	syms, _ := json.Marshal(symNames)

	langJSON, _ := json.Marshal(filesByLang)
	user := fmt.Sprintf(`Analyze this project and write one paragraph summarizing what it does and how it's structured.

Project: %s
Files: %d (%d lines)
Languages: %s
Entry points: %s
Frameworks: %s
External deps: %s
Hub files: %s
Key symbols: %s`,
		index.Project.Name, index.Project.TotalFiles, index.Project.TotalLines,
		langJSON, entryPoints, frameworks, deps, hubs, syms)

	user = TruncateToTokens(user, maxInputTokens)
	result, err := callLLM(ctx, client, system, user)
	if err != nil {
		return "", err
	}
	WriteMarkdown(brainDir, "context/project-summary.md", fmt.Sprintf("# %s\n\n%s\n", index.Project.Name, result))
	LogInfo("Generated project-summary.md")
	return result, nil
}

// RunPhase2 runs all Phase 2 semantic analysis.
func RunPhase2(ctx context.Context, cred config.Credential, root string, index *BrainIndex, brainDir string) error {
	client := anthropic.NewClient(cred.RequestOptions()...)

	// 1. Project summary
	if _, err := generateProjectSummary(ctx, client, index, brainDir); err != nil {
		LogError("Project summary failed: %v", err)
	}

	LogInfo("Phase 2 complete")
	return nil
}
