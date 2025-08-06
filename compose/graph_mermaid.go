package compose

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type OutputFormat string

const (
	OutputMMD OutputFormat = "mmd"
	OutputPNG OutputFormat = "png"
	OutputSVG OutputFormat = "svg"
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
	path    string
	name    string
	formats []OutputFormat
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

func WithFormats(formats ...OutputFormat) DrawMermaidOption {
	return func(d *DrawMermaid) {
		d.formats = formats
	}
}

// NewDrawMermaid creates a new DrawMermaid instance with optional configuration.
func NewDrawMermaid(opts ...DrawMermaidOption) *DrawMermaid {
	defaultPath, _ := filepath.Abs("./output")
	d := &DrawMermaid{
		path:    defaultPath,
		name:    "graph",
		formats: []OutputFormat{OutputMMD},
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

	for _, format := range d.formats {
		switch format {
		case OutputMMD:
			if err := d.writeToFile([]byte(code)); err != nil {
				log.Printf("[Mermaid] Failed to save MMD: %v\n", err)
			} else {
				log.Printf("[Mermaid] MMD Saved: %s\n", filepath.Join(d.path, d.getMmdName()))
			}
		case OutputPNG, OutputSVG:
			if err := d.downloadImage(code, string(format)); err != nil {
				log.Printf("[Mermaid] %s Failed: %v\n", strings.ToUpper(string(format)), err)
			}
		default:
			log.Printf("[Mermaid] Unknown format: %s\n", format)
		}
	}
}

// writeToFile writes Mermaid code to file, ensuring safety.
func (d *DrawMermaid) writeToFile(data []byte) error {
	if err := os.MkdirAll(d.path, 0755); err != nil {
		return fmt.Errorf("create dir %q: %w", d.path, err)
	}
	outputPath := filepath.Join(d.path, d.getMmdName())

	if !strings.HasPrefix(outputPath, d.path) {
		return fmt.Errorf("invalid output path: %s (outside of %s)", outputPath, d.path)
	}

	if info, err := os.Stat(outputPath); err == nil && info.IsDir() {
		return fmt.Errorf("output path is a directory: %s", outputPath)
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (d *DrawMermaid) getMmdName() string {
	fileName := d.name
	if !strings.HasSuffix(fileName, ".mmd") {
		fileName += ".mmd"
	}
	return fileName
}

func (d *DrawMermaid) downloadImage(code, fileType string) error {
	encoded := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString([]byte(code))

	var url string
	switch fileType {
	case string(OutputPNG):
		url = fmt.Sprintf("https://mermaid.ink/img/%s?type=png&bgColor=white", encoded)
	case string(OutputSVG):
		url = fmt.Sprintf("https://mermaid.ink/svg/%s", encoded)
	default:
		return fmt.Errorf("unsupported file type: %s", fileType)
	}

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("download %s failed: %w", fileType, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s failed: status %s", fileType, resp.Status)
	}

	if err := os.MkdirAll(d.path, 0755); err != nil {
		return fmt.Errorf("create dir %q: %w", d.path, err)
	}

	fileName := d.name
	if ext := filepath.Ext(fileName); ext != "" {
		fileName = strings.TrimSuffix(fileName, ext)
	}

	fileName += "." + fileType
	outputPath := filepath.Join(d.path, fileName)

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create %s file: %w", fileType, err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return fmt.Errorf("save %s: %w", fileType, err)
	}

	log.Printf("[Mermaid] %s Saved: %s\n", strings.ToUpper(fileType), outputPath)
	return nil
}
