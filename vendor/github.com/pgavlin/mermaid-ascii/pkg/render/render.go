// Package render provides the top-level API for rendering Mermaid diagrams
// as ASCII/Unicode text. It auto-detects diagram types and dispatches to
// the appropriate parser and renderer.
//
// Usage:
//
//	output, err := render.Render(mermaidInput, diagram.DefaultConfig())
package render

import (
	"fmt"
	"strings"

	"github.com/pgavlin/mermaid-ascii/pkg/architecture"
	"github.com/pgavlin/mermaid-ascii/pkg/blockdiagram"
	"github.com/pgavlin/mermaid-ascii/pkg/c4"
	"github.com/pgavlin/mermaid-ascii/pkg/classdiagram"
	"github.com/pgavlin/mermaid-ascii/pkg/diagram"
	"github.com/pgavlin/mermaid-ascii/pkg/erdiagram"
	"github.com/pgavlin/mermaid-ascii/pkg/gantt"
	"github.com/pgavlin/mermaid-ascii/pkg/gitgraph"
	"github.com/pgavlin/mermaid-ascii/pkg/graph"
	"github.com/pgavlin/mermaid-ascii/pkg/journey"
	"github.com/pgavlin/mermaid-ascii/pkg/kanban"
	"github.com/pgavlin/mermaid-ascii/pkg/mindmap"
	"github.com/pgavlin/mermaid-ascii/pkg/packet"
	"github.com/pgavlin/mermaid-ascii/pkg/piechart"
	"github.com/pgavlin/mermaid-ascii/pkg/quadrant"
	"github.com/pgavlin/mermaid-ascii/pkg/requirement"
	"github.com/pgavlin/mermaid-ascii/pkg/sankey"
	"github.com/pgavlin/mermaid-ascii/pkg/sequence"
	"github.com/pgavlin/mermaid-ascii/pkg/statediagram"
	"github.com/pgavlin/mermaid-ascii/pkg/timeline"
	"github.com/pgavlin/mermaid-ascii/pkg/xychart"
	"github.com/pgavlin/mermaid-ascii/pkg/zenuml"
)

// Render parses and renders a Mermaid diagram as ASCII/Unicode text.
// It auto-detects the diagram type from the input.
func Render(input string, config *diagram.Config) (string, error) {
	if config == nil {
		config = diagram.DefaultConfig()
	}

	// Normalize escaped newlines (e.g. from curl) once at the entry point,
	// so individual parsers don't need to handle this.
	input = strings.ReplaceAll(input, `\n`, "\n")

	diag, err := Detect(input)
	if err != nil {
		return "", fmt.Errorf("failed to detect diagram type: %w", err)
	}

	if err := diag.Parse(input); err != nil {
		return "", fmt.Errorf("failed to parse %s diagram: %w", diag.Type(), err)
	}

	output, err := diag.Render(config)
	if err != nil {
		return "", fmt.Errorf("failed to render %s diagram: %w", diag.Type(), err)
	}

	return output, nil
}

// detector pairs a detection function with a diagram constructor.
type detector struct {
	detect func(input string) bool
	create func() diagram.Diagram
}

