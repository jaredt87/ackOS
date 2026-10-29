package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jaredt87/ackOS/integrations/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	apiURL := flag.String("api-url", os.Getenv("ACKOS_API_URL"), "ackos-api origin (HTTPS required except loopback)")
	bearerToken := flag.String("api-token", os.Getenv("ACKOS_API_BEARER_TOKEN"), "ackos-api bearer token (or ACKOS_API_BEARER_TOKEN)")
	timeout := flag.Duration("api-timeout", 10*time.Second, "maximum duration of each ackos-api HTTP request")
	flag.Parse()

	adapter, err := mcp.NewHTTPAPIAdapter(mcp.HTTPAPIConfig{
		BaseURL: *apiURL,
		BearerToken: *bearerToken,
		Timeout: *timeout,
	})
	if err != nil {
		log.Fatalf("configure production MCP adapter: %v", err)
	}

	server := adapter.MCPServer()
	log.Printf("ackOS production MCP adapter connected to configured ackos-api origin %s", *apiURL)
	if err := server.Run(context.Background(), &mcpsdk.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		log.Fatal(err)
	}
}
