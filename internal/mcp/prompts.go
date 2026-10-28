package mcp

import (
	"context"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// addPrompts registers the optional workflow prompts. They encode safe
// multi-step flows (search → locate → clip) so agents do not have to
// rediscover the tool choreography.
func (s *Server) addPrompts(srv *mcpsdk.Server) {
	srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "find_and_clip_scene",
		Description: "Find the scene containing a line of dialog and clip it with padding.",
		Arguments: []*mcpsdk.PromptArgument{{
			Name:        "query",
			Description: "The dialog text to look for",
			Required:    true,
		}},
	}, findAndClipScene)
	srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "summarize_show_quotes",
		Description: "Collect notable quotes for one show and summarize them.",
		Arguments: []*mcpsdk.PromptArgument{{
			Name:        "show",
			Description: "The show title to summarize",
			Required:    true,
		}},
	}, summarizeShowQuotes)
	srv.AddPrompt(&mcpsdk.Prompt{
		Name:        "audit_processing_queue",
		Description: "Report queue, scan and index health and flag stuck or failing jobs.",
	}, auditProcessingQueue)
}

// prompt builds a single-user-message prompt result.
func prompt(text string) *mcpsdk.GetPromptResult {
	return &mcpsdk.GetPromptResult{Messages: []*mcpsdk.PromptMessage{{
		Role:    "user",
		Content: &mcpsdk.TextContent{Text: text},
	}}}
}

// findAndClipScene returns the search → scene → clip workflow.
func findAndClipScene(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	query := req.Params.Arguments["query"]
	if query == "" {
		return nil, fmt.Errorf("query argument is required")
	}
	text := fmt.Sprintf(`Find the scene containing the line %q and produce a clip of it.

1. Call search_quotes with query %q.
2. Pick the best hit and call get_scene_by_line with its chunkId to confirm the context.
3. Call create_clip for that media with start/end from the cue span plus a second of padding on each side.
4. Report the clip URL and the quote it contains.`,
		query, query)
	return prompt(text), nil
}

// summarizeShowQuotes returns the quote-collection workflow.
func summarizeShowQuotes(_ context.Context, req *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	show := req.Params.Arguments["show"]
	if show == "" {
		return nil, fmt.Errorf("show argument is required")
	}
	text := fmt.Sprintf(`Summarize the notable dialog for %q.

1. Call search_quotes with show set to %q and a few broad query terms.
2. Call get_scene_by_line for the most interesting chunkIds to capture context.
3. Produce a short summary of recurring themes with timestamps.`,
		show, show)
	return prompt(text), nil
}

// auditProcessingQueue returns the queue-health workflow.
func auditProcessingQueue(context.Context, *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
	return prompt(`Audit the processing queue.

1. Call get_processing_status with includeRecentJobs enabled.
2. Call list_jobs and flag jobs stuck in running or error state.
3. Summarize: queue depth, paused state, scan status, index health, and any media that failed with its error message. Do not mutate the queue unless asked.`), nil
}
