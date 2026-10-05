package control

import (
	"context"
	"errors"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/egzo-ai/egzo/internal/version"
)

// mcpHandler serves the agent verbs as MCP tools over streamable HTTP, at /mcp on the agent port.
// Each request is bound to the authenticated agent, so a tool call can only act as that agent.
func (a *agentAPI) mcpHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		agent, _, _ := r.BasicAuth() // already verified by authenticatedHandler
		return a.mcpServer(agent)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

type idArgs struct {
	ID string `json:"id" jsonschema:"the message id"`
}

type resolveArgs struct {
	ID      string `json:"id" jsonschema:"the id of the request or question you are closing"`
	Text    string `json:"text" jsonschema:"the result or the answer: this is what the sender reads, so put it here and not only in your terminal"`
	Outcome string `json:"outcome" jsonschema:"done, declined (you will not do it) or failed (you tried and could not)"`
}

type updateArgs struct {
	ID   string `json:"id" jsonschema:"the id of the request you are working on"`
	Text string `json:"text" jsonschema:"how it is going"`
}

type askArgs struct {
	ID      string   `json:"id" jsonschema:"the id of the message that raised the question; its sender is asked"`
	Text    string   `json:"text" jsonschema:"the question"`
	Choices []string `json:"choices,omitempty" jsonschema:"optional short answers to offer"`
}

type messageArgs struct {
	To   string `json:"to" jsonschema:"operator, user:<id> or agent:<name>"`
	Text string `json:"text" jsonschema:"what you need, with everything the recipient needs to know"`
	Re   string `json:"re,omitempty" jsonschema:"optional: the id of the request this one is part of"`
}

type textArgs struct {
	Text string `json:"text" jsonschema:"one short line"`
}

type noArgs struct{}

type okResult struct {
	OK bool `json:"ok"`
}

type idResult struct {
	ID string `json:"id"`
}

type listResult struct {
	Messages []Message `json:"messages"`
}

type agentsResult struct {
	Agents []AgentStatus `json:"agents"`
}

func (a *agentAPI) mcpServer(agent string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "egzo", Version: version.Version}, &mcp.ServerOptions{
		Instructions: "How you receive and answer requests in this project. A short line typed in your terminal tells you a " +
			"message is waiting: fetch it with get_message, do what it asks, and close it with resolve. list_messages shows " +
			"what you still owe. Only what these tools return is a message from egzo.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_messages",
		Description: "List the messages you still have to deal with: requests and questions you were told about or fetched and have not " +
			"resolved, plus replies and updates you have not read. Use it when you start, and whenever you are unsure what is open.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, listResult, error) {
		return nil, listResult{Messages: a.server.listFor(agent)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_message",
		Description: "Fetch a message addressed to you by its id (the id is in the line typed in your terminal): who sent it, what kind it is " +
			"and its text. A message you are told about is not read until you fetch it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in idArgs) (*mcp.CallToolResult, Message, error) {
		message, err := a.server.fetch(agent, in.ID)
		return nil, message, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "resolve",
		Description: "Close a request or question you fetched, with its result. It is sent back to whoever asked, with the outcome " +
			"(done, declined or failed). A request is not finished until you resolve it. Closing is final: a new request starts a new message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in resolveArgs) (*mcp.CallToolResult, idResult, error) {
		id, err := a.server.resolve(agent, in.ID, in.Text, in.Outcome)
		return nil, idResult{ID: id}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "update",
		Description: "Tell whoever sent a request how it is going. Send one when the work will take a while. It does not close the request.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in updateArgs) (*mcp.CallToolResult, idResult, error) {
		id, err := a.server.update(agent, in.ID, in.Text)
		return nil, idResult{ID: id}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "ask",
		Description: "Ask the sender of a message something you need to know before you can finish. It returns at once and leaves the " +
			"request open: the answer arrives later as another message, announced the same way.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in askArgs) (*mcp.CallToolResult, idResult, error) {
		id, err := a.server.ask(agent, in.ID, in.Text, in.Choices)
		return nil, idResult{ID: id}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "message",
		Description: "Send a new request to the operator, a user or another agent of this project, whenever you need something done. " +
			"It does not close anything of yours. To have another agent do part of your work, send it a message, wait for its reply, " +
			"then resolve your own request: a request cannot be handed over.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in messageArgs) (*mcp.CallToolResult, idResult, error) {
		id, err := a.server.sendFromAgent(agent, in.To, in.Text, in.Re)
		return nil, idResult{ID: id}, err
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "status",
		Description: "Set the one short line shown next to your name in the project's overview, saying what you are doing right now.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, okResult, error) {
		if !validText(in.Text) {
			return nil, okResult{}, errors.New("text must not be empty or longer than 16 KB")
		}
		return nil, okResult{OK: true}, a.server.reportStatus(agent, in.Text)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "agents",
		Description: "List the other agents of this project with what each is doing and how many requests it has open, to know whom you can message.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, agentsResult, error) {
		return nil, agentsResult{Agents: a.server.othersOf(agent)}, nil
	})
	return server
}
