package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/jaredt87/ackOS/integrations/mcp"
	"github.com/jaredt87/ackOS/kernel"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type demoExecutor struct{}

func (demoExecutor) Execute(context.Context, kernel.Transition, kernel.Authority) kernel.ExecutionResult {
	return kernel.ExecutionResult{Success: true, Message: "demo executor completed the authorized transition"}
}

type demoVerifier struct{}

func (demoVerifier) Verify(context.Context, kernel.Transition, kernel.Authority) (kernel.Observation, error) {
	return kernel.NewObservation("demo", "unused", 0, time.Now().UTC())
}

type verifier struct {
	executor demoExecutor
}

func (verifier) Verify(ctx context.Context, t kernel.Transition, a kernel.Authority) (kernel.Observation, error) {
	return kernel.NewObservation(t.Subject, t.After, 1, time.Now().UTC())
}

func (verifier) Observe(context.Context, string) (kernel.Observation, error) {
	return kernel.NewObservation("svc", "initial", 1, time.Now().UTC())
}

func main() {
	httpAddr := flag.String("http", "", "serve Streamable HTTP at this address instead of stdio")
	flag.Parse()

	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server, err := mcp.NewServer(runtime, demoExecutor{}, verifier{}, verifier{})
	if err != nil {
		log.Fatal(err)
	}

	if *httpAddr != "" {
		log.Printf("ackOS MCP server listening at %s", *httpAddr)
		log.Fatal(http.ListenAndServe(*httpAddr, server.StreamableHTTPHandler()))
	}

	mcpServer := server.MCPServer()
	if err := mcpServer.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintln(log.Writer(), err)
		log.Fatal(err)
	}
}