// detectors lists all supported diagram types, checked in order.
// Graph/flowchart is the fallback and is not in this list.
var detectors = []detector{
	{sequence.IsSequenceDiagram, func() diagram.Diagram { return &wrapper[sequence.SequenceDiagram]{name: "sequence", parse: sequence.Parse, render: sequence.Render} }},
	{classdiagram.IsClassDiagram, func() diagram.Diagram { return &wrapper[classdiagram.ClassDiagram]{name: "classDiagram", parse: classdiagram.Parse, render: classdiagram.Render} }},
	{statediagram.IsStateDiagram, func() diagram.Diagram { return &wrapper[statediagram.StateDiagram]{name: "stateDiagram", parse: statediagram.Parse, render: statediagram.Render} }},
	{erdiagram.IsERDiagram, func() diagram.Diagram { return &wrapper[erdiagram.ERDiagram]{name: "erDiagram", parse: erdiagram.Parse, render: erdiagram.Render} }},
	{gantt.IsGanttDiagram, func() diagram.Diagram { return &wrapper[gantt.GanttDiagram]{name: "gantt", parse: gantt.Parse, render: gantt.Render} }},
	{piechart.IsPieChart, func() diagram.Diagram { return &wrapper[piechart.PieChart]{name: "pie", parse: piechart.Parse, render: piechart.Render} }},
	{mindmap.IsMindmapDiagram, func() diagram.Diagram { return &wrapper[mindmap.MindmapDiagram]{name: "mindmap", parse: mindmap.Parse, render: mindmap.Render} }},
	{timeline.IsTimelineDiagram, func() diagram.Diagram { return &wrapper[timeline.TimelineDiagram]{name: "timeline", parse: timeline.Parse, render: timeline.Render} }},
	{gitgraph.IsGitGraph, func() diagram.Diagram { return &wrapper[gitgraph.GitGraph]{name: "gitGraph", parse: gitgraph.Parse, render: gitgraph.Render} }},
	{journey.IsJourneyDiagram, func() diagram.Diagram { return &wrapper[journey.JourneyDiagram]{name: "journey", parse: journey.Parse, render: journey.Render} }},
	{quadrant.IsQuadrantChart, func() diagram.Diagram { return &wrapper[quadrant.QuadrantChart]{name: "quadrantChart", parse: quadrant.Parse, render: quadrant.Render} }},
	{xychart.IsXYChart, func() diagram.Diagram { return &wrapper[xychart.XYChart]{name: "xychart-beta", parse: xychart.Parse, render: xychart.Render} }},
	{c4.IsC4Diagram, func() diagram.Diagram { return &wrapper[c4.C4Diagram]{name: "C4Context", parse: c4.Parse, render: c4.Render} }},
	{requirement.IsRequirementDiagram, func() diagram.Diagram { return &wrapper[requirement.RequirementDiagram]{name: "requirementDiagram", parse: requirement.Parse, render: requirement.Render} }},
	{blockdiagram.IsBlockDiagram, func() diagram.Diagram { return &wrapper[blockdiagram.BlockDiagram]{name: "block-beta", parse: blockdiagram.Parse, render: blockdiagram.Render} }},
	{sankey.IsSankeyDiagram, func() diagram.Diagram { return &wrapper[sankey.SankeyDiagram]{name: "sankey-beta", parse: sankey.Parse, render: sankey.Render} }},
	{packet.IsPacketDiagram, func() diagram.Diagram { return &wrapper[packet.PacketDiagram]{name: "packet-beta", parse: packet.Parse, render: packet.Render} }},
	{kanban.IsKanbanBoard, func() diagram.Diagram { return &wrapper[kanban.KanbanBoard]{name: "kanban", parse: kanban.Parse, render: kanban.Render} }},
	{architecture.IsArchitectureDiagram, func() diagram.Diagram { return &wrapper[architecture.ArchitectureDiagram]{name: "architecture-beta", parse: architecture.Parse, render: architecture.Render} }},
	{zenuml.IsZenUML, func() diagram.Diagram { return &wrapper[zenuml.ZenUMLDiagram]{name: "zenuml", parse: zenuml.Parse, render: zenuml.Render} }},
}

// Detect identifies the diagram type from the input text and returns
// the appropriate Diagram implementation.
func Detect(input string) (diagram.Diagram, error) {
	input = strings.TrimSpace(input)
	for _, d := range detectors {
		if d.detect(input) {
			return d.create(), nil
		}
	}
	// Graph/flowchart is the default fallback
	return &wrapper[graph.Properties]{name: "graph", parse: graph.Parse, render: graph.Render}, nil
}

// wrapper is a generic diagram adapter for diagram types that follow the
// Parse(string) → (*T, error), Render(*T, *Config) → (string, error) pattern.
type wrapper[T any] struct {
	name   string
	parse  func(string) (*T, error)
	render func(*T, *diagram.Config) (string, error)
	parsed *T
}

func (w *wrapper[T]) Parse(input string) error {
	p, err := w.parse(input)
	if err != nil {
		return err
	}
	w.parsed = p
	return nil
}

func (w *wrapper[T]) Render(config *diagram.Config) (string, error) {
	if w.parsed == nil {
		return "", fmt.Errorf("not parsed")
	}
	return w.render(w.parsed, config)
}

func (w *wrapper[T]) Type() string { return w.name }

