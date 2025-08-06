package compose

import (
	"context"
	"log"
	"testing"
)

// go test -run ^TestGenerateMermaidFlowchart$
func TestGenerateMermaidFlowchart(t *testing.T) {
	ctx := context.Background()
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("node_1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + " process by node_1,", nil
	}))
	_ = g.AddLambdaNode("node_2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + " process by node_2,", nil
	}))
	_ = g.AddLambdaNode("node_3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + " process by node_3,", nil
	}))

	_ = g.AddEdge(START, "node_1")
	_ = g.AddEdge("node_1", "node_2")
	_ = g.AddEdge("node_2", "node_3")
	_ = g.AddEdge("node_3", END)

	_, err := g.Compile(ctx, WithGraphCompileCallbacks(
		NewDrawMermaid(
			WithName("output_graph"),
			WithFormats(
				OutputSVG,
				OutputPNG,
				OutputMMD,
			)),
	))
	if err != nil {
		log.Printf("compile graph failed, err=%v", err)
		return
	}
}
