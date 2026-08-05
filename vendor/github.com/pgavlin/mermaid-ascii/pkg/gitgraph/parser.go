// Package gitgraph provides parsing and rendering of Mermaid gitGraph diagrams.
package gitgraph

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/parser"
)

// gitGraphKeyword is the keyword that identifies a gitGraph diagram in Mermaid syntax.
const gitGraphKeyword = "gitGraph"

// GitGraph represents a parsed git graph diagram containing commits and branches.
type GitGraph struct {
	Commits       []*Commit
	Branches      []*Branch
	CurrentBranch string
}

// Commit represents a single commit in the git graph.
type Commit struct {
	ID      string
	Message string
	Tag     string
	Type    CommitType
	Branch  string
	Parents []string
	Lane    int
}

// Branch represents a git branch with its display lane position.
type Branch struct {
	Name        string
	Lane        int
	StartCommit string
}

// CommitType represents the visual type of a commit node.
type CommitType int

const (
	// Normal represents a standard commit.
	Normal CommitType = iota
	// Reverse represents a reversed commit, displayed with a distinct marker.
	Reverse
	// Highlight represents a highlighted commit, displayed with emphasis.
	Highlight
)

// IsGitGraph reports whether the input text is a gitGraph diagram.
func IsGitGraph(input string) bool {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "%%") {
			continue
		}
		return trimmed == gitGraphKeyword
	}
	return false
}

// Parse parses Mermaid gitGraph text into a GitGraph.
func Parse(input string) (*GitGraph, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	s := parser.NewScanner(input)
	s.SkipNewlines()

	tok := s.Peek()
	if tok.Kind != parser.TokenIdent || tok.Text != gitGraphKeyword {
		return nil, fmt.Errorf("expected %q keyword", gitGraphKeyword)
	}
	s.Next()
	s.SkipNewlines()

	gg := &GitGraph{
		Commits:       []*Commit{},
		Branches:      []*Branch{},
		CurrentBranch: "main",
	}

	mainBranch := &Branch{Name: "main", Lane: 0}
	gg.Branches = append(gg.Branches, mainBranch)
	branchMap := map[string]*Branch{"main": mainBranch}
	nextLane := 1
	commitCounter := 0

	for !s.AtEnd() {
		s.SkipNewlines()
		if s.AtEnd() {
			break
		}

		tok := s.Peek()
		if tok.Kind != parser.TokenIdent {
			parser.SkipToEndOfLine(s)
			continue
		}

		switch tok.Text {
		case "branch":
			s.Next()
			s.SkipWhitespace()
			name := ""
			if s.Peek().Kind == parser.TokenIdent {
				name = s.Next().Text
			}
			if name != "" {
				if _, exists := branchMap[name]; !exists {
					branch := &Branch{Name: name, Lane: nextLane}
					if len(gg.Commits) > 0 {
						branch.StartCommit = gg.Commits[len(gg.Commits)-1].ID
					}
					gg.Branches = append(gg.Branches, branch)
					branchMap[name] = branch
					nextLane++
				}
			}
			parser.SkipToEndOfLine(s)

		case "checkout":
			s.Next()
			s.SkipWhitespace()
			if s.Peek().Kind == parser.TokenIdent {
				name := s.Next().Text
				if _, exists := branchMap[name]; exists {
					gg.CurrentBranch = name
				}
			}
			parser.SkipToEndOfLine(s)

		case "commit":
			s.Next()
			s.SkipWhitespace()
			id, msg, tag, commitType := parseCommitArgs(s)
			if id == "" {
				id = fmt.Sprintf("c%d", commitCounter)
			}
			commitCounter++

			branch := branchMap[gg.CurrentBranch]
			gg.Commits = append(gg.Commits, &Commit{
				ID:      id,
				Message: msg,
				Tag:     tag,
				Type:    commitType,
				Branch:  gg.CurrentBranch,
				Lane:    branch.Lane,
			})

		case "merge":
			s.Next()
			s.SkipWhitespace()
			sourceBranch := ""
			if s.Peek().Kind == parser.TokenIdent {
				sourceBranch = s.Next().Text
			}
			s.SkipWhitespace()
			id, _, tag, _ := parseCommitArgs(s)
			if id == "" {
				id = fmt.Sprintf("m%d", commitCounter)
			}
			commitCounter++

			branch := branchMap[gg.CurrentBranch]
			gg.Commits = append(gg.Commits, &Commit{
				ID:      id,
				Tag:     tag,
				Branch:  gg.CurrentBranch,
				Lane:    branch.Lane,
				Parents: []string{sourceBranch},
			})

		case "cherry":
			// "cherry-pick" tokenizes as Ident("cherry") Operator("-") Ident("pick")
			s.Next()
			if s.Peek().Kind == parser.TokenOperator && s.Peek().Text == "-" {
				s.Next()
				if s.Peek().Kind == parser.TokenIdent && s.Peek().Text == "pick" {
					s.Next()
				}
			}
			s.SkipWhitespace()
			// Expect id: "commitId"
			cherryID := ""
			if s.Peek().Kind == parser.TokenIdent && s.Peek().Text == "id" {
				s.Next()
				if s.Peek().Kind == parser.TokenColon {
					s.Next()
					s.SkipWhitespace()
					if s.Peek().Kind == parser.TokenString {
						cherryID = s.Next().Text
					}
				}
			}
			commitCounter++
			branch := branchMap[gg.CurrentBranch]
			gg.Commits = append(gg.Commits, &Commit{
				ID:      fmt.Sprintf("cp%d", commitCounter),
				Message: "cherry-pick " + cherryID,
				Branch:  gg.CurrentBranch,
				Lane:    branch.Lane,
				Parents: []string{cherryID},
			})
			parser.SkipToEndOfLine(s)

		default:
			parser.SkipToEndOfLine(s)
		}
	}

	if len(gg.Commits) == 0 {
		return nil, fmt.Errorf("no commits found")
	}

	return gg, nil
}

// parseCommitArgs parses optional key:value args like id: "x" msg: "y" tag: "z" type: NORMAL
func parseCommitArgs(s *parser.Scanner) (id, msg, tag string, commitType CommitType) {
	for !s.AtEnd() {
		tok := s.Peek()
		if tok.Kind == parser.TokenNewline || tok.Kind == parser.TokenEOF {
			break
		}
		if tok.Kind != parser.TokenIdent {
			s.Next()
			continue
		}
		key := tok.Text
		s.Next()
		if s.Peek().Kind != parser.TokenColon {
			continue
		}
		s.Next() // consume ':'
		s.SkipWhitespace()

		switch key {
		case "id":
			if s.Peek().Kind == parser.TokenString {
				id = s.Next().Text
			}
		case "msg":
			if s.Peek().Kind == parser.TokenString {
				msg = s.Next().Text
			}
		case "tag":
			if s.Peek().Kind == parser.TokenString {
				tag = s.Next().Text
			}
		case "type":
			if s.Peek().Kind == parser.TokenIdent {
				switch s.Next().Text {
				case "REVERSE":
					commitType = Reverse
				case "HIGHLIGHT":
					commitType = Highlight
				}
			}
		}
		s.SkipWhitespace()
	}
	return
}
