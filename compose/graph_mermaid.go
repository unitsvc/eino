package compose

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// GenerateMermaidFlowchart generates a Mermaid flowchart string from the provided GraphInfo.
func GenerateMermaidFlowchart(info *GraphInfo) string {
	var buf bytes.Buffer
	buf.WriteString("graph TD\n")

	// escapeID replaces characters that are invalid in Mermaid node IDs.
	escapeID := func(s string) string {
		return strings.NewReplacer(
			"-", "_",
			".", "_",
			" ", "_",
			"(", "_",
			")", "_",
			"[", "_",
			"]", "_",
			"{", "_",
			"}", "_",
			"<", "_",
			">", "_",
			"/", "_",
			"\\", "_",
			"|", "_",
			"\"", "_",
			":", "_",
		).Replace(s)
	}

	// idMap maps the original node keys to valid Mermaid node IDs.
	idMap := make(map[string]string)
	for nodeKey := range info.Nodes {
		idMap[nodeKey] = "N_" + escapeID(nodeKey)
	}
	// Explicitly map the special 'start' and 'end' nodes.
	idMap["start"] = "StartNode"
	idMap["end"] = "EndNode"

	// seenEdges is used to prevent duplicate edges in the output.
	seenEdges := make(map[string]bool)
	// edgeKey creates a unique string key for an edge to check for duplicates.
	edgeKey := func(from, to string) string {
		return from + "-->" + to
	}

	// Add the special Start and End nodes to the chart.
	buf.WriteString("    StartNode([Start])\n")
	buf.WriteString("    EndNode([End])\n")

	// Add all user-defined nodes to the chart.
	for nodeKey, nodeInfo := range info.Nodes {
		id := idMap[nodeKey]
		component := nodeInfo.Component
		if component == "" {
			component = "Node"
		}
		buf.WriteString(fmt.Sprintf("    %s[\"%s: %s\"]\n", id, nodeKey, component))
	}
	buf.WriteString("\n")

	// --- Add Control Flow Edges (Edges) ---
	// These edges define the execution order of the nodes.
	for from, toList := range info.Edges {
		fromID, ok := idMap[from]
		if !ok {
			continue
		}
		for _, to := range toList {
			toID, ok := idMap[to]
			if !ok {
				continue
			}
			key := edgeKey(fromID, toID)
			if !seenEdges[key] {
				buf.WriteString(fmt.Sprintf("    %s --> %s\n", fromID, toID))
				seenEdges[key] = true
			}
		}
	}

	// --- Add Data Flow Edges (DataEdges) ---
	// These edges define the data passing between nodes.
	// They are rendered as dashed lines.
	for from, toList := range info.DataEdges {
		fromID, ok := idMap[from]
		if !ok {
			continue
		}
		for _, to := range toList {
			toID, ok := idMap[to]
			if !ok {
				continue
			}
			key := edgeKey(fromID, toID)
			if seenEdges[key] {
				continue // Skip if a solid control flow edge already exists
			}
			buf.WriteString(fmt.Sprintf("    %s -.-> %s\n", fromID, toID))
			seenEdges[key] = true
		}
	}

	// --- Add Branch Flow Edges ---
	// For nodes with a Branch, use the `endNodes` map to determine the next possible nodes.
	// This connects the decision node to its potential targets.
	for fromNode, branchList := range info.Branches {
		if len(branchList) == 0 {
			continue
		}

		fromID, ok := idMap[fromNode]
		if !ok {
			continue
		}

		for _, branch := range branchList {
			// Check if the branch has any defined end nodes.
			endNodes := branch.GetEndNode() // Assuming this is the correct method name.
			if len(endNodes) == 0 {
				log.Printf("Warning: Branch for node '%s' has no end nodes defined. Cannot generate outgoing edges.", fromNode)
				continue
			}

			// Generate a control flow edge to each target node in endNodes.
			for targetNode := range endNodes {
				toID, ok := idMap[targetNode]
				if !ok {
					log.Printf("Warning: Branch target node '%s' is not found in the node list.", targetNode)
					continue
				}

				key := edgeKey(fromID, toID)
				if !seenEdges[key] {
					buf.WriteString(fmt.Sprintf("    %s --> %s\n", fromID, toID))
					seenEdges[key] = true
				}
			}
		}
	}

	return buf.String()
}

// DrawMermaid handles Mermaid diagram generation and writing.
type DrawMermaid struct {
	path string
	name string
}

// Option defines a configuration function for DrawMermaid.
type DrawMermaidOption func(*DrawMermaid)

// WithPath sets the output directory for the Mermaid file.
func WithPath(path string) DrawMermaidOption {
	return func(d *DrawMermaid) {
		if path == "" {
			return
		}
		if absPath, err := filepath.Abs(filepath.Clean(path)); err == nil {
			d.path = absPath
		}
	}
}

// WithName sets the output file name for the Mermaid diagram.
func WithName(name string) DrawMermaidOption {
	return func(d *DrawMermaid) {
		if name != "" {
			d.name = filepath.Base(name)
		}
	}
}

// NewDrawMermaid creates a new DrawMermaid instance with optional configuration.
func NewDrawMermaid(opts ...DrawMermaidOption) *DrawMermaid {
	defaultPath, _ := filepath.Abs("./output")
	d := &DrawMermaid{
		path: defaultPath,
		name: "graph.mmd",
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// OnFinish writes the generated Mermaid diagram to file after graph compilation.
func (d *DrawMermaid) OnFinish(ctx context.Context, info *GraphInfo) {
	if info == nil {
		log.Println("[Mermaid] Skip: GraphInfo is nil")
		return
	}

	code := GenerateMermaidFlowchart(info)
	if code == "" {
		log.Println("[Mermaid] Skip: Empty Mermaid code")
		return
	}

	if err := d.writeToFile([]byte(code)); err != nil {
		log.Printf("[Mermaid] Failed: %v\n", err)
	} else {
		log.Printf("[Mermaid] Saved: %s\n", filepath.Join(d.path, d.name))
	}
}

// writeToFile writes Mermaid code to file, ensuring safety.
func (d *DrawMermaid) writeToFile(data []byte) error {
	if err := os.MkdirAll(d.path, 0755); err != nil {
		return fmt.Errorf("create dir %q: %w", d.path, err)
	}

	outputPath := filepath.Join(d.path, d.name)
	if !strings.HasPrefix(outputPath, d.path) {
		return fmt.Errorf("invalid output path: %s (outside of %s)", outputPath, d.path)
	}

	if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
		return fmt.Errorf("output path is a directory: %s", outputPath)
	}

	return os.WriteFile(outputPath, data, 0644)
}
