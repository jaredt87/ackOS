package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/jaredt87/ackOS/integrations/mcp"
	"github.com/jaredt87/ackOS/integrations/synthetic"
	"github.com/jaredt87/ackOS/kernel"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	httpAddr := flag.String("http", "", "serve Streamable HTTP at this address instead of stdio")
	flag.Parse()

	// The standalone binary uses a synthetic external resource so the demo
	// exercises the real provider boundary without pretending to control Git,
	// AWS, Kubernetes, or another domain-specific system.
	resource := synthetic.NewResource("demo-resource", "initial")
	executor := synthetic.Executor{Resource: resource}
	verifier := synthetic.Verifier{Resource: resource}
	recoveryObserver := synthetic.RecoveryObserver{Resource: resource}

	runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
	server, err := mcp.NewServer(runtime, executor, verifier, recoveryObserver)
	if err != nil {
		log.Fatal(err)
	}

	if *httpAddr != "" {
		log.Printf("ackOS MCP server listening at %s", *httpAddr)
		httpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server {
			return server.MCPServer()
		}, &mcpsdk.StreamableHTTPOptions{
			JSONResponse:               true,
			DisableLocalhostProtection: true,
		})
		log.Fatal(http.ListenAndServe(*httpAddr, httpHandler))
	}

	mcpServer := server.MCPServer()
	if err := mcpServer.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintln(log.Writer(), err)
		log.Fatal(err)
	}
}
