package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/jaredt87/ackOS/integrations/git"
	"github.com/jaredt87/ackOS/integrations/mcp"
	"github.com/jaredt87/ackOS/integrations/synthetic"
	"github.com/jaredt87/ackOS/kernel"
	"github.com/jaredt87/ackOS/control"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	httpAddr := flag.String("http", "", "serve Streamable HTTP at this address instead of stdio")
	repoPath := flag.String("repo", "", "repository root for Git-provider mode")
	branch := flag.String("branch", "", "full target ref (for example refs/heads/main)")
	targetPath := flag.String("path", "", "existing repository-relative target file")
	flag.Parse()

	var server *mcp.Server
	if *repoPath != "" || *branch != "" || *targetPath != "" {
		if *repoPath == "" || *branch == "" || *targetPath == "" {
			log.Fatal("Git-provider mode requires all three flags: --repo, --branch refs/heads/..., and --path")
		}
		repository, err := git.Open(*repoPath)
		if err != nil {
			log.Fatal(err)
		}
		provider, err := git.NewProvider(repository, git.RefName(*branch), *targetPath)
		if err != nil {
			log.Fatal(err)
		}
		// Startup deliberately trusts the repository as it exists now. The
		// observed blob seeds this process's single kernel root; restarting
		// after an out-of-band edit therefore adopts that edit without auth.
		initial, err := provider.Observe(context.Background(), control.ObserveRequest{
			Target: control.ResourceRef{ID: provider.Subject()},
		})
		if err != nil {
			log.Fatalf("Git target must exist and be readable at startup: %v", err)
		}
		runtime := kernel.NewRuntime(initial.Resource.Fingerprint, kernel.AllowPolicy{})
		server, err = mcp.NewServerWithProvider(runtime, provider)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("ackOS Git provider configured: repo=%s branch=%s path=%s", *repoPath, *branch, *targetPath)
	} else {
		// The default standalone mode keeps the synthetic demo resource.
		resource := synthetic.NewResource("demo-resource", "initial")
		executor := synthetic.Executor{Resource: resource}
		verifier := synthetic.Verifier{Resource: resource}
		recoveryObserver := synthetic.RecoveryObserver{Resource: resource}
		runtime := kernel.NewRuntime("initial", kernel.AllowPolicy{})
		var err error
		server, err = mcp.NewServer(runtime, executor, verifier, recoveryObserver, recoveryObserver)
		if err != nil {
			log.Fatal(err)
		}
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
