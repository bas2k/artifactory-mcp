package mcpserver

import (
	"context"
	_ "embed"
	"strings"
	"text/template"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const instructionsURI = "artifactory://instructions"

//go:embed instructions.md
var instructionsMarkdown string

var instructionsTemplate = template.Must(template.New("instructions").Parse(instructionsMarkdown))

func renderInstructions(disabledTools []string) string {
	disabled := make(map[string]bool, len(disabledTools))
	for _, name := range disabledTools {
		disabled[name] = true
	}
	var text strings.Builder
	if err := instructionsTemplate.Execute(&text, disabled); err != nil {
		panic(err)
	}
	return text.String()
}

func addInstructionsResource(server *mcp.Server, instructions string) {
	server.AddResource(&mcp.Resource{
		URI:         instructionsURI,
		Name:        "usage_instructions",
		Title:       "Artifactory usage instructions",
		Description: "Short guidance for repository discovery, searches, paths, paging, and permissions, tailored to enabled tools.",
		MIMEType:    "text/markdown",
		Size:        int64(len(instructions)),
	}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      instructionsURI,
			MIMEType: "text/markdown",
			Text:     instructions,
		}}}, nil
	})
}
