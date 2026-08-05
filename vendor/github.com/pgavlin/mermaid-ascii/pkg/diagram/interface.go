package diagram

// Diagram is the interface for all diagram types (graph, sequence, etc.)
type Diagram interface {
	// Parse parses the given Mermaid input text into the diagram's internal model.
	Parse(input string) error
	// Render renders the parsed diagram as a string using the provided configuration.
	Render(config *Config) (string, error)
	// Type returns the diagram type identifier (e.g., "graph", "sequence").
	Type() string
}
